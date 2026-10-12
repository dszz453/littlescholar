// Package transcode 视频转码：把网盘上的源视频转成浏览器可直接播放的 HLS。
//
// 为什么需要转码：
//  1. 网盘里的视频常见 MKV / RMVB / AVI 容器，或 H.265 / DTS 音轨，
//     手机浏览器（尤其 iOS Safari）直接播放会失败或只有画面没声音。
//  2. 部分源站不支持 Range，无法拖动进度条。切成 HLS 分片后可随机访问。
//  3. 直链有签名有效期，转码产物落盘后不再依赖源站。
//
// 设计要点：
//   - **不把远端 URL 直接喂给 ffmpeg**。网盘直链需要 Cookie/签名头，
//     ffmpeg 的 header 传递能力有限且难以复用现有的分块加速器。
//     这里先用 Accelerator 把源文件拉到本地临时目录，再让 ffmpeg 读本地文件。
//     代价是多一次磁盘写入，换来的是命令简单、可复用加速、失败可重试。
//   - **不复用 task.WorkerPool**。它的 runJob 硬编码 10 分钟超时，
//     而一部电影转码动辄半小时以上。转码需要自己的并发闸门与超时策略。
package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrDisabled 转码功能未启用
var ErrDisabled = errors.New("转码功能未启用")

// ErrFFmpegMissing ffmpeg 可执行文件不存在
var ErrFFmpegMissing = errors.New("未找到 ffmpeg，请确认已安装并在 PATH 中，或配置 transcode.ffmpeg_path")

// ErrBusy 并发已满
var ErrBusy = errors.New("转码队列已满，请稍后重试")

// Profile 一档清晰度
type Profile struct {
	// Name 档位名，会成为子目录名与 m3u8 文件名，如 720p
	Name string
	// Height 目标高度，宽度按比例自动计算
	Height int
	// VideoBitrate 视频码率，如 "1800k"
	VideoBitrate string
	// MaxRate 峰值码率
	MaxRate string
	// BufSize 码率缓冲
	BufSize string
	// AudioBitrate 音频码率，如 "128k"
	AudioBitrate string
}

// DefaultProfiles 默认清晰度档位
//
// 只做 3 档：孩子端多在手机/平板上看，1080p 对家庭的带宽与存储都不划算。
// 源视频若比某档还低，ffmpeg 会因 scale=-2:min(...) 而保持原分辨率，不放大。
var DefaultProfiles = []Profile{
	{Name: "360p", Height: 360, VideoBitrate: "500k", MaxRate: "600k", BufSize: "1200k", AudioBitrate: "64k"},
	{Name: "480p", Height: 480, VideoBitrate: "1000k", MaxRate: "1200k", BufSize: "2400k", AudioBitrate: "96k"},
	{Name: "720p", Height: 720, VideoBitrate: "1800k", MaxRate: "2100k", BufSize: "4200k", AudioBitrate: "128k"},
}

// SegmentSeconds HLS 分片时长。10 秒在起播延迟与请求数之间较平衡。
const SegmentSeconds = 10

// Engine 转码引擎
type Engine struct {
	ffmpegPath  string
	concurrency int
	sem         chan struct{}
	mu          sync.Mutex
	running     map[uint64]*exec.Cmd // taskID → 进程，用于取消
}

// New 创建引擎
//
// ffmpegPath 为空时用 "ffmpeg"（依赖 PATH）。
// concurrency 为同时进行的转码任务数上限，建议 1~2：
// ffmpeg 编码是 CPU 密集型的，开太多会拖垮整个服务。
func New(ffmpegPath string, concurrency int) *Engine {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 4 {
		concurrency = 4
	}
	return &Engine{
		ffmpegPath:  ffmpegPath,
		concurrency: concurrency,
		sem:         make(chan struct{}, concurrency),
		running:     make(map[uint64]*exec.Cmd),
	}
}

// Available 检查 ffmpeg 是否可用
func (e *Engine) Available() bool {
	_, err := exec.LookPath(e.ffmpegPath)
	return err == nil
}

