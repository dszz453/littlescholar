package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// StudyHandler 学习记录与 AI 学情分析
type StudyHandler struct {
	db *gorm.DB
	ai *AIHandler
}

func NewStudyHandler(db *gorm.DB, aiHandler *AIHandler) *StudyHandler {
	return &StudyHandler{db: db, ai: aiHandler}
}

// Overview 学习统计概览
func (h *StudyHandler) Overview(c *gin.Context) {
	userID := util.GetUintQuery(c, "userId", 0)
	startDate := c.DefaultQuery("startDate", time.Now().AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := c.DefaultQuery("endDate", time.Now().Format("2006-01-02"))

	q := h.db.Model(&model.StudyRecord{}).
		Where("happened_at >= ? AND happened_at < ?", startDate, endDate+" 23:59:59")
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}

	// 总时长（秒）
	var totalSeconds int64
	q.Where("action IN ?", []string{"play_progress", "play_finish"}).
		Select("COALESCE(SUM(duration),0)").Scan(&totalSeconds)

	// 按目录统计（分钟）
	type categoryStat struct {
		CategoryID   uint64 `json:"categoryId"`
		CategoryName string `json:"categoryName"`
		Minutes      int64  `json:"minutes"`
	}
	var byCategory []categoryStat
	h.db.Model(&model.StudyRecord{}).
		Select("category_id, category_name, COALESCE(SUM(duration),0)/60 as minutes").
		Where("happened_at >= ? AND happened_at < ?", startDate, endDate+" 23:59:59").
		Where("user_id = ?", userID).
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Group("category_id, category_name").
		Order("minutes desc").
		Scan(&byCategory)

	// 按天统计
	type dayStat struct {
		Date    string `json:"date"`
		Minutes int64  `json:"minutes"`
	}
	var byDay []dayStat
	h.db.Model(&model.StudyRecord{}).
		Select("study_date as date, COALESCE(SUM(duration),0)/60 as minutes").
		Where("happened_at >= ? AND happened_at < ?", startDate, endDate+" 23:59:59").
		Where("user_id = ?", userID).
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Group("study_date").Order("study_date asc").
		Scan(&byDay)

	// 按小时分布
	type hourStat struct {
		Hour    int   `json:"hour"`
		Minutes int64 `json:"minutes"`
	}
	var byHour []hourStat
	h.db.Model(&model.StudyRecord{}).
		Select("study_hour as hour, COALESCE(SUM(duration),0)/60 as minutes").
		Where("happened_at >= ? AND happened_at < ?", startDate, endDate+" 23:59:59").
		Where("user_id = ?", userID).
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Group("study_hour").Order("study_hour asc").
		Scan(&byHour)

	// 完成 / 进行中资源数
	var finished, inProgress int64
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ?", userID, true).Count(&finished)
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ? AND percentage > 0", userID, false).Count(&inProgress)

	// 学习天数（去重）
	var daysCount int64
	h.db.Model(&model.StudyRecord{}).
		Where("happened_at >= ? AND happened_at < ?", startDate, endDate+" 23:59:59").
		Where("user_id = ?", userID).
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Distinct("study_date").Count(&daysCount)

	util.OK(c, gin.H{
		"totalMinutes":        totalSeconds / 60,
		"totalDays":           daysCount,
		"byCategory":          byCategory,
		"byDay":               byDay,
		"byHour":              byHour,
		"finishedResources":   finished,
		"inProgressResources": inProgress,
	})
}

