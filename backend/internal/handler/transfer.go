package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// TransferHandler 转存任务
type TransferHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
	pool      *task.WorkerPool
}

func NewTransferHandler(db *gorm.DB, encryptor *util.Encryptor, pool *task.WorkerPool) *TransferHandler {
	return &TransferHandler{db: db, encryptor: encryptor, pool: pool}
}

type createTransferRequest struct {
	ShareURL      string `json:"shareUrl" binding:"required"`
	Passcode      string `json:"passcode"`
	DiskType      string `json:"diskType" binding:"required"`
	DiskAccountID uint64 `json:"diskAccountId"`
	TargetDirID   string `json:"targetDirId"`
	CategoryID    uint64 `json:"categoryId"`
	Title         string `json:"title"`
}

// Create 创建转存任务
func (h *TransferHandler) Create(c *gin.Context) {
	var req createTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "分享链接和网盘类型不能为空")
		return
	}

	// 选择账号：未指定则取该网盘默认账号
	accountID := req.DiskAccountID
	if accountID == 0 {
		var acc model.DiskAccount
		if err := h.db.Where("disk_type = ? AND status = ?", req.DiskType, model.DiskAccountValid).
			Order("is_default desc, id asc").First(&acc).Error; err != nil {
			util.BadRequest(c, "该网盘暂无可用账号，请先绑定")
			return
		}
		accountID = acc.ID
	}

	t := &model.TransferTask{
		Title:         orDefaultStr(req.Title, "转存任务"),
		DiskType:      req.DiskType,
		ShareURL:      req.ShareURL,
		SharePassword: req.Passcode,
		DiskAccountID: accountID,
		TargetDirID:   req.TargetDirID,
		CategoryID:    req.CategoryID,
		Status:        model.TaskStatusPending,
		OperatorID:    util.CurrentUserID(c),
	}
	if err := h.db.Create(t).Error; err != nil {
		util.ServerError(c, "创建任务失败")
		return
	}

	h.submit(t)
	util.OK(c, gin.H{"taskId": t.ID})
}

