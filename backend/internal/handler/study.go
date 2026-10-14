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

// playStudyActions 计入「学习时长」的行为类型
var playStudyActions = []string{"play_progress", "play_finish"}

// resolveStudyUser 解析统计对象。
//
// 显式 userId 参数优先（管理端按选定用户查看）；未传时回落到**当前登录用户**。
// 不能像以前那样缺省成 0 —— user_id=0 会让「总时长按全站合计、按目录/按天
// 分布却硬编码 user_id=0 返回空」自相矛盾，孩子端正是那个不传 userId 的调用方。
func resolveStudyUser(c *gin.Context) uint64 {
	if v := util.GetUintQuery(c, "userId", 0); v > 0 {
		return v
	}
	return util.CurrentUserID(c)
}

// scopedRecords 构造「日期区间 + 用户 + 学习行为」的基础查询。
//
// 区间比较用 study_date（写入时按本地时间生成的 YYYY-MM-DD）而不是 happened_at：
// 字符串字典序即时间序，彻底避开「纯日期串 vs 带时区时间戳」的隐式转换，
// 也省掉了原先 endDate+" 23:59:59" 漏掉最后一秒的毛病。
//
// 每次调用都新建查询：gorm 的链式 Where 会挂在同一个 Statement 上，
// 复用变量会让条件互相污染。
func (h *StudyHandler) scopedRecords(from, to string, userID uint64) *gorm.DB {
	q := h.db.Model(&model.StudyRecord{}).
		Where("study_date >= ? AND study_date <= ?", from, to).
		Where("action IN ?", playStudyActions)
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	return q
}

// sumDuration 统计区间内累计学习秒数
func (h *StudyHandler) sumDuration(from, to string, userID uint64) int64 {
	var sec int64
	h.scopedRecords(from, to, userID).
		Select("COALESCE(SUM(duration),0)").Scan(&sec)
	return sec
}