// Version 返回 ffmpeg 版本首行，用于启动自检日志
func (e *Engine) Version(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, e.ffmpegPath, "-version").Output()
	if err != nil {
		return "", err
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(line), nil
}

// Concurrency 当前并发上限
func (e *Engine) Concurrency() int { return e.concurrency }

// Acquire 占用一个转码槽位。调用方必须在结束后 Release。
// 返回 ErrBusy 时表示当前已有 concurrency 个任务在跑。
func (e *Engine) Acquire(ctx context.Context) error {
	select {
	case e.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}
}

// Release 释放槽位
func (e *Engine) Release() {
	select {
	case <-e.sem:
	default:
	}
}

// Cancel 取消指定任务的转码进程
func (e *Engine) Cancel(taskID uint64) bool {
	e.mu.Lock()
	cmd, ok := e.running[taskID]
	e.mu.Unlock()
	if !ok {
		return false
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return true
}

// RunningCount 当前在跑的任务数
func (e *Engine) RunningCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running)
}

func (e *Engine) register(taskID uint64, cmd *exec.Cmd) {
	e.mu.Lock()
	e.running[taskID] = cmd
	e.mu.Unlock()
}

func (e *Engine) unregister(taskID uint64) {
	e.mu.Lock()
	delete(e.running, taskID)
	e.mu.Unlock()
}

// Request 一次转码请求
type Request struct {
	// TaskID 数据库中的 TranscodeTask.ID，用于进度回写与取消
	TaskID uint64
	// SourcePath 本地源文件路径（已由调用方下载完成）
	SourcePath string
	// OutputDir 输出根目录，实际输出到 OutputDir/<TaskID>/ 下
	OutputDir string
	// Profiles 目标清晰度，为空时用 DefaultProfiles
	Profiles []Profile
	// DurationSeconds 源时长（秒），用于计算进度百分比。为 0 时进度按时间估算
	DurationSeconds int
	// OnProgress 进度回调，percent 为 0~100
	OnProgress func(percent int)
}

// Result 转码产物
type Result struct {
	// MasterPath master.m3u8 的绝对路径
	MasterPath string
	// MasterURL 相对 URL 前缀，如 /transcode/12
	MasterURL string
	// Variants 各档位产物
	Variants []Variant
	// Duration 实际耗时
	Duration time.Duration
}

// Variant 单档产物
type Variant struct {
	Name      string
	Height    int
	Playlist  string // 相对 master 的路径，如 720p/index.m3u8
	Bandwidth int    // 用于 master.m3u8 的 BANDWIDTH 属性（bit/s）
}

