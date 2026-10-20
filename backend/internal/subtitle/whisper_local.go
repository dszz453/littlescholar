package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LocalWhisper 在本机跑 whisper.cpp（whisper-cli），把音频转成 SRT。
//
// # 为什么最后走到这条路
//
// 云端的 /audio/transcriptions 只需要"一个能转写的模型名"，听着很简单，
// 实际上被网关卡死：这台库接的中转只有对话/图像/视频模型，
// 四个 whisper 候选名（whisper-1 / gpt-4o-transcribe / whisper-large-v3 /
// whisper）全回 `model_not_found: No available channel for model ...`。
// 而学习资料（新概念课文、绘本、儿歌）网上根本没有现成字幕可搜 ——
// 于是"把模型搬到自己机器上"成了唯一稳的路。
//
// # 为什么是 whisper.cpp 而不是别的
//
// 它是**单文件静态二进制**：不用 pip、不用 CUDA、不用 Python 运行时，
// 塞进现有 alpine 镜像就能跑，跟 Go 服务端一样"拷进去就能用"。
// 量化过的 small.en 只有一百多 MB，4 核 NAS 上跑 10 分钟音频大约 1~2 分钟。
type LocalWhisper struct {
	// Bin whisper-cli 可执行文件路径。
	Bin string
	// Model ggml 模型文件路径。也允许指向一个**目录**，
	// 此时自动挑里面最合适的 ggml-*.bin（见 resolveModel）。
	Model string
	// Language 期望语言，空则由模型自行判断。英文学习资料传 en 更准更快。
	Language string
	// Threads 推理线程数。留 1~2 个核给正在播放的视频，否则孩子那边会卡。
	Threads int

	// run 执行外部命令，测试注入用。默认 exec.CommandContext。
	run func(ctx context.Context, name string, args []string) ([]byte, error)
}

// NewLocalWhisper 构造。threads <= 0 时用默认值。
func NewLocalWhisper(bin, model, language string, threads int) *LocalWhisper {
	return &LocalWhisper{
		Bin:      strings.TrimSpace(bin),
		Model:    strings.TrimSpace(model),
		Language: strings.TrimSpace(language),
		Threads:  threads,
	}
}

// defaultWhisperThreads 默认线程数。
//
// 不取满核：这台 NAS 只有 4 核，听写跑起来的时候孩子很可能正在看视频，
// 把核吃干净的表现是**播放卡顿**，而卡顿看起来像网络问题，极难和听写联系起来。
const defaultWhisperThreads = 3

// Enabled 是否可用。
//
// 必须**真的 stat 一下**而不是只看配置有没有值：镜像/挂载没准备好时
// 配置是有值的，这时如果判定"可用"，每次播放都会起一个注定失败的转写，
// 还会写 24 小时冷却标记 —— 补好文件后一天内都不会再试。
func (w *LocalWhisper) Enabled() bool {
	if w == nil {
		return false
	}
	bin := strings.TrimSpace(w.Bin)
	if bin == "" {
		return false
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return false
	}
	_, err := w.resolveModel()
	return err == nil
}

// resolveModel 定位真正要用的 ggml 模型文件。
//
// 允许配置目录：模型体积大、换模型（base → small）时不想改配置，
// 往目录里丢文件即可。同一个目录里有多个时按**优先级**挑，
// 顺序是"英文 small 量化 > 英文 small > 英文 base > 其它"。
func (w *LocalWhisper) resolveModel() (string, error) {
	p := strings.TrimSpace(w.Model)
	if p == "" {
		return "", fmt.Errorf("未配置 whisper 模型")
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("whisper 模型不存在：%s", p)
	}
	if !st.IsDir() {
		return p, nil
	}

	matches, _ := filepath.Glob(filepath.Join(p, "ggml-*.bin"))
	if len(matches) == 0 {
		return "", fmt.Errorf("目录里没有 ggml-*.bin：%s", p)
	}
	rank := func(name string) int {
		n := strings.ToLower(filepath.Base(name))
		switch {
		case strings.Contains(n, "small.en-q"):
			return 0
		case strings.Contains(n, "small.en"):
			return 1
		case strings.Contains(n, "base.en-q"):
			return 2
		case strings.Contains(n, "base.en"):
			return 3
		case strings.Contains(n, "small"):
			return 4
		case strings.Contains(n, "base"):
			return 5
		}
		return 9
	}
	sort.SliceStable(matches, func(i, j int) bool { return rank(matches[i]) < rank(matches[j]) })
	return matches[0], nil
}

