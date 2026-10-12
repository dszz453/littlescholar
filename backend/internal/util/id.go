package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewSessionID 生成会话 ID
func NewSessionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(b)
}

// NewRequestID 生成请求 ID
func NewRequestID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