// Run 执行一次转码：源文件 → 多档 HLS + master playlist
//
// 输出结构：
//
//	OutputDir/<TaskID>/
//	├── master.m3u8
//	├── 360p/index.m3u8 + seg_000.ts ...
//	├── 480p/index.m3u8 + seg_000.ts ...
//	└── 720p/index.m3u8 + seg_000.ts ...
func (e *Engine) Run(ctx context.Context, req Request) (*Result, error) {
	if !e.Available() {
		return nil, ErrFFmpegMissing
	}
	if req.SourcePath == "" {
		return nil, errors.New("源文件路径为空")
	}
	if _, err := os.Stat(req.SourcePath); err != nil {
		return nil, fmt.Errorf("源文件不可读: %w", err)
	}

	profiles := req.Profiles
	if len(profiles) == 0 {
		profiles = DefaultProfiles
	}

	outRoot := filepath.Join(req.OutputDir, strconv.FormatUint(req.TaskID, 10))
	// 重跑前清理旧产物，避免残留分片混进新播放列表
	if err := os.RemoveAll(outRoot); err != nil {
		return nil, fmt.Errorf("清理旧产物失败: %w", err)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}

	started := time.Now()
	variants := make([]Variant, 0, len(profiles))

	for i, p := range profiles {
		target := filepath.Join(outRoot, p.Name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, fmt.Errorf("创建档位目录失败: %w", err)
		}

		// 进度均分到各档，最后一档收尾到 100
		base := i * 100 / len(profiles)
		span := 100 / len(profiles)
		onP := func(pct int) {
			if req.OnProgress == nil {
				return
			}
			req.OnProgress(base + pct*span/100)
		}

		if err := e.transcodeOne(ctx, req, p, target, onP); err != nil {
			return nil, fmt.Errorf("转码 %s 失败: %w", p.Name, err)
		}

		variants = append(variants, Variant{
			Name:      p.Name,
			Height:    p.Height,
			Playlist:  p.Name + "/index.m3u8",
			Bandwidth: parseBitrate(p.VideoBitrate) + parseBitrate(p.AudioBitrate),
		})
	}

	masterPath := filepath.Join(outRoot, "master.m3u8")
	if err := writeMaster(masterPath, variants); err != nil {
		return nil, fmt.Errorf("生成 master playlist 失败: %w", err)
	}

	if req.OnProgress != nil {
		req.OnProgress(100)
	}

	return &Result{
		MasterPath: masterPath,
		MasterURL:  "/transcode/" + strconv.FormatUint(req.TaskID, 10) + "/master.m3u8",
		Variants:   variants,
		Duration:   time.Since(started),
	}, nil
}

// transcodeOne 转码单个档位为 HLS
func (e *Engine) transcodeOne(
	ctx context.Context,
	req Request,
	p Profile,
	targetDir string,
	onProgress func(int),
) error {
	args := buildArgs(req.SourcePath, targetDir, p)

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)

	// 进度走 stdout，错误走 stderr —— 分开读，互不干扰
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	if req.TaskID > 0 {
		e.register(req.TaskID, cmd)
		defer e.unregister(req.TaskID)
	}

	var (
		tailErr strings.Builder
		errMu   sync.Mutex
		wg      sync.WaitGroup
	)

	// 读进度
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if pct, ok := parseProgress(sc.Text(), req.DurationSeconds); ok {
				onProgress(pct)
			}
		}
	}()

	// 读错误
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			errMu.Lock()
			// 只保留最后 20 行，避免超长输出撑爆内存
			if tailErr.Len() > 0 {
				tailErr.WriteByte('\n')
			}
			tailErr.WriteString(line)
			if tailErr.Len() > 8000 {
				tailErr.Reset()
			}
			errMu.Unlock()
		}
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	if waitErr != nil {
		// context 取消与超时会走到这里
		if ctx.Err() != nil {
			return ctx.Err()
		}
		errMu.Lock()
		detail := strings.TrimSpace(tailErr.String())
		errMu.Unlock()
		if detail == "" {
			detail = waitErr.Error()
		}
		return errors.New(detail)
	}

	// 产物自检：没有 m3u8 说明 ffmpeg 静默失败了
	if _, err := os.Stat(filepath.Join(targetDir, "index.m3u8")); err != nil {
		errMu.Lock()
		detail := strings.TrimSpace(tailErr.String())
		errMu.Unlock()
		if detail == "" {
			detail = "ffmpeg 未产出 index.m3u8"
		}
		return errors.New(detail)
	}
	return nil
}