// BatchCreate 批量转存
func (h *TransferHandler) BatchCreate(c *gin.Context) {
	var req struct {
		DiskAccountID uint64 `json:"diskAccountId"`
		TargetDirID   string `json:"targetDirId"`
		CategoryID    uint64 `json:"categoryId"`
		Items         []struct {
			ShareURL string `json:"shareUrl"`
			Passcode string `json:"passcode"`
			DiskType string `json:"diskType"`
			Title    string `json:"title"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		util.BadRequest(c, "请选择要转存的资源")
		return
	}

	var taskIDs []uint64
	operatorID := util.CurrentUserID(c)
	for _, it := range req.Items {
		accountID := req.DiskAccountID
		if accountID == 0 {
			var acc model.DiskAccount
			if err := h.db.Where("disk_type = ? AND status = ?", it.DiskType, model.DiskAccountValid).
				Order("is_default desc, id asc").First(&acc).Error; err != nil {
				continue
			}
			accountID = acc.ID
		}
		t := &model.TransferTask{
			Title:         orDefaultStr(it.Title, "转存任务"),
			DiskType:      it.DiskType,
			ShareURL:      it.ShareURL,
			SharePassword: it.Passcode,
			DiskAccountID: accountID,
			TargetDirID:   req.TargetDirID,
			CategoryID:    req.CategoryID,
			Status:        model.TaskStatusPending,
			OperatorID:    operatorID,
		}
		if err := h.db.Create(t).Error; err == nil {
			taskIDs = append(taskIDs, t.ID)
			h.submit(t)
		}
	}
	util.OK(c, gin.H{"taskIds": taskIDs, "count": len(taskIDs)})
}

// submit 提交到工作池
func (h *TransferHandler) submit(t *model.TransferTask) {
	taskID := t.ID
	h.pool.Submit(&task.Job{
		ID:       uuid.NewString(),
		Name:     "transfer",
		MaxRetry: 2,
		Payload:  taskID,
		Handler: func(ctx context.Context, payload interface{}) error {
			id, _ := payload.(uint64)
			return h.processTransfer(ctx, id)
		},
	})
}

// processTransfer 执行转存
func (h *TransferHandler) processTransfer(ctx context.Context, taskID uint64) error {
	var t model.TransferTask
	if err := h.db.First(&t, taskID).Error; err != nil {
		return fmt.Errorf("任务不存在: %w", err)
	}
	if t.Status == model.TaskStatusCanceled {
		return nil
	}

	now := time.Now()
	h.db.Model(&t).Updates(map[string]interface{}{
		"status": model.TaskStatusRunning, "started_at": now, "progress": 10,
	})

	// 加载账号
	var acc model.DiskAccount
	if err := h.db.First(&acc, t.DiskAccountID).Error; err != nil {
		return h.fail(&t, "网盘账号不存在")
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return h.fail(&t, "账号凭据解密失败")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	cred := &adapter.Credential{
		DiskType: adapter.DiskType(acc.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}

	a, err := adapter.Get(adapter.DiskType(t.DiskType))
	if err != nil {
		return h.fail(&t, err.Error())
	}

	h.setProgress(&t, 30, "正在解析分享链接")
	share, err := a.ParseShare(ctx, cred, t.ShareURL, t.SharePassword)
	if err != nil {
		return h.fail(&t, "解析分享失败: "+err.Error())
	}

	if t.Title == "转存任务" || t.Title == "" {
		h.db.Model(&t).Update("title", share.Title)
	}

	h.setProgress(&t, 60, "正在转存")
	result, err := a.SaveToDir(ctx, cred, share, t.TargetDirID)
	if err != nil {
		return h.fail(&t, "转存失败: "+err.Error())
	}

	// 转存成功 → 建立资源记录
	var resourceID *uint64
	if t.CategoryID > 0 {
		res := &model.Resource{
			CategoryID:    t.CategoryID,
			Title:         orDefaultStr(share.Title, t.Title),
			DiskType:      t.DiskType,
			ShareURL:      t.ShareURL,
			Passcode:      t.SharePassword,
			DiskAccountID: &t.DiskAccountID,
			FileCount:     result.Count,
			TotalSize:     share.TotalSize,
			Status:        model.ResourceStatusAlive,
			MediaType:     model.MediaTypeVideo,
		}
		if err := h.db.Create(res).Error; err == nil {
			resourceID = &res.ID
		}
	}

	finish := time.Now()
	h.db.Model(&t).Updates(map[string]interface{}{
		"status":              model.TaskStatusSuccess,
		"progress":            100,
		"message":             result.Message,
		"finished_at":         finish,
		"created_resource_id": resourceID,
	})
	log.Printf("[transfer] 任务 %d 转存成功: %s", t.ID, result.Message)
	return nil
}

func (h *TransferHandler) setProgress(t *model.TransferTask, progress int, msg string) {
	h.db.Model(t).Updates(map[string]interface{}{"progress": progress, "message": msg})
}

func (h *TransferHandler) fail(t *model.TransferTask, msg string) error {
	now := time.Now()
	h.db.Model(t).Updates(map[string]interface{}{
		"status": model.TaskStatusFailed, "message": msg, "finished_at": now,
	})
	return fmt.Errorf("%s", msg)
}

// List 任务列表
func (h *TransferHandler) List(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	status := c.Query("status")

	q := h.db.Model(&model.TransferTask{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)

	var list []model.TransferTask
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.Page(c, total, page, pageSize, list)
}

// Retry 重试任务
func (h *TransferHandler) Retry(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	var t model.TransferTask
	if err := h.db.First(&t, id).Error; err != nil {
		util.NotFound(c, "任务不存在")
		return
	}
	h.db.Model(&t).Updates(map[string]interface{}{
		"status": model.TaskStatusPending, "progress": 0, "message": "已重新排队",
	})
	h.submit(&t)
	util.OKWithMessage(c, "已重新提交", nil)
}

// Cancel 取消任务
func (h *TransferHandler) Cancel(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	h.db.Model(&model.TransferTask{}).Where("id = ? AND status IN ?", id,
		[]string{model.TaskStatusPending, model.TaskStatusRunning}).
		Updates(map[string]interface{}{"status": model.TaskStatusCanceled, "message": "已取消"})
	util.OKWithMessage(c, "已取消", nil)
}
