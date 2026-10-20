package handler

import (
	"fmt"
	"log"
	"net/http"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/subtitle"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// SubtitleHandler 给视频/音频配字幕。
//
// 顺序是刻意的：**本地同名文件 → 容器内嵌轨 → 联网匹配**。
//
//   - 本地同名文件最可信（压制组自己配的，时间轴一定是准的）；
//   - 内嵌轨次之（片源自带，不用猜）；
//   - 联网最后（可能匹配错片子，只在前两者都没有时才用）。
//
// 全都找不到就回 204，前端静默 —— 没字幕是常态，不该弹一个错误框
// 打断孩子看动画片。
type SubtitleHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	enc    *util.Encryptor
	acc    *task.Accelerator
	ffmpeg string
	probe  string
	outDir string
	secret string
	online *subtitle.Online

	locks sync.Map // uint64 → *sync.Mutex
	sem   chan struct{}
}

// NewSubtitleHandler 构造。
func NewSubtitleHandler(db *gorm.DB, perm *service.PermissionService, enc *util.Encryptor,
	acc *task.Accelerator, ffmpegPath, outDir, jwtSecret, osKey string) *SubtitleHandler {

	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "ls-subtitle")
	}
	if acc == nil {
		acc = task.NewAccelerator(4)
	}
	_ = os.MkdirAll(outDir, 0o755)
	return &SubtitleHandler{
		db:     db,
		perm:   perm,
		enc:    enc,
		acc:    acc,
		ffmpeg: strings.TrimSpace(ffmpegPath),
		probe:  ffprobeOf(ffmpegPath),
		outDir: outDir,
		secret: jwtSecret,
		online: subtitle.NewOnline(osKey),
		sem:    make(chan struct{}, 2),
	}
}

// ffprobeOf 从 ffmpeg 路径推出 ffprobe 路径。
//
// 只配了 ffmpeg 的部署（绝大多数）也能用 —— 同一个包里必有 ffprobe，
// 名字只差一个字母。推不出来就交给 PATH。
func ffprobeOf(ffmpegPath string) string {
	p := strings.TrimSpace(ffmpegPath)
	if p == "" {
		return "ffprobe"
	}
	base := filepath.Base(p)
	if strings.Contains(base, "ffmpeg") {
		return filepath.Join(filepath.Dir(p), strings.Replace(base, "ffmpeg", "ffprobe", 1))
	}
	return "ffprobe"
}

// Serve GET /api/subtitle/:id?t=<ticket>[&refresh=1]
//
// 返回 text/vtt。没有字幕时 204（不是 404 —— 404 会被前端当成"接口坏了"）。
func (h *SubtitleHandler) Serve(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		c.String(http.StatusBadRequest, "参数错误")
		return
	}
	if !verifyStreamTicket(h.secret, id, c.Query("t")) {
		c.JSON(http.StatusOK, gin.H{"code": 401, "message": "链接已过期，请重新打开"})
		return
	}

	cached := filepath.Join(h.outDir, fmt.Sprintf("%d.vtt", id))
	refresh := c.Query("refresh") == "1"

	if !refresh && fileExists(cached) {
		h.serveFile(c, cached, readSource(h.outDir, id))
		return
	}
	// 负缓存：确认没有字幕，24 小时内不再探一遍（探测要打网盘 API）。
	if !refresh {
		noneFile := filepath.Join(h.outDir, fmt.Sprintf("%d.none", id))
		if st, err := os.Stat(noneFile); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
			c.Status(http.StatusNoContent)
			return
		}
	}

	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 404, "message": "资源不存在"})
		return
	}

	mu := h.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	// 双检：等锁期间可能已经被另一个请求做好了
	if !refresh && fileExists(cached) {
		h.serveFile(c, cached, readSource(h.outDir, id))
		return
	}

	vtt, source, err := h.build(&res)
	if err != nil || strings.TrimSpace(vtt) == "" {
		if err != nil {
			log.Printf("字幕：资源 %d 没配上（%v）", id, err)
		}
		_ = os.WriteFile(filepath.Join(h.outDir, fmt.Sprintf("%d.none", id)), []byte(""), 0o644)
		c.Status(http.StatusNoContent)
		return
	}

	tmp := cached + ".part"
	if err := os.WriteFile(tmp, []byte(vtt), 0o644); err != nil {
		c.Status(http.StatusNoContent)
		return
	}
	if err := os.Rename(tmp, cached); err != nil {
		_ = os.Remove(tmp)
		c.Status(http.StatusNoContent)
		return
	}
	_ = os.WriteFile(filepath.Join(h.outDir, fmt.Sprintf("%d.source", id)), []byte(source), 0o644)
	_ = os.Remove(filepath.Join(h.outDir, fmt.Sprintf("%d.none", id)))

	h.serveFile(c, cached, source)
}

