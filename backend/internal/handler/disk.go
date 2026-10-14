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
	_ "github.com/littlescholar/core/internal/adapter/local"
	_ "github.com/littlescholar/core/internal/adapter/quark"
	_ "github.com/littlescholar/core/internal/adapter/tianyi"
	_ "github.com/littlescholar/core/internal/adapter/uc"
	_ "github.com/littlescholar/core/internal/adapter/webdav"
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

// Types 支持的接入类型列表
func (h *DiskHandler) Types(c *gin.Context) {
	var out []gin.H
	for _, t := range adapter.List() {
		// 默认支持扫码（5 个网盘都支持）；WebDAV / 本地硬盘通过 QRCapable
		// 声明为 false，前端据此把「扫码添加」置灰。
		supportQR := true
		if qc, ok := t.(adapter.QRCapable); ok {
			supportQR = qc.SupportsQR()
		}
		out = append(out, gin.H{
			"type":          string(t.Type()),
			"name":          t.Name(),
			"supportQR":     supportQR,
			"supportManual": true,
			// isStorage：WebDAV / 本地硬盘这类"存储源"，前端归类到
			// 「存储接入」分组，与网盘账号分开呈现。
			"isStorage": adapter.IsStorageType(t.Type()),
		})
	}
	util.OK(c, out)
}

// publicExtraKeys 允许返回给前端的 Extra 键（白名单）。
//
// 为什么用白名单而不是黑名单：Extra 里混着两类东西 ——
// 一类是"配置参数"（WebDAV 地址、本地根目录、并发线程数），前端要回显、要编辑；
// 另一类是"凭证"（阿里云盘 refresh_token、迅雷密码……），泄露即等于交出账号。
// 黑名单漏一个键就等于泄一个凭证，白名单漏一个键只是少显示一个字段。
var publicExtraKeys = map[string]bool{
	"url":           true,
	"rootPath":      true,
	"isVip":         true,
	"is_vip":        true,
	"threadLimit":   true,
	"thread_limit":  true,
	"openApiUrl":    true,
	"open_api_url":  true,
	"accelerate":    true,
	"noMemberBoost": true,
	"path":          true,
	"saveDir":       true,
}

// DiskAccountDTO 账号返回结构。
//
// 直接在 model.DiskAccount 上 embed：CookieEncrypted / Extra 本来就是
// json:"-"，不会泄露；这里额外补一个经过白名单过滤的 extraPublic
// 供前端回显配置。
type DiskAccountDTO struct {
	model.DiskAccount
	ExtraPublic map[string]string `json:"extraPublic"`
	// IsStorage 便于前端区分"网盘"与"存储源（WebDAV/本地硬盘）"
	IsStorage bool `json:"isStorage"`
}

// List 账号列表
//
// 支持 ?diskType=webdav 过滤：管理后台的「WebDAV 资源」页只需要
// 存储源账号，把网盘账号一并返回会让用户在下拉里选到夸克账号，
// 然后得到一个"这个账号不是 WebDAV"的报错。
//
// 支持 ?group=storage / disk 做分组过滤：资源接入页把
// 「网盘接入」与「存储接入（WebDAV/本地硬盘）」分两个 tab。
func (h *DiskHandler) List(c *gin.Context) {
	var accounts []model.DiskAccount
	q := h.db.Order("disk_type asc, id asc")
	if t := c.Query("diskType"); t != "" {
		q = q.Where("disk_type = ?", t)
	}
	if g := c.Query("group"); g == "storage" {
		q = q.Where("disk_type IN ?", adapter.StorageTypes)
	} else if g == "disk" {
		q = q.Where("disk_type NOT IN ?", adapter.StorageTypes)
	}
	if err := q.Find(&accounts).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}

	out := make([]DiskAccountDTO, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, DiskAccountDTO{
			DiskAccount: a,
			ExtraPublic: publicExtra(a.Extra),
			IsStorage:   adapter.IsStorageType(adapter.DiskType(a.DiskType)),
		})
	}
	util.OK(c, out)
}

// publicExtra 按白名单过滤 Extra，只回传配置类字段。
func publicExtra(extra model.JSONMap) map[string]string {
	if len(extra) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(extra))
	for k, v := range extra {
		if !publicExtraKeys[k] {
			continue
		}
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
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

	// 确认成功 → 落库。
	//
	// partial 也落库：拿到了 BDUSS 但补不到 STOKEN（能读不能写）。
	// 这种情况以前也报 confirmed，界面显示"绑定成功"，用户要到下一次
	// 转存才撞上失败；现在状态如实区分，前端会用黄色提示讲清楚缺什么。
	if (result.Status == "confirmed" || result.Status == "partial") && result.Cookie != "" {
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
			"status": result.Status, "account_id": acc.ID, "cookie_raw": "",
		})
		resp["accountId"] = acc.ID
		resp["nickname"] = acc.Nickname
	} else {
		h.db.Model(&sess).Update("status", result.Status)
	}

	util.OK(c, resp)
}

