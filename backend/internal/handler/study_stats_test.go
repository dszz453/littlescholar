package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 学习统计的取值口径。
//
// 这条链路曾经整体失效：孩子端「我的」页今日/本周恒为 0、学习记录恒为空。
// 现象像"没落库"，实际是三个问题叠在一起：
//
//	1) 前端读的字段（todayDuration / weekDuration / finishedCount）后端从不返回；
//	2) 孩子端不传 userId → 后端 userID=0：总时长走"全站合计"，而按目录/按天/
//	   按小时的查询硬编码 user_id=0 又必然为空。同一个响应里两套口径自相矛盾；
//	3) 孩子端根本没有 /app/study/records 路由，明细无从获取。
//
// 这里把 (2) 的正确口径钉死：**没传 userId 就认当前登录用户，绝不退化成 0**。
func TestStudyOverviewScopesToCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newStudyTestDB(t)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local)
	yesterday := today.AddDate(0, 0, -1)

	const kidID = uint64(2)
	const adminID = uint64(1)

	// 孩子：今天 3×60 秒、昨天 120 秒
	seedStudyRecord(t, db, kidID, "牛津树 L1", "英语", today, 60)
	seedStudyRecord(t, db, kidID, "牛津树 L1", "英语", today.Add(time.Minute), 60)
	seedStudyRecord(t, db, kidID, "牛津树 L2", "英语", today.Add(2*time.Minute), 60)
	seedStudyRecord(t, db, kidID, "数学思维", "数学", yesterday, 120)

	// 别人的数据绝不能混进来 —— 这正是"不传 userId"时最容易失守的地方
	seedStudyRecord(t, db, adminID, "他人的记录", "其它", today, 600)

	mustCreate(t, db, &model.PlayRecord{UserID: kidID, ResourceID: 11, Finished: true, Duration: 300})
	mustCreate(t, db, &model.PlayRecord{UserID: kidID, ResourceID: 12, Finished: false, Percentage: 40, Duration: 300})
	mustCreate(t, db, &model.PlayRecord{UserID: adminID, ResourceID: 99, Finished: true, Duration: 300})

	h := NewStudyHandler(db, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/app/study/overview", nil)
	// 孩子端就是这么调的：**不传** userId，身份只体现在登录态里
	c.Set("userID", kidID)
	h.Overview(c)

	data := decodeStudyData(t, w)

	if got := numField(t, data, "todaySeconds"); got != 180 {
		t.Errorf("todaySeconds = %d，期望 180", got)
	}
	// (180 + 120) / 60
	if got := numField(t, data, "totalMinutes"); got != 5 {
		t.Errorf("totalMinutes = %d，期望 5", got)
	}
	if got := numField(t, data, "totalDays"); got != 2 {
		t.Errorf("totalDays = %d，期望 2", got)
	}
	if got := numField(t, data, "finishedResources"); got != 1 {
		t.Errorf("finishedResources = %d，期望 1（只算孩子自己的）", got)
	}
	if got := numField(t, data, "inProgressResources"); got != 1 {
		t.Errorf("inProgressResources = %d，期望 1", got)
	}

	// 本周至少包含今天
	if got := numField(t, data, "weekSeconds"); got < 180 {
		t.Errorf("weekSeconds = %d，应 >= todaySeconds(180)", got)
	}

	// 分布查询必须和总数同口径 —— 否则就会出现"总数有值、图表全空"
	cats := listField(t, data, "byCategory")
	if len(cats) != 2 {
		t.Fatalf("byCategory 有 %d 组，期望 2 组（英语/数学），实际 %v", len(cats), cats)
	}
	for _, item := range cats {
		m, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("byCategory 元素类型异常: %T", item)
		}
		if m["categoryName"] == "其它" {
			t.Errorf("byCategory 混进了他人的记录: %v", m)
		}
	}

	// 分布值必须真的带出来，但要允许"分组合计 < 总数"。
	//
	// 三条分布查询在 SQL 里做的是 `SUM(duration)/60` —— 每个分组各自向下取整，
	// 余数（最多 59 秒/组）会被丢掉，所以 **分组合计天然可能比 totalMinutes 少
	// 最多 (组数-1) 分钟**。这是整数除法口径决定的，不是数据错误；线上实测
	// byCategory 3 组合计 7 分钟、totalMinutes 8 分钟就是这个原因。
	// 断言写成"只能少一个余数以内"，而不是严格相等 —— 严格相等只在种子数据
	// 恰好都是 60 的整数倍时才成立，换个种子就假红。
	//
	// 更要紧的是它们曾经**整列为零**：`Model(...).Select("... AS minutes").Scan(&[]T)`
	// 会按模型 schema 核对列，聚合别名不在字段里就被静默丢弃（PostgreSQL 上必现、
	// SQLite 上侥幸能过）。所以这里不能只看合计，必须确认真的有值。
	days := listField(t, data, "byDay")
	if len(days) != 2 {
		t.Fatalf("byDay 有 %d 组，期望 2 天，实际 %v", len(days), days)
	}
	daySum := 0
	for _, item := range days {
		m := item.(map[string]interface{})
		daySum += int(m["minutes"].(float64))
	}
	if daySum <= 0 {
		t.Errorf("byDay 全为零，分布统计没带出数据: %v", days)
	}
	if daySum > numField(t, data, "totalMinutes") {
		t.Errorf("byDay 合计 %d 分钟 > totalMinutes %d（不该出现）",
			daySum, numField(t, data, "totalMinutes"))
	}

	hours := listField(t, data, "byHour")
	if len(hours) == 0 {
		t.Fatal("byHour 为空，时段分布没带出数据")
	}
	hourSum := 0
	for _, item := range hours {
		m := item.(map[string]interface{})
		hourSum += int(m["minutes"].(float64))
	}
	if hourSum <= 0 {
		t.Errorf("byHour 全为零，时段分布没带出数据: %v", hours)
	}
	if hourSum > numField(t, data, "totalMinutes") {
		t.Errorf("byHour 合计 %d 分钟 > totalMinutes %d（不该出现）",
			hourSum, numField(t, data, "totalMinutes"))
	}

	// byCategory 同理：合计只能因取整余数而偏小，且不得为零
	sum := 0
	for _, item := range cats {
		m := item.(map[string]interface{})
		sum += int(m["minutes"].(float64))
	}
	if sum <= 0 {
		t.Errorf("byCategory 全为零，分布统计没带出数据: %v", cats)
	}
	if sum > numField(t, data, "totalMinutes") {
		t.Errorf("byCategory 合计 %d 分钟 > totalMinutes %d（不该出现）",
			sum, numField(t, data, "totalMinutes"))
	}
	if numField(t, data, "totalMinutes")-sum > len(cats)-1 {
		t.Errorf("byCategory 取整损失 %d 分钟超过组数上限 %d",
			numField(t, data, "totalMinutes")-sum, len(cats)-1)
	}
}

