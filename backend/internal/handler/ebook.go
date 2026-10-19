package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ebook"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// EbookHandler 把浏览器读不了的电子书（mobi/azw3/cbr/fb2）转成 EPUB 再给前端。
//
// 为什么不让孩子端自己解：MOBI 的 PalmDOC 压缩、CBR 的 RAR 解包，
// 在 WebView 里要么没有可用库、要么内存撑不住（一个漫画包上百 MB）。
// 服务端转一次、落盘缓存，之后每次打开都是直接读文件。
type EbookHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	enc    *util.Encryptor
	acc    *task.Accelerator
	outDir string
	secret string

	// locks 按资源 ID 串行化转换：同一个孩子连续点两次，
	// 不该触发两次完整的下载 + 转换。
	locks sync.Map // uint64 → *sync.Mutex
	// sem 限制同时转换的本书数（内存吃紧的机器上尤其重要）
	sem chan struct{}
}

// NewEbookHandler 构造。outDir 为产物目录（不存在会自动创建）。
func NewEbookHandler(db *gorm.DB, perm *service.PermissionService, enc *util.Encryptor,
	acc *task.Accelerator, outDir, jwtSecret string) *EbookHandler {
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "ls-ebook")
	}
	if acc == nil {
		// 8 路并发：电子书一般几 MB，不需要播放那条链路的 16~32 路
		acc = task.NewAccelerator(8)
	}
	_ = os.MkdirAll(outDir, 0o755)
	return &EbookHandler{
		db:     db,
		perm:   perm,
		enc:    enc,
		acc:    acc,
		outDir: outDir,
		secret: jwtSecret,
		sem:    make(chan struct{}, 2),
	}
}

// Serve GET /api/ebook/:id?t=<ticket>
//
// 和 /api/stream/:id 一样用票据鉴权（浏览器里 <img>/fetch 不方便带 header），
// 票据复用同一套签名。
func (h *EbookHandler) Serve(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	if !verifyStreamTicket(h.secret, id, c.Query("t")) {
		util.Unauthorized(c, "播放链接已过期，请重新打开")
		return
	}
	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	ext := service.ResourceExt(&res)
	if !service.EbookNeedsConvertExt(ext) {
		util.BadRequest(c, "这个格式不需要转换")
		return
	}

	dst := filepath.Join(h.outDir, fmt.Sprintf("%d.epub", id))
	if !fileExists(dst) {
		if err := h.convert(c.Request.Context(), &res, ext, dst); err != nil {
			log.Printf("[ebook] 转换失败 resource=%d ext=%s: %v", id, ext, err)
			util.ServerError(c, convertErrText(err))
			return
		}
	}

	c.Header("Content-Type", "application/epub+zip")
	c.Header("Cache-Control", "private, max-age=600")
	c.File(dst)
}

// NeedsConvert 供播放信息接口判断这本书要不要走转换。
func (h *EbookHandler) NeedsConvert(res *model.Resource) bool {
	if res == nil || res.MediaType != model.MediaTypeEbook {
		return false
	}
	return service.EbookNeedsConvertExt(service.ResourceExt(res))
}

// URLFor 生成带票据的电子书地址。
func (h *EbookHandler) URLFor(id uint64) string {
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/ebook/%d?t=%s", id, SignStreamTicket(h.secret, id, exp))
}

func (h *EbookHandler) convert(ctx context.Context, res *model.Resource, ext, dst string) error {
	mu, _ := h.locks.LoadOrStore(res.ID, &sync.Mutex{})
	mtx := mu.(*sync.Mutex)
	mtx.Lock()
	defer mtx.Unlock()

	// 拿到锁后再判一次：前一个请求可能刚好转完
	if fileExists(dst) {
		return nil
	}

	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("服务器正忙，请稍后再试")
	}

	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()

	tmpSrc := filepath.Join(h.outDir, fmt.Sprintf("%d.src.%s", res.ID, ext))
	defer os.Remove(tmpSrc)
	if err := h.fetchSource(ctx, res, tmpSrc); err != nil {
		return err
	}
	// 先写临时产物再原子改名：并发/中断时前端不会拿到半个 zip
	tmpDst := dst + ".part"
	defer os.Remove(tmpDst)
	if err := ebook.Convert(tmpSrc, ext, tmpDst); err != nil {
		return err
	}
	return os.Rename(tmpDst, dst)
}

func (h *EbookHandler) fetchSource(ctx context.Context, res *model.Resource, dst string) error {
	return downloadResourceSource(ctx, h.db, h.enc, h.acc, res, dst)
}

// downloadResourceSource 把一条资源的源文件拉到本地。
//
// 电子书转换、音频转码、图片转码三条链路都要这一步（取直链 → 下载），
// 抽成包级函数免得三份实现各自漂移。
func downloadResourceSource(ctx context.Context, db *gorm.DB, enc *util.Encryptor,
	acc *task.Accelerator, res *model.Resource, dst string) error {
	link, err := resolveDirectLinkFor(db, enc, res)
	if err != nil {
		return err
	}
	// 本地硬盘（PRO 的 NAS 目录）拿到的是 file://，直接拷贝比走 HTTP 快得多
	if strings.HasPrefix(link.URL, "file://") {
		return copyLocal(strings.TrimPrefix(link.URL, "file://"), dst)
	}
	if acc == nil {
		return fmt.Errorf("下载器未初始化")
	}
	return acc.Download(ctx, link.URL, link.Headers, dst, nil)
}

func copyLocal(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// convertErrText 把内部错误翻译成家长看得懂的一句话（细节留在日志里）。
func convertErrText(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "DRM"):
		return "这本书有版权保护，暂时打不开"
	case strings.Contains(msg, "HUFF"):
		return "这本书的压缩方式暂时不支持，可以先用别的阅读器看"
	case strings.Contains(msg, "太大"):
		return msg
	case strings.Contains(msg, "记录") || strings.Contains(msg, "解析"):
		return "这本书的文件好像损坏了"
	case strings.Contains(msg, "direct") || strings.Contains(msg, "直链") || strings.Contains(msg, "账号"):
		return "拿不到文件地址，请让家长检查网盘账号"
	}
	return "转换失败，请稍后再试"
}
