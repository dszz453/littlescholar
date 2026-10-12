package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPDoer 轻量 HTTP 客户端（供外部服务调用）
type HTTPDoer struct {
	client *http.Client
}

func NewHTTPDoer(timeout time.Duration) *HTTPDoer {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPDoer{
		client: &http.Client{Timeout: timeout},
	}
}

// PostJSON POST JSON 并解析响应
func (d *HTTPDoer) PostJSON(ctx context.Context, url string, body interface{}, headers map[string]string, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("响应解析失败: %w (原始: %s)", err, truncateStr(string(data), 300))
		}
	}
	return nil
}

// GetJSON GET 并解析响应
func (d *HTTPDoer) GetJSON(ctx context.Context, url string, headers map[string]string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateStr(string(data), 300))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("响应解析失败: %w", err)
		}
	}
	return nil
}

// GetRaw GET 返回原始字节
func (d *HTTPDoer) GetRaw(ctx context.Context, url string, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
