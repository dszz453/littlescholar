package baidu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// 这一组锁住百度扫码登录链路上「读代码看不出来、错了也不报错」的地方。
//
// 背景：用户报的是"百度网盘扫码无法登录，后台和客户端都是这样"。
// 两边的扫码实现是同一份代码，所以一起坏 —— 这本身就说明问题在**共享的参数口径**上，
// 而不是某个界面。
//
// 定位方式是把百度登录页自己发的请求抓出来（CDP 监听 passport.baidu.com/v2/）：
//
//	getqrcode?lp=pc&qrloginfrom=pc&gid=…&tpl=netdisk&apiver=v3&…
//	unicast?channel_id=…&gid=…&tpl=netdisk&_sdkFrom=1&apiver=v3&…
//
// 对照当时的实现，两处对不上：
//
//	1. getqrcode **没有 tpl**，轮询却用 tpl=netdisk —— 生成与查询挂在两个口径上；
//	2. gid 每次现生成、轮询侧完全不传 —— 等于换台设备去问别人的通道。
//
// 这两种错都**不会返回错误码**，只会让"手机确认之后网盘侧永远等不到结果"，
// 表现就是扫码没反应。所以这里不测"函数返回了什么"，而是测"请求里到底带了什么"。

const testSign = "0123456789abcdef0123456789abcdef"

