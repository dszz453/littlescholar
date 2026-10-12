package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	_ "github.com/littlescholar/core/internal/adapter/aliyun"
	_ "github.com/littlescholar/core/internal/adapter/baidu"
	_ "github.com/littlescholar/core/internal/adapter/quark"
	_ "github.com/littlescholar/core/internal/adapter/tianyi"
	_ "github.com/littlescholar/core/internal/adapter/uc"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// DiskHandler 网盘账号管理
type DiskHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
}

func NewDiskHandler(db *gorm.DB, encryptor *util.Encryptor) *DiskHandler {
	return &DiskHandler{db: db, encryptor: encryptor}
}

// Types 支持的网盘类型列表
func (h *DiskHandler) Types(c *gin.Context) {
	var out []gin.H
	for _, t := range adapter.List() {
		out = append(out, gin.H{
			"type":         string(t.Type()),
			"name":         t.Name(),
			"supportQR":    true,
			"supportManual": true,
		})
	}
	util.OK(c, out)
}

// List 账号列表
func (h *DiskHandler) List(c *gin.Context) {
	var accounts []model.DiskAccount
	if err := h.db.Order("disk_type asc, id asc").Find(&accounts).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.OK(c, accounts)
}

// GetQRCode 获取扫码二维码
func (h *DiskHandler) GetQRCode(c *gin.Context) {
	var req struct {
		DiskType string `json:"diskType" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请指定网盘类型")
		return
	}
	a, err := adapter.Get(adapter.DiskType(req.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	qr, err := a.GetQRCode(ctx)
	if err != nil {
		util.ServerError(c, "获取二维码失败: "+err.Error())
		return
	}

	// 持久化会话，便于多实例 / 重启后仍可轮询
	sess := &model.QRLoginSession{
		SessionID: qr.SessionID,
		DiskType:  req.DiskType,
		Status:    "waiting",
		QRContent: qr.Content,
		QRImage:   qr.Image,
		ExpiresAt: time.Now().Add(time.Duration(qr.ExpiresIn) * time.Second),
	}
	h.db.Create(sess)

	util.OK(c, gin.H{
		"sessionId": qr.SessionID,
		"qrContent": qr.Content,
		"qrImage":   qr.Image,
		"expiresIn": qr.ExpiresIn,
	})
}

// PollQRCode 轮询扫码状态
func (h *DiskHandler) PollQRCode(c *gin.Context) {
	sessionID := c.Param("sessionId")
	if sessionID == "" {
		util.BadRequest(c, "缺少会话 ID")
		return
	}

	var sess model.QRLoginSession
	if err := h.db.Where("session_id = ?", sessionID).First(&sess).Error; err != nil {
		util.NotFound(c, "扫码会话不存在")
		return
	}
	if time.Now().After(sess.ExpiresAt) {
		h.db.Model(&sess).Update("status", "expired")
		util.OK(c, gin.H{"status": "expired"})
		return
	}

	a, err := adapter.Get(adapter.DiskType(sess.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	result, err := a.PollQRCode(ctx, sessionID)
	if err != nil {
		util.OK(c, gin.H{"status": "waiting", "message": err.Error()})
		return
	}

	resp := gin.H{"status": result.Status, "message": result.Message}

	// 确认成功 → 落库
	if result.Status == "confirmed" && result.Cookie != "" {
		encrypted, err := h.encryptor.Encrypt(result.Cookie)
		if err != nil {
			util.ServerError(c, "凭据加密失败")
			return
		}
		acc := &model.DiskAccount{
			DiskType:        sess.DiskType,
			Nickname:        orDefaultStr(result.Nickname, adapter.DiskTypeNames[adapter.DiskType(sess.DiskType)]),
			Avatar:          result.Avatar,
			CookieEncrypted: encrypted,
			CookiePreview:   util.MaskSecret(result.Cookie),
			Status:          model.DiskAccountValid,
		}
		if len(result.Extra) > 0 {
			extra := model.JSONMap{}
			for k, v := range result.Extra {
				extra[k] = v
			}
			acc.Extra = extra
		}
		// 同类型第一个账号自动设为默认
		var cnt int64
		h.db.Model(&model.DiskAccount{}).Where("disk_type = ?", sess.DiskType).Count(&cnt)
		acc.IsDefault = cnt == 0

		if err := h.db.Create(acc).Error; err != nil {
			util.ServerError(c, "保存账号失败")
			return
		}
		h.db.Model(&sess).Updates(map[string]interface{}{
			"status": "confirmed", "account_id": acc.ID, "cookie_raw": "",
		})
		resp["accountId"] = acc.ID
		resp["nickname"] = acc.Nickname
	} else {
		h.db.Model(&sess).Update("status", result.Status)
	}

	util.OK(c, resp)
}

// AddManual 手动添加（粘贴 Cookie / token）
func (h *DiskHandler) AddManual(c *gin.Context) {
	var req struct {
		DiskType string            `json:"diskType" binding:"required"`
		Cookie   string            `json:"cookie" binding:"required"`
		Nickname string            `json:"nickname"`
		Extra    map[string]string `json:"extra"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "网盘类型和 Cookie 不能为空")
		return
	}
	if !adapter.IsSupported(adapter.DiskType(req.DiskType)) {
		util.BadRequest(c, "不支持的网盘类型")
		return
	}

	// 校验 cookie 有效性
	a, _ := adapter.Get(adapter.DiskType(req.DiskType))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	cred := &adapter.Credential{
		DiskType: adapter.DiskType(req.DiskType),
		Cookie:   strings.TrimSpace(req.Cookie),
		Extra:    req.Extra,
	}
	info, err := a.ValidateCookie(ctx, cred)
	if err != nil {
		util.BadRequest(c, "Cookie 校验失败: "+err.Error())
		return
	}

	encrypted, err := h.encryptor.Encrypt(req.Cookie)
	if err != nil {
		util.ServerError(c, "凭据加密失败")
		return
	}

	acc := &model.DiskAccount{
		DiskType:        req.DiskType,
		Nickname:        orDefaultStr(req.Nickname, info.Nickname, a.Name()),
		Avatar:          info.Avatar,
		CookieEncrypted: encrypted,
		CookiePreview:   util.MaskSecret(req.Cookie),
		Status:          model.DiskAccountValid,
		CapacityUsed:    info.CapacityUsed,
		CapacityTotal:   info.CapacityTotal,
		LastCheckAt:     ptrTime(time.Now()),
	}
	if len(req.Extra) > 0 {
		extra := model.JSONMap{}
		for k, v := range req.Extra {
			extra[k] = v
		}
		acc.Extra = extra
	}
	var cnt int64
	h.db.Model(&model.DiskAccount{}).Where("disk_type = ?", req.DiskType).Count(&cnt)
	acc.IsDefault = cnt == 0

	if err := h.db.Create(acc).Error; err != nil {
		util.ServerError(c, "保存账号失败")
		return
	}
	util.OK(c, gin.H{"id": acc.ID, "nickname": acc.Nickname})
}