// Records 学习记录明细
func (h *StudyHandler) Records(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	userID := util.GetUintQuery(c, "userId", 0)

	q := h.db.Model(&model.StudyRecord{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	var total int64
	q.Count(&total)

	var list []model.StudyRecord
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)
	util.Page(c, total, page, pageSize, list)
}

// Analyze 生成 AI 学情报告
func (h *StudyHandler) Analyze(c *gin.Context) {
	var req struct {
		UserID      uint64     `json:"userId" binding:"required"`
		StartDate   string     `json:"startDate"`
		EndDate     string     `json:"endDate"`
		AIConfigID  uint64     `json:"aiConfigId"`
		ExtraPrompt string     `json:"extraPrompt"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请指定要分析的用户")
		return
	}

	var user model.User
	if err := h.db.First(&user, req.UserID).Error; err != nil {
		util.NotFound(c, "用户不存在")
		return
	}

	startDate := orDefaultStr(req.StartDate, time.Now().AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := orDefaultStr(req.EndDate, time.Now().Format("2006-01-02"))

	// 收集统计数据
	stats := h.collectStats(req.UserID, startDate, endDate)

	// 用户可访问的目录
	var grants []model.UserCategoryGrant
	h.db.Preload("Category").Where("user_id = ?", req.UserID).Find(&grants)
	var resources []string
	for _, g := range grants {
		if g.Category != nil {
			resources = append(resources, g.Category.Name)
		}
	}

	client, configID, err := h.ai.DefaultClient()
	if err != nil {
		util.ServerError(c, "未配置 AI 接口，请先在后台配置")
		return
	}
	if req.AIConfigID > 0 {
		if c2, err := h.ai.buildClient(req.AIConfigID); err == nil {
			client = c2
			configID = req.AIConfigID
		}
	}

	messages := ai.BuildStudyAnalysisPrompt(user.Nickname, user.Grade, stats, resources)
	if req.ExtraPrompt != "" {
		messages = append(messages, ai.Message{Role: "user", Content: "补充要求：" + req.ExtraPrompt})
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	var result struct {
		Summary     string   `json:"summary"`
		Highlights  []string `json:"highlights"`
		Weaknesses  []string `json:"weaknesses"`
		Suggestions []string `json:"suggestions"`
	}
	if err := client.ChatJSON(ctx, messages, &result); err != nil {
		util.ServerError(c, "AI 分析失败: "+err.Error())
		return
	}

	report := &model.AIReport{
		UserID:      req.UserID,
		AIConfigID:  configID,
		PeriodStart: parseDate(startDate),
		PeriodEnd:   parseDate(endDate),
		Summary:     result.Summary,
		Highlights:  model.StringArray(result.Highlights),
		Weaknesses:  model.StringArray(result.Weaknesses),
		Suggestions: model.StringArray(result.Suggestions),
		Stats:       toJSONMap(stats),
	}
	if err := h.db.Create(report).Error; err != nil {
		util.ServerError(c, "保存报告失败")
		return
	}
	util.OK(c, gin.H{"reportId": report.ID})
}

// Reports 报告列表
func (h *StudyHandler) Reports(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	userID := util.GetUintQuery(c, "userId", 0)

	q := h.db.Model(&model.AIReport{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	var total int64
	q.Count(&total)

	var list []model.AIReport
	q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)
	util.Page(c, total, page, pageSize, list)
}

// ReportDetail 报告详情
func (h *StudyHandler) ReportDetail(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的报告 ID")
		return
	}
	var report model.AIReport
	if err := h.db.First(&report, id).Error; err != nil {
		util.NotFound(c, "报告不存在")
		return
	}
	util.OK(c, report)
}

// DeleteReport 删除报告
func (h *StudyHandler) DeleteReport(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的报告 ID")
		return
	}
	h.db.Delete(&model.AIReport{}, id)
	util.OKWithMessage(c, "删除成功", nil)
}

// collectStats 汇总统计数据
func (h *StudyHandler) collectStats(userID uint64, startDate, endDate string) map[string]interface{} {
	type catStat struct {
		CategoryName string `json:"categoryName"`
		Minutes      int64  `json:"minutes"`
	}
	var byCategory []catStat
	h.db.Model(&model.StudyRecord{}).
		Select("category_name, COALESCE(SUM(duration),0)/60 as minutes").
		Where("user_id = ? AND happened_at >= ? AND happened_at < ?", userID, startDate, endDate+" 23:59:59").
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Group("category_name").Order("minutes desc").Scan(&byCategory)

	type dayStat struct {
		Date    string `json:"date"`
		Minutes int64  `json:"minutes"`
	}
	var byDay []dayStat
	h.db.Model(&model.StudyRecord{}).
		Select("study_date as date, COALESCE(SUM(duration),0)/60 as minutes").
		Where("user_id = ? AND happened_at >= ? AND happened_at < ?", userID, startDate, endDate+" 23:59:59").
		Where("action IN ?", []string{"play_progress", "play_finish"}).
		Group("study_date").Order("study_date asc").Scan(&byDay)

	var totalMinutes int64
	for _, d := range byDay {
		totalMinutes += d.Minutes
	}

	var finished int64
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ?", userID, true).Count(&finished)

	return map[string]interface{}{
		"periodDays":         len(byDay),
		"totalMinutes":       totalMinutes,
		"avgMinutesPerDay":   safeDiv(totalMinutes, int64(len(byDay))),
		"byCategory":         byCategory,
		"byDay":              byDay,
		"finishedResources":  finished,
	}
}

func safeDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func parseDate(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Now()
	}
	return t
}

func toJSONMap(v interface{}) model.JSONMap {
	m, ok := v.(map[string]interface{})
	if !ok {
		return model.JSONMap{}
	}
	return model.JSONMap(m)
}
