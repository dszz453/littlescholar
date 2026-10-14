package baidu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// 这一组锁住百度分享链路上「肉眼看不出来、错了却只报一个误导性错误码」的地方。
// 三条都是线上真实踩过、并且每一个都伪装成了别的问题：
//
//	1. surl 多带了前导 1        -> errno=105「所访问的页面不存在」，看着像分享过期
//	2. body 被 JSON 序列化      -> errno=-12，看着像提取码错
//	3. randsk 没解码就当 sekey  -> errno=200025「提取码输入错误」，看着像密码错
//
// 三者都会被旧的错误映射翻译成「分享链接已失效」，把排查方向引向"链接是不是过期了"。
// 所以这里全部按"服务端会怎么拒绝"来构造假服务端，而不是只断言字符串长什么样。

// fakePan 造一个能识别上述三种错误的假 pan.baidu.com。
//
// 行为刻意模仿线上实测结果（同一分享、同一时刻的对照实验）：
//
//	/share/verify?surl=1xxxx  -> 105
//	/share/verify?surl=xxxx   -> 0 + randsk
//	/api/shorturlinfo         -> shareid / uk
//	/share/list               -> 没有 BDCLND cookie 就 -9；shorturl 带 1 也 -9
//	/share/transfer           -> sekey 被二次编码就 200025
func fakePan(t *testing.T, log *[]string) http.HandlerFunc {
	t.Helper()
	rec := func(s string) {
		if log != nil {
			*log = append(*log, s)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		// ⚠️ 读完 body 必须复位再交给 ParseForm —— 否则流已到末尾，
		// ParseForm 拿不到任何字段，测试会"因为自己的 bug"而失败，
		// 看起来却像是被测量的代码有问题。
		r.Body = io.NopCloser(bytes.NewReader(raw))

		switch {
		case strings.HasSuffix(r.URL.Path, "/share/verify"):
			rec("verify " + r.URL.RawQuery + " # " + string(raw))
			// 模仿真实服务端：surl 带前导 1 就回 105
			if strings.HasPrefix(r.URL.Query().Get("surl"), "1") {
				_, _ = w.Write([]byte(`{"errno":105,"err_msg":""}`))
				return
			}
			if err := r.ParseForm(); err != nil || r.PostFormValue("pwd") == "" {
				_, _ = w.Write([]byte(`{"errno":-12}`))
				return
			}
			if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
				_, _ = w.Write([]byte(`{"errno":-12}`))
				return
			}
			// randsk 刻意带上需要转义的字符，逼出"必须解码"这条约束
			_, _ = w.Write([]byte(`{"errno":0,"randsk":"ab%2Fcd%3D"}`))

		case strings.HasSuffix(r.URL.Path, "/api/gettemplatevariable"):
			rec("gettemplatevariable " + r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"errno":0,"result":{"bdstoken":"tk-123"}}`))

		case strings.HasSuffix(r.URL.Path, "/api/shorturlinfo"):
			rec("shorturlinfo " + r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"errno":-9,"shareid":28547068284,"uk":623310390}`))

		case strings.HasSuffix(r.URL.Path, "/share/list"):
			rec("list " + r.URL.RawQuery + " # cookie=" + r.Header.Get("Cookie"))
			if !strings.Contains(r.Header.Get("Cookie"), "BDCLND=") {
				_, _ = w.Write([]byte(`{"errno":-9}`))
				return
			}
			if strings.HasPrefix(r.URL.Query().Get("shorturl"), "1") {
				_, _ = w.Write([]byte(`{"errno":-9}`))
				return
			}
			// ⚠️ fs_id / size / isdir 在这里**故意用字符串** —— 线上实测
			// /share/list 就是给字符串的（而 /api/list 给数字）。声明成 int64
			// 会让整条响应解析失败，报「cannot unmarshal string into …」，
			// 与分享本身无关却同样把转存卡死。
			_, _ = w.Write([]byte(`{"errno":0,"list":[{"fs_id":"31245039880635",` +
				`"path":"/a.mp4","server_filename":"a.mp4","isdir":"0","size":"12"}]}`))

		case strings.HasSuffix(r.URL.Path, "/share/transfer"):
			rec("transfer " + r.URL.RawQuery)
			// 二次编码的 sekey 里会出现 %25
			if strings.Contains(r.URL.Query().Get("sekey"), "%25") {
				_, _ = w.Write([]byte(`{"errno":200025,"show_msg":"提取码输入错误，请重试"}`))
				return
			}
			if r.URL.Query().Get("sekey") != "ab/cd=" {
				_, _ = w.Write([]byte(`{"errno":2}`))
				return
			}
			_, _ = w.Write([]byte(`{"errno":0,"info":[{"errno":0,"path":"/a.mp4"}]}`))

		default:
			rec("unexpected " + r.URL.Path)
			_, _ = w.Write([]byte(`{"errno":0}`))
		}
	}
}