// Validate 校验账号
func (h *DiskHandler) Validate(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	acc, cred, err := h.loadAccount(id)
	if err != nil {
		util.NotFound(c, err.Error())
		return
	}

	a, err := adapter.Get(adapter.DiskType(acc.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	info, err := a.ValidateCookie(ctx, cred)
	now := time.Now()
	if err != nil {
		h.db.Model(acc).Updates(map[string]interface{}{
			"status": model.DiskAccountInvalid, "last_check_at": now,
		})
		util.OK(c, gin.H{"valid": false, "message": err.Error()})
		return
	}
	h.db.Model(acc).Updates(map[string]interface{}{
		"status":         model.DiskAccountValid,
		"last_check_at":  now,
		"nickname":       orDefaultStr(info.Nickname, acc.Nickname),
		"avatar":         info.Avatar,
		"capacity_used":  info.CapacityUsed,
		"capacity_total": info.CapacityTotal,
	})
	util.OK(c, gin.H{"valid": true, "info": info})
}

// UpdateCookie 更新 Cookie
func (h *DiskHandler) UpdateCookie(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var req struct {
		Cookie string `json:"cookie" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "Cookie 不能为空")
		return
	}
	var acc model.DiskAccount
	if err := h.db.First(&acc, id).Error; err != nil {
		util.NotFound(c, "账号不存在")
		return
	}
	encrypted, err := h.encryptor.Encrypt(req.Cookie)
	if err != nil {
		util.ServerError(c, "加密失败")
		return
	}
	h.db.Model(&acc).Updates(map[string]interface{}{
		"cookie_encrypted": encrypted,
		"cookie_preview":   util.MaskSecret(req.Cookie),
		"status":           model.DiskAccountValid,
	})
	util.OKWithMessage(c, "Cookie 已更新", nil)
}

// SetDefault 设为默认账号
func (h *DiskHandler) SetDefault(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var acc model.DiskAccount
	if err := h.db.First(&acc, id).Error; err != nil {
		util.NotFound(c, "账号不存在")
		return
	}
	h.db.Transaction(func(tx *gorm.DB) error {
		tx.Model(&model.DiskAccount{}).Where("disk_type = ?", acc.DiskType).Update("is_default", false)
		return tx.Model(&acc).Update("is_default", true).Error
	})
	util.OKWithMessage(c, "已设为默认", nil)
}

// Delete 删除账号
func (h *DiskHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	if err := h.db.Delete(&model.DiskAccount{}, id).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "删除成功", nil)
}

// Browse 浏览网盘目录（用于选择转存目标）
func (h *DiskHandler) Browse(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	dirID := c.Query("dirId")

	acc, cred, err := h.loadAccount(id)
	if err != nil {
		util.NotFound(c, err.Error())
		return
	}
	a, err := adapter.Get(adapter.DiskType(acc.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	items, err := a.ListDir(ctx, cred, dirID)
	if err != nil {
		util.ServerError(c, "浏览目录失败: "+err.Error())
		return
	}
	util.OK(c, gin.H{"dirId": dirID, "items": items})
}

// MakeDir 在网盘新建目录
func (h *DiskHandler) MakeDir(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var req struct {
		ParentID string `json:"parentId"`
		Name     string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "目录名不能为空")
		return
	}
	acc, cred, err := h.loadAccount(id)
	if err != nil {
		util.NotFound(c, err.Error())
		return
	}
	a, err := adapter.Get(adapter.DiskType(acc.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	newID, err := a.MakeDir(ctx, cred, req.ParentID, req.Name)
	if err != nil {
		util.ServerError(c, "新建目录失败: "+err.Error())
		return
	}
	util.OK(c, gin.H{"dirId": newID})
}

// loadAccount 加载账号并解密 cookie
func (h *DiskHandler) loadAccount(id uint64) (*model.DiskAccount, *adapter.Credential, error) {
	var acc model.DiskAccount
	if err := h.db.First(&acc, id).Error; err != nil {
		return nil, nil, fmt.Errorf("账号不存在")
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("凭据解密失败")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	return &acc, &adapter.Credential{
		DiskType: adapter.DiskType(acc.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, nil
}

func ptrTime(t time.Time) *time.Time { return &t }