// AddManual 手动添加（粘贴 Cookie / token / 存储源参数）
func (h *DiskHandler) AddManual(c *gin.Context) {
	var req struct {
		DiskType string            `json:"diskType" binding:"required"`
		Cookie   string            `json:"cookie"`
		Nickname string            `json:"nickname"`
		Extra    map[string]string `json:"extra"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "缺少网盘类型")
		return
	}
	if !adapter.IsSupported(adapter.DiskType(req.DiskType)) {
		util.BadRequest(c, "不支持的接入类型")
		return
	}
	req.Cookie = strings.TrimSpace(req.Cookie)

	// 存储源（WebDAV / 本地硬盘）没有 cookie，靠 Extra 里的地址/路径；
	// 网盘则必须有 cookie 或 token。分别校验，避免"填了空 cookie
	// 也保存成功，之后每次转存都失败"这种延迟爆炸的坑。
	if adapter.IsStorageType(adapter.DiskType(req.DiskType)) {
		if err := validateStorageExtra(adapter.DiskType(req.DiskType), req.Extra, req.Cookie); err != nil {
			util.BadRequest(c, err.Error())
			return
		}
	} else if req.Cookie == "" {
		util.BadRequest(c, "请填写 Cookie / Token")
		return
	}

	// 校验凭据有效性
	a, _ := adapter.Get(adapter.DiskType(req.DiskType))
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	cred := &adapter.Credential{
		DiskType: adapter.DiskType(req.DiskType),
		Cookie:   req.Cookie,
		Extra:    req.Extra,
	}
	info, err := a.ValidateCookie(ctx, cred)
	if err != nil {
		// 与 Validate 同样的兜底：账号信息接口不通不代表 cookie 不能用。
		// 能列出根目录就收下，避免"填了明明能用的 cookie 却被拒"。
		if _, listErr := a.ListDir(ctx, cred, ""); listErr == nil {
			info = &adapter.AccountInfo{}
		} else {
			util.BadRequest(c, "校验失败: "+err.Error())
			return
		}
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

// validateStorageExtra 校验存储源的必填参数。
func validateStorageExtra(t adapter.DiskType, extra map[string]string, cookieFallback string) error {
	get := func(k string) string {
		if extra == nil {
			return ""
		}
		return strings.TrimSpace(extra[k])
	}
	switch t {
	case adapter.DiskWebdav:
		if get("url") == "" {
			return fmt.Errorf("请填写 WebDAV 地址")
		}
	case adapter.DiskLocal:
		// 兼容把路径填在 cookie 字段的老表单
		if get("rootPath") == "" && cookieFallback == "" {
			return fmt.Errorf("请填写服务器上的目录路径")
		}
	}
	return nil
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
		// 兜底：账号信息接口换个域名就可能 404/改版，而"能不能列目录"
		// 才是用户真正在意的能力（也是他判断"实际能点开"的依据）。
		//
		// 所以这里再用一次实际使用路径复核：列根目录能成功就判有效，
		// 只把昵称留空。宁可少标一个"失效"，也不要冤枉一个能用的账号。
		if _, listErr := a.ListDir(ctx, cred, ""); listErr == nil {
			h.db.Model(acc).Updates(map[string]interface{}{
				"status": model.DiskAccountValid, "last_check_at": now,
			})
			util.OK(c, gin.H{"valid": true, "message": "账号信息接口不可用，但目录可正常访问"})
			return
		}
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

// RenameRemote 重命名接入源里的文件/目录（写操作直通远端）。
func (h *DiskHandler) RenameRemote(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var req struct {
		FileID  string `json:"fileId" binding:"required"`
		NewName string `json:"newName" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "缺少 fileId 或 newName")
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	if err := a.Rename(ctx, cred, req.FileID, req.NewName); err != nil {
		util.ServerError(c, "重命名失败: "+err.Error())
		return
	}
	util.OKWithMessage(c, "已重命名", nil)
}

// DeleteRemote 删除接入源里的文件/目录。
//
// 这是**不可逆**的远端删除，所以前端必须二次确认；这里只负责执行。
func (h *DiskHandler) DeleteRemote(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var req struct {
		FileIDs []string `json:"fileIds" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.FileIDs) == 0 {
		util.BadRequest(c, "请选择要删除的文件")
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()

	if err := a.DeleteFile(ctx, cred, req.FileIDs); err != nil {
		util.ServerError(c, "删除失败: "+err.Error())
		return
	}
	util.OKWithMessage(c, "已删除", nil)
}

// UpdateAccount 更新接入源配置（昵称 / 凭据 / 配置参数）。
//
// 与 UpdateCookie 的分工：UpdateCookie 是"只换 cookie"的快捷入口（网盘扫码失效时用）；
// 本接口是"通用编辑"，WebDAV 改地址、本地硬盘改根目录、网盘补充配置都走它。
func (h *DiskHandler) UpdateAccount(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的账号 ID")
		return
	}
	var acc model.DiskAccount
	if err := h.db.First(&acc, id).Error; err != nil {
		util.NotFound(c, "接入源不存在")
		return
	}
	var req struct {
		Nickname *string            `json:"nickname"`
		Cookie   *string            `json:"cookie"`
		Extra    *map[string]string `json:"extra"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	updates := map[string]interface{}{}
	if req.Nickname != nil && strings.TrimSpace(*req.Nickname) != "" {
		updates["nickname"] = strings.TrimSpace(*req.Nickname)
	}
	if req.Cookie != nil {
		cookie := strings.TrimSpace(*req.Cookie)
		if cookie != "" {
			encrypted, err := h.encryptor.Encrypt(cookie)
			if err != nil {
				util.ServerError(c, "凭据加密失败")
				return
			}
			updates["cookie_encrypted"] = encrypted
			updates["cookie_preview"] = util.MaskSecret(cookie)
			updates["status"] = model.DiskAccountValid
		}
	}
	if req.Extra != nil {
		merged := model.JSONMap{}
		for k, v := range acc.Extra {
			merged[k] = v
		}
		for k, v := range *req.Extra {
			merged[k] = v
		}
		updates["extra"] = merged
	}
	if len(updates) == 0 {
		util.OKWithMessage(c, "没有需要更新的内容", nil)
		return
	}
	if err := h.db.Model(&acc).Updates(updates).Error; err != nil {
		util.ServerError(c, "保存失败")
		return
	}
	util.OKWithMessage(c, "已保存", nil)
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
