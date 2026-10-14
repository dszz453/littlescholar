package handler

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// awardMinDays 一周里至少达标几天才发奖状。
//
// 定 5 天而不是 7 天：一周七天全勤对小学低年级太苛刻，偶尔缺一天很正常，
// 门槛设太高会让"坚持"变成挫败。5 天是"这一周确实养成了习惯"的合理标准。
const awardMinDays = 5

// PlanHandler 学习计划 / 每日打卡 / 每周 AI 奖状
type PlanHandler struct {
	db   *gorm.DB
	perm *service.PermissionService
	ai   *AIHandler
}

func NewPlanHandler(db *gorm.DB, perm *service.PermissionService, aiHandler *AIHandler) *PlanHandler {
	return &PlanHandler{db: db, perm: perm, ai: aiHandler}
}

// targetUserID 解析操作对象。
//
// 只有管理员能用 ?userId= 指定别人，孩子永远只能操作自己的计划 ——
// 否则孩子传个 userId 就能给自己的账号塞满别人的计划数据。
func (h *PlanHandler) targetUserID(c *gin.Context) uint64 {
	if util.IsAdmin(c) {
		if v := util.GetUintQuery(c, "userId", 0); v > 0 {
			return v
		}
	}
	return util.CurrentUserID(c)
}

// ============ 目录范围与当日时长 ============