func newTestBaidu(t *testing.T, log *[]string) *Baidu {
	t.Helper()
	srv := httptest.NewServer(fakePan(t, log))
	t.Cleanup(srv.Close)
	return &Baidu{
		http:    adapter.NewHTTPClient(5 * time.Second),
		panBase: srv.URL,
		tokens:  make(map[string]tokenEntry),
	}
}

func testCred() *adapter.Credential {
	return &adapter.Credential{DiskType: adapter.DiskBaidu, Cookie: "BDUSS=x; STOKEN=y"}
}

// TestUnlockShareStripsLeadingOneFromSurl 是本组最值钱的一条。
//
// 假服务端按线上实测行为拒绝带前导 1 的 surl（回 105），所以只要有人
// 把 normalizeShareCode 去掉、或改成"保留原样"，这里立刻红。
func TestUnlockShareStripsLeadingOneFromSurl(t *testing.T) {
	var log []string
	b := newTestBaidu(t, &log)
	if _, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd1"); err != nil {
		t.Fatalf("unlockShare 失败：%v（%v）", err, log)
	}
	joined := strings.Join(log, " | ")
	if strings.Contains(joined, "surl=1abcDEF") {
		t.Errorf("surl 不能带前导 1（实测恒返回 errno=105），实际：%s", joined)
	}
	if !strings.Contains(joined, "surl=abcDEF") {
		t.Errorf("surl 应去掉前导 1，实际：%s", joined)
	}
}

func TestUnlockShareSendsFormEncodedBody(t *testing.T) {
	var log []string
	b := newTestBaidu(t, &log)
	tk, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd1")
	if err != nil {
		t.Fatalf("unlockShare 失败：%v（%v）", err, log)
	}
	joined := strings.Join(log, " | ")
	if !strings.Contains(joined, "pwd=pwd1") {
		t.Errorf("请求体应是 form 编码且含 pwd=pwd1，实际：%s", joined)
	}
	if strings.Contains(joined, `{"pwd"`) {
		t.Errorf("请求体不能是 JSON（服务端解析不到 pwd，一律 -12）：%s", joined)
	}
	// randsk 是 URL 编码过的，作为 sekey 前必须解码，否则服务端回 200025。
	if tk.SeKey != "ab/cd=" {
		t.Errorf("SeKey 应是解码后的 randsk，实际 %q", tk.SeKey)
	}
	if tk.BDCLND != "ab%2Fcd%3D" {
		t.Errorf("BDCLND 应是原样的（编码）randsk，实际 %q", tk.BDCLND)
	}
}

