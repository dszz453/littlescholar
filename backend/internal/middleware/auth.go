package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AuthMiddleware JWT 认证中间件
func AuthMiddleware(jwtManager *util.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			util.Unauthorized(c, "缺少认证令牌")
			return
		}
		claims, err := jwtManager.Parse(token)
		if err != nil {
			util.Unauthorized(c, "令牌无效或已过期")
			return
		}
		if claims.Type != util.TokenAccess {
			util.Unauthorized(c, "令牌类型错误")
			return
		}
		c.Set("userID", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		c.Next()
	}
}

// RequireAdmin 要求管理员权限
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !util.IsAdmin(c) {
			util.Forbidden(c, "需要管理员权限")
			return
		}
		c.Next()
	}
}

// RequireActiveUser 要求账号处于有效状态（未禁用、未过期）
// 需要在 AuthMiddleware 之后使用，且需要 db 查询用户状态
func RequireActiveUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := util.CurrentUserID(c)
		if userID == 0 {
			util.Unauthorized(c, "未登录")
			return
		}
		var user model.User
		if err := db.First(&user, userID).Error; err != nil {
			util.Unauthorized(c, "用户不存在")
			return
		}
		if !user.IsActive() {
			if user.IsExpired() {
				util.Forbidden(c, "账号已过期，请联系管理员续期")
			} else {
				util.Forbidden(c, "账号已被禁用")
			}
			return
		}
		c.Set("user", &user)
		c.Next()
	}
}

// extractToken 从 Header 或 query 提取 token
func extractToken(c *gin.Context) string {
	// 1. Authorization: Bearer xxx
	auth := c.GetHeader("Authorization")
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		return auth
	}
	// 2. query 参数（用于播放地址等场景）
	return c.Query("token")
}