// categoryScopeIDs 目录自身 + 所有子孙的 ID。
//
// 计划是挂在某个目录上的，但资源常常挂在它的子目录里（例如计划挂在
// 「乐学英语」而内容在「新概念英语」下）。只统计直属目录会永远算作 0 分钟。
func (h *PlanHandler) categoryScopeIDs(categoryID uint64) []uint64 {
	var cat model.Category
	if err := h.db.First(&cat, categoryID).Error; err != nil {
		return nil
	}
	seen := map[uint64]bool{cat.ID: true}
	out := []uint64{cat.ID}
	if cat.Path != "" {
		var more []uint64
		// path LIKE '/1/%' 会连自己一起命中（'/1/' 也匹配 '/1/%'），
		// 所以这里必须去重，否则 IN 列表里出现重复元素。
		if err := h.db.Model(&model.Category{}).
			Where("path LIKE ?", cat.Path+"%").Pluck("id", &more).Error; err == nil {
			for _, id := range more {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// studySecondsOn 指定日期在「目录集合」下的累计学习秒数。
func (h *PlanHandler) studySecondsOn(userID uint64, catIDs []uint64, date string) int64 {
	if len(catIDs) == 0 {
		return 0
	}
	var sec int64
	h.db.Model(&model.StudyRecord{}).
		Where("user_id = ? AND study_date = ? AND action IN ?", userID, date, playStudyActions).
		Where("category_id IN ?", catIDs).
		Select("COALESCE(SUM(duration),0)").Scan(&sec)
	return sec
}

// syncCheckIn 按当日真实学习情况维护打卡记录。
//
// 只写"达标"的记录，不写未完成的 —— 打卡表是荣誉墙不是待办清单，
// 而且周统计里直接 `completed = true` 去数就行，不用额外过滤。
// 返回 (当日分钟数, 是否达标)。
func (h *PlanHandler) syncCheckIn(plan *model.StudyPlan, date string) (int, bool) {
	catIDs := h.categoryScopeIDs(plan.CategoryID)
	if len(catIDs) == 0 {
		// 目录已被删除：没有可统计的范围，当作未达标而不是报错
		return 0, false
	}
	minutes := int(h.studySecondsOn(plan.UserID, catIDs, date) / 60)
	if minutes < plan.MinMinutes {
		return minutes, false
	}

	var ci model.StudyCheckIn
	err := h.db.Where("user_id = ? AND plan_id = ? AND study_date = ?",
		plan.UserID, plan.ID, date).First(&ci).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		h.db.Create(&model.StudyCheckIn{
			UserID: plan.UserID, PlanID: plan.ID, StudyDate: date,
			Minutes: minutes, Completed: true,
		})
	case err == nil && minutes > ci.Minutes:
		// 已经打过卡了，孩子又学了一会儿 → 把分钟数刷成更大的
		h.db.Model(&ci).Update("minutes", minutes)
	}
	return minutes, true
}

// ============ 日期工具 ============

// weekRange 返回所在周的周一与周日（YYYY-MM-DD）。
func weekRange(t time.Time) (string, string) {
	off := weekdayOffsetMonday(t)
	return t.AddDate(0, 0, -off).Format("2006-01-02"),
		t.AddDate(0, 0, 6-off).Format("2006-01-02")
}

// daysBetweenInclusive 起止日期之间的天数（含首尾）。结束早于开始时返回 0。
func daysBetweenInclusive(a, b string) int {
	ta, err1 := time.ParseInLocation("2006-01-02", a, time.Local)
	tb, err2 := time.ParseInLocation("2006-01-02", b, time.Local)
	if err1 != nil || err2 != nil || tb.Before(ta) {
		return 0
	}
	// 用日期分量计算，避开夏令时导致的 ±1 小时偏差
	return int(tb.Sub(ta).Hours()/24) + 1
}

// ============ 计划 CRUD ============

type planRequest struct {
	Title      string `json:"title"`
	CategoryID uint64 `json:"categoryId" binding:"required"`
	StartDate  string `json:"startDate" binding:"required"`
	EndDate    string `json:"endDate" binding:"required"`
	MinMinutes int    `json:"minMinutes"`
}

// planItem 计划列表项（含进度）
type planItem struct {
	ID           uint64 `json:"id"`
	Title        string `json:"title"`
	CategoryID   uint64 `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	StartDate    string `json:"startDate"`
	EndDate      string `json:"endDate"`
	MinMinutes   int    `json:"minMinutes"`
	Status       string `json:"status"`

	DaysChecked int  `json:"daysChecked"` // 已达标天数
	DaysElapsed int  `json:"daysElapsed"` // 从开始到今天（含）已过去的天数
	DaysTotal   int  `json:"daysTotal"`   // 整个区间天数
	ActiveToday bool `json:"activeToday"` // 今天是否在计划区间内

	TodayMinutes   int  `json:"todayMinutes"`
	TodayCompleted bool `json:"todayCompleted"`
	Expired        bool `json:"expired"`
}

// decorate 把计划补齐为带进度的列表项。
func (h *PlanHandler) decorate(plan *model.StudyPlan, today string) planItem {
	it := planItem{
		ID: plan.ID, Title: plan.Title, CategoryID: plan.CategoryID,
		CategoryName: plan.CategoryName, StartDate: plan.StartDate,
		EndDate: plan.EndDate, MinMinutes: plan.MinMinutes, Status: plan.Status,
		DaysTotal: daysBetweenInclusive(plan.StartDate, plan.EndDate),
		Expired:   today > plan.EndDate,
	}

	var checked int64
	h.db.Model(&model.StudyCheckIn{}).
		Where("user_id = ? AND plan_id = ? AND completed = ?", plan.UserID, plan.ID, true).
		Count(&checked)
	it.DaysChecked = int(checked)

	// 已过天数只数到今天为止，否则"进度 3/30 天"在刚开始时毫无意义
	elapsedTo := today
	if elapsedTo > plan.EndDate {
		elapsedTo = plan.EndDate
	}
	if elapsedTo >= plan.StartDate {
		it.DaysElapsed = daysBetweenInclusive(plan.StartDate, elapsedTo)
	}

	it.ActiveToday = plan.Status == model.StudyPlanStatusActive &&
		plan.StartDate <= today && today <= plan.EndDate
	if it.ActiveToday {
		it.TodayMinutes, it.TodayCompleted = h.syncCheckIn(plan, today)
	}
	return it
}

// List 我的学习计划
func (h *PlanHandler) List(c *gin.Context) {
	userID := h.targetUserID(c)
	today := time.Now().Format("2006-01-02")

	var plans []model.StudyPlan
	if err := h.db.Where("user_id = ?", userID).
		Order("status asc, id desc").Find(&plans).Error; err != nil {
		util.ServerError(c, "查询学习计划失败")
		return
	}

	items := make([]planItem, 0, len(plans))
	for i := range plans {
		items = append(items, h.decorate(&plans[i], today))
	}
	util.OK(c, items)
}

// Create 新建学习计划
func (h *PlanHandler) Create(c *gin.Context) {
	userID := h.targetUserID(c)
	if userID == 0 {
		util.Unauthorized(c, "未登录")
		return
	}
	var req planRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请选择目录并填写起止日期")
		return
	}

	start, end, err := normalizePlanRange(req.StartDate, req.EndDate)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	var cat model.Category
	if err := h.db.First(&cat, req.CategoryID).Error; err != nil {
		util.BadRequest(c, "目录不存在")
		return
	}

	// 只能给自己有权限的目录建计划。孩子若能对未授权目录建计划，
	// 计划会永远统计不到时长（因为那些资源他本来就读不到），
	// 表现为"计划怎么都完不成"，比直接拒绝更让人困惑。
	if !util.IsAdmin(c) {
		user := currentUser(c)
		if user != nil {
			ids, all, err := h.perm.AccessibleCategoryIDs(user)
			if err != nil {
				util.ServerError(c, "权限校验失败")
				return
			}
			if !allowCategoryID(ids, all, req.CategoryID) {
				util.Forbidden(c, "没有这个目录的访问权限")
				return
			}
		}
	}

	plan := &model.StudyPlan{
		UserID:       userID,
		Title:        orDefaultStr(req.Title, cat.Name),
		CategoryID:   cat.ID,
		CategoryName: cat.Name,
		StartDate:    start,
		EndDate:      end,
		MinMinutes:   clampMinutes(req.MinMinutes),
		Status:       model.StudyPlanStatusActive,
	}
	if err := h.db.Create(plan).Error; err != nil {
		util.ServerError(c, "创建学习计划失败")
		return
	}
	util.OK(c, h.decorate(plan, time.Now().Format("2006-01-02")))
}

// Update 修改学习计划
func (h *PlanHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的计划 ID")
		return
	}
	plan, ok := h.mustOwnPlan(c, id)
	if !ok {
		return
	}

	var req planRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	start, end, err := normalizePlanRange(req.StartDate, req.EndDate)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	updates := map[string]interface{}{
		"start_date":  start,
		"end_date":    end,
		"min_minutes": clampMinutes(req.MinMinutes),
	}
	if req.CategoryID > 0 {
		var cat model.Category
		if err := h.db.First(&cat, req.CategoryID).Error; err != nil {
			util.BadRequest(c, "目录不存在")
			return
		}
		updates["category_id"] = cat.ID
		updates["category_name"] = cat.Name
	}
	if req.Title != "" {
		updates["title"] = req.Title
	}
	if err := h.db.Model(plan).Updates(updates).Error; err != nil {
		util.ServerError(c, "更新失败")
		return
	}
	h.db.First(plan, plan.ID)
	util.OK(c, h.decorate(plan, time.Now().Format("2006-01-02")))
}

// Delete 删除（取消）学习计划
//
// 连带删除该计划的打卡记录与奖状关联 —— 打卡记录脱离计划没有意义，
// 留着只会在周统计里体现为"有过打卡但找不到对应计划"。
func (h *PlanHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的计划 ID")
		return
	}
	plan, ok := h.mustOwnPlan(c, id)
	if !ok {
		return
	}
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", plan.ID).Delete(&model.StudyCheckIn{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.StudyPlan{}, plan.ID).Error
	})
	if err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "已删除", nil)
}

// mustOwnPlan 取计划并校验归属。越权时统一返回 404（不暴露"这个 ID 存在"）。
func (h *PlanHandler) mustOwnPlan(c *gin.Context, id uint64) (*model.StudyPlan, bool) {
	var plan model.StudyPlan
	if err := h.db.First(&plan, id).Error; err != nil {
		util.NotFound(c, "学习计划不存在")
		return nil, false
	}
	if !util.IsAdmin(c) && plan.UserID != util.CurrentUserID(c) {
		util.NotFound(c, "学习计划不存在")
		return nil, false
	}
	return &plan, true
}

// ============ 今日打卡 ============

// Today 今日总览：孩子一进「学习计划」页就拿这个决定提示什么。
func (h *PlanHandler) Today(c *gin.Context) {
	userID := h.targetUserID(c)
	today := time.Now().Format("2006-01-02")

	var plans []model.StudyPlan
	h.db.Where("user_id = ? AND status = ?", userID, model.StudyPlanStatusActive).
		Where("start_date <= ? AND end_date >= ?", today, today).
		Order("id asc").Find(&plans)

	items := make([]planItem, 0, len(plans))
	allDone := len(plans) > 0
	for i := range plans {
		it := h.decorate(&plans[i], today)
		if !it.TodayCompleted {
			allDone = false
		}
		items = append(items, it)
	}

	// 本周坚持情况随今日总览一起下发：孩子端的"再坚持几天能拿奖状"
	// 全靠这几个数，单独开一个接口只会多一次往返。
	weekStart, weekEnd := weekRange(time.Now())
	stat := h.weekStatOf(userID, weekStart, weekEnd)
	var awardedCount int64
	h.db.Model(&model.StudyAward{}).
		Where("user_id = ? AND week_start = ?", userID, weekStart).Count(&awardedCount)

	util.OK(c, gin.H{
		"date":      today,
		"hasPlan":   len(items) > 0,
		"completed": allDone,
		"plans":     items,

		"weekStart":   weekStart,
		"weekEnd":     weekEnd,
		"weekDays":    stat.DaysChecked,
		"weekMinutes": stat.TotalMinutes,
		"awardDays":   awardMinDays,
		"awarded":     awardedCount > 0,
	})
}

// ============ 每周 AI 奖状 ============

// weekStat 一周的学习统计（喂给奖状生成）
type weekStat struct {
	DaysChecked  int
	TotalMinutes int
	Categories   []string
	Titles       []string
}

// weekStatOf 汇总某个用户某一周的学习情况。
func (h *PlanHandler) weekStatOf(userID uint64, weekStart, weekEnd string) weekStat {
	var out weekStat

	var days int64
	h.db.Model(&model.StudyCheckIn{}).
		Where("user_id = ? AND completed = ?", userID, true).
		Where("study_date >= ? AND study_date <= ?", weekStart, weekEnd).
		Distinct("study_date").Count(&days)
	out.DaysChecked = int(days)

	var sec int64
	h.db.Model(&model.StudyRecord{}).
		Where("user_id = ? AND study_date >= ? AND study_date <= ?", userID, weekStart, weekEnd).
		Where("action IN ?", playStudyActions).
		Select("COALESCE(SUM(duration),0)").Scan(&sec)
	out.TotalMinutes = int(sec / 60)

	out.Categories = h.distinctRecentColumn("category_name", userID, weekStart, weekEnd, 8)
	out.Titles = h.distinctRecentColumn("resource_title", userID, weekStart, weekEnd, 10)
	return out
}

// distinctRecentColumn 取某列的去重值，按最近出现的时间倒序。
//
// 用 Rows() 手工扫描而不是 Pluck：这里带了 GROUP BY + 聚合排序键，
// Pluck 会自作主张重写 SELECT 列表（本项目已被 gorm 的"静默丢列"坑过一次）。
func (h *PlanHandler) distinctRecentColumn(column string, userID uint64, from, to string, limit int) []string {
	// 用 Model 而不是 Table：Table("study_records") 会绕过 gorm 的软删除
	// 条件，把已删记录也算进奖状文案里（孩子会看到自己没学过的内容）。
	rows, err := h.db.Model(&model.StudyRecord{}).
		Select(column).
		Where("user_id = ? AND study_date >= ? AND study_date <= ?", userID, from, to).
		Where(column + " <> ''").
		Group(column).
		Order("MAX(happened_at) DESC").
		Limit(limit).
		Rows()
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]string, 0, limit)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// GenerateWeeklyAward 为指定用户生成某一周的奖状。
//
// 返回 (奖状, 是否本次新生成, error)：
//   - 本周达标天数不够 → (nil, false, nil)，不是错误
//   - 已经发过          → (已有那张, false, nil)
//   - 本次新生成        → (新奖状, true, nil)
func (h *PlanHandler) GenerateWeeklyAward(userID uint64, weekStart, weekEnd string) (*model.StudyAward, bool, error) {
	// 已发过就直接返回 —— 幂等，调度器每天跑也不会重复颁奖
	var existed model.StudyAward
	if err := h.db.Where("user_id = ? AND week_start = ?", userID, weekStart).
		First(&existed).Error; err == nil {
		return &existed, false, nil
	}

	stat := h.weekStatOf(userID, weekStart, weekEnd)
	if stat.DaysChecked < awardMinDays {
		return nil, false, nil
	}

	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		return nil, false, err
	}

	client, configID, err := h.ai.DefaultClient()
	if err != nil {
		return nil, false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var result ai.WeeklyAwardResult
	if err := client.ChatJSON(ctx, ai.BuildWeeklyAwardPrompt(ai.WeeklyAwardData{
		ChildName:    user.Nickname,
		Grade:        user.Grade,
		WeekStart:    weekStart,
		WeekEnd:      weekEnd,
		DaysChecked:  stat.DaysChecked,
		TotalMinutes: stat.TotalMinutes,
		Categories:   stat.Categories,
		Titles:       stat.Titles,
	}), &result); err != nil {
		return nil, false, err
	}

	award := &model.StudyAward{
		UserID:       userID,
		WeekStart:    weekStart,
		WeekEnd:      weekEnd,
		DaysChecked:  stat.DaysChecked,
		TotalMinutes: stat.TotalMinutes,
		Title:        orDefaultStr(result.Title, "坚持之星"),
		Emoji:        orDefaultStr(result.Emoji, "🏆"),
		Content:      result.Content,
		Color:        ai.NormalizeAwardColor(result.Color),
		AIConfigID:   configID,
	}

	// 调度器与端上刷新可能同时触发同一张奖状，靠唯一索引 + DoNothing 兜住，
	// 拿到冲突也不报错，回头把已有那张读出来返回。
	if err := h.db.Clauses(clause.OnConflict{DoNothing: true}).Create(award).Error; err != nil {
		return nil, false, err
	}
	if award.ID == 0 {
		if err := h.db.Where("user_id = ? AND week_start = ?", userID, weekStart).
			First(&existed).Error; err == nil {
			return &existed, false, nil
		}
		return nil, false, errors.New("奖状写入冲突且无法回读")
	}
	return award, true, nil
}

// Awards 我的奖状列表
func (h *PlanHandler) Awards(c *gin.Context) {
	userID := h.targetUserID(c)
	page, pageSize := util.GetPage(c)

	q := h.db.Model(&model.StudyAward{}).Where("user_id = ?", userID)
	var total int64
	q.Count(&total)

	var list []model.StudyAward
	q.Order("week_start desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)
	util.Page(c, total, page, pageSize, list)
}

// RefreshAward 立刻尝试为本周期生成奖状（端上"我能不能拿奖状"按钮）。
func (h *PlanHandler) RefreshAward(c *gin.Context) {
	userID := h.targetUserID(c)
	weekStart, weekEnd := weekRange(time.Now())

	award, generated, err := h.GenerateWeeklyAward(userID, weekStart, weekEnd)
	if err != nil {
		util.ServerError(c, "生成奖状失败: "+err.Error())
		return
	}
	if award == nil {
		stat := h.weekStatOf(userID, weekStart, weekEnd)
		util.OK(c, gin.H{
			"generated": false,
			"award":     nil,
			"needDays":  awardMinDays,
			"days":      stat.DaysChecked,
			"message":   "本周再坚持几天就能拿到奖状啦",
		})
		return
	}
	util.OK(c, gin.H{"generated": generated, "award": award})
}

// ============ 定时生成 ============

// StartAwardScheduler 周期性检查并为达标的孩子生成周奖状。
//
// 与 service.StartAliveScheduler 同一套骨架：stopCh + ticker + 首轮延迟 +
// 每轮 recover。不这么写的话，一次 AI 调用 panic 会让整个后台循环静默死掉。
func (h *PlanHandler) StartAwardScheduler(logger *zap.Logger, interval, firstDelay time.Duration) func() {
	if h == nil || logger == nil {
		return func() {}
	}
	stopCh := make(chan struct{})

	go func() {
		if firstDelay > 0 {
			select {
			case <-time.After(firstDelay):
			case <-stopCh:
				return
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							logger.Error("周奖状生成 panic", zap.Any("recover", r))
						}
					}()
					h.runAwardRound(logger)
				}()
			}
		}
	}()
	return func() { close(stopCh) }
}

// runAwardRound 扫一遍所有孩子账号，为本周已达标者颁奖。
func (h *PlanHandler) runAwardRound(logger *zap.Logger) {
	weekStart, weekEnd := weekRange(time.Now())

	var userIDs []uint64
	if err := h.db.Model(&model.User{}).
		Where("role = ?", model.RoleKid).Pluck("id", &userIDs).Error; err != nil {
		logger.Warn("读取孩子账号失败", zap.Error(err))
		return
	}

	for _, uid := range userIDs {
		award, generated, err := h.GenerateWeeklyAward(uid, weekStart, weekEnd)
		if err != nil {
			// AI 不可用是常态（额度/网络），记 warn 下轮再试，不中断整轮
			logger.Warn("生成周奖状失败", zap.Uint64("userId", uid), zap.Error(err))
			continue
		}
		if generated {
			logger.Info("已生成周奖状",
				zap.Uint64("userId", uid),
				zap.String("week", weekStart),
				zap.String("title", award.Title))
		}
	}
}

// ============ 参数校验助手 ============

// normalizePlanRange 校验并规范化日期区间。
func normalizePlanRange(start, end string) (string, string, error) {
	ts, err := time.ParseInLocation("2006-01-02", start, time.Local)
	if err != nil {
		return "", "", errors.New("开始日期格式应为 YYYY-MM-DD")
	}
	te, err := time.ParseInLocation("2006-01-02", end, time.Local)
	if err != nil {
		return "", "", errors.New("结束日期格式应为 YYYY-MM-DD")
	}
	if te.Before(ts) {
		return "", "", errors.New("结束日期不能早于开始日期")
	}
	// 上限一年：再长就不叫"学习计划"了，而且打卡统计会变得很重
	if te.Sub(ts).Hours()/24 > 366 {
		return "", "", errors.New("计划区间最长一年")
	}
	return ts.Format("2006-01-02"), te.Format("2006-01-02"), nil
}

// clampMinutes 每日最低时长的合理区间。
func clampMinutes(v int) int {
	if v <= 0 {
		return 20
	}
	if v > 600 {
		return 600
	}
	return v
}
