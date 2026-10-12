package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// MappingHandler 资源映射
//
// 用途：把存储源（WebDAV / 网盘）里的一个远端目录绑定到系统的一个内容目录，
// 之后一次「同步」就把该目录下的媒体文件全部登记为资源。
//
// 为什么值得单独做一个概念：NAS 上本来就是按目录整理好的，
// 每次进新片都手动「浏览 → 勾选 → 导入」既繁琐又容易漏。
// 映射把"这个远端目录对应系统里哪个目录"这件事**持久化**下来，
// 变成一个可以反复执行、可回看的操作。
type MappingHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
	importSvc *service.ImportService
}

func NewMappingHandler(db *gorm.DB, encryptor *util.Encryptor) *MappingHandler {
	return &MappingHandler{
		db:        db,
		encryptor: encryptor,
		importSvc: service.NewImportService(db),
	}
}

// List 映射列表
func (h *MappingHandler) List(c *gin.Context) {
	var list []model.ResourceMapping
	q := h.db.Preload("Account").Preload("Category")
	if t := c.Query("accountId"); t != "" {
		q = q.Where("account_id = ?", t)
	}
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.OK(c, list)
}

type mappingRequest struct {
	Name          string `json:"name"`
	AccountID     uint64 `json:"accountId" binding:"required"`
	RemoteDirID   string `json:"remoteDirId"`
	RemoteDirName string `json:"remoteDirName"`
	CategoryID    uint64 `json:"categoryId" binding:"required"`
	// 用指针是为了区分"没传"和"传了 false" —— 布尔零值没法区分，
	// 直接写 bool 会导致更新时把 recursive 静默改成 false。
	Recursive *bool `json:"recursive"`
	Enabled   *bool `json:"enabled"`
}

// Create 新建映射
func (h *MappingHandler) Create(c *gin.Context) {
	var req mappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误：需要 accountId 与 categoryId")
		return
	}
	if err := h.validateRefs(req.AccountID, req.CategoryID); err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	m := &model.ResourceMapping{
		Name:          req.Name,
		AccountID:     req.AccountID,
		RemoteDirID:   req.RemoteDirID,
		RemoteDirName: req.RemoteDirName,
		CategoryID:    req.CategoryID,
		Recursive:     boolOr(req.Recursive, true),
		Enabled:       boolOr(req.Enabled, true),
	}
	if m.Name == "" {
		m.Name = h.autoName(req.AccountID, req.RemoteDirID)
	}
	if err := h.db.Create(m).Error; err != nil {
		util.ServerError(c, "创建失败")
		return
	}
	util.OK(c, gin.H{"id": m.ID})
}

// Update 更新映射
func (h *MappingHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}
	var m model.ResourceMapping
	if err := h.db.First(&m, id).Error; err != nil {
		util.NotFound(c, "映射不存在")
		return
	}
	var req mappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.AccountID > 0 {
		updates["account_id"] = req.AccountID
	}
	if req.CategoryID > 0 {
		updates["category_id"] = req.CategoryID
	}
	// remoteDirId 允许显式改成根目录（空串），所以不判空、直接写。
	updates["remote_dir_id"] = req.RemoteDirID
	updates["remote_dir_name"] = req.RemoteDirName
	if req.Recursive != nil {
		updates["recursive"] = *req.Recursive
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.db.Model(&m).Updates(updates).Error; err != nil {
		util.ServerError(c, "保存失败")
		return
	}
	util.OKWithMessage(c, "已保存", nil)
}

// Delete 删除映射（不动已导入的资源）
func (h *MappingHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}
	// 只删映射本身，不删已导入的资源 —— 删资源是不可逆的，
	// 不该由一个"取消配置"的动作顺带触发。
	if err := h.db.Delete(&model.ResourceMapping{}, id).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "已删除映射（已导入的资源保留）", nil)
}

