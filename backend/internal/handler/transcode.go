package handler

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/transcode"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// TranscodeHandler 视频转码
//
// svc 允许为 nil（转码未启用时 main.go 不会构造 service），
// 因此每个方法入口都先做可用性守卫，避免 nil 解引用 panic。
type TranscodeHandler struct {
	db  *gorm.DB
	svc *service.TranscodeService
}

func NewTranscodeHandler(db *gorm.DB, svc *service.TranscodeService) *TranscodeHandler {
	return &TranscodeHandler{db: db, svc: svc}
}

// available 转码服务是否可用；不可用时直接写响应并返回 false
func (h *TranscodeHandler) available(c *gin.Context) bool {
	if h.svc == nil || !h.svc.Enabled() {
		util.BadRequest(c, "转码功能未启用，请确认已安装 ffmpeg 且 transcode.enabled=true")
		return false
	}
	return true
}

// Status 转码能力状态（供后台系统页展示）
//
// 这个接口不设守卫：即使未启用也要能返回"未启用"这个事实。
func (h *TranscodeHandler) Status(c *gin.Context) {
	if h.svc == nil {
		util.OK(c, gin.H{"service": gin.H{"enabled": false}, "enabled": false})
		return
	}
	util.OK(c, gin.H{
		"service": h.svc.Status(),
		"enabled": h.svc.Enabled(),
	})
}

// Create 手动创建转码任务
//
// POST /admin/transcode  { "resourceId": 12 }
func (h *TranscodeHandler) Create(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req struct {
		ResourceID uint64 `json:"resourceId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误：需要 resourceId")
		return
	}

	var res model.Resource
	if err := h.db.First(&res, req.ResourceID).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	if res.MediaType != model.MediaTypeVideo {
		util.BadRequest(c, "只有视频资源需要转码")
		return
	}

	t, err := h.svc.Create(c.Request.Context(), &res)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	util.OKWithMessage(c, "转码任务已创建", gin.H{
		"taskId":     t.ID,
		"status":     t.Status,
		"masterUrl":  service.MasterURLForTask(t.ID),
		"resourceId": res.ID,
	})
}

// List 转码任务列表
//
// GET /admin/transcode/tasks?page=1&pageSize=20&status=running&resourceId=12
func (h *TranscodeHandler) List(c *gin.Context) {
	page, pageSize := util.GetPage(c)

	q := h.db.Model(&model.TranscodeTask{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if rid := util.GetUintQuery(c, "resourceId", 0); rid > 0 {
		q = q.Where("resource_id = ?", rid)
	}

	var total int64
	q.Count(&total)

	var tasks []model.TranscodeTask
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks)

	// 带出资源标题，前端列表不必再查一次
	type out struct {
		model.TranscodeTask
		ResourceTitle string `json:"resourceTitle"`
		MasterURL     string `json:"masterUrl"`
	}
	list := make([]out, 0, len(tasks))
	for _, t := range tasks {
		title := ""
		var res model.Resource
		if h.db.Select("title").First(&res, t.ResourceID).Error == nil {
			title = res.Title
		}
		master := ""
		if t.Status == model.TaskStatusSuccess {
			master = service.MasterURLForTask(t.ID)
		}
		list = append(list, out{
			TranscodeTask: t,
			ResourceTitle: title,
			MasterURL:     master,
		})
	}

	util.Page(c, total, page, pageSize, list)
}

// Detail 单个任务详情
//
// GET /admin/transcode/tasks/:id
func (h *TranscodeHandler) Detail(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}

	var t model.TranscodeTask
	if err := h.db.First(&t, id).Error; err != nil {
		util.NotFound(c, "任务不存在")
		return
	}

	var res model.Resource
	h.db.First(&res, t.ResourceID)

	master := ""
	if t.Status == model.TaskStatusSuccess {
		master = service.MasterURLForTask(t.ID)
	}

	util.OK(c, gin.H{
		"task":          t,
		"resourceTitle": res.Title,
		"resourceType":  res.MediaType,
		"masterUrl":     master,
		"profiles":      transcode.DefaultProfiles,
		"segmentSeconds": transcode.SegmentSeconds,
	})
}

// Cancel 取消任务
//
// POST /admin/transcode/tasks/:id/cancel
func (h *TranscodeHandler) Cancel(c *gin.Context) {
	if !h.available(c) {
		return
	}
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	if err := h.svc.Cancel(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			util.NotFound(c, "任务不存在")
			return
		}
		util.BadRequest(c, err.Error())
		return
	}
	util.OKWithMessage(c, "已取消", nil)
}

// Delete 删除任务记录（不删磁盘产物，产物由管理员手工清理）
//
// DELETE /admin/transcode/tasks/:id
func (h *TranscodeHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}

	var t model.TranscodeTask
	if err := h.db.First(&t, id).Error; err != nil {
		util.NotFound(c, "任务不存在")
		return
	}
	// 运行中的任务不允许直接删记录，否则进程还在跑却失去追踪
	if t.Status == model.TaskStatusRunning || t.Status == model.TaskStatusPending {
		util.BadRequest(c, "任务进行中，请先取消再删除")
		return
	}

	if err := h.db.Delete(&model.TranscodeTask{}, id).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "已删除", nil)
}

// GC 手动触发一次产物清理
//
// POST /admin/transcode/gc
//
// 定时任务已在后台每 6 小时跑一次，这个端点给管理员一个"现在就清"的按钮，
// 典型场景是磁盘告警时先手动腾空间。
func (h *TranscodeHandler) GC(c *gin.Context) {
	if !h.available(c) {
		return
	}
	res := h.svc.RunGC()
	util.OKWithMessage(c, fmt.Sprintf("清理完成，释放 %d MB", res.FreedMB), res)
}

// writeErr 把 service 层错误映射为合适的 HTTP 语义
func (h *TranscodeHandler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, transcode.ErrDisabled):
		util.BadRequest(c, "转码功能未启用，请检查 transcode.enabled 配置")
	case errors.Is(err, transcode.ErrFFmpegMissing):
		util.ServerError(c, "服务器未安装 ffmpeg，无法转码")
	case errors.Is(err, transcode.ErrBusy):
		util.Fail(c, util.CodeTooMany, "转码繁忙，请稍后重试")
	default:
		util.ServerError(c, err.Error())
	}
}
