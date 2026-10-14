package handler

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 学习计划这组测试守三件事：
//
//  1. 「每天最低时长」的达标判定 —— 这是"打卡"的唯一口径，算错的话
//     孩子要么永远完不成、要么没学就拿到奖状。
//  2. 统计范围必须覆盖子目录 —— 计划常常挂在父目录而内容在子目录里，
//     只统计直属目录会让计划永远是 0 分钟。
//  3. 日期区间的口径（周一为起点、含首尾）。

func newPlanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:plan_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserCategoryGrant{}, &model.Category{},
		&model.Resource{}, &model.StudyRecord{},
		&model.StudyPlan{}, &model.StudyCheckIn{}, &model.StudyAward{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// newPlanTree 建一个"父目录 + 子目录"的两层结构。
func newPlanTree(t *testing.T, db *gorm.DB) (parent, child model.Category) {
	t.Helper()
	parent = model.Category{Name: "乐学英语", IsPublic: true, Level: 1}
	mustCreate(t, db, &parent)
	parent.Path = fmt.Sprintf("/%d/", parent.ID)
	db.Model(&model.Category{}).Where("id = ?", parent.ID).Update("path", parent.Path)

	child = model.Category{Name: "新概念英语", ParentID: &parent.ID, Level: 2}
	mustCreate(t, db, &child)
	child.Path = parent.Path + fmt.Sprintf("%d/", child.ID)
	db.Model(&model.Category{}).Where("id = ?", child.ID).Update("path", child.Path)
	return
}

func seedPlanKid(t *testing.T, db *gorm.DB) *model.User {
	t.Helper()
	kid := &model.User{
		Username: "plan_kid", Nickname: "安宁",
		Role: model.RoleKid, Status: model.UserStatusEnabled, Grade: "三年级",
	}
	mustCreate(t, db, kid)
	return kid
}

// seedPlayRecord 造一条计入学习时长的心跳记录。
func seedPlayRecord(t *testing.T, db *gorm.DB, userID, catID uint64, catName, date string, seconds int) {
	t.Helper()
	mustCreate(t, db, &model.StudyRecord{
		UserID: userID, ResourceID: 1, CategoryID: catID, CategoryName: catName,
		ResourceTitle: "第一课", Action: "play_progress", Duration: seconds,
		HappenedAt: parseDate(date).Add(10 * time.Hour),
		StudyDate:  date, StudyHour: 10,
	})
}

// TestCheckInRequiresMinMinutes 未达最低时长不算打卡，达标后落一条记录。
func TestCheckInRequiresMinMinutes(t *testing.T) {
	db := newPlanTestDB(t)
	parent, _ := newPlanTree(t, db)
	kid := seedPlanKid(t, db)

	plan := model.StudyPlan{
		UserID: kid.ID, CategoryID: parent.ID, CategoryName: parent.Name,
		StartDate: "2026-10-01", EndDate: "2026-10-31", MinMinutes: 20,
		Status: model.StudyPlanStatusActive,
	}
	mustCreate(t, db, &plan)
	h := &PlanHandler{db: db}
	today := "2026-10-14"

	// 学了 10 分钟 —— 不够
	seedPlayRecord(t, db, kid.ID, parent.ID, parent.Name, today, 600)
	minutes, done := h.syncCheckIn(&plan, today)
	if done {
		t.Errorf("10 分钟不该判定达标（门槛 20 分钟）")
	}
	if minutes != 10 {
		t.Errorf("当日分钟数=%d，期望 10", minutes)
	}
	var n int64
	db.Model(&model.StudyCheckIn{}).Count(&n)
	if n != 0 {
		t.Errorf("未达标却写了 %d 条打卡记录，打卡表只应记成功", n)
	}

	// 又学了 15 分钟 —— 累计 25 分钟，达标
	seedPlayRecord(t, db, kid.ID, parent.ID, parent.Name, today, 900)
	minutes, done = h.syncCheckIn(&plan, today)
	if !done {
		t.Fatalf("累计 25 分钟应判定达标，实际 minutes=%d", minutes)
	}
	if minutes != 25 {
		t.Errorf("当日分钟数=%d，期望 25", minutes)
	}

	// 反复调用不能刷出重复记录
	h.syncCheckIn(&plan, today)
	h.syncCheckIn(&plan, today)
	db.Model(&model.StudyCheckIn{}).Count(&n)
	if n != 1 {
		t.Errorf("打卡记录 %d 条，期望 1 条（同一天同一计划只应有一条）", n)
	}

	// 再学一会儿，分钟数应该被刷新成更大的值
	seedPlayRecord(t, db, kid.ID, parent.ID, parent.Name, today, 600)
	if _, _ = h.syncCheckIn(&plan, today); false {
		// no-op：仅为可读性保留分支
	}
	var ci model.StudyCheckIn
	db.Where("plan_id = ?", plan.ID).First(&ci)
	if ci.Minutes != 35 {
		t.Errorf("打卡分钟数=%d，期望刷新为 35", ci.Minutes)
	}
}

// TestCheckInCountsSubCategories 计划挂父目录、内容在子目录时也要算进去。
//
// 这是最容易出错的一环：孩子实际看的内容大多在子目录里，
// 只统计直属目录会让"计划永远完不成"，而界面完全看不出哪里错了。
func TestCheckInCountsSubCategories(t *testing.T) {
	db := newPlanTestDB(t)
	parent, child := newPlanTree(t, db)
	kid := seedPlanKid(t, db)

	plan := model.StudyPlan{
		UserID: kid.ID, CategoryID: parent.ID, CategoryName: parent.Name,
		StartDate: "2026-10-01", EndDate: "2026-10-31", MinMinutes: 10,
		Status: model.StudyPlanStatusActive,
	}
	mustCreate(t, db, &plan)
	h := &PlanHandler{db: db}

	// 记录挂在**子目录**上
	seedPlayRecord(t, db, kid.ID, child.ID, child.Name, "2026-10-14", 15*60)

	minutes, done := h.syncCheckIn(&plan, "2026-10-14")
	if !done {
		t.Errorf("子目录里的 15 分钟应计入父目录的计划，实际 minutes=%d done=%v", minutes, done)
	}
	if minutes != 15 {
		t.Errorf("当日分钟数=%d，期望 15", minutes)
	}
}

// TestCheckInIgnoresOtherUsers 别人的学习时长不能算进我的计划。
func TestCheckInIgnoresOtherUsers(t *testing.T) {
	db := newPlanTestDB(t)
	parent, _ := newPlanTree(t, db)
	kid := seedPlanKid(t, db)
	other := &model.User{Username: "other_kid", Role: model.RoleKid, Status: model.UserStatusEnabled}
	mustCreate(t, db, other)

	plan := model.StudyPlan{
		UserID: kid.ID, CategoryID: parent.ID, CategoryName: parent.Name,
		StartDate: "2026-10-01", EndDate: "2026-10-31", MinMinutes: 10,
		Status: model.StudyPlanStatusActive,
	}
	mustCreate(t, db, &plan)
	h := &PlanHandler{db: db}

	seedPlayRecord(t, db, other.ID, parent.ID, parent.Name, "2026-10-14", 60*60)
	if minutes, done := h.syncCheckIn(&plan, "2026-10-14"); done || minutes != 0 {
		t.Errorf("把别人的时长算进了我的计划：minutes=%d done=%v", minutes, done)
	}
}

// TestCategoryScopeIDsDedup 范围里不能出现重复 ID（自身会被 path LIKE 再命中一次）。
func TestCategoryScopeIDsDedup(t *testing.T) {
	db := newPlanTestDB(t)
	parent, child := newPlanTree(t, db)
	h := &PlanHandler{db: db}

	ids := h.categoryScopeIDs(parent.ID)
	seen := map[uint64]int{}
	for _, id := range ids {
		seen[id]++
	}
	if seen[parent.ID] != 1 {
		t.Errorf("父目录 ID 出现 %d 次，期望 1 次：%v", seen[parent.ID], ids)
	}
	if seen[child.ID] != 1 {
		t.Errorf("子目录 ID 出现 %d 次，期望 1 次：%v", seen[child.ID], ids)
	}
	if len(ids) != 2 {
		t.Errorf("范围应有 2 个目录，实际 %v", ids)
	}
}

// TestWeekRange 周一为起点、周日为终点。
func TestWeekRange(t *testing.T) {
	// 2026-10-14 是周三，所在周为 10-12（一）~ 10-18（日）
	wed := time.Date(2026, 10, 14, 10, 30, 0, 0, time.Local)
	start, end := weekRange(wed)
	if start != "2026-10-12" || end != "2026-10-18" {
		t.Errorf("周三的周区间 = %s ~ %s，期望 2026-10-12 ~ 2026-10-18", start, end)
	}

	// 周一与周日必须落在同一周内
	mon := time.Date(2026, 10, 12, 0, 0, 0, 0, time.Local)
	s2, e2 := weekRange(mon)
	if s2 != start || e2 != end {
		t.Errorf("周一的周区间 = %s ~ %s，应与周三一致", s2, e2)
	}
	sun := time.Date(2026, 10, 18, 23, 59, 0, 0, time.Local)
	s3, e3 := weekRange(sun)
	if s3 != start || e3 != end {
		t.Errorf("周日的周区间 = %s ~ %s，应与周三一致（周日属于本周而不是下周）", s3, e3)
	}
}

func TestDaysBetweenInclusive(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2026-10-01", "2026-10-01", 1},
		{"2026-10-01", "2026-10-02", 2},
		{"2026-10-12", "2026-10-18", 7},
		{"2026-10-02", "2026-10-01", 0}, // 结束早于开始
		{"bad", "2026-10-01", 0},
	}
	for _, tc := range cases {
		if got := daysBetweenInclusive(tc.a, tc.b); got != tc.want {
			t.Errorf("daysBetweenInclusive(%s, %s)=%d，期望 %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestNormalizePlanRange 日期区间校验。
func TestNormalizePlanRange(t *testing.T) {
	if _, _, err := normalizePlanRange("2026-10-01", "2026-09-30"); err == nil {
		t.Error("结束早于开始应被拒绝")
	}
	if _, _, err := normalizePlanRange("2026/10/01", "2026-10-30"); err == nil {
		t.Error("非 YYYY-MM-DD 格式应被拒绝")
	}
	if _, _, err := normalizePlanRange("2026-10-01", "2028-10-30"); err == nil {
		t.Error("超过一年的区间应被拒绝")
	}
	s, e, err := normalizePlanRange("2026-10-01", "2026-10-31")
	if err != nil || s != "2026-10-01" || e != "2026-10-31" {
		t.Errorf("正常区间被拒或改写: %s %s %v", s, e, err)
	}
}

// TestClampMinutes 每日最低时长的边界。
func TestClampMinutes(t *testing.T) {
	cases := map[int]int{0: 20, -5: 20, 30: 30, 600: 600, 9999: 600}
	for in, want := range cases {
		if got := clampMinutes(in); got != want {
			t.Errorf("clampMinutes(%d)=%d，期望 %d", in, got, want)
		}
	}
}

// TestAwardSkipsWhenBelowThreshold 达标天数不够时不生成，且**不算错误**。
//
// 这里 h.ai 为 nil —— 如果实现没在调 AI 之前就返回，测试会直接 panic，
// 顺带守住"未达标时不该发起 AI 调用"这件事。
func TestAwardSkipsWhenBelowThreshold(t *testing.T) {
	db := newPlanTestDB(t)
	parent, _ := newPlanTree(t, db)
	kid := seedPlanKid(t, db)
	h := &PlanHandler{db: db}

	plan := model.StudyPlan{
		UserID: kid.ID, CategoryID: parent.ID, CategoryName: parent.Name,
		StartDate: "2026-10-12", EndDate: "2026-10-18", MinMinutes: 10,
		Status: model.StudyPlanStatusActive,
	}
	mustCreate(t, db, &plan)

	// 只有 3 天达标，不够 5 天
	for _, d := range []string{"2026-10-12", "2026-10-13", "2026-10-14"} {
		mustCreate(t, db, &model.StudyCheckIn{
			UserID: kid.ID, PlanID: plan.ID, StudyDate: d, Minutes: 30, Completed: true,
		})
	}

	award, generated, err := h.GenerateWeeklyAward(kid.ID, "2026-10-12", "2026-10-18")
	if err != nil {
		t.Fatalf("未达标不应返回错误，实际: %v", err)
	}
	if generated || award != nil {
		t.Errorf("只有 3 天达标却生成了奖状: %+v", award)
	}
}

// TestWeekStatCountsDistinctDays 多个计划同日打卡只算一天。
func TestWeekStatCountsDistinctDays(t *testing.T) {
	db := newPlanTestDB(t)
	parent, _ := newPlanTree(t, db)
	kid := seedPlanKid(t, db)
	h := &PlanHandler{db: db}

	// 同一天、两个计划各打一次卡 —— 打卡天数仍应是 1
	for i := 0; i < 2; i++ {
		mustCreate(t, db, &model.StudyCheckIn{
			UserID: kid.ID, PlanID: uint64(i + 1),
			StudyDate: "2026-10-14", Minutes: 30, Completed: true,
		})
	}
	seedPlayRecord(t, db, kid.ID, parent.ID, parent.Name, "2026-10-14", 25*60)

	stat := h.weekStatOf(kid.ID, "2026-10-12", "2026-10-18")
	if stat.DaysChecked != 1 {
		t.Errorf("打卡天数=%d，期望 1（同一天多计划只算一天）", stat.DaysChecked)
	}
	if stat.TotalMinutes != 25 {
		t.Errorf("总分钟数=%d，期望 25", stat.TotalMinutes)
	}
	if len(stat.Categories) != 1 || stat.Categories[0] != parent.Name {
		t.Errorf("学习主题=%v，期望 [%s]", stat.Categories, parent.Name)
	}
	if len(stat.Titles) != 1 {
		t.Errorf("内容标题=%v，期望 1 条（同标题去重）", stat.Titles)
	}
}
