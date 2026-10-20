package subtitle

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 假转写网关：照 OpenAI /audio/transcriptions 的形状，记录收到的字段。
//
// 为什么必须搭假的：这条链路上有三处"错了也不报错、只表现为没字幕"的地方 ——
// multipart 字段名（file/model/response_format）、Authorization 头、
// 以及对 `response_format=srt` 返回纯文本的解析。对着真网关测要 key 要额度，
// 等于不测。
type fakeASR struct {
	srv      *httptest.Server
	gotModel string
	gotFmt   string
	gotLang  string
	gotAuth  string
	gotBytes int
}

func newFakeASR(t *testing.T, body string, status int) *fakeASR {
	t.Helper()
	f := &fakeASR{}
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.gotModel = r.FormValue("model")
		f.gotFmt = r.FormValue("response_format")
		f.gotLang = r.FormValue("language")
		f.gotAuth = r.Header.Get("Authorization")
		if fh, _, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(fh)
			f.gotBytes = len(b)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// 造一个真实的小文件（内容是假的也没关系：这里测的是"有没有把字节传上去"）。
func tempAudio(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.mp3")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 1234)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const asrSRT = "1\n00:00:01,000 --> 00:00:03,000\nThis is a pen.\n\n2\n00:00:04,000 --> 00:00:06,000\nThat is a book.\n"

func TestTranscribeSendsProperRequest(t *testing.T) {
	f := newFakeASR(t, asrSRT, http.StatusOK)
	tr := NewTranscriber(f.srv.URL, "sk-test", "", "en")

	srt, err := tr.TranscribeFile(context.Background(), tempAudio(t))
	if err != nil {
		t.Fatalf("转写应当成功：%v", err)
	}
	if !strings.Contains(srt, "This is a pen.") {
		t.Fatalf("字幕正文丢了：%q", srt)
	}
	if f.gotModel != DefaultASRModel {
		t.Fatalf("没传默认模型：%q", f.gotModel)
	}
	if f.gotFmt != "srt" {
		t.Fatalf("没要 srt，解析会走错分支：%q", f.gotFmt)
	}
	if f.gotLang != "en" {
		t.Fatalf("没传语言：%q", f.gotLang)
	}
	if f.gotAuth != "Bearer sk-test" {
		t.Fatalf("鉴权头不对：%q", f.gotAuth)
	}
	if f.gotBytes != 1234 {
		t.Fatalf("上传的字节数不对：%d", f.gotBytes)
	}
}

// 有些网关不理 response_format，一律回 JSON。这时必须能兼容，
// 否则表现是"转写成功了但字幕是空的" —— 最难排查的那一类。
func TestTranscribeAcceptsJSONWrappedSRT(t *testing.T) {
	body := `{"text":"1\n00:00:01,000 --> 00:00:03,000\nHello there.\n"}`
	f := newFakeASR(t, body, http.StatusOK)
	tr := NewTranscriber(f.srv.URL, "k", "", "")

	srt, err := tr.TranscribeFile(context.Background(), tempAudio(t))
	if err != nil {
		t.Fatalf("JSON 包裹的 SRT 应当被接受：%v", err)
	}
	if !strings.Contains(srt, "Hello there.") {
		t.Fatalf("没抠出字幕：%q", srt)
	}
}

func TestTranscribeReportsHTTPError(t *testing.T) {
	f := newFakeASR(t, `{"error":{"message":"model not found"}}`, http.StatusBadRequest)
	tr := NewTranscriber(f.srv.URL, "k", "no-such-model", "")

	_, err := tr.TranscribeFile(context.Background(), tempAudio(t))
	if err == nil {
		t.Fatal("网关报错却当成功 —— 会被当成\"这集没人声\"永久放弃")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("错误里没带状态码，线上分不清是模型名错还是额度用完：%v", err)
	}
}

func TestTranscribeDisabledWithoutKeyOrBase(t *testing.T) {
	if NewTranscriber("", "k", "", "").Enabled() {
		t.Fatal("没有 base 却认为可用")
	}
	if NewTranscriber("http://x/v1", "", "", "").Enabled() {
		t.Fatal("没有 key 却认为可用")
	}
	if _, err := NewTranscriber("", "", "", "").TranscribeFile(context.Background(), "x"); err == nil {
		t.Fatal("未启用却转写成功了")
	}
}
