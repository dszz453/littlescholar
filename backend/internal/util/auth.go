package util

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword 生成 bcrypt 密码哈希
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword 校验密码
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// TokenType token 类型
const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

// Claims JWT 载荷
type Claims struct {
	UserID   uint64 `json:"uid"`
	Username string `json:"uname"`
	Role     string `json:"role"`
	Type     string `json:"typ"`
	jwt.RegisteredClaims
}

// JWTManager JWT 管理器
type JWTManager struct {
	secret     []byte
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewJWTManager(secret, issuer string, accessHours, refreshHours int) *JWTManager {
	return &JWTManager{
		secret:     []byte(secret),
		issuer:     issuer,
		accessTTL:  time.Duration(accessHours) * time.Hour,
		refreshTTL: time.Duration(refreshHours) * time.Hour,
	}
}

// Generate 生成 access + refresh token
func (m *JWTManager) Generate(userID uint64, username, role string) (access, refresh string, expiresAt int64, err error) {
	now := time.Now()
	accessExp := now.Add(m.accessTTL)
	refreshExp := now.Add(m.refreshTTL)

	accessClaims := &Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Type:     TokenAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExp),
		},
	}
	refreshClaims := &Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Type:     TokenRefresh,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExp),
		},
	}

	access, err = jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(m.secret)
	if err != nil {
		return "", "", 0, err
	}
	refresh, err = jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(m.secret)
	if err != nil {
		return "", "", 0, err
	}
	return access, refresh, accessExp.Unix(), nil
}

// Parse 解析并校验 token
func (m *JWTManager) Parse(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("签名方法异常")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("token 无效")
	}
	return claims, nil
}
