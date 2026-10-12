package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AuthHandler 认证处理
type AuthHandler struct {
	db  *gorm.DB
	jwt *util.JWTManager
}

func NewAuthHandler(db *gorm.DB, jwt *util.JWTManager) *AuthHandler {
	return &AuthHandler{db: db, jwt: jwt}
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 登录（后台与 C 端共用，通过路径区分要求的角色）
func (h *AuthHandler) Login(requireRole string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			util.BadRequest(c, "请输入用户名和密码")
			return
		}

		var user model.User
		if err := h.db.Where("username = ?", req.Username).First(&user).Error; err != nil {
			util.Unauthorized(c, "用户名或密码错误")
			return
		}
		if !util.CheckPassword(user.PasswordHash, req.Password) {
			util.Unauthorized(c, "用户名或密码错误")
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
		if requireRole == model.RoleAdmin && user.Role != model.RoleAdmin {
			util.Forbidden(c, "非管理员账号")
			return
		}

		access, refresh, expiresAt, err := h.jwt.Generate(user.ID, user.Username, user.Role)
		if err != nil {
			util.ServerError(c, "生成令牌失败")
			return
		}

		now := time.Now()
		h.db.Model(&user).Update("last_login_at", now)

		util.OK(c, gin.H{
			"token":        access,
			"refreshToken": refresh,
			"expiresAt":    expiresAt,
			"user": gin.H{
				"id":       user.ID,
				"username": user.Username,
				"nickname": user.Nickname,
				"role":     user.Role,
				"avatar":   user.Avatar,
				"grade":    user.Grade,
			},
		})
	}
}

// Refresh 刷新 token
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refreshToken" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "缺少 refreshToken")
		return
	}
	claims, err := h.jwt.Parse(req.RefreshToken)
	if err != nil || claims.Type != util.TokenRefresh {
		util.Unauthorized(c, "refreshToken 无效或已过期")
		return
	}

	var user model.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil {
		util.Unauthorized(c, "用户不存在")
		return
	}
	if !user.IsActive() {
		util.Forbidden(c, "账号不可用")
		return
	}

	access, refresh, expiresAt, err := h.jwt.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		util.ServerError(c, "生成令牌失败")
		return
	}
	util.OK(c, gin.H{"token": access, "refreshToken": refresh, "expiresAt": expiresAt})
}

// Profile 当前用户信息
func (h *AuthHandler) Profile(c *gin.Context) {
	userID := util.CurrentUserID(c)
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		util.Unauthorized(c, "用户不存在")
		return
	}

	resp := gin.H{
		"id":        user.ID,
		"username":  user.Username,
		"nickname":  user.Nickname,
		"role":      user.Role,
		"avatar":    user.Avatar,
		"grade":     user.Grade,
		"age":       user.Age,
		"status":    user.Status,
		"expiresAt": user.ExpiresAt,
	}

	// 非管理员返回授权目录
	if user.Role != model.RoleAdmin {
		var grants []model.UserCategoryGrant
		h.db.Preload("Category").Where("user_id = ?", user.ID).Find(&grants)
		resp["grants"] = grants
	}
	util.OK(c, resp)
}

// Logout 退出登录（无状态 JWT，前端删除 token 即可）
func (h *AuthHandler) Logout(c *gin.Context) {
	util.OKWithMessage(c, "已退出登录", nil)
}
