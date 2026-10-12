package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// UserHandler 用户管理
type UserHandler struct {
	db   *gorm.DB
	perm *service.PermissionService
}

func NewUserHandler(db *gorm.DB, perm *service.PermissionService) *UserHandler {
	return &UserHandler{db: db, perm: perm}
}

// List 用户列表
func (h *UserHandler) List(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	keyword := c.Query("keyword")
	role := c.Query("role")

	q := h.db.Model(&model.User{})
	if keyword != "" {
		q = q.Where("username LIKE ? OR nickname LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if role != "" {
		q = q.Where("role = ?", role)
	}

	var total int64
	q.Count(&total)

	var users []model.User
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}

	// 附授权数量
	type out struct {
		model.User
		GrantCount int64 `json:"grantCount"`
	}
	list := make([]out, 0, len(users))
	for _, u := range users {
		var cnt int64
		h.db.Model(&model.UserCategoryGrant{}).Where("user_id = ?", u.ID).Count(&cnt)
		list = append(list, out{User: u, GrantCount: cnt})
	}
	util.Page(c, total, page, pageSize, list)
}

type createUserRequest struct {
	Username  string      `json:"username" binding:"required"`
	Password  string      `json:"password" binding:"required"`
	Nickname  string      `json:"nickname"`
	Grade     string      `json:"grade"`
	Age       int         `json:"age"`
	Avatar    string      `json:"avatar"`
	ExpiresAt *time.Time  `json:"expiresAt"`
	Grants    []grantItem `json:"grants"`
}

type grantItem struct {
	CategoryID  uint64     `json:"categoryId"`
	CanDownload bool       `json:"canDownload"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

// Create 创建用户
func (h *UserHandler) Create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "用户名和密码不能为空")
		return
	}
	if len(req.Password) < 4 {
		util.BadRequest(c, "密码长度至少 4 位")
		return
	}

	var exists int64
	h.db.Model(&model.User{}).Where("username = ?", req.Username).Count(&exists)
	if exists > 0 {
		util.BadRequest(c, "用户名已存在")
		return
	}

	hash, err := util.HashPassword(req.Password)
	if err != nil {
		util.ServerError(c, "密码加密失败")
		return
	}

	user := &model.User{
		Username:     req.Username,
		PasswordHash: hash,
		Nickname:     orDefaultStr(req.Nickname, req.Username),
		Role:         model.RoleKid,
		Grade:        req.Grade,
		Age:          req.Age,
		Avatar:       req.Avatar,
		Status:       model.UserStatusEnabled,
		ExpiresAt:    req.ExpiresAt,
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return h.applyGrants(tx, user.ID, req.Grants)
	})
	if err != nil {
		util.ServerError(c, "创建用户失败")
		return
	}
	util.OK(c, gin.H{"id": user.ID})
}

// applyGrants 写入目录授权
func (h *UserHandler) applyGrants(tx *gorm.DB, userID uint64, grants []grantItem) error {
	if len(grants) == 0 {
		return nil
	}
	rows := make([]model.UserCategoryGrant, 0, len(grants))
	for _, g := range grants {
		if g.CategoryID == 0 {
			continue
		}
		rows = append(rows, model.UserCategoryGrant{
			UserID:      userID,
			CategoryID:  g.CategoryID,
			CanDownload: g.CanDownload,
			ExpiresAt:   g.ExpiresAt,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}

type updateUserRequest struct {
	Nickname  *string    `json:"nickname"`
	Grade     *string    `json:"grade"`
	Age       *int       `json:"age"`
	Avatar    *string    `json:"avatar"`
	Status    *int8      `json:"status"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// Update 更新用户
func (h *UserHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的用户 ID")
		return
	}
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		util.NotFound(c, "用户不存在")
		return
	}
	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.Grade != nil {
		updates["grade"] = *req.Grade
	}
	if req.Age != nil {
		updates["age"] = *req.Age
	}
	if req.Avatar != nil {
		updates["avatar"] = *req.Avatar
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.ExpiresAt != nil {
		updates["expires_at"] = req.ExpiresAt
	}
	if len(updates) == 0 {
		util.OK(c, user)
		return
	}
	if err := h.db.Model(&user).Updates(updates).Error; err != nil {
		util.ServerError(c, "更新失败")
		return
	}
	h.perm.InvalidateUser(id) // 状态或有效期可能已变，清缓存
	h.db.First(&user, id)
	util.OK(c, user)
}

// ResetPassword 重置密码
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的用户 ID")
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Password) < 4 {
		util.BadRequest(c, "密码长度至少 4 位")
		return
	}
	hash, err := util.HashPassword(req.Password)
	if err != nil {
		util.ServerError(c, "密码加密失败")
		return
	}
	if err := h.db.Model(&model.User{}).Where("id = ?", id).Update("password_hash", hash).Error; err != nil {
		util.ServerError(c, "重置失败")
		return
	}
	util.OKWithMessage(c, "密码已重置", nil)
}

// SetGrants 设置用户目录授权（覆盖式）
func (h *UserHandler) SetGrants(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的用户 ID")
		return
	}
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		util.NotFound(c, "用户不存在")
		return
	}
	var req struct {
		Grants []grantItem `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.UserCategoryGrant{}).Error; err != nil {
			return err
		}
		return h.applyGrants(tx, id, req.Grants)
	})
	if err != nil {
		util.ServerError(c, "设置授权失败")
		return
	}
	h.perm.InvalidateUser(id) // 授权已变，清掉该用户权限缓存
	util.OKWithMessage(c, "授权已更新", nil)
}

// GetGrants 获取用户授权
func (h *UserHandler) GetGrants(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的用户 ID")
		return
	}
	var grants []model.UserCategoryGrant
	h.db.Preload("Category").Where("user_id = ?", id).Find(&grants)
	util.OK(c, grants)
}

// Delete 删除用户
func (h *UserHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的用户 ID")
		return
	}
	if id == util.CurrentUserID(c) {
		util.BadRequest(c, "不能删除自己")
		return
	}
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		util.NotFound(c, "用户不存在")
		return
	}
	if user.Role == model.RoleAdmin {
		var adminCount int64
		h.db.Model(&model.User{}).Where("role = ? AND id != ?", model.RoleAdmin, id).Count(&adminCount)
		if adminCount == 0 {
			util.BadRequest(c, "系统必须保留至少一个管理员")
			return
		}
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.UserCategoryGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", id).Delete(&model.PlayRecord{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, id).Error
	})
	if err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	h.perm.InvalidateUser(id) // 用户已删，清缓存
	util.OKWithMessage(c, "删除成功", nil)
}

func orDefaultStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
