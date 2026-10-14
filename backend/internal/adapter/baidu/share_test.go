package baidu

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// 这一组锁住「百度分享接口的请求体必须是 form 编码」。
//
// 为什么值得专门写测试：两种 body 都会失败，但错误码不同 ——
//
//	JSON body -> errno=-12（参数压根没被解析）
//	form body -> errno=105（未登录，属于另一个层面）
//
// 而旧代码把所有非 0 都映射成 ErrShareExpired，于是线上每一条百度转存
// 都报「分享链接已失效」，可那个链接明明是好的（同一个链接
// /api/shorturlinfo 能正常返回 shareid/uk）。这句错误信息成功地把排查
// 方向引向了「是不是链接过期了」，绕了很久才回到请求本身。
//
// 光读代码看不出 form 与 JSON 的差别（两者都是"发个 body 过去"），
// 只有把请求截下来断言才靠得住。

func newTestBaidu(t *testing.T, h http.HandlerFunc) *Baidu {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Baidu{
		http:    adapter.NewHTTPClient(5 * time.Second),
		panBase: srv.URL,
		tokens:  make(map[string]tokenEntry),
	}
}

func TestShareVerifySendsFormEncodedBody(t *testing.T) {
	var gotCT, gotBody, gotQuery string
	b := newTestBaidu(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotQuery = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		// ⚠️ 读完 body 必须复位再交给 ParseForm —— 否则流已到末尾，
		// ParseForm 拿不到任何字段，测试会"因为自己的 bug"而失败，
		// 看起来却像是被测量的代码有问题。
		r.Body = io.NopCloser(bytes.NewReader(raw))
		// 模仿真实服务端：按 form 解析，取不到 pwd 就回 -12
		// （这条判据与线上实测一致：JSON body 一律 -12，form body 才会
		//  走到 105「未登录」那一层）
		if err := r.ParseForm(); err != nil || r.PostFormValue("pwd") == "" {
			_, _ = w.Write([]byte(`{"errno":-12}`))
			return
		}
		if strings.HasPrefix(strings.TrimSpace(gotBody), "{") {
			_, _ = w.Write([]byte(`{"errno":-12}`))
			return
		}
		_, _ = w.Write([]byte(`{"errno":0,"randsk":"RS","shareid":123,"uk":456}`))
	})

	cred := &adapter.Credential{DiskType: adapter.DiskBaidu, Cookie: "BDUSS=x"}
	shareID, uk, randsk, err := b.shareVerify(context.Background(), cred, "1abcDEF", "pwd1")
	if err != nil {
		t.Fatalf("shareVerify 失败了：%v（服务端收到的 body=%q）", err, gotBody)
	}
	if !strings.Contains(gotCT, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type 应为表单，实际 %q", gotCT)
	}
	if !strings.Contains(gotBody, "pwd=pwd1") {
		t.Errorf("请求体应含 pwd=pwd1（form 编码），实际 %q", gotBody)
	}
	if strings.HasPrefix(strings.TrimSpace(gotBody), "{") {
		t.Errorf("请求体不能是 JSON：%q", gotBody)
	}
	// surl 必须保留前导的 1（去掉就变成 errno=2「链接出错了」，实测过）
	if !strings.Contains(gotQuery, "surl=1abcDEF") {
		t.Errorf("surl 参数应原样保留分享短码，实际 query=%q", gotQuery)
	}
	if shareID != 123 || uk != 456 || randsk != "RS" {
		t.Errorf("解析结果不对：shareid=%d uk=%d randsk=%q", shareID, uk, randsk)
	}
}

func TestShareVerifyUsesBrowserUAAndShareReferer(t *testing.T) {
	var ua, referer string
	b := newTestBaidu(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		referer = r.Header.Get("Referer")
		_, _ = w.Write([]byte(`{"errno":0,"randsk":"RS","shareid":1,"uk":2}`))
	})

	cred := &adapter.Credential{DiskType: adapter.DiskBaidu, Cookie: "BDUSS=x"}
	if _, _, _, err := b.shareVerify(context.Background(), cred, "1abcDEF", "p"); err != nil {
		t.Fatalf("shareVerify 失败: %v", err)
	}
	if strings.Contains(ua, "netdisk;P2SP") {
		t.Errorf("分享类网页接口要浏览器 UA，客户端 UA 会被拒，实际 %q", ua)
	}
	if !strings.Contains(referer, "/s/1abcDEF") {
		t.Errorf("Referer 应是分享页地址，实际 %q", referer)
	}
}

func TestShareVerifyMapsErrnoIntoMessage(t *testing.T) {
	b := newTestBaidu(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":-12,"err_msg":""}`))
	})
	cred := &adapter.Credential{DiskType: adapter.DiskBaidu, Cookie: "BDUSS=x"}
	_, _, _, err := b.shareVerify(context.Background(), cred, "1abcDEF", "pwd1")
	if err == nil {
		t.Fatal("errno=-12 应当返回错误")
	}
	// 错误里必须带上 errno：以前只说"分享已失效"，把"链接真过期"和
	// "我们这边登录态不完整"混成了一句，排查时无从下手。
	if !strings.Contains(err.Error(), "errno=-12") {
		t.Errorf("错误信息应带 errno 便于区分原因，实际：%v", err)
	}
}

func TestShareVerifyNeedPassword(t *testing.T) {
	b := newTestBaidu(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":-9}`))
	})
	cred := &adapter.Credential{DiskType: adapter.DiskBaidu, Cookie: "BDUSS=x"}
	_, _, _, err := b.shareVerify(context.Background(), cred, "1abcDEF", "")
	if err != adapter.ErrNeedPassword {
		t.Errorf("errno=-9 应映射为 ErrNeedPassword，实际 %v", err)
	}
}
