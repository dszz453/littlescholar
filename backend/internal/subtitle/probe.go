package subtitle

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// 这个文件只做两件"读音频"的事：量时长、找有声区间。
//
// 它们服务于「大模型写字幕」那条链路：模型只给文本，时间轴要靠音频自己算。
// 单独放一个文件是因为调用方是 handler（批量任务），和播放链路的
// 「找字幕文件 / 抽内嵌轨」没有关系 —— 混在 finder.go 里会让人以为
// 播放时也要跑 ffmpeg。

// ProbeDuration 读媒体时长（秒）。读不到返回 0。
//
// 本地路径和带 headers 的 http 直链都能读（ffprobe 只读容器头，不下载全片）。
func ProbeDuration(ctx context.Context, ffprobe, input string, headers map[string]string) float64 {
	if strings.TrimSpace(input) == "" {
		return 0
	}
	if strings.TrimSpace(ffprobe) == "" {
		ffprobe = "ffprobe"
	}
	args := []string{"-v", "error"}
	if h := headerArg(headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args,
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", input,
	)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, ffprobe, args...).Output()
	if err != nil {
		return 0
	}
	return ParseDuration(string(out))
}

// DetectSpeech 用 ffmpeg 的 silencedetect 找出**有声区间**。
//
// 参数是踩出来的，不是拍的：
//   - d=0.30：朗读里的换气通常只有一百多毫秒，短于 0.3 秒的停顿不当句子边界；
//   - noise=-32dB：绘本朗读底噪很低，门限再高会把轻声的词也判成静音，
//     句子就会被从中间切断。
//
// total 是总时长：ffmpeg 只告诉我们"哪里是静音"，最后一段有声区间的结尾
// 要靠总时长补出来。total <= 0 时只用 silence_end 之后的片段，可能丢掉尾巴。
//
// 失败（没有音轨 / 网络读不到 / 超时）一律返回 nil —— 调用方会退回按字数均分。
// 这里**不要**返回 error：时间轴糙一点仍可用，为它让整条字幕失败不值得。
func DetectSpeech(ctx context.Context, ffmpeg, input string, headers map[string]string, total float64) []Span {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	if strings.TrimSpace(ffmpeg) == "" {
		ffmpeg = "ffmpeg"
	}
	args := []string{"-nostdin", "-hide_banner", "-v", "info"}
	if h := headerArg(headers); h != "" {
		args = append(args, "-headers", h)
	}
	args = append(args,
		"-i", input,
		// 只解音频：视频片源也走这条路，把画面一起解码纯属浪费
		"-vn", "-map", "0:a:0",
		"-af", "silencedetect=noise=-32dB:d=0.30",
		"-f", "null", "-",
	)

	// 远程直链要顺着读完整个音频；给足时间但不无限等 ——
	// 一轮任务里卡住一条，后面的全排队。
	timeout := 150 * time.Second
	if total > 0 {
		est := time.Duration(total*3) * time.Second
		if est > timeout {
			timeout = est
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, ffmpeg, args...)
	out, _ := cmd.CombinedOutput()
	// 即便退出码非 0，stderr 里也可能已经有可用的 silence 记录（最后被超时打断）。
	return ParseSilenceDetect(string(out), total)
}