func (w *LocalWhisper) threads() int {
	if w.Threads > 0 {
		return w.Threads
	}
	return defaultWhisperThreads
}

func (w *LocalWhisper) exec(ctx context.Context, name string, args []string) ([]byte, error) {
	if w.run != nil {
		return w.run(ctx, name, args)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// TranscribeFile 把一个音频文件转成 SRT 文本。
//
// 输入必须是 16kHz 单声道 PCM WAV：whisper.cpp 不开 ffmpeg 支持时
// 只认这种容器，喂 mp3 的报错是 "failed to read audio file"，
// 而 worker 那边已经按普通格式抽过音轨了 —— 所以调用方（handler）
// 要在本地引擎下把抽音轨的参数换成 wav（见 extractAudio）。
func (w *LocalWhisper) TranscribeFile(ctx context.Context, path string) (string, error) {
	if !w.Enabled() {
		return "", fmt.Errorf("本地 whisper 不可用（缺 whisper-cli 或模型）")
	}
	model, err := w.resolveModel()
	if err != nil {
		return "", err
	}

	// 输出前缀放在源文件旁边：同一个临时目录随任务一起清掉，
	// 不额外引入一处需要自己收拾的目录。
	//
	// 注意 MkdirTemp 失败时**绝不能**退回去删除 filepath.Dir(path)：
	// 那是整个听写工作目录（还可能有别的任务在里面），
	// 一次清理会把别人的音轨和产物一起删掉。
	outDir, err := os.MkdirTemp(filepath.Dir(path), "asr-out-")
	if err != nil {
		outDir = filepath.Join(os.TempDir(), "asr-out-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if mkErr := os.MkdirAll(outDir, 0o755); mkErr != nil {
			return "", fmt.Errorf("建不了输出目录：%w", mkErr)
		}
	}
	defer os.RemoveAll(outDir)
	prefix := filepath.Join(outDir, "out")

	// 参数只用 whisper-cli 里存在了很多年的几个：
	//   -m 模型 / -f 输入 / -l 语言 / -t 线程 / -osrt 输出 SRT / -of 输出前缀
	// 刻意不写温度、不关打印：这两类的短参数名在各版本间改过名
	// （-tp、-np 都属"新加的"），拼错一个就是整条链路 100% 失败，
	// 而报错只是用法提示 —— 从日志里完全看不出是参数写错了。
	// 温度默认就是 0（贪心解码），不写也是要的行为。
	args := []string{
		"-m", model,
		"-f", path,
		"-t", strconv.Itoa(w.threads()),
		"-osrt",
		"-of", prefix,
	}
	if lang := strings.TrimSpace(w.Language); lang != "" && !strings.EqualFold(lang, "auto") {
		args = append(args, "-l", lang)
	}

	if out, err := w.exec(ctx, strings.TrimSpace(w.Bin), args); err != nil {
		return "", fmt.Errorf("whisper 转写失败：%v：%s", err, whisperSnippet(out))
	}

	srtPath := prefix + ".srt"
	data, err := os.ReadFile(srtPath)
	if err != nil {
		return "", fmt.Errorf("whisper 没有产出字幕文件：%w", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		// 静音/纯音乐：正常结果，交给上层判"音频里没有人声"
		return "", fmt.Errorf("whisper 转写结果为空")
	}
	return text, nil
}

// whisperSnippet 掐一段输出，别把整页进度条塞进日志/失败标记。
func whisperSnippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "（无输出）"
	}
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(s, "\n")
	// 取最后几行：whisper 的关键报错（模型格式不对、读不了音频）总在末尾，
	// 前面是加载模型的常规输出。
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	s = strings.Join(lines, " | ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