// categoryStat / dayStat / hourStat 聚合结果的响应结构
type categoryStat struct {
	CategoryID   uint64 `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	Minutes      int64  `json:"minutes"`
}

type dayStat struct {
	Date    string `json:"date"`
	Minutes int64  `json:"minutes"`
}

type hourStat struct {
	Hour    int   `json:"hour"`
	Minutes int64 `json:"minutes"`
}

// 三条分布查询一律走 Rows() + 手工按序扫描，**不用** Scan(&自定义切片)。
//
// 原因：`Model(&StudyRecord{}).Select("... AS secs").Scan(&[]T)` 里，gorm 会拿
// Model 的 schema 去核对查询返回了哪些列，聚合别名 `secs` 不在 StudyRecord 的字段里
// 就被静默丢弃 —— 不报错、直接留零值。SQLite 驱动下侥幸能过、PostgreSQL 下必现
// （线上实测：totalMinutes=19 而 byCategory 全为 0），是"单测绿、线上废"的典型。
// 手工扫描只认 SELECT 顺序，与驱动无关，测试与线上才真正走同一条路径。
func (h *StudyHandler) categoryStats(from, to string, userID uint64) ([]categoryStat, error) {
	rows, err := h.scopedRecords(from, to, userID).
		Select("category_id, category_name, COALESCE(SUM(duration),0) AS secs").
		Group("category_id, category_name").
		Order("secs DESC").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]categoryStat, 0)
	for rows.Next() {
		var (
			it   categoryStat
			secs int64
		)
		if err := rows.Scan(&it.CategoryID, &it.CategoryName, &secs); err != nil {
			return nil, err
		}
		it.Minutes = secs / 60
		out = append(out, it)
	}
	return out, rows.Err()
}

func (h *StudyHandler) dayStats(from, to string, userID uint64) ([]dayStat, error) {
	rows, err := h.scopedRecords(from, to, userID).
		Select("study_date AS d, COALESCE(SUM(duration),0) AS secs").
		Group("study_date").
		Order("d ASC").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]dayStat, 0)
	for rows.Next() {
		var (
			it   dayStat
			secs int64
		)
		if err := rows.Scan(&it.Date, &secs); err != nil {
			return nil, err
		}
		it.Minutes = secs / 60
		out = append(out, it)
	}
	return out, rows.Err()
}

func (h *StudyHandler) hourStats(from, to string, userID uint64) ([]hourStat, error) {
	rows, err := h.scopedRecords(from, to, userID).
		Select("study_hour AS h, COALESCE(SUM(duration),0) AS secs").
		Group("study_hour").
		Order("h ASC").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]hourStat, 0)
	for rows.Next() {
		var (
			it   hourStat
			secs int64
		)
		if err := rows.Scan(&it.Hour, &secs); err != nil {
			return nil, err
		}
		it.Minutes = secs / 60
		out = append(out, it)
	}
	return out, rows.Err()
}

// weekdayOffsetMonday 距本周一的天数（周一=0 … 周日=6）
func weekdayOffsetMonday(t time.Time) int {
	return (int(t.Weekday()) + 6) % 7
}

// Overview 学习统计概览
func (h *StudyHandler) Overview(c *gin.Context) {
	userID := resolveStudyUser(c)

	now := time.Now()
	startDate := c.DefaultQuery("startDate", now.AddDate(0, 0, -30).Format("2006-01-02"))
	endDate := c.DefaultQuery("endDate", now.Format("2006-01-02"))

	// 总时长（秒）
	totalSeconds := h.sumDuration(startDate, endDate, userID)

	// 分布统计（按目录 / 按天 / 按小时）与总数共用同一套过滤条件，
	// 否则又会出现"总数有值、图表全空"的口径分裂。
	byCategory, err := h.categoryStats(startDate, endDate, userID)
	if err != nil {
		util.ServerError(c, "读取分类统计失败")
		return
	}
	byDay, err := h.dayStats(startDate, endDate, userID)
	if err != nil {
		util.ServerError(c, "读取学习趋势失败")
		return
	}
	byHour, err := h.hourStats(startDate, endDate, userID)
	if err != nil {
		util.ServerError(c, "读取时段分布失败")
		return
	}

	// 完成 / 进行中资源数
	var finished, inProgress int64
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ?", userID, true).Count(&finished)
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ? AND percentage > 0", userID, false).Count(&inProgress)

	// 学习天数（去重）
	var daysCount int64
	h.scopedRecords(startDate, endDate, userID).Distinct("study_date").Count(&daysCount)

	// 今日 / 本周（周一为起点）
	today := now.Format("2006-01-02")
	weekStart := now.AddDate(0, 0, -weekdayOffsetMonday(now)).Format("2006-01-02")
	todaySeconds := h.sumDuration(today, today, userID)
	weekSeconds := h.sumDuration(weekStart, today, userID)

	util.OK(c, gin.H{
		"totalMinutes":        totalSeconds / 60,
		"totalDays":           daysCount,
		"byCategory":          byCategory,
		"byDay":               byDay,
		"byHour":              byHour,
		"finishedResources":   finished,
		"inProgressResources": inProgress,

		// 孩子端「我的」页顶部三个数字直接吃这几个字段
		"todaySeconds": todaySeconds,
		"weekSeconds":  weekSeconds,
		"todayMinutes": todaySeconds / 60,
		"weekMinutes":  weekSeconds / 60,
	})
}

// Records 学习记录明细。
//
// groupBy=resource 时按资源聚合：一次播放会每 15 秒落一条心跳记录，
// 直接列明细的话孩子端「学习记录」会被同一个资源刷屏，看不出"学了什么"。
// 聚合后每行 = 一个资源，带累计时长与最后学习时间。
func (h *StudyHandler) Records(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	userID := resolveStudyUser(c)

	if c.Query("groupBy") == "resource" {
		h.recordsGroupedByResource(c, page, pageSize, userID)
		return
	}

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

// recordsGroupedByResource 按资源聚合的学习记录
//
// 这里刻意用裸 SQL + 手工 Scan，而不是 `Model(...).Scan(&聚合结构)`：
// gorm 的自动扫描在这种"聚合别名 + 自定义结构"的组合下会**静默丢列**
// （实测 `COUNT(*) AS times`、`MAX(happened_at) AS happened_at` 都扫不进来，
// 不报错、直接给零值），踩过一次不想再踩。手工 Scan 按 SELECT 顺序取，稳。
func (h *StudyHandler) recordsGroupedByResource(c *gin.Context, page, pageSize int, userID uint64) {
	where := ""
	countArgs := []interface{}{}
	if userID > 0 {
		where = " WHERE user_id = ?"
		countArgs = append(countArgs, userID)
	}

	var total int64
	if err := h.db.Raw("SELECT COUNT(DISTINCT resource_id) FROM study_records"+where, countArgs...).
		Scan(&total).Error; err != nil {
		util.ServerError(c, "统计学习记录失败")
		return
	}

	const cols = `SELECT resource_id,
			MAX(resource_title)       AS resource_title,
			MAX(category_id)          AS category_id,
			MAX(category_name)        AS category_name,
			MAX(media_type)           AS media_type,
			COALESCE(SUM(duration),0) AS duration,
			MAX(happened_at)          AS happened_at,
			COUNT(*)                  AS times
		FROM study_records`

	args := append([]interface{}{}, countArgs...)
	// PostgreSQL 不允许 ORDER BY 引用既不在 GROUP BY、又不是聚合结果的列，
	// 所以排序键必须写作 MAX(happened_at)。
	sqlText := cols + where + " GROUP BY resource_id ORDER BY MAX(happened_at) DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize, (page-1)*pageSize)

	rows, err := h.db.Raw(sqlText, args...).Rows()
	if err != nil {
		util.ServerError(c, "查询学习记录失败")
		return
	}
	defer rows.Close()

	list := make([]studyItem, 0)
	for rows.Next() {
		var it studyItem
		// happened_at 扫成 interface{} 再自行解析：PostgreSQL 的 timestamptz
		// 直接给 time.Time，而 SQLite 存的是字符串，sql.NullTime 只认
		// time.Time（字符串会报 unsupported Scan），两边都要能跑。
		var rawHappenedAt interface{}
		if err := rows.Scan(
			&it.ResourceID, &it.ResourceTitle, &it.CategoryID,
			&it.CategoryName, &it.MediaType, &it.Duration,
			&rawHappenedAt, &it.Times,
		); err != nil {
			util.ServerError(c, "读取学习记录失败")
			return
		}
		it.HappenedAt = parseFlexibleTime(rawHappenedAt)
		list = append(list, it)
	}

	util.Page(c, total, page, pageSize, list)
}

// parseFlexibleTime 解析各数据库驱动返回的时间表示
func parseFlexibleTime(v interface{}) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case []byte:
		return parseFlexibleTime(string(t))
	case string:
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05-07:00",
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05.999999999",
			time.DateOnly,
		} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// studyItem 按资源聚合后的一行
type studyItem struct {
	ResourceID    uint64    `json:"resourceId"`
	ResourceTitle string    `json:"resourceTitle"`
	CategoryID    uint64    `json:"categoryId"`
	CategoryName  string    `json:"categoryName"`
	MediaType     string    `json:"mediaType"`
	Duration      int64     `json:"duration"`
	HappenedAt    time.Time `json:"happenedAt"`
	Times         int64     `json:"times"`
}

// Analyze 生成 AI 学情报告
func (h *StudyHandler) Analyze(c *gin.Context) {
	var req struct {
		UserID      uint64 `json:"userId" binding:"required"`
		StartDate   string `json:"startDate"`
		EndDate     string `json:"endDate"`
		AIConfigID  uint64 `json:"aiConfigId"`
		ExtraPrompt string `json:"extraPrompt"`
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

// collectStats 汇总统计数据（喂给 AI 学情分析）
func (h *StudyHandler) collectStats(userID uint64, startDate, endDate string) map[string]interface{} {
	// 与 Overview 共用同一套分布查询，避免 AI 报告里的统计口径与页面不一致。
	byCategory, err := h.categoryStats(startDate, endDate, userID)
	if err != nil {
		byCategory = nil
	}
	byDay, err := h.dayStats(startDate, endDate, userID)
	if err != nil {
		byDay = nil
	}

	var totalMinutes int64
	for _, d := range byDay {
		totalMinutes += d.Minutes
	}

	var finished int64
	h.db.Model(&model.PlayRecord{}).Where("user_id = ? AND finished = ?", userID, true).Count(&finished)

	return map[string]interface{}{
		"periodDays":        len(byDay),
		"totalMinutes":      totalMinutes,
		"avgMinutesPerDay":  safeDiv(totalMinutes, int64(len(byDay))),
		"byCategory":        byCategory,
		"byDay":             byDay,
		"finishedResources": finished,
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
