package quark

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient 极简 http 客户端，用于需要读取 Set-Cookie 的场景
type httpClient struct {
	c *http.Client
}

func (h *httpClient) do(req *http.Request) (string, error) {
	if h.c == nil {
		h.c = &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // 不自动跳转，便于抓取 cookie
			},
		}
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// 排空 body
	_, _ = io.Copy(io.Discard, resp.Body)

	var pairs []string
	for _, ck := range resp.Cookies() {
		if ck.Name == "" || ck.Value == "" {
			continue
		}
		pairs = append(pairs, ck.Name+"="+ck.Value)
	}
	return strings.Join(pairs, "; "), nil
}
