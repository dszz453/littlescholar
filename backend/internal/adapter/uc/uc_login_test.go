package uc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// checkLoginSession 是为了拦住一类很容易踩的坑：
// ValidateCookie 原先只打 drive.uc.cn/account/info —— 那是**网页引导接口，
// 游客会话同样返回 success**。线上因此出现过账号状态"有效"、
// 但列目录/转存/播放全部 401 code=31001 require login [guest]。
//
// 这个用例把两个方向都钉住：明确"未登录"要判失效，其它错误不能误杀。
func TestCheckLoginSession(t *testing.T) {
	newUC := func(h http.HandlerFunc) (*UC, func()) {
		srv := httptest.NewServer(h)
		u := New()
		u.probeBase = srv.URL
		return u, srv.Close
	}

	t.Run("明确未登录 → 判失效", func(t *testing.T) {
		u, done := newUC(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"code":31001,"message":"require login [guest]"}`))
		})
		defer done()
		err := u.checkLoginSession(context.Background(), &adapter.Credential{Cookie: "ctoken=guest"})
		if !errors.Is(err, adapter.ErrCookieInvalid) {
			t.Fatalf("期望 ErrCookieInvalid，得到 %v", err)
		}
	})

	t.Run("只按 message 也能认出来", func(t *testing.T) {
		u, done := newUC(func(w http.ResponseWriter, _ *http.Request) {
			// 有些网关会把 code 抹平，只留 message
			_, _ = w.Write([]byte(`{"code":0,"message":"Require Login [guest]"}`))
		})
		defer done()
		if err := u.checkLoginSession(context.Background(), &adapter.Credential{}); !errors.Is(err, adapter.ErrCookieInvalid) {
			t.Fatalf("期望 ErrCookieInvalid，得到 %v", err)
		}
	})

	t.Run("正常会话 → 不判失效", func(t *testing.T) {
		u, done := newUC(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":0,"code":0,"message":"ok","data":{"list":[]}}`))
		})
		defer done()
		if err := u.checkLoginSession(context.Background(), &adapter.Credential{}); err != nil {
			t.Fatalf("不应报错，得到 %v", err)
		}
	})

	t.Run("限流等其它错误 → 不误杀", func(t *testing.T) {
		u, done := newUC(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":42901,"message":"too many requests"}`))
		})
		defer done()
		if err := u.checkLoginSession(context.Background(), &adapter.Credential{}); err != nil {
			t.Fatalf("限流不应判 cookie 失效，得到 %v", err)
		}
	})

	t.Run("网络异常 → 不误杀", func(t *testing.T) {
		u, done := newUC(func(_ http.ResponseWriter, _ *http.Request) {
			time.Sleep(2 * time.Second)
		})
		defer done()
		u.http = adapter.NewHTTPClient(150 * time.Millisecond)
		start := time.Now()
		if err := u.checkLoginSession(context.Background(), &adapter.Credential{}); err != nil {
			t.Fatalf("超时不应判 cookie 失效，得到 %v", err)
		}
		if d := time.Since(start); d > 1500*time.Millisecond {
			t.Errorf("超时控制未生效，耗时 %v", d)
		}
	})
}

// 请求本身要带上 cookie 与 UC 的常规参数，否则线上探到的永远是未登录。
func TestCheckLoginSessionSendsCookieAndQuery(t *testing.T) {
	var gotCookie, gotQuery, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotQuery = r.URL.RawQuery
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	u := New()
	u.probeBase = srv.URL
	if err := u.checkLoginSession(context.Background(), &adapter.Credential{Cookie: "ctoken=abc; __puus=xyz"}); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if gotCookie != "ctoken=abc; __puus=xyz" {
		t.Errorf("cookie 未透传，实际 %q", gotCookie)
	}
	for _, want := range []string{"pdir_fid=0", "fr=pc", "pr=UCBrowser", "_size=1"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("查询串缺 %s，实际 %q", want, gotQuery)
		}
	}
	if gotUA == "" {
		t.Error("User-Agent 未透传（UC 会校验）")
	}
}