// groupBy=resource：一次播放每 15 秒落一条心跳，孩子端「学习记录」必须按资源
// 聚合，否则同一个资源会把列表刷屏，家长看到的是"学了很多次"，而不是"学了什么"。
func TestStudyRecordsGroupedByResource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newStudyTestDB(t)

	now := time.Now()
	base := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, time.Local)
	const kidID = uint64(2)

	// 同一个资源 4 次心跳，每次 15 秒
	for i := 0; i < 4; i++ {
		seedStudyRecord(t, db, kidID, "牛津树 L1", "英语", base.Add(time.Duration(i)*15*time.Second), 15)
	}
	// 另一个资源 2 次
	seedStudyRecord(t, db, kidID, "数学思维", "数学", base.Add(time.Hour), 20)
	seedStudyRecord(t, db, kidID, "数学思维", "数学", base.Add(time.Hour+time.Minute), 20)

	h := NewStudyHandler(db, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/app/study/records?groupBy=resource&page=1&pageSize=20", nil)
	c.Set("userID", kidID)
	h.Records(c)

	data := decodeStudyData(t, w)

	if got := numField(t, data, "total"); got != 2 {
		t.Errorf("total = %d，期望 2 个资源（而不是 6 条记录）", got)
	}

	list := listField(t, data, "list")
	if len(list) != 2 {
		t.Fatalf("list 有 %d 行，期望 2 行", len(list))
	}

	// 最近学的排在最前：数学思维最后一次 10:01 > 牛津树 09:00
	first := list[0].(map[string]interface{})
	if first["resourceTitle"] != "数学思维" {
		t.Errorf("首行 = %v，期望最近学习的「数学思维」", first["resourceTitle"])
	}

	byTitle := map[string]map[string]interface{}{}
	for _, it := range list {
		m := it.(map[string]interface{})
		byTitle[m["resourceTitle"].(string)] = m
	}

	if got := int(byTitle["牛津树 L1"]["duration"].(float64)); got != 60 {
		t.Errorf("牛津树累计时长 = %d 秒，期望 60（4×15）", got)
	}
	if got := int(byTitle["牛津树 L1"]["times"].(float64)); got != 4 {
		t.Errorf("牛津树 times = %d，期望 4", got)
	}
	if got := int(byTitle["数学思维"]["duration"].(float64)); got != 40 {
		t.Errorf("数学思维累计时长 = %d 秒，期望 40", got)
	}
}

// ---- helpers ----

func newStudyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:study_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.StudyRecord{}, &model.PlayRecord{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

func seedStudyRecord(t *testing.T, db *gorm.DB, userID uint64, title, catName string, at time.Time, seconds int) {
	t.Helper()
	rec := model.StudyRecord{
		UserID: userID,
		// 资源 ID 由标题派生：同一个标题的多次心跳必须落在同一个资源上，
		// 否则"按资源聚合"的测试会因为造假数据而失真。
		ResourceID:    resourceIDOf(title),
		CategoryID:    7,
		CategoryName:  catName,
		ResourceTitle: title,
		Action:        "play_progress",
		Duration:      seconds,
		HappenedAt:    at,
		StudyDate:     at.Format("2006-01-02"),
		StudyHour:     at.Hour(),
	}
	mustCreate(t, db, &rec)
}

// resourceIDOf 由标题稳定派生一个资源 ID（FNV-1a）
func resourceIDOf(title string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(title); i++ {
		h ^= uint64(title[i])
		h *= 1099511628211
	}
	return h % 1000000
}

func decodeStudyData(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	if code := respCode(t, w); code != 0 {
		t.Fatalf("接口返回失败: code=%d body=%s", code, w.Body.String())
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, w.Body.String())
	}
	if resp.Data == nil {
		t.Fatalf("响应 data 为空: %s", w.Body.String())
	}
	return resp.Data
}

func numField(t *testing.T, data map[string]interface{}, key string) int {
	t.Helper()
	v, ok := data[key]
	if !ok {
		t.Fatalf("响应缺少字段 %q（实际字段：%v）", key, dataKeys(data))
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("字段 %q 不是数字，实际 %T", key, v)
	}
	return int(f)
}

func listField(t *testing.T, data map[string]interface{}, key string) []interface{} {
	t.Helper()
	v, ok := data[key]
	if !ok {
		t.Fatalf("响应缺少字段 %q（实际字段：%v）", key, dataKeys(data))
	}
	list, ok := v.([]interface{})
	if !ok {
		t.Fatalf("字段 %q 不是数组，实际 %T", key, v)
	}
	return list
}

func dataKeys(data map[string]interface{}) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys
}
