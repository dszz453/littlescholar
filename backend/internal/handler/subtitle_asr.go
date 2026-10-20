package handler

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/subtitle"
)

// AI 语音听写字幕（ASR）。
//
// 为什么要它：这套库是**英语学习资料**（新概念课文、英文绘本、儿歌），
// 网上没有现成字幕可搜 —— 之前接的 OpenSubtitles 对学习资料等于零命中。
// 学习资料的字幕只有一条路：自己听写。
//
// 触发时机是**按需**：孩子点开播放、而前面几道（同目录/内嵌/联网）都没配上，
// 才在这里起一个后台任务把音轨转成字幕，转完存库。理由很实际 ——
// 全库 1308 条音视频，每条都转一遍既慢又费额度，而绝大多数集孩子根本不会看到。
//
// 三个必须守住的约束：
//  1. **同一资源只跑一份**：孩子切前后台会重复请求字幕接口，
//     不去重就会同时起七八个 ffmpeg + 转写请求。
//  2. **并发上限 1**：这台 NAS 只有 4 核、可用内存 2.7G，
//     转写本身不占本地 CPU，但抽音轨要跑 ffmpeg，不能和正在看的播放抢。
//  3. **失败要留痕、且别死循环**：转不出来（静音、纯音乐、网关拒绝）
//     就写个失败标记，24 小时内不再重试；否则孩子每点一次都会重新烧一遍额度。
const (
	// asrChunkSec 单片时长。网关对单文件有 25MB 上限，
	// 16k 单声道 32kbps 的 mp3 约 100 分钟才到 25MB —— 10 分钟一片留足余量。
	asrChunkSec = 600
	// asrMaxBytes 超过这个体积就切片。
	asrMaxBytes = 24 << 20
	// asrFailTTL 转写失败后的冷却时间。
	asrFailTTL = 24 * time.Hour
	// asrTimeout 单条资源的总超时。一小时音频在网关侧通常几分钟出结果，
	// 留 30 分钟兜住"排长队"的情况。
	asrTimeout = 30 * time.Minute
)

// subtitlePendingHeader 转写进行中时回给前端的提示头。
//
// 为什么用头而不是换状态码：204 是"现在没有字幕"的既有语义，
// 前端拿它做静默处理；加一个头就能在不破坏旧逻辑的前提下告诉新版前端
// "过一会儿再来看看"，而不是让人以为"这集就是没字幕"。
const subtitlePendingHeader = "X-Subtitle-Pending"

// WithASR 挂上语音听写能力（链式，便于在 router 里就地配置）。
func (h *SubtitleHandler) WithASR(t *subtitle.Transcriber) *SubtitleHandler {
	h.asr = t
	if t != nil && t.Enabled() {
		h.asrSem = make(chan struct{}, 1)
	}
	return h
}

// StartASR 判断"这次要不要起听写"，并在需要时异步启动。
//
// 返回值告诉调用方：**有没有一个转写正在进行或刚刚启动**。
// 为 true 时接口回 204 + `X-Subtitle-Pending: 1`，让前端过一会儿再来取。
func (h *SubtitleHandler) StartASR(res *model.Resource) bool {
	if h == nil || h.asr == nil || !h.asr.Enabled() {
		return false
	}
	if res.MediaType != model.MediaTypeVideo && res.MediaType != model.MediaTypeAudio {
		return false
	}
	// 刚失败过就别再烧额度
	if h.asrFailed(res.ID) {
		return false
	}
	// 单飞：已经有一份在跑就直接告诉前端"等一下"
	if _, busy := h.asrLocks.LoadOrStore(res.ID, struct{}{}); busy {
		return true
	}

	r := *res // 值拷贝：goroutine 里不能再碰 gin 的请求对象
	run := h.asrRunner
	if run == nil {
		run = h.runASR
	}
	go run(r)
	return true
}