func TestShareMetaComesFromShorturlinfo(t *testing.T) {
	var log []string
	b := newTestBaidu(t, &log)
	tk, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd")
	if err != nil {
		t.Fatalf("unlockShare 失败：%v", err)
	}
	// /share/verify 的响应里没有 shareid/uk，必须另外去 shorturlinfo 取。
	// 旧代码直接读 verify 响应，拿到的恒是 0，转存时 from="0"。
	if tk.ShareID != "28547068284" || tk.UK != "623310390" {
		t.Errorf("shareid/uk 应来自 shorturlinfo，实际 %s / %s", tk.ShareID, tk.UK)
	}
	if !strings.Contains(strings.Join(log, " | "), "shorturlinfo") {
		t.Errorf("应当调用过 shorturlinfo，实际：%v", log)
	}
}

// TestShareVerifyUsesBrowserUAAndShareReferer 网页接口只认浏览器 UA。
func TestUnlockShareUsesBrowserUAAndShareReferer(t *testing.T) {
	var ua, referer string
	var log []string
	b := newTestBaidu(t, &log)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/share/verify") {
			ua = r.Header.Get("User-Agent")
			referer = r.Header.Get("Referer")
		}
		fakePan(t, nil)(w, r)
	}))
	t.Cleanup(srv.Close)
	b.panBase = srv.URL

	if _, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "p"); err != nil {
		t.Fatalf("unlockShare 失败: %v", err)
	}
	if strings.Contains(ua, "netdisk;P2SP") {
		t.Errorf("分享类网页接口要浏览器 UA，客户端 UA 会被拒，实际 %q", ua)
	}
	if !strings.Contains(referer, "/s/abcDEF") {
		t.Errorf("Referer 应是分享页地址（且不带前导 1），实际 %q", referer)
	}
}

func TestUnlockShareMapsErrnoIntoMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":-62,"err_msg":""}`))
	}))
	t.Cleanup(srv.Close)
	b := &Baidu{http: adapter.NewHTTPClient(5 * time.Second), panBase: srv.URL,
		tokens: make(map[string]tokenEntry)}

	_, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd1")
	if err == nil {
		t.Fatal("errno=-62 应当返回错误")
	}
	// 错误里必须带上 errno：以前只说"分享已失效"，把"链接真过期"和
	// "我们这边参数不对"混成了一句，排查时无从下手。
	if !strings.Contains(err.Error(), "errno=-62") {
		t.Errorf("错误信息应带 errno 便于区分原因，实际：%v", err)
	}
}

func TestUnlockShareNeedPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":-9}`))
	}))
	t.Cleanup(srv.Close)
	b := &Baidu{http: adapter.NewHTTPClient(5 * time.Second), panBase: srv.URL,
		tokens: make(map[string]tokenEntry)}

	if _, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", ""); err != adapter.ErrNeedPassword {
		t.Errorf("errno=-9 应映射为 ErrNeedPassword，实际 %v", err)
	}
}

// TestListShareDirCarriesBDCLNDCookie /share/list 必须带 BDCLND cookie。
//
// randsk 作为 query 参数 sekey 传过去会回 -9（"还需要提取码"），
// 只有放进 Cookie 才认。同一请求只差这一处，实测从 -9 变 0。
func TestListShareDirCarriesBDCLNDCookie(t *testing.T) {
	var log []string
	b := newTestBaidu(t, &log)
	tk, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd")
	if err != nil {
		t.Fatalf("unlockShare 失败：%v", err)
	}
	files, err := b.listShareDir(context.Background(), testCred(), tk, "")
	if err != nil {
		t.Fatalf("listShareDir 失败（多半是 BDCLND cookie 没带上）：%v（%v）", err, log)
	}
	if len(files) != 1 || files[0].Name != "a.mp4" || files[0].IsDir {
		t.Errorf("解析结果不对：%+v", files)
	}
	// 字符串形态的 fs_id 要能正常解析出来（flexInt64）
	if files[0].ID != "31245039880635" {
		t.Errorf("fs_id 应解析为 31245039880635，实际 %q", files[0].ID)
	}
	if files[0].Size != 12 {
		t.Errorf("size 应解析为 12，实际 %d", files[0].Size)
	}
	joined := strings.Join(log, " | ")
	if !strings.Contains(joined, "BDCLND=ab%2Fcd%3D") {
		t.Errorf("Cookie 里应带原样（编码）的 BDCLND，实际：%s", joined)
	}
	if strings.Contains(joined, "sekey=") {
		t.Errorf("randsk 不应作为 query 参数 sekey 传给 /share/list（会回 -9），实际：%s", joined)
	}
}

