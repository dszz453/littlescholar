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
//
// # 速度的真实数字（别按"应该很快"去设计）
//
// 2026-10-20 在目标 NAS（Intel Celeron J1900，4 核，无 AVX / 无 F16C）实测：
// tiny.en-q5_1 处理一个 30 秒窗口约 50~60 秒，即 **0.5x 实时**；
// 而 whisper 会把不足 30 秒的音频补齐到 30 秒算，所以"一条 3 秒的提示音"
// 和"30 秒的句子"成本一样。换成 3 分钟的绘本音频就是 5~7 分钟。
//
// 这个量级决定了产品形态：**字幕只能是"提前生成、下次就有"**，
// 不能指望孩子点开就等。所以 collector 的批跑、以及"字幕生成中"的
// 前端提示都不是可选项 —— 它们是这套东西能不能用的前提。
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
	// 顺序按"这台机器能不能在合理时间内跑完"排，不是按识别质量排。
	//
	// ⚠️ 2026-10-20 线上实测（Celeron J1900 / 无 AVX / 4 核）：
	//   base.en（F16, 141MB）跑 3 秒音频 > 100s 还没结束；
	//   tiny.en（F16, 74MB）100s；tiny.en-q5_1（30MB）62s。
	// 差距主要来自**权重量化**：这台 CPU 没有 F16C（为兼容 AVX 一并关掉了），
	// F16 权重每层都要先软件转成 F32 才能算，等于凭空多一倍内存搬运。
	// 换成 q5_1（走整数点积）立刻快一倍，这比换小模型有效得多。
	rank := func(name string) int {
		n := strings.ToLower(filepath.Base(name))
		switch {
		case strings.Contains(n, "tiny.en-q"):
			return 0
		case strings.Contains(n, "tiny.en"):
			return 1
		case strings.Contains(n, "base.en-q"):
			return 2
		case strings.Contains(n, "base.en"):
			return 3
		case strings.Contains(n, "small.en-q"):
			return 4
		case strings.Contains(n, "small.en"):
			return 5
		case strings.Contains(n, "tiny-q"), strings.Contains(n, "tiny"):
			return 6
		case strings.Contains(n, "base"):
			return 7
		case strings.Contains(n, "small"):
			return 8
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
	//
	// 解码策略三个参数是**后加的**（Dockerfile 把 whisper.cpp 钉在 v1.7.4，
	// 这三个在那一版的 --help 里确认过），加它们是因为线上实测差距明显：
	//
	//   -bs 1 -bo 1  默认是 `5 beams + best of 5` —— 每一步都要维护 5 条候选，
	//                对清晰朗读的课文没有收益，在这台 4 核弱 CPU 上纯烧时间。
	//                代价是遇到口音重/背景嘈杂时略差，可接受。
	//   -nf          关掉温度回退。开头是音乐、或者整段没人声时，
	//                默认会因为置信度低而把温度 0→0.2→…→1.0 逐个重试，
	//                等于同一段音频解码五六遍；表现是"这段音频特别慢"。
	//                关掉后这类音频只是识别结果差，不会卡住整条队列。
	//
	// 不写 -tp（温度）：默认就是 0，贪心。
	args := []string{
		"-m", model,
		"-f", path,
		"-t", strconv.Itoa(w.threads()),
		"-bs", "1",
		"-bo", "1",
		"-nf",
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
