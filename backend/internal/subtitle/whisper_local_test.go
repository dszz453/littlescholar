package subtitle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本地 whisper 这条路上，"错了但不报错"的地方比云端更多：
//   - 模型路径填成目录 / 填错：不 stat 就判"可用" → 每次播放都起一个注定失败的任务；
//   - 抽音轨写成 mp3 而不是 16k WAV：whisper 报 "failed to read audio file"，
//     看着像文件损坏；
//   - 参数名拼错（-np / -tp 是后加的短参数）：whisper-cli 只回一段 usage，
//     表现是"转写永远是空"。
// 下面每条都是照着这些故障形状写的。

// fakeWhisper 造一套"能通过 Enabled() 检查"的 bin + model。
func fakeWhisper(t *testing.T) (*LocalWhisper, string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	model := filepath.Join(dir, "model.bin")
	for _, p := range []string{bin, model} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &LocalWhisper{Bin: bin, Model: model, Language: "en", Threads: 3}, bin, model
}

func TestLocalWhisperEnabledNeedsRealFiles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(model, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		w    *LocalWhisper
		want bool
	}{
		{"齐全", &LocalWhisper{Bin: bin, Model: model}, true},
		// 配置有值但文件不在：这是镜像没带模型时的真实形态，
		// 必须判不可用，否则会不断起失败任务 + 写 24 小时冷却。
		{"模型不存在", &LocalWhisper{Bin: bin, Model: filepath.Join(dir, "nope.bin")}, false},
		{"bin 不存在", &LocalWhisper{Bin: filepath.Join(dir, "nope"), Model: model}, false},
		{"没配 bin", &LocalWhisper{Model: model}, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.w.Enabled(); got != c.want {
				t.Fatalf("Enabled() = %v, want %v", got, c.want)
			}
		})
	}
}

// 模型目录里同时有好几个时，必须挑**量化过的英文 tiny**。
//
// 这条优先级是照着线上实测定的，不是照着"质量越高越好"定的：
// 目标机是 Celeron J1900（无 AVX / 无 F16C），F16 权重每层都要软件转 F32，
// 同一段 3 秒音频 base.en(F16) >100s、tiny.en(F16) 100s、tiny.en-q5_1 62s。
// 挑错的后果不是报错，而是"一集要转半小时" —— 没人会去查，只会觉得这功能坏了。
func TestLocalWhisperResolveModelPrefersQuantizedTiny(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"ggml-small.en-q5_1.bin",
		"ggml-base.en.bin",
		"ggml-base.en-q5_1.bin",
		"ggml-tiny.en.bin",       // F16，同尺寸但慢一倍
		"ggml-tiny.en-q5_1.bin",  // 要的是它
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := &LocalWhisper{Model: dir}
	got, err := w.resolveModel()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "ggml-tiny.en-q5_1.bin" {
		t.Fatalf("挑中 %s，想要 ggml-tiny.en-q5_1.bin", filepath.Base(got))
	}
	// 目录形态也要能通过 Enabled（bin 得是真的存在）
	bin := filepath.Join(dir, "whisper-cli")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.Bin = bin
	if !w.Enabled() {
		t.Fatal("目录里有模型却判不可用")
	}
}

// 只有非量化版时也得能用 —— 宁可慢，不能"整个不可用"。
func TestLocalWhisperResolveModelFallsBackToNonQuantized(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"ggml-base.en.bin", "ggml-tiny.en.bin"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&LocalWhisper{Model: dir}).resolveModel()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "ggml-tiny.en.bin" {
		t.Fatalf("挑中 %s，想要 ggml-tiny.en.bin", filepath.Base(got))
	}
}

func TestLocalWhisperEmptyModelDirNotEnabled(t *testing.T) {
	dir := t.TempDir()
	if (&LocalWhisper{Bin: dir + "/x", Model: dir}).Enabled() {
		t.Fatal("空目录不该判可用")
	}
}