// Sync 立即同步一个映射
func (h *MappingHandler) Sync(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}
	var m model.ResourceMapping
	if err := h.db.First(&m, id).Error; err != nil {
		util.NotFound(c, "映射不存在")
		return
	}
	result := h.syncOne(c.Request.Context(), &m)
	util.OK(c, result)
}

// SyncAll 同步全部启用的映射
func (h *MappingHandler) SyncAll(c *gin.Context) {
	var list []model.ResourceMapping
	h.db.Where("enabled = ?", true).Find(&list)

	created, skipped, failed := 0, 0, 0
	for i := range list {
		r := h.syncOne(c.Request.Context(), &list[i])
		if r.Error != "" {
			failed++
			continue
		}
		created += r.Created
		skipped += r.Skipped
	}
	util.OK(c, gin.H{
		"total": len(list), "created": created,
		"skipped": skipped, "failed": failed,
	})
}

// syncResult 单个映射的同步结果
type syncResult struct {
	MappingID   uint64 `json:"mappingId"`
	Name        string `json:"name"`
	Created     int    `json:"created"`
	Skipped     int    `json:"skipped"`
	SkippedDirs int    `json:"skippedDirs"`
	Truncated   bool   `json:"truncated"`
	Error       string `json:"error,omitempty"`
}

// syncOne 执行一次同步并落库同步快照。
//
// 无论成功失败都写快照：用户在列表里最需要知道的是
// "上次同步是什么时候、有没有报错"，报错信息比成功计数更重要。
func (h *MappingHandler) syncOne(ctx context.Context, m *model.ResourceMapping) syncResult {
	res := syncResult{MappingID: m.ID, Name: m.Name}
	now := time.Now()

	acc, cred, a, err := loadSource(h.db, h.encryptor, m.AccountID)
	if err != nil {
		res.Error = err.Error()
	} else {
		// 递归遍历可能很久（NAS 大目录一次 PROPFIND 几十秒），
		// 超时给到 3 分钟而不是常用值 30s。
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()

		targets, stats := service.CollectDiskFiles(sctx, a, cred,
			[]service.CollectItem{{FileID: m.RemoteDirID, IsDir: true}},
			m.Recursive, service.DefaultMaxImportFiles)

		res.SkippedDirs = stats.SkippedDirs
		res.Truncated = stats.Truncated
		if len(targets) == 0 {
			res.Error = "该目录下没有找到可导入的媒体文件"
		} else if created, skipped, rerr := h.importSvc.Register(acc, m.CategoryID, targets); rerr != nil {
			res.Error = rerr.Error()
		} else {
			res.Created = created
			res.Skipped = skipped
		}
	}

	updates := map[string]interface{}{
		"last_sync_at":      now,
		"last_sync_created": res.Created,
		"last_sync_skipped": res.Skipped,
		"last_sync_error":   res.Error,
	}
	if err := h.db.Model(m).Updates(updates).Error; err != nil {
		log.Printf("[mapping] 写入同步快照失败 mapping=%d: %v", m.ID, err)
	}
	return res
}

func (h *MappingHandler) validateRefs(accountID, categoryID uint64) error {
	var cnt int64
	h.db.Model(&model.DiskAccount{}).Where("id = ?", accountID).Count(&cnt)
	if cnt == 0 {
		return fmt.Errorf("存储账号不存在")
	}
	h.db.Model(&model.Category{}).Where("id = ?", categoryID).Count(&cnt)
	if cnt == 0 {
		return fmt.Errorf("系统目录不存在")
	}
	return nil
}

// autoName 用户没填名字时，用"账号名 · 远端目录名"拼一个可读的。
func (h *MappingHandler) autoName(accountID uint64, remoteDirID string) string {
	var acc model.DiskAccount
	name := ""
	if h.db.First(&acc, accountID).Error == nil {
		name = acc.Nickname
	}
	dir := remoteDirID
	if dir == "" || dir == "/" {
		dir = "根目录"
	}
	if name == "" {
		return dir
	}
	return name + " · " + dir
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
