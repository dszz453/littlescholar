package ai

import (
	"testing"
	"time"
)

// WithTimeout 必须是副本：同一个 AI 配置还要服务聊天这类短请求，
// 改了原实例等于把所有请求的超时一起拉长。
func TestClientWithTimeout(t *testing.T) {
	c := NewClient(Config{BaseURL: "https://example.com/v1", APIKey: "k", Model: "m", TimeoutSec: 5})
	long := c.WithTimeout(180 * time.Second)
	if long == c {
		t.Fatal("应该返回副本，返回了同一个实例")
	}
	if long.http == c.http {
		t.Fatal("HTTPDoer 应该换成新的（超时不同）")
	}
	if long.Model() != c.Model() {
		t.Fatalf("模型名被改掉了：%q → %q", c.Model(), long.Model())
	}
	if got := c.WithTimeout(0); got != c {
		t.Fatal("非正超时应该原样返回")
	}
}
