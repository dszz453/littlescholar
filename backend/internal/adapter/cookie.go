package adapter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchCookies 用给定请求头 GET 一个地址，只为了把响应里的 Set-Cookie 捞出来。
//
// 为什么需要它：夸克/UC（以及不少其它网盘）有一批关键 cookie —— 最典型的
// 是 __puus —— **并不在换票接口的响应里下发**，而要等下一次业务请求才随
// Set-Cookie 补上。少这一个 cookie，后续的列目录、取直链都会以 401 收场，
// 表现就是"扫码明明成功了，一用就失效"。
//
// 刻意关掉自动跳转：换票类的接口常常用 302 把 cookie 挂在跳转响应上，
// 跟着跳走反而拿不到。
func FetchCookies(ctx context.Context, rawURL string, headers map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return CookiesOf(resp), nil
}

// CookiesOf 把响应里的 Set-Cookie 拼成 "k=v; k=v" 形式。
func CookiesOf(resp *http.Response) string {
	var pairs []string
	for _, ck := range resp.Cookies() {
		if ck.Name == "" || ck.Value == "" {
			continue
		}
		pairs = append(pairs, ck.Name+"="+ck.Value)
	}
	return strings.Join(pairs, "; ")
}

// MergeCookie 把 extra 里**新增或更新**的 cookie 合进 base。
//
// 同名项取 extra 的值 —— 服务端后下发的才是新的；直接字符串拼接做不到
// 这点，反而会让 base 里一个过期的 __puus 把新值挡住。
func MergeCookie(base, extra string) string {
	if strings.TrimSpace(extra) == "" {
		return base
	}
	jar := map[string]string{}
	var order []string

	absorb := func(s string) {
		for _, part := range strings.Split(s, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, _, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			if _, seen := jar[k]; !seen {
				order = append(order, k)
			}
			jar[k] = part
		}
	}
	absorb(base)
	absorb(extra)

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, jar[k])
	}
	return strings.Join(out, "; ")
}
