package subtitle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Transcriber 用 OpenAI 兼容的 /audio/transcriptions 把音轨转成字幕。
//
// 为什么是这条路：这套库是**英语学习资料**（新概念课文、英文绘本、儿歌），
// 不是电影电视剧 —— 网上根本没有现成字幕可搜，OpenSubtitles 那条路
// 对学习资料等于零。唯一能拿到字幕的办法是**自己听写**。
//
// 之所以直接要 response_format=srt 而不是 json/verbose_json：
// 仓库里已经有一份久经考验的 SRT→VTT 转换（含编码嗅探、时间轴清洗），
// 再解析一遍分段 JSON 属于重复造轮子，还容易在毫秒取整上出错。
type Transcriber struct {
	// Base 形如 https://host/v1（末尾斜杠会被去掉）
	Base string
	// Key 复用后台"AI 配置"里那枚 key
	Key string
	// Model 转写模型名。默认 whisper-1（OpenAI 兼容网关的通用叫法）。
	Model string
	// Language 期望语言，空则让模型自己判断。英文学习资料传 en 更稳。
	Language string
	Client   *http.Client

	// mu 保护的只有"当前确认可用的模型名"。
	mu        sync.Mutex
	knownGood string
}

// DefaultASRModel 默认转写模型。
const DefaultASRModel = "whisper-1"

// asrModelCandidates 模型名兜底候选。
//
// 为什么要试一串：各家中转网关对转写模型的叫法不统一（whisper-1 /
// gpt-4o-transcribe / whisper-large-v3 …），而"模型不存在"在它们那里
// 通常只是 400 或 404 + 一句英文 —— 从报错里根本看不出该填什么。
// 挨个试一遍、把成功的那个记住，比让用户去猜要靠谱。
var asrModelCandidates = []string{"whisper-1", "gpt-4o-transcribe", "whisper-large-v3", "whisper"}

// Enabled 是否可用。
func (t *Transcriber) Enabled() bool {
	return t != nil && strings.TrimSpace(t.Base) != "" && strings.TrimSpace(t.Key) != ""
}

func (t *Transcriber) base() string {
	return strings.TrimRight(strings.TrimSpace(t.Base), "/")
}

func (t *Transcriber) model() string {
	if m := strings.TrimSpace(t.Model); m != "" {
		return m
	}
	return DefaultASRModel
}

func (t *Transcriber) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	// 转写是"上传音频 + 等服务端识别"，比普通对话慢得多：
	// OpenAI 官方对 25MB 内的文件通常一两分钟内返回，这里给足 6 分钟。
	return &http.Client{Timeout: 6 * time.Minute}
}

// TranscribeFile 把一个音频文件转成 SRT 文本。
//
// filename 只用于 multipart 里的文件名（网关按扩展名判断容器格式，
// 传错会让它报"不支持的格式"），必须带真实扩展名。
func (t *Transcriber) TranscribeFile(ctx context.Context, path string) (string, error) {
	if !t.Enabled() {
		return "", fmt.Errorf("语音转写未启用")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读音频失败：%w", err)
	}

	models := t.modelsToTry()
	var firstErr error
	for _, m := range models {
		srt, err := t.post(ctx, filepath.Base(path), data, m)
		if err == nil {
			t.setKnownGood(m)
			return srt, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		// 只有"模型名不对"才值得换一个再试；
		// 额度用完、鉴权失败、文件太大重试多少次都一样，别浪费请求。
		if !looksLikeModelError(err) {
			return "", err
		}
	}
	return "", firstErr
}

// post 发一次转写请求。
func (t *Transcriber) post(ctx context.Context, name string, data []byte, model string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("读音频失败：%w", err)
	}
	_ = mw.WriteField("model", model)
	_ = mw.WriteField("response_format", "srt")
	if lang := strings.TrimSpace(t.Language); lang != "" {
		_ = mw.WriteField("language", lang)
	}
	// 温度 0：听写要的是"它到底说了什么"，不是"合理的猜测"
	_ = mw.WriteField("temperature", "0")
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.base()+"/audio/transcriptions", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+t.Key)

	resp, err := t.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("转写服务连不上")
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("转写失败（HTTP %d，model=%s）：%s",
			resp.StatusCode, model, snippet(body))
	}
	// 正常情况下正文就是 SRT；但有些网关不管 response_format，一律回 JSON，
	// 所以这里要能兼容 —— 否则表现是"转写成功了但字幕是空的"，极难排查。
	if text := srtFromBody(body); text != "" {
		return text, nil
	}
	return "", fmt.Errorf("转写结果里没有字幕文本（model=%s）：%s", model, snippet(body))
}

// modelsToTry 依次尝试的模型名：已确认可用的排最前，然后是配置的，再是候选。
func (t *Transcriber) modelsToTry() []string {
	t.mu.Lock()
	good := t.knownGood
	t.mu.Unlock()

	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	add(good)
	add(t.model())
	for _, m := range asrModelCandidates {
		add(m)
	}
	return out
}

// setKnownGood 记住跑通的模型名，之后不再挨个试。
func (t *Transcriber) setKnownGood(m string) {
	t.mu.Lock()
	t.knownGood = m
	t.mu.Unlock()
}

// looksLikeModelError 判断"换个模型名可能就好了"。
func looksLikeModelError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "model") || strings.Contains(s, "http 404")
}

// srtFromBody 从响应正文里抠出 SRT。
func srtFromBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return s
	}
	// 退路：网关回了个 JSON，里面的 text 字段是纯文本（带不带时间轴看实现）。
	var v struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		if strings.TrimSpace(v.Text) != "" && looksLikeSRT(v.Text) {
			return v.Text
		}
	}
	return ""
}

// snippet 截断错误正文，避免把整页 HTML 塞进日志。
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// NewTranscriber 构造。base 形如 https://host/v1；model 留空用 whisper-1。
func NewTranscriber(base, key, model, language string) *Transcriber {
	return &Transcriber{
		Base:     strings.TrimSpace(base),
		Key:      strings.TrimSpace(key),
		Model:    strings.TrimSpace(model),
		Language: strings.TrimSpace(language),
	}
}
