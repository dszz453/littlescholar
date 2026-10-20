package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// subDirs 常见字幕子目录名。
//
// 影视压制组几乎都这几种写法：视频在根目录，字幕统一扔进 Subs/ 里。
// 不去这里找，就会出现"明明有字幕却说没有"。
var subDirs = []string{"subs", "sub", "subtitles", "subtitle", "字幕", "中文字幕"}

// FindNearby 在视频旁边找最合适的字幕文件。
//
// 返回 (路径, 分数)。分数 0 表示没找到。
// 只在**同一目录**（及其常见字幕子目录）里找 —— 全盘扫描既慢又可能
// 匹配到另一集的字幕，那比没有字幕更糟。
func FindNearby(videoPath string) (string, int) {
	videoName := filepath.Base(videoPath)
	dir := filepath.Dir(videoPath)

	best, bestScore := "", 0
	tryDir := func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !IsSubtitle(e.Name()) {
				continue
			}
			sc := MatchScore(videoName, e.Name())
			if sc > bestScore {
				best, bestScore = filepath.Join(d, e.Name()), sc
			}
		}
	}

	tryDir(dir, 0)
	if bestScore < 100 {
		for _, sd := range subDirs {
			tryDir(filepath.Join(dir, sd), 1)
		}
	}
	return best, bestScore
}

// ReadAndNormalize 读字幕文件并转成 VTT。
func ReadAndNormalize(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) > 8<<20 {
		return "", fmt.Errorf("字幕文件过大")
	}
	return Normalize(raw)
}

// Track 一条内嵌字幕轨。
type Track struct {
	Index    int    `json:"index"`    // 全局 stream index
	SubIndex int    `json:"subIndex"` // 字幕轨序号（0 起），给 -map 0:s:N 用
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

// ProbeTracks 用 ffprobe 看视频里有没有内嵌字幕轨。
//
// 为什么值得做：大量 mkv 把中文字幕**烧在容器里**，外面没有 srt 文件，
// 但播放器能切出来。用户看到的"这个片没字幕"其实是"没找到轨"。
//
// 输入可以是本地路径，也可以是带 headers 的 http 直链。
// 对 http 只读容器头（moov/段头），几秒内能出结果；读不到就当没有。
func ProbeTracks(ctx context.Context, ffprobe, input string, headers map[string]string) []Track {
	if strings.TrimSpace(ffprobe) == "" {
		ffprobe = "ffprobe"
	}
	args := []string{"-v", "error"}
	if h := headerArg(headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args,
		"-select_streams", "s",
		"-show_entries", "stream=index,codec_name:stream_tags=language,title",
		"-of", "json",
		"-i", input,
	)

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffprobe, args...).Output()
	if err != nil {
		return nil
	}
	var parsed struct {
		Streams []struct {
			Index     int               `json:"index"`
			CodecName string            `json:"codec_name"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil
	}
	tracks := make([]Track, 0, len(parsed.Streams))
	for i, s := range parsed.Streams {
		t := Track{
			Index:    s.Index,
			SubIndex: i,
			Codec:    s.CodecName,
			Language: s.Tags["language"],
			Title:    s.Tags["title"],
		}
		// 图形字幕（dvd_subtitle / hdmv_pgs_subtitle）没法转成文本，
		// 转出来是一堆空行 —— 直接不列为候选。
		if isBitmapSub(s.CodecName) {
			continue
		}
		tracks = append(tracks, t)
	}
	return tracks
}

// isBitmapSub 判断是不是"图形字幕"（位图，不能转文本）。
func isBitmapSub(codec string) bool {
	switch strings.ToLower(codec) {
	case "dvd_subtitle", "hdmv_pgs_subtitle", "dvb_subtitle", "xsub":
		return true
	}
	return false
}

// PickTrack 从多条轨里挑一条：优先中文，其次第一条。
func PickTrack(tracks []Track) (Track, bool) {
	if len(tracks) == 0 {
		return Track{}, false
	}
	rank := func(t Track) int {
		lang := strings.ToLower(t.Language + " " + t.Title)
		switch {
		case strings.Contains(lang, "zh"), strings.Contains(lang, "chi"),
			strings.Contains(lang, "chs"), strings.Contains(lang, "cht"),
			strings.Contains(lang, "中文"), strings.Contains(lang, "简"),
			strings.Contains(lang, "繁"):
			return 2
		case strings.Contains(lang, "en"), strings.Contains(lang, "eng"):
			return 1
		}
		return 0
	}
	sort.SliceStable(tracks, func(i, j int) bool { return rank(tracks[i]) > rank(tracks[j]) })
	return tracks[0], true
}

// ExtractTrack 把内嵌字幕轨导出成 VTT。
//
// ⚠️ 只对**本地文件**调用。ffmpeg 提取字幕轨时可能顺序读完整个容器，
// 对网盘 http 直链做这件事等于把整部片子下载一遍 —— 服务器上完全不可接受。
// 调用方负责判断 input 是不是 file://。
func ExtractTrack(ctx context.Context, ffmpeg, input string, subIndex int, dst string) error {
	if strings.TrimSpace(ffmpeg) == "" {
		ffmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	tmp := dst + ".part"
	_ = os.Remove(tmp)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", input,
		"-map", fmt.Sprintf("0:s:%d", subIndex),
		"-f", "webvtt",
		tmp,
	}
	out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("提取内嵌字幕失败: %s", msg)
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("这条字幕轨是空的")
	}
	return os.Rename(tmp, dst)
}

// headerArg 把 headers 拼成 ffmpeg 认的 `K: V\r\n` 形式。
func headerArg(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	var b strings.Builder
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(headers[k])
		b.WriteString("\r\n")
	}
	return b.String()
}

// HumanSize 只是给日志/提示用。
func HumanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + "B"
	}
	if n < 1024*1024 {
		return strconv.FormatInt(n/1024, 10) + "KB"
	}
	return strconv.FormatInt(n/(1024*1024), 10) + "MB"
}
