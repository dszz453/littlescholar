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

// AudioFormatHandler 把浏览器放不出来的音频（wma / ape / amr / ac3 / dts …）
// 转成单文件 AAC（.m4a）再交给 <audio>。
//
// 为什么不复用视频那套 HLS 转码：
//   - HLS 的价值是"边转边播 + 多档码率"，音频只有几十 MB，转完再播更简单；
//   - 部分 Android WebView 的 <audio> 不认 m3u8，认 AAC 是 100%。
//
// 产物按资源 ID 落盘缓存，第二次打开直接读文件。
type AudioFormatHandler struct {
	db      *gorm.DB
	enc     *util.Encryptor
	acc     *task.Accelerator
	ffmpeg  string
	outDir  string
	secret  string
	locks   sync.Map
	sem     chan struct{}
}

func NewAudioFormatHandler(db *gorm.DB, enc *util.Encryptor, acc *task.Accelerator,
	ffmpegPath, outDir, jwtSecret string) *AudioFormatHandler {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "ls-audiofmt")
	}
	_ = os.MkdirAll(outDir, 0o755)
	if acc == nil {
		acc = task.NewAccelerator(8)
	}
	return &AudioFormatHandler{
		db: db, enc: enc, acc: acc,
		ffmpeg: ffmpegPath, outDir: outDir, secret: jwtSecret,
		sem: make(chan struct{}, 2),
	}
}

// NeedsConvert 这条音频资源是不是"浏览器放不出来、必须转"。
//
// 判据只看**扩展名**，不看 media_type：
//
//   - media_type 是后补列，存量行可能还是导入时统一写死的 "video"，
//     而早年这条 `res.MediaType != audio` 的判断会让它们**永远**走不到转码，
//     直接拿直链塞进 <video> —— 浏览器解不了 wma，孩子看到的就是
//     "这个音频放不了"（2026-10-20 线上「大猫」目录下的 wma 就是这个形状）；
//   - 扩展名才是文件格式的权威来源，且 AudioNeedsTranscodeExt 里全是
//     音频专用格式，不存在"把视频误判成音频"的风险。
func (h *AudioFormatHandler) NeedsConvert(res *model.Resource) bool {
	if h == nil || res == nil {
		return false
	}
	switch res.MediaType {
	case model.MediaTypeEbook, model.MediaTypeImage, model.MediaTypePDF:
		// 电子书/图片有各自的转换链，别抢
		return false
	}
	return service.AudioNeedsTranscodeExt(service.ResourceExt(res))
}

// URLFor 带票据的地址（与 stream / ebook 同一套签名）。
func (h *AudioFormatHandler) URLFor(id uint64) string {
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/audio/%d?t=%s", id, SignStreamTicket(h.secret, id, exp))
}

// Serve GET /api/audio/:id?t=<ticket>
func (h *AudioFormatHandler) Serve(c *gin.Context) {
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
	// 明确请求转换的，只要不是"确定不需要转"就放行：
	// 扩展名未知的资源只能靠转换兜底，硬拒等于让它彻底播不了。
	if !h.NeedsConvert(&res) && service.ResourceExt(&res) != "" {
		util.BadRequest(c, "这个音频不需要转换")
		return
	}

	dst := filepath.Join(h.outDir, fmt.Sprintf("%d.m4a", id))
	if !fileExists(dst) {
		if err := h.convert(c.Request.Context(), &res, dst); err != nil {
			log.Printf("[audiofmt] 转换失败 resource=%d: %v", id, err)
			util.ServerError(c, "这个音频暂时转不了，请稍后再试")
			return
		}
	}
	c.Header("Content-Type", "audio/mp4")
	c.Header("Cache-Control", "private, max-age=600")
	c.File(dst)
}

func (h *AudioFormatHandler) convert(ctx context.Context, res *model.Resource, dst string) error {
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
	case <-time.After(30 * time.Second):
		return fmt.Errorf("服务器正忙")
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	ext := service.ResourceExt(res)
	src := filepath.Join(h.outDir, fmt.Sprintf("%d.src.%s", res.ID, ext))
	defer os.Remove(src)
	if err := h.fetchSource(ctx, res, src); err != nil {
		return err
	}

	tmp := dst + ".part"
	defer os.Remove(tmp)
	args := []string{
		"-y", "-nostdin", "-i", src,
		// 视频流必须丢掉：有些 .mka/.wma 其实封了封面图（mjpeg），
		// 不 -vn 的话 ffmpeg 会尝试把它当视频编码，白等一场。
		"-vn",
		"-c:a", "aac",
		"-b:a", "128k",
		"-ac", "2",
		// faststart：把 moov 放到文件头，边下边播不用等整首下完
		"-movflags", "+faststart",
		"-f", "mp4",
		"-loglevel", "error",
		tmp,
	}
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, truncateTail(string(out), 400))
	}
	return os.Rename(tmp, dst)
}

func (h *AudioFormatHandler) fetchSource(ctx context.Context, res *model.Resource, dst string) error {
	return downloadResourceSource(ctx, h.db, h.enc, h.acc, res, dst)
}

func truncateTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