// 参数与输出：ffmpeg 产出的 wav 必须原样传给 -f，
// 输出必须走 -osrt + -of（仓库里的 SRT→VTT 转换要靠这个文件）。
func TestLocalWhisperArgsAndSrtOutput(t *testing.T) {
	w, bin, model := fakeWhisper(t)
	audio := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(audio, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotName string
	var gotArgs []string
	w.run = func(ctx context.Context, name string, args []string) ([]byte, error) {
		gotName, gotArgs = name, args
		// 模拟 whisper-cli：把 SRT 写到 -of 前缀 + .srt
		var prefix string
		for i, a := range args {
			if a == "-of" && i+1 < len(args) {
				prefix = args[i+1]
			}
		}
		return []byte(""), os.WriteFile(prefix+".srt",
			[]byte("1\n00:00:00,000 --> 00:00:02,000\nHello there.\n"), 0o644)
	}

	got, err := w.TranscribeFile(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Hello there.") {
		t.Fatalf("没读回字幕：%q", got)
	}
	if gotName != bin {
		t.Fatalf("跑的不是配置的 bin：%s", gotName)
	}

	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"-m " + model, "-f " + audio, "-osrt", "-of ", "-t 3", "-l en",
		// 解码策略：默认 5 beams 在这台弱 CPU 上纯烧时间；
		// -nf 关掉温度回退，否则音乐/静音段会被解码五六遍。
		"-bs 1", "-bo 1", "-nf",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("参数里缺 %q：%s", want, joined)
		}
	}
	// whisper.cpp 不开 ffmpeg 支持时只认 wav；这里若把 mp3 塞进去，
	// 报错是 "failed to read audio file"，看着像文件坏了。
	if strings.Contains(joined, ".mp3") {
		t.Fatalf("参数里出现了 mp3：%s", joined)
	}
}

// 没产出 srt（静音、模型异常退出但 exit=0）：必须报错，
// 不能返回空串让上层以为"转写成功但没人说话"。
func TestLocalWhisperMissingSrtIsError(t *testing.T) {
	w, _, _ := fakeWhisper(t)
	w.run = func(ctx context.Context, name string, args []string) ([]byte, error) {
		return []byte("whisper_full_with_state: failed"), nil
	}
	if _, err := w.TranscribeFile(context.Background(), filepath.Join(t.TempDir(), "a.wav")); err == nil {
		t.Fatal("没有 srt 却没报错")
	}
}

func TestLocalWhisperCommandFailureCarriesOutput(t *testing.T) {
	w, _, _ := fakeWhisper(t)
	w.run = func(ctx context.Context, name string, args []string) ([]byte, error) {
		return []byte("error: unknown argument: -np\nusage: whisper-cli [options]"), context.DeadlineExceeded
	}
	_, err := w.TranscribeFile(context.Background(), filepath.Join(t.TempDir(), "a.wav"))
	if err == nil {
		t.Fatal("命令失败却没报错")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Fatalf("报错里没带上命令输出，线上会看不出是参数写错：%v", err)
	}
}

// ===== 与 Transcriber 的协作 =====

func TestTranscriberLocalOnlyIsEnabled(t *testing.T) {
	w, _, _ := fakeWhisper(t)
	tr := &Transcriber{Local: w}
	if !tr.Enabled() {
		t.Fatal("只有本地引擎时判了不可用")
	}
	if tr.Engine() != "local" {
		t.Fatalf("引擎 = %s，想要 local", tr.Engine())
	}
}

// 本地可用时必须**不碰 HTTP**：Base 是空的，一旦走网关必然失败。
// 这条同时守住"以为切本地了、其实还在打网关"的误判。
func TestTranscriberRoutesToLocal(t *testing.T) {
	w, _, _ := fakeWhisper(t)
	w.run = func(ctx context.Context, name string, args []string) ([]byte, error) {
		var prefix string
		for i, a := range args {
			if a == "-of" && i+1 < len(args) {
				prefix = args[i+1]
			}
		}
		return nil, os.WriteFile(prefix+".srt", []byte("1\n00:00:00,000 --> 00:00:01,000\nlocal\n"), 0o644)
	}
	tr := &Transcriber{Local: w} // 刻意不配 Base/Key
	got, err := tr.TranscribeFile(context.Background(), filepath.Join(t.TempDir(), "a.wav"))
	if err != nil {
		t.Fatalf("本地引擎没接住：%v", err)
	}
	if !strings.Contains(got, "local") {
		t.Fatalf("结果不是本地引擎给的：%q", got)
	}
}

func TestTranscriberEngineReport(t *testing.T) {
	if got := (&Transcriber{}).Engine(); got != "off" {
		t.Fatalf("空配置引擎 = %s，想要 off", got)
	}
	if got := (&Transcriber{Base: "http://x/v1", Key: "k"}).Engine(); got != "http" {
		t.Fatalf("只有网关时引擎 = %s，想要 http", got)
	}
	// 本地+网关都在时优先本地：本地不耗额度、不受网关模型缺货影响
	w, _, _ := fakeWhisper(t)
	if got := (&Transcriber{Base: "http://x/v1", Key: "k", Local: w}).Engine(); got != "local" {
		t.Fatalf("两条都在时引擎 = %s，想要 local", got)
	}
}