// buildArgs 构造 ffmpeg 参数
//
// 关键参数说明：
//   - -profile:v main / -level 4.0：兼容性最好的 H.264 档位，老设备也能硬解
//   - -pix_fmt yuv420p：否则某些设备显示绿屏
//   - -preset veryfast：速度与体积的折中，家庭场景不值得为省一点体积等更久
//   - scale=-2:min(H,ih)：等比缩放到目标高度且不放大；-2 保证宽为偶数（H.264 要求）
//   - hls_playlist_type vod：生成完整的点播播放列表（含 #EXT-X-ENDLIST），
//     否则播放器会当作直播流一直等待
func buildArgs(src, targetDir string, p Profile) []string {
	scale := fmt.Sprintf("scale=-2:'min(%d,ih)'", p.Height)
	segPattern := filepath.Join(targetDir, "seg_%04d.ts")
	playlist := filepath.Join(targetDir, "index.m3u8")

	return []string{
		// 覆盖输出，不询问
		"-y",
		// 输入。禁用 stdin，避免 ffmpeg 因等待交互而挂住
		"-nostdin",
		"-i", src,

		// 视频
		"-c:v", "libx264",
		"-profile:v", "main",
		"-level", "4.0",
		"-pix_fmt", "yuv420p",
		"-preset", "veryfast",
		"-vf", scale,
		"-b:v", p.VideoBitrate,
		"-maxrate", p.MaxRate,
		"-bufsize", p.BufSize,

		// 音频：统一转 AAC，源若是 DTS/AC3 也能播
		"-c:a", "aac",
		"-b:a", p.AudioBitrate,
		"-ac", "2",

		// HLS 封装
		"-f", "hls",
		"-hls_time", strconv.Itoa(SegmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segPattern,
		"-hls_flags", "independent_segments",

		// 进度独立走 stdout（pipe:1），错误留在 stderr。
		// 两个流分开，解析器就不必靠前缀猜测，也不会把错误信息误判成进度。
		"-progress", "pipe:1",
		"-nostats",
		"-loglevel", "error",

		playlist,
	}
}

// parseProgress 从 ffmpeg 的输出行解析进度百分比
//
// 兼容两种格式：
//  1. -progress 输出的 out_time_ms=1234567890（微秒）
//  2. 传统 stderr 的 time=00:01:23.45
func parseProgress(line string, totalSec int) (int, bool) {
	if totalSec <= 0 {
		return 0, false
	}

	if strings.HasPrefix(line, "out_time_ms=") {
		raw := strings.TrimPrefix(line, "out_time_ms=")
		v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return 0, false
		}
		// ffmpeg 的 out_time_ms 单位实际是微秒（历史遗留命名）
		sec := float64(v) / 1_000_000.0
		return clampPercent(sec, totalSec), true
	}

	if i := strings.Index(line, "time="); i >= 0 {
		raw := line[i+len("time="):]
		if j := strings.IndexAny(raw, " \t"); j >= 0 {
			raw = raw[:j]
		}
		sec, ok := parseClock(strings.TrimSpace(raw))
		if !ok {
			return 0, false
		}
		return clampPercent(sec, totalSec), true
	}

	return 0, false
}

// parseClock 解析 HH:MM:SS.ms 或 MM:SS.ms
func parseClock(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var total float64
	for _, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + f
	}
	return total, true
}

func clampPercent(sec float64, totalSec int) int {
	if totalSec <= 0 {
		return 0
	}
	pct := int(sec / float64(totalSec) * 100)
	if pct < 0 {
		return 0
	}
	// 留 1% 给封尾与 master 生成，避免进度条提前到 100 却还在跑
	if pct > 99 {
		return 99
	}
	return pct
}

// parseBitrate 把 "1800k" 转成 1800000
func parseBitrate(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"):
		mult = 1000
		s = strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult = 1000 * 1000
		s = strings.TrimSuffix(s, "m")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v * mult
}

// writeMaster 生成 master.m3u8
func writeMaster(path string, variants []Variant) error {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	for _, v := range variants {
		sb.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"avc1.4d401f,mp4a.40.2\"\n",
			v.Bandwidth, estimateWidth(v.Height), v.Height))
		sb.WriteString(v.Playlist)
		sb.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// estimateWidth 按 16:9 估算宽度，仅用于 RESOLUTION 声明
//
// 真实宽度由 ffmpeg 按源比例决定（scale=-2），
// 播放器以实际分片为准，这里只是让 master 的属性看起来合理。
func estimateWidth(height int) int {
	w := height * 16 / 9
	if w%2 != 0 {
		w++
	}
	return w
}

// CopyFile 供外部把已下载的临时文件搬到目标位置
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
