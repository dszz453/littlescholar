package handler

import (
	"context"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/config"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// SystemHandler 系统配置
type SystemHandler struct {
	db     *gorm.DB
	cfg    *config.Config
	pansou *service.PansouClient
	pool   *task.WorkerPool
	start  time.Time
}

func NewSystemHandler(db *gorm.DB, cfg *config.Config, pansou *service.PansouClient, pool *task.WorkerPool, start time.Time) *SystemHandler {
	return &SystemHandler{db: db, cfg: cfg, pansou: pansou, pool: pool, start: start}
}

// GetConfig 获取系统配置
func (h *SystemHandler) GetConfig(c *gin.Context) {
	var items []model.SystemConfig
	h.db.Order("`group` asc, key asc").Find(&items)

	m := make(map[string]string, len(items))
	for _, it := range items {
		m[it.Key] = it.Value
	}
	// 合并配置文件中的默认值
	if v := m["pansou_url"]; v == "" {
		m["pansou_url"] = h.cfg.External.PansouURL
	}
	if v := m["pancheck_url"]; v == "" {
		m["pancheck_url"] = h.cfg.External.PancheckURL
	}
	util.OK(c, m)
}

// UpdateConfig 更新系统配置
func (h *SystemHandler) UpdateConfig(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	for k, v := range req {
		var existed model.SystemConfig
		err := h.db.Where("key = ?", k).First(&existed).Error
		if err == gorm.ErrRecordNotFound {
			h.db.Create(&model.SystemConfig{Key: k, Value: v, Group: "general"})
		} else {
			h.db.Model(&existed).Update("value", v)
		}
	}
	util.OKWithMessage(c, "配置已保存", nil)
}

// Status 系统状态
func (h *SystemHandler) Status(c *gin.Context) {
	// 数据库
	dbOnline := true
	if sqlDB, err := h.db.DB(); err == nil {
		if err := sqlDB.Ping(); err != nil {
			dbOnline = false
		}
	}

	// pansou
	pansouOnline := false
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if _, err := h.pansou.Health(ctx); err == nil {
		pansouOnline = true
	}

	// 统计
	var userCount, resourceCount, categoryCount, taskCount int64
	h.db.Model(&model.User{}).Count(&userCount)
	h.db.Model(&model.Resource{}).Count(&resourceCount)
	h.db.Model(&model.Category{}).Count(&categoryCount)
	h.db.Model(&model.TransferTask{}).
		Where("status IN ?", []string{model.TaskStatusPending, model.TaskStatusRunning}).
		Count(&taskCount)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	util.OK(c, gin.H{
		"version":        "1.0.0",
		"uptime":         int(time.Since(h.start).Seconds()),
		"dbOnline":       dbOnline,
		"pansouOnline":   pansouOnline,
		"goroutines":     runtime.NumGoroutine(),
		"memoryMB":       m.Alloc / 1024 / 1024,
		"transferQueue":  h.pool.QueueLen(),
		"pendingTasks":   taskCount,
		"userCount":      userCount,
		"resourceCount":  resourceCount,
		"categoryCount":  categoryCount,
	})
}
