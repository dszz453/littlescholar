package util

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// Encryptor 使用 AES-256-GCM 加密敏感数据（网盘 cookie、AI apiKey）
type Encryptor struct {
	key []byte
}

// NewEncryptor 由配置密钥派生 32 字节密钥
func NewEncryptor(secret string) (*Encryptor, error) {
	if secret == "" {
		return nil, errors.New("加密密钥不能为空")
	}
	// 使用 SHA-256 派生固定长度密钥，兼容任意长度输入
	sum := sha256.Sum256([]byte(secret))
	return &Encryptor{key: sum[:]}, nil
}

// Encrypt 加密并返回 base64 字符串
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 解密 base64 字符串
func (e *Encryptor) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	nonce, data := raw[:ns], raw[ns:]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// MaskSecret 脱敏展示，如 sk-1234****abcd
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 12 {
		return "****"
	}
	return s[:6] + "****" + s[len(s)-4:]
}