// build 按"本地 → 内嵌 → 联网"的顺序找字幕。
func (h *SubtitleHandler) build(res *model.Resource) (string, string, error) {
	// ① 本地文件 / 本地硬盘：直接读同目录
	localPath := ""
	link, linkErr := resolveDirectLinkFor(h.db, h.enc, res)
	if linkErr == nil && strings.HasPrefix(link.URL, "file://") {
		localPath = strings.TrimPrefix(link.URL, "file://")
	}
	if localPath != "" {
		if p, score := subtitle.FindNearby(localPath); p != "" && score > 0 {
			vtt, err := subtitle.ReadAndNormalize(p)
			if err == nil && vtt != "" {
				return vtt, "同目录字幕 · " + filepath.Base(p), nil
			}
		}
	}

	// ② 容器内嵌轨
	//
	// 本地文件才提取：对网盘直链做提取会把整部片子下载一遍。
	// 但**探测**可以用直链做（只读容器头），命中后再决定值不值。
	var input string
	var headers map[string]string
	if localPath != "" {
		input = localPath
	} else if linkErr == nil && link.URL != "" {
		input = link.URL
		headers = link.Headers
	}
	if input != "" {
		tracks := subtitle.ProbeTracks(context.Background(), h.probe, input, headers)
		if t, ok := subtitle.PickTrack(tracks); ok && localPath != "" {
			dst := filepath.Join(h.outDir, fmt.Sprintf("%d-track%d.vtt", res.ID, t.SubIndex))
			if err := subtitle.ExtractTrack(context.Background(), h.ffmpeg, localPath, t.SubIndex, dst); err == nil {
				if b, err := os.ReadFile(dst); err == nil && len(b) > 0 {
					return string(b), "内嵌字幕轨 · " + langText(t.Language, t.Title), nil
				}
			}
		}
	}

	// ③ 联网匹配（没配 key 就跳过）
	if h.online.Enabled() {
		title, season, episode := subtitle.SplitTitle(res.Title)
		vtt, src, err := h.online.Search(context.Background(), title, season, episode)
		if err == nil && vtt != "" {
			return vtt, src, nil
		}
		if err != nil {
			log.Printf("字幕：联网匹配失败（资源 %d，%s）：%v", res.ID, title, err)
		}
	}

	// ④ 都没有
	if linkErr != nil {
		return "", "", linkErr
	}
	return "", "", fmt.Errorf("没有可用的字幕")
}

func (h *SubtitleHandler) serveFile(c *gin.Context, path, source string) {
	if source == "" {
		source = "缓存"
	}
	c.Header("Content-Type", "text/vtt; charset=utf-8")
	c.Header("Cache-Control", "private, max-age=600")
	// <track> 在某些内核里按跨域请求走，带上 CORS 头避免整条被拒
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("X-Subtitle-Source", source)
	c.File(path)
}

func (h *SubtitleHandler) lockFor(id uint64) *sync.Mutex {
	v, _ := h.locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// readSource 读缓存时把来源也带回去（诊断用）。
func readSource(dir string, id uint64) string {
	b, err := os.ReadFile(filepath.Join(dir, strconv.FormatUint(id, 10)+".source"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func langText(lang, title string) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "zh", "chi", "zho", "chs", "cht":
		return "中文"
	case "en", "eng":
		return "英文"
	case "":
		return "未知语言"
	}
	return lang
}