// TestSaveToDirSendsDecodedSeKey 转存的 sekey 必须是解码后的值。
func TestSaveToDirSendsDecodedSeKey(t *testing.T) {
	var log []string
	b := newTestBaidu(t, &log)
	tk, err := b.unlockShare(context.Background(), testCred(), "1abcDEF", "pwd")
	if err != nil {
		t.Fatalf("unlockShare 失败：%v", err)
	}
	share := &adapter.ShareInfo{
		ShareURL: "https://pan.baidu.com/s/1abcDEF",
		RootID:   tk.ShareID,
		OwnerID:  tk.UK,
		SeKey:    tk.SeKey,
		Files:    []adapter.FileItem{{ID: "99", Name: "a.mp4"}},
	}
	if _, err := b.SaveToDir(context.Background(), testCred(), share, "/"); err != nil {
		t.Fatalf("SaveToDir 失败（多半是 sekey 编码不对）：%v（%v）", err, log)
	}
	joined := strings.Join(log, " | ")
	if !strings.Contains(joined, "sekey=ab%2Fcd%3D") {
		t.Errorf("sekey 应是解码值再被 url 编码一次（ab%%2Fcd%%3D），实际：%s", joined)
	}
}

// TestFlexInt64AcceptsBothForms 百度同一个字段会在数字/字符串之间摇摆。
//
// 实测：/share/list 的 fs_id 是 "31245039880635"（字符串），
// /api/list 的 fs_id 是 31245039880635（数字）。用 int64 接字符串那条时，
// json.Unmarshal 会让**整条响应**失败 —— 线上就是这么炸的。
// 顺带注意 fs_id 已经是 3.1e13，超过 float64 的安全整数范围，不能走浮点。
func TestFlexInt64AcceptsBothForms(t *testing.T) {
	var v struct {
		A flexInt64 `json:"a"`
		B flexInt64 `json:"b"`
		C flexInt64 `json:"c"`
		D flexInt64 `json:"d"`
	}
	body := `{"a":"31245039880635","b":31245039880635,"c":"","d":null}`
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("两种形态都应能解析：%v", err)
	}
	if v.A != 31245039880635 || v.B != 31245039880635 {
		t.Errorf("解析结果不对：a=%d b=%d", v.A, v.B)
	}
	if v.C != 0 || v.D != 0 {
		t.Errorf("空串/null 应落到 0：c=%d d=%d", v.C, v.D)
	}
	if got := v.A.Int64(); got != 31245039880635 {
		t.Errorf("Int64() = %d", got)
	}
}

// TestNormalizeShareCode 兜住边界：不以前导 1 开头的短码原样保留。
func TestNormalizeShareCode(t *testing.T) {
	cases := map[string]string{
		"1abcDEF": "abcDEF",
		"abcDEF":  "abcDEF",
		"1":       "1", // 只有一个字符时不能把它删空
		"":        "",
		"11a":     "1a",
	}
	for in, want := range cases {
		if got := normalizeShareCode(in); got != want {
			t.Errorf("normalizeShareCode(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestAPIBaseFollowsPanBase 单测必须能把 /api/* 一起接住，
// 否则测试会悄悄打到真实的 pan.baidu.com。
func TestAPIBaseFollowsPanBase(t *testing.T) {
	b := &Baidu{panBase: "http://127.0.0.1:1234"}
	if got := b.api(); got != "http://127.0.0.1:1234/api" {
		t.Errorf("api() 应跟随 panBase，实际 %q", got)
	}
	if _, err := url.Parse(b.api()); err != nil {
		t.Errorf("api() 应当是可解析的 URL: %v", err)
	}
}