// asrDir 听写用的临时目录（和字幕缓存放一起，容器重建即清空）。
func (h *SubtitleHandler) asrDir() string {
	d := filepath.Join(h.outDir, "asr")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func (h *SubtitleHandler) asrFailMarker(id uint64) string {
	return filepath.Join(h.outDir, fmt.Sprintf("%d.asrfail", id))
}

// asrFailed 最近是否转写失败过。
func (h *SubtitleHandler) asrFailed(id uint64) bool {
	st, err := os.Stat(h.asrFailMarker(id))
	if err != nil {
		return false
	}
	return time.Since(st.ModTime()) < asrFailTTL
}

func (h *SubtitleHandler) markASRFailed(id uint64, reason string) {
	log.Printf("字幕：资源 %d 听写失败（%s），%v 内不再重试", id, reason, asrFailTTL)
	_ = os.WriteFile(h.asrFailMarker(id), []byte(reason), 0o644)
}

// runASR 完整的一轮：取直链 → 抽音轨 → （必要时切片）→ 逐片转写 → 合并 → 入库。
func (h *SubtitleHandler) runASR(res model.Resource) {
	defer h.asrLocks.Delete(res.ID)

	// 并发上限 1：和播放抢 CPU 会把孩子正在看的那一集卡住
	if h.asrSem != nil {
		select {
		case h.asrSem <- struct{}{}:
			defer func() { <-h.asrSem }()
		default:
			// 已经有一条在转：不排队，等它转完下次播放再来。
			// 排队会让"连点十几集"变成一串几分钟的积压任务。
			log.Printf("字幕：资源 %d 排在别的听写后面，本次跳过", res.ID)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), asrTimeout)
	defer cancel()

	start := time.Now()
	link, err := resolveDirectLinkFor(h.db, h.enc, &res)
	if err != nil {
		h.markASRFailed(res.ID, err.Error())
		return
	}

	dir := h.asrDir()
	base := fmt.Sprintf("%d-%d", res.ID, time.Now().Unix())
	mp3 := filepath.Join(dir, base+".mp3")

	if err := h.extractAudio(ctx, link, mp3); err != nil {
		h.markASRFailed(res.ID, "抽音轨失败："+err.Error())
		return
	}
	defer os.Remove(mp3)

	parts, offsets, err := h.splitIfNeeded(mp3, dir, base)
	if err != nil {
		h.markASRFailed(res.ID, "切片失败："+err.Error())
		return
	}
	if len(parts) > 1 {
		defer func() {
			for _, p := range parts {
				_ = os.Remove(p)
			}
		}()
	}

	var segments []string
	for i, p := range parts {
		if ctx.Err() != nil {
			h.markASRFailed(res.ID, "超时")
			return
		}
		srt, err := h.asr.TranscribeFile(ctx, p)
		if err != nil {
			// 某一片失败就整条失败：缺一段的字幕比没有更误导
			// （孩子看着看着字幕突然不走了，会以为播放器坏了）
			h.markASRFailed(res.ID, err.Error())
			return
		}
		vtt, err := subtitle.Normalize([]byte(srt))
		if err != nil || strings.TrimSpace(vtt) == "" {
			h.markASRFailed(res.ID, "转写结果无法解析")
			return
		}
		segments = append(segments, subtitle.Shift(vtt, offsets[i]))
	}

	vtt := segments[0]
	if len(segments) > 1 {
		vtt = subtitle.Merge(segments)
	}
	n := subtitle.CountCues(vtt)
	if n == 0 {
		// 纯音乐/静音资料会走到这里：正常，但要留痕，别让人以为在转
		h.markASRFailed(res.ID, "音频里没有人声")
		return
	}

	if h.store != nil {
		if err := h.store.Save(&model.ResourceSubtitle{
			ResourceID: res.ID,
			Lang:       h.asr.Language,
			Source:     "AI 听写",
			Origin:     "asr",
			Confidence: 75,
			Cues:       n,
			Size:       len(vtt),
			VTT:        vtt,
		}); err != nil {
			h.markASRFailed(res.ID, "入库失败："+err.Error())
			return
		}
	}
	// 顺手写缓存：下次请求直接命中，不用再查一次库
	h.writeCache(filepath.Join(h.outDir, fmt.Sprintf("%d.vtt", res.ID)), res.ID, vtt, "AI 听写")
	_ = os.Remove(h.asrFailMarker(res.ID))
	log.Printf("字幕：资源 %d 听写完成（%d 条，用时 %s）",
		res.ID, n, time.Since(start).Round(time.Second))
}

// extractAudio 用 ffmpeg 抽成 16k 单声道 32kbps 的 mp3。
//
// 直链直接喂给 ffmpeg（它支持 http range，不会为了开头几分钟
// 把整部片子拖下来）；本地硬盘走文件路径。
func (h *SubtitleHandler) extractAudio(ctx context.Context, link *adapter.DirectLink, dst string) error {
	if link == nil || link.URL == "" {
		return fmt.Errorf("拿不到播放地址")
	}
	src := link.URL
	if strings.HasPrefix(src, "file://") {
		src = fileURLToPath(src)
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	// 云盘直链几乎都校验 UA / Referer，不带头会直接 403 ——
	// 而 403 在 ffmpeg 嘴里只是 "Server returned 4xx"，看不出是头的问题。
	if len(link.Headers) > 0 {
		keys := make([]string, 0, len(link.Headers))
		for k := range link.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(link.Headers[k])
			b.WriteString("\r\n")
		}
		args = append(args, "-headers", b.String())
	}
	args = append(args,
		"-i", src,
		"-vn", "-ac", "1", "-ar", "16000", "-b:a", "32k",
		"-f", "mp3", dst)

	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return fmt.Errorf("%v：%s", err, msg)
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		return fmt.Errorf("抽出来的音轨是空的（多半没有音轨）")
	}
	return nil
}

// splitIfNeeded 体积超限时按固定时长切片，返回各片路径与各自的起始秒数。
//
// 用"整 600 秒"切片而不是按静音切：ffmpeg 的 segment 对音频是精确切，
// 每片起点正好是 i×600，拼回去的偏移量就是确定的 —— 不需要再读时长。
func (h *SubtitleHandler) splitIfNeeded(mp3, dir, base string) ([]string, []float64, error) {
	st, err := os.Stat(mp3)
	if err != nil {
		return nil, nil, err
	}
	if st.Size() <= asrMaxBytes {
		return []string{mp3}, []float64{0}, nil
	}

	pattern := filepath.Join(dir, base+"-%03d.mp3")
	cmd := exec.Command(h.ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", mp3,
		"-f", "segment", "-segment_time", fmt.Sprintf("%d", asrChunkSec),
		"-c", "copy", pattern)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("%v：%s", err, strings.TrimSpace(string(out)))
	}

	matches, _ := filepath.Glob(filepath.Join(dir, base+"-*.mp3"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("切完没找到分片")
	}
	offsets := make([]float64, len(matches))
	for i := range matches {
		offsets[i] = float64(i * asrChunkSec)
	}
	return matches, offsets, nil
}
