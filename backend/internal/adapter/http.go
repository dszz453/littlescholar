package adapter

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultUA 通用 User-Agent
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// HTTPClient 网盘请求通用客户端
type HTTPClient struct {
	client *http.Client
}

// NewHTTPClient 创建客户端
func NewHTTPClient(timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPClient{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

// Request 构造请求
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Query   map[string]string
	Body    interface{} // 会被 JSON 序列化
	RawBody []byte
}

// Response 包装响应
type Response struct {
	StatusCode int
	Body       []byte
	Header     http.Header
}

// Do 执行请求
func (c *HTTPClient) Do(ctx context.Context, req *Request) (*Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("URL 解析失败: %w", err)
	}
	if len(req.Query) > 0 {
		q := u.Query()
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	var bodyReader io.Reader
	if req.RawBody != nil {
		bodyReader = bytes.NewReader(req.RawBody)
	} else if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("请求体序列化失败: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	if _, ok := req.Headers["User-Agent"]; !ok {
		httpReq.Header.Set("User-Agent", DefaultUA)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if req.Body != nil && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}, nil
}

// DoJSON 执行请求并解析 JSON 到 out
func (c *HTTPClient) DoJSON(ctx context.Context, req *Request, out interface{}) (*Response, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, fmt.Errorf("响应解析失败: %w (原始内容: %s)", err, truncate(string(resp.Body), 300))
		}
	}
	return resp, nil
}

// CookieJar 简易 cookie 管理
type CookieJar map[string]string

// ParseCookie 解析 cookie 字符串到 map
func ParseCookie(s string) CookieJar {
	jar := make(CookieJar)
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			jar[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return jar
}

// String 转回 cookie 字符串
func (j CookieJar) String() string {
	var parts []string
	for k, v := range j {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// Get 取 cookie 值
func (j CookieJar) Get(key string) string { return j[key] }

// Set 设置 cookie
func (j CookieJar) Set(key, val string) { j[key] = val }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
