package handler

import (
	"context"
	"log"
	"runtime"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/config"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SystemHandler 系统配置
type SystemHandler struct {
	db       *gorm.DB
	cfg      *config.Config
	pansou   *service.PansouClient
	pancheck *service.PancheckClient
	pool     *task.WorkerPool
	start    time.Time
}

func NewSystemHandler(db *gorm.DB, cfg *config.Config, pansou *service.PansouClient,
	pancheck *service.PancheckClient, pool *task.WorkerPool, start time.Time) *SystemHandler {
	return &SystemHandler{db: db, cfg: cfg, pansou: pansou, pancheck: pancheck, pool: pool, start: start}
}

// GetConfig 获取系统配置
func (h *SystemHandler) GetConfig(c *gin.Context) {
	items := make([]model.SystemConfig, 0, 16)

	// 用 clause.OrderByColumn 而不是手写 "`group` asc, key asc"。
	//
	// 原因：group 是 PostgreSQL 的保留字，而反引号是 MySQL 专有语法，
	// 写死引号必然在其中一边炸。交给 GORM 按 dialector 生成才对。
	//
	// 真实教训：原写法让 PG 一直报
	//   syntax error at or near "group" (SQLSTATE 42601)
	// 而这里原先没检查 error、接口照样返回 200 + 空 map，
	// 所以「设置页看不到已保存的值」这个问题被掩盖了好几轮，
	// 直到翻容器日志才被发现。加日志是这个修复的一半。
	if err := h.db.
		Order(clause.OrderByColumn{Column: clause.Column{Name: "group"}}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}}).
		Find(&items).Error; err != nil {
		log.Printf("[system] 读取系统配置失败（本次仅返回配置文件默认值）: %v", err)
	}

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
	h.applyExternalConfig(req)
	util.OKWithMessage(c, "配置已保存", nil)
}

// applyExternalConfig 把外部服务地址的变更立刻作用到运行中的客户端。
//
// 背景（真实故障）：用户在后台把 pansou_url 从默认的 http://pansou:8888
// 改成 http://192.168.31.1:8001 后，搜索依旧失败 —— 因为 PansouClient 的
// baseURL 是启动时固化的，必须重启容器才生效。用户完全无从得知这一点。
//
// 这里只处理本次提交中**出现过**的 key：没提交的沿用数据库现有值，
// 避免把未修改的配置误当空值覆盖掉。
func (h *SystemHandler) applyExternalConfig(changed map[string]string) {
	cur := func(key, fallback string) string {
		if v, ok := changed[key]; ok {
			return v
		}
		var row model.SystemConfig
		if err := h.db.Where("key = ?", key).First(&row).Error; err == nil {
			return row.Value
		}
		return fallback
	}

	if _, ok := changed["pansou_url"]; ok {
		h.pansou.Configure(cur("pansou_url", h.cfg.External.PansouURL),
			cur("pansou_token", h.cfg.External.PansouToken))
		log.Printf("[system] PanSou 地址已热更新为 %s", cur("pansou_url", h.cfg.External.PansouURL))
	} else if _, ok := changed["pansou_token"]; ok {
		h.pansou.Configure(cur("pansou_url", h.cfg.External.PansouURL),
			cur("pansou_token", h.cfg.External.PansouToken))
	}

	// PanCheck 同样要能热更新 —— 否则"改了地址但检测还用老的"，
	// 排查起来比配置没改还费劲。
	if h.pancheck != nil {
		if _, ok := changed["pancheck_url"]; ok {
			h.pancheck.Configure(cur("pancheck_url", h.cfg.External.PancheckURL),
				cur("pancheck_password", h.cfg.External.PancheckPass))
			log.Printf("[system] PanCheck 地址已热更新为 %s", cur("pancheck_url", h.cfg.External.PancheckURL))
		} else if _, ok := changed["pancheck_password"]; ok {
			h.pancheck.Configure(cur("pancheck_url", h.cfg.External.PancheckURL),
				cur("pancheck_password", h.cfg.External.PancheckPass))
		}
	}
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

	// pancheck（存活性检测）
	pancheckOnline := false
	if h.pancheck != nil && h.pancheck.Configured() {
		if err := h.pancheck.Health(ctx); err == nil {
			pancheckOnline = true
		}
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
		"pancheckOnline": pancheckOnline,
		"goroutines":     runtime.NumGoroutine(),
		"memoryMB":       m.Alloc / 1024 / 1024,
		"transferQueue":  h.pool.QueueLen(),
		"pendingTasks":   taskCount,
		"userCount":      userCount,
		"resourceCount":  resourceCount,
		"categoryCount":  categoryCount,
	})
}
