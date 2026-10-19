package handler

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// ImageFormatHandler 把浏览器解不了的图片（HEIC / HEIF / TIFF）转成 JPEG。
//
// 为什么需要：iPhone 拍的绘本照片默认是 HEIC，安卓 WebView 一律显示空白；
// 扫描件常是 TIFF，浏览器同样不认。这两类在"家长自己拍/扫"的场景里很常见，
// 而原图通常只有几 MB，转一次不到一秒。
type ImageFormatHandler struct {
	db     *gorm.DB
	enc    *util.Encryptor
	acc    *task.Accelerator
	ffmpeg string
	outDir string
	secret string
	locks  sync.Map
	sem    chan struct{}
}

func NewImageFormatHandler(db *gorm.DB, enc *util.Encryptor, acc *task.Accelerator,
	ffmpegPath, outDir, jwtSecret string) *ImageFormatHandler {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "ls-imagefmt")
	}
	_ = os.MkdirAll(outDir, 0o755)
	if acc == nil {
		acc = task.NewAccelerator(4)
	}
	return &ImageFormatHandler{
		db: db, enc: enc, acc: acc,
		ffmpeg: ffmpegPath, outDir: outDir, secret: jwtSecret,
		sem: make(chan struct{}, 3),
	}
}

// NeedsConvert 这张图是不是"浏览器解不了、必须转"。
func (h *ImageFormatHandler) NeedsConvert(res *model.Resource) bool {
	if h == nil || res == nil || res.MediaType != model.MediaTypeImage {
		return false
	}
	return service.ImageNeedsTranscodeExt(service.ResourceExt(res))
}

func (h *ImageFormatHandler) URLFor(id uint64) string {
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/image/%d?t=%s", id, SignStreamTicket(h.secret, id, exp))
}

// Serve GET /api/image/:id?t=<ticket>
func (h *ImageFormatHandler) Serve(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	if !verifyStreamTicket(h.secret, id, c.Query("t")) {
		util.Unauthorized(c, "链接已过期，请重新打开")
		return
	}
	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	if !h.NeedsConvert(&res) {
		util.BadRequest(c, "这张图片不需要转换")
		return
	}
	dst := filepath.Join(h.outDir, fmt.Sprintf("%d.jpg", id))
	if !fileExists(dst) {
		if err := h.convert(c.Request.Context(), &res, dst); err != nil {
			log.Printf("[imagefmt] 转换失败 resource=%d: %v", id, err)
			util.ServerError(c, "这张图片暂时打不开，请稍后再试")
			return
		}
	}
	// 允许浏览器缓存：图片基本不会变，省一次转码/回源
	c.Header("Content-Type", "image/jpeg")
	c.Header("Cache-Control", "private, max-age=3600")
	c.File(dst)
}

func (h *ImageFormatHandler) convert(ctx context.Context, res *model.Resource, dst string) error {
	mu, _ := h.locks.LoadOrStore(res.ID, &sync.Mutex{})
	mtx := mu.(*sync.Mutex)
	mtx.Lock()
	defer mtx.Unlock()
	if fileExists(dst) {
		return nil
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Second):
		return fmt.Errorf("服务器正忙")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	ext := service.ResourceExt(res)
	src := filepath.Join(h.outDir, fmt.Sprintf("%d.src.%s", res.ID, ext))
	defer os.Remove(src)
	if err := downloadResourceSource(ctx, h.db, h.enc, h.acc, res, src); err != nil {
		return err
	}
	tmp := dst + ".part"
	defer os.Remove(tmp)

	cmd := exec.CommandContext(ctx, h.ffmpeg,
		"-y", "-nostdin", "-i", src,
		// 只取第一帧：HEIC 的"多图/深度图"会解码出好几帧，
		// 不加这个参数 ffmpeg 会把它们拼成一张长图。
		"-frames:v", "1",
		"-q:v", "3",
		"-loglevel", "error",
		tmp,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, truncateTail(string(out), 300))
	}
	return os.Rename(tmp, dst)
}