// fakePassport 假 passport 域：记录两个请求的 query，并按状态返回。
func fakePassport(t *testing.T, qrQuery, uniQuery *url.Values, uniBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/v2/api/getqrcode"):
			*qrQuery = q
			_, _ = w.Write([]byte(`{"errno":0,"sign":"` + testSign + `",` +
				`"imgurl":"passport.baidu.com/v2/api/qrcode?sign=` + testSign + `"}`))
		case strings.HasSuffix(r.URL.Path, "/channel/unicast"):
			*uniQuery = q
			_, _ = w.Write([]byte(uniBody))
		default:
			_, _ = w.Write([]byte(`{"errno":0}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// statusScanned 是 unicast 的"已扫码待确认"响应：channel_v 是 URL 编码的 JSON。
const statusScanned = `{"errno":0,"channel_v":"%7B%22status%22%3A1%7D"}`

func newQRTestBaidu(srv *httptest.Server) *Baidu {
	return &Baidu{
		http:         adapter.NewHTTPClient(5 * time.Second),
		passportBase: srv.URL,
		tokens:       make(map[string]tokenEntry),
	}
}

// TestGetQRCodeSendsTplAndGid 生成端必须带 tpl=netdisk 与 gid。
func TestGetQRCodeSendsTplAndGid(t *testing.T) {
	var qrQuery, uniQuery url.Values
	srv := fakePassport(t, &qrQuery, &uniQuery, statusScanned)
	b := newQRTestBaidu(srv)

	qr, err := b.GetQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetQRCode 失败: %v", err)
	}

	if got := qrQuery.Get("tpl"); got != "netdisk" {
		t.Errorf("getqrcode 必须带 tpl=netdisk（百度登录页实测如此），实际 tpl=%q", got)
	}
	gid := qrQuery.Get("gid")
	if gid == "" {
		t.Errorf("getqrcode 必须带 gid —— 百度用 (channel_id, gid) 定位扫码会话")
	}
	if got := qrQuery.Get("lp"); got != "pc" {
		t.Errorf("lp 应为 pc，实际 %q", got)
	}
	if got := qrQuery.Get("qrloginfrom"); got != "pc" {
		t.Errorf("qrloginfrom 应为 pc，实际 %q", got)
	}

	// sessionID 必须把 gid 带回来，否则轮询那端无从复用。
	if !strings.Contains(qr.SessionID, "|"+gid) {
		t.Errorf("sessionID 里应带上 gid（形如 baidu_<channel_id>|<gid>），实际 %q", qr.SessionID)
	}
	if want := "baidu_" + testSign; !strings.HasPrefix(qr.SessionID, want) {
		t.Errorf("sessionID 应以 %q 开头，实际 %q", want, qr.SessionID)
	}
}

// TestPollQRCodeReusesSameGid 轮询端必须用**同一个 gid**，且 tpl 与生成端一致。
//
// 这是本轮最关键的一条：以前轮询压根不带 gid，而"没带"和"带了别的值"
// 在服务端看是一回事 —— 都不是当初登记这个通道的那台设备。
func TestPollQRCodeReusesSameGid(t *testing.T) {
	var qrQuery, uniQuery url.Values
	srv := fakePassport(t, &qrQuery, &uniQuery, statusScanned)
	b := newQRTestBaidu(srv)

	qr, err := b.GetQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetQRCode 失败: %v", err)
	}
	res, err := b.PollQRCode(context.Background(), qr.SessionID)
	if err != nil {
		t.Fatalf("PollQRCode 失败: %v", err)
	}
	if res.Status != "scanned" {
		t.Fatalf("status 应为 scanned，实际 %q（message=%s）", res.Status, res.Message)
	}

	if got, want := uniQuery.Get("channel_id"), testSign; got != want {
		t.Errorf("channel_id 应为 %q，实际 %q", want, got)
	}
	if got, want := uniQuery.Get("gid"), qrQuery.Get("gid"); got != want {
		t.Errorf("轮询的 gid 必须与生成时一致：生成 %q，轮询 %q", want, got)
	}
	if got := uniQuery.Get("tpl"); got != "netdisk" {
		t.Errorf("unicast 必须带 tpl=netdisk（与生成端同口径），实际 %q", got)
	}
	if got := uniQuery.Get("_sdkFrom"); got != "1" {
		t.Errorf("unicast 应照百度登录页带 _sdkFrom=1，实际 %q", got)
	}
}

// TestPollQRCodeAcceptsLegacySession 老形态 sessionID（没有 gid）仍要能用。
//
// 用户手里可能还捏着改版前拿到的 session；让它们直接失效是没必要的伤害。
func TestPollQRCodeAcceptsLegacySession(t *testing.T) {
	var qrQuery, uniQuery url.Values
	srv := fakePassport(t, &qrQuery, &uniQuery, statusScanned)
	b := newQRTestBaidu(srv)

	res, err := b.PollQRCode(context.Background(), "baidu_"+testSign)
	if err != nil {
		t.Fatalf("旧格式 sessionID 不该报错: %v", err)
	}
	if res.Status != "scanned" {
		t.Fatalf("status 应为 scanned，实际 %q", res.Status)
	}
	if got := uniQuery.Get("channel_id"); got != testSign {
		t.Errorf("channel_id 应为 %q，实际 %q", testSign, got)
	}
	if got := uniQuery.Get("gid"); got != "" {
		t.Errorf("旧格式没有 gid 时不该凭空造一个，实际 %q", got)
	}
}

// TestGetQRCodeNeverReturnsExternalImage 是本轮的**回归断言**。
//
// 改之前，Image 被填成了 passport.baidu.com 的**外链**：
//
//	"image": "https://passport.baidu.com/v2/api/qrcode?sign=…&lp=pc&qrloginfrom=pc"
//
// 于是上层 handler 判断"驱动自带图片"，跳过 qrgen，前端直接把它当 <img src>
// 指到第三方站点。二维码能不能看见，取决于客户端**当时能不能连上百度** ——
// 离线、内网、被墙、或浏览器策略稍严一点，就是一片空白，
// 而卡片上还写着"请用手机扫描二维码"。
//
// 夸克/UC 只回 Content（由 qrgen 渲染成内嵌 data URL），所以它们从没出过这事。
func TestGetQRCodeNeverReturnsExternalImage(t *testing.T) {
	var qrQuery, uniQuery url.Values
	srv := fakePassport(t, &qrQuery, &uniQuery, statusScanned)
	b := newQRTestBaidu(srv)

	qr, err := b.GetQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetQRCode 失败: %v", err)
	}

	if qr.Image != "" {
		t.Errorf("Image 不该是外链（实际 %q）—— 外链必须丢掉，交给上层用 Content 渲染内嵌二维码", qr.Image)
	}
	// content 必须保留，而且要补上协议头 —— 它是"扫码后打开的那个地址"。
	if want := "https://passport.baidu.com/v2/api/qrcode?sign=" + testSign; qr.Content != want {
		t.Errorf("Content 应为 %q，实际 %q", want, qr.Content)
	}
}

// TestGetQRCodeKeepsInlineImage 驱动真给了自包含图片就照用，不要一刀切丢掉。
func TestGetQRCodeKeepsInlineImage(t *testing.T) {
	const inline = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errno":0,"sign":"` + testSign + `","img":"` + inline + `"}`))
	}))
	t.Cleanup(srv.Close)
	b := newQRTestBaidu(srv)

	qr, err := b.GetQRCode(context.Background())
	if err != nil {
		t.Fatalf("GetQRCode 失败: %v", err)
	}
	if qr.Image != inline {
		t.Errorf("自包含 base64 应原样保留，实际 %q", qr.Image)
	}
}

// TestIsInlineImage 表驱动覆盖判定边界。
func TestIsInlineImage(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		why  string
	}{
		{"", false, "空串"},
		{"data:image/png;base64,iVBORw0KGgo=", true, "data URL"},
		{"iVBORw0KGgoAAAANSUhEUg==", true, "纯 base64"},
		{"https://passport.baidu.com/v2/api/qrcode?sign=x", false, "http 外链"},
		{"passport.baidu.com/v2/api/qrcode?sign=x", false, "无协议的外链也是外链"},
		{"//passport.baidu.com/x.png", false, "协议相对 URL"},
		{"<html>", false, "HTML 片段"},
	}
	for _, c := range cases {
		if got := isInlineImage(c.in); got != c.want {
			t.Errorf("isInlineImage(%q) = %v，期望 %v（%s）", c.in, got, c.want, c.why)
		}
	}
}
