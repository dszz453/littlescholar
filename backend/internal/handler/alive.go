package handler

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AliveHandler 资源存活巡检
type AliveHandler struct {
	db *gorm.DB
	svc *service.ResourceAliveService
}

func NewAliveHandler(db *gorm.DB, svc *service.ResourceAliveService) *AliveHandler {
	return &AliveHandler{db: db, svc: svc}
}

// available 巡检服务是否可用
//
// 未配置检测引擎时不静默失败，明确告诉管理员缺什么。
func (h *AliveHandler) available(c *gin.Context) bool {
	if h.svc == nil || !h.svc.Enabled() {
		util.BadRequest(c, "未配置检测引擎，请先设置 external.pansou_url 或 external.pancheck_url")
		return false
	}
	return true
}

// Status 巡检能力状态
//
// 不设守卫：即使未配置也要能回报"未配置"这个事实。
func (h *AliveHandler) Status(c *gin.Context) {
	if h.svc == nil {
		util.OK(c, gin.H{"service": gin.H{"enabled": false}, "enabled": false})
		return
	}

	// 顺带统计各状态资源数，后台页面一次拿全
	type counts struct {
		Status string `json:"status"`
		Total  int64  `json:"total"`
	}
	var rows []counts
	h.db.Model(&model.Resource{}).
		Select("status, count(*) as total").
		Group("status").Scan(&rows)

	dist := map[string]int64{
		model.ResourceStatusAlive:   0,
		model.ResourceStatusDead:    0,
		model.ResourceStatusUnknown: 0,
	}
	var total int64
	for _, r := range rows {
		dist[r.Status] = r.Total
		total += r.Total
	}

	util.OK(c, gin.H{
		"service":      h.svc.Status(),
		"enabled":      h.svc.Enabled(),
		"distribution": dist,
		"total":        total,
	})
}

// Start 启动一轮巡检
//
// POST /admin/alive/check  { "trigger": "manual", "mode": "stale" }
//
// mode: stale（默认，只查过期的）/ all（全量）/ dead（只重查已判失效的）
func (h *AliveHandler) Start(c *gin.Context) {
	if !h.available(c) {
		return
	}
	var req struct {
		Mode    string `json:"mode"`
		Trigger string `json:"trigger"`
	}
	// 允许空 body：全部走默认值
	_ = c.ShouldBindJSON(&req)

	taskID, err := h.svc.Start(req.Trigger, req.Mode)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAliveCheckRunning):
			util.Fail(c, util.CodeTooMany, "已有巡检任务在执行，请等它跑完")
		case errors.Is(err, service.ErrNoResources):
			util.OKWithMessage(c, "没有需要巡检的资源", gin.H{"taskId": 0})
		default:
			util.ServerError(c, err.Error())
		}
		return
	}
	util.OKWithMessage(c, "巡检已启动", gin.H{"taskId": taskID})
}

// Tasks 巡检任务列表
//
// GET /admin/alive/tasks?page=1&pageSize=20
func (h *AliveHandler) Tasks(c *gin.Context) {
	page, pageSize := util.GetPage(c)

	q := h.db.Model(&model.AliveCheckTask{})
	var total int64
	q.Count(&total)

	var tasks []model.AliveCheckTask
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks)

	util.Page(c, total, page, pageSize, tasks)
}

// Detail 巡检任务详情
func (h *AliveHandler) Detail(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	var t model.AliveCheckTask
	if err := h.db.First(&t, id).Error; err != nil {
		util.NotFound(c, "任务不存在")
		return
	}
	util.OK(c, t)
}

// DeadList 失效资源列表
//
// GET /admin/alive/dead?limit=50
//
// 单独做一个端点是刻意的：失效资源是"需要人处理"的清单，
// 而资源列表页的主用途是管理正常资源，两件事混在一起会让列表被失效项淹没。
func (h *AliveHandler) DeadList(c *gin.Context) {
	limit := util.GetIntQuery(c, "limit", 50)
	list, err := h.svc.DeadResources(limit)
	if err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.OK(c, list)
}
