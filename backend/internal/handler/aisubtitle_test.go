package handler

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/subtitle"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var aiSubSeq int32

// newAISubFixture 建一个独立内存库 + 服务实例。
//
// 每个用例一个库名（不是共用 file::memory:）：共用会让上一个用例的行漏进来，
// 表现成"明明没排队却说已排队"这种假故障。这个坑字幕那边踩过。
func newAISubFixture(t *testing.T) (*gorm.DB, *AISubtitleService) {
	t.Helper()
	dsn := "file:aisub" + strconv.Itoa(int(atomic.AddInt32(&aiSubSeq, 1))) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Category{}, &model.Resource{}, &model.ResourceSubtitle{},
		&model.AISubtitleTask{}, &model.AISubtitlePlan{}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	enc, err := util.NewEncryptor("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("加密器初始化失败：%v", err)
	}
	aiH := NewAIHandler(db, enc)
	svc := NewAISubtitleHandler(db, subtitle.NewStore(db), aiH, "", t.TempDir(), t.TempDir())
	return db, svc
}

// ============================================================
//  计划时段
// ============================================================

// 跨零点是这个功能最主要的用法（晚上 10 点到早上 6 点），
// 写成"必须同一天内"等于让主要用法用不了。
func TestInWindow(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 10, 20, h, m, 0, 0, time.Local)
	}
	cases := []struct {
		name       string
		h, m       int
		start, end int
		want       bool
	}{
		{"晚上时段-23点", 23, 0, 22 * 60, 6 * 60, true},
		{"晚上时段-凌晨2点", 2, 0, 22 * 60, 6 * 60, true},
		{"晚上时段-起点", 22, 0, 22 * 60, 6 * 60, true},
		{"晚上时段-终点不含", 6, 0, 22 * 60, 6 * 60, false},
		{"晚上时段-白天", 12, 0, 22 * 60, 6 * 60, false},
		{"晚上时段-差一分钟", 21, 59, 22 * 60, 6 * 60, false},
		{"白天时段-在其中", 12, 0, 9 * 60, 17 * 60, true},
		{"白天时段-结束不含", 17, 0, 9 * 60, 17 * 60, false},
		{"起止相同等于没有时段", 12, 0, 9 * 60, 9 * 60, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InWindow(at(c.h, c.m), c.start, c.end); got != c.want {
				t.Fatalf("%02d:%02d 在 [%s, %s) 里 = %v，想要 %v",
					c.h, c.m, MinuteText(c.start), MinuteText(c.end), got, c.want)
			}
		})
	}
}

// 计划保存时要把越界的值收回来：batch=0 会让"每轮"什么也不做，
// gap=9999 会让一批卡两个小时。
func TestSavePlanClamps(t *testing.T) {
	_, svc := newAISubFixture(t)
	if err := svc.SavePlan(&model.AISubtitlePlan{
		Enabled:       true,
		StartMinute:   22 * 60,
		EndMinute:     6 * 60,
		BatchPerRound: 0,
		GapSeconds:    9999,
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	plan, err := svc.LoadPlan()
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if plan.BatchPerRound != model.AISubtitleDefaultBatch {
		t.Fatalf("batch = %d，想要默认 %d", plan.BatchPerRound, model.AISubtitleDefaultBatch)
	}
	if plan.GapSeconds != model.AISubtitleDefaultGapSeconds {
		t.Fatalf("gap = %d，想要默认 %d", plan.GapSeconds, model.AISubtitleDefaultGapSeconds)
	}
	if !plan.Enabled || plan.StartMinute != 22*60 || plan.EndMinute != 6*60 {
		t.Fatalf("其余字段不该被改：%+v", plan)
	}
}

// ============================================================
//  目录名（匹配要素）
// ============================================================

func TestParsePathIDs(t *testing.T) {
	got := parsePathIDs("/1/12/35/")
	want := []uint64{1, 12, 35}
	if len(got) != len(want) {
		t.Fatalf("得到 %v，想要 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("得到 %v，想要 %v", got, want)
		}
	}
	if n := len(parsePathIDs("")); n != 0 {
		t.Fatalf("空路径应为空，得到 %d 项", n)
	}
}

// 目录链要拼全：叶子往往叫"三级"，光看它看不出是哪套书。
func TestCategoryPath(t *testing.T) {
	db, svc := newAISubFixture(t)
	root := &model.Category{Name: "英语", Path: "/1/"}
	db.Create(root)
	mid := &model.Category{Name: "分级阅读", Path: "/1/2/"}
	db.Create(mid)
	leaf := &model.Category{Name: "大猫英语三级", Path: "/1/2/3/"}
	db.Create(leaf)

	got := svc.categoryPath(leaf.ID)
	want := "英语 / 分级阅读 / 大猫英语三级"
	if got != want {
		t.Fatalf("目录路径 = %q，想要 %q", got, want)
	}
	if got := svc.categoryPath(0); got != "" {
		t.Fatalf("没有目录时应为空，得到 %q", got)
	}
}

// 远端目录：WebDAV 是路径（有用），网盘是 ID（对模型只有干扰）。
func TestRemoteDirHint(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"550e8400-e29b-41d4-a716-446655440000", ""},       // 纯 ID，没有路径分隔符
		{"/media/local/大猫英语/三级", "大猫英语 / 三级"},              // 取最后两层
		{"/vol1/1000/xuexi", "1000 / xuexi"},               // 普通路径
		{"f0b2c3d4e5a6b7c8d9e0f1a2b3c4d5e6", ""},           // 十六进制 ID
		{"/大猫英语/0f0e0d0c0b0a0908070605040302010f", "大猫英语"}, // ID 段被丢掉
	}
	for _, c := range cases {
		if got := remoteDirHint(c.in); got != c.want {
			t.Fatalf("remoteDirHint(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

// ============================================================
//  队列
// ============================================================

func makeCategory(t *testing.T, db *gorm.DB) *model.Category {
	t.Helper()
	cat := &model.Category{Name: "英语", Path: "/1/"}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	return cat
}

// 重复点"加入队列"不该产生第二条任务；非音视频不该入队。
func TestEnqueueDedupAndFilter(t *testing.T) {
	db, svc := newAISubFixture(t)
	cat := makeCategory(t, db)
	audio := &model.Resource{CategoryID: cat.ID, Title: "The Helper Bird", MediaType: model.MediaTypeAudio, Ext: "m4a"}
	db.Create(audio)
	pdf := &model.Resource{CategoryID: cat.ID, Title: "某本 PDF", MediaType: model.MediaTypePDF, Ext: "pdf"}
	db.Create(pdf)

	first, err := svc.Enqueue([]uint64{audio.ID, pdf.ID})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	if first.Added != 1 || first.Skipped != 1 {
		t.Fatalf("第一次入队 = %+v，想要 added=1 skipped=1", first)
	}
	second, err := svc.Enqueue([]uint64{audio.ID})
	if err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	if second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("重复入队 = %+v，想要 added=0 skipped=1", second)
	}
	var n int64
	db.Model(&model.AISubtitleTask{}).Count(&n)
	if n != 1 {
		t.Fatalf("任务数 = %d，想要 1", n)
	}
}

// 已经完成的重排会重置成 pending（用户又点了一次就该再跑）。
func TestEnqueueResetsFinishedTask(t *testing.T) {
	db, svc := newAISubFixture(t)
	cat := makeCategory(t, db)
	r := &model.Resource{CategoryID: cat.ID, Title: "Book", MediaType: model.MediaTypeAudio, Ext: "mp3"}
	db.Create(r)
	if _, err := svc.Enqueue([]uint64{r.ID}); err != nil {
		t.Fatalf("入队失败：%v", err)
	}
	db.Model(&model.AISubtitleTask{}).Where("resource_id = ?", r.ID).
		Updates(map[string]interface{}{"status": model.AISubtitleStatusDone})

	res, err := svc.Enqueue([]uint64{r.ID})
	if err != nil {
		t.Fatalf("重排失败：%v", err)
	}
	if res.Reset != 1 {
		t.Fatalf("重排结果 = %+v，想要 reset=1", res)
	}
	var task model.AISubtitleTask
	db.Where("resource_id = ?", r.ID).First(&task)
	if task.Status != model.AISubtitleStatusPending {
		t.Fatalf("状态 = %q，想要 pending", task.Status)
	}
}

// 已有"同目录/内嵌/联网"字幕时默认跳过 —— 那些比模型复原的可信，
// 覆盖它们是降级。这条如果坏了，重跑一次就会把好字幕冲掉。
func TestRunTaskSkipsWhenBetterSubtitleExists(t *testing.T) {
	db, svc := newAISubFixture(t)
	cat := makeCategory(t, db)
	r := &model.Resource{CategoryID: cat.ID, Title: "Book", MediaType: model.MediaTypeAudio, Ext: "mp3"}
	db.Create(r)
	if err := svc.store.Save(&model.ResourceSubtitle{
		ResourceID: r.ID,
		Source:     "同目录字幕 · Book.srt",
		Origin:     "sibling",
		Confidence: 90,
		VTT:        "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n",
	}); err != nil {
		t.Fatalf("准备字幕失败：%v", err)
	}
	task := &model.AISubtitleTask{
		ResourceID: r.ID, Title: r.Title, Status: model.AISubtitleStatusPending,
	}
	db.Create(task)

	plan, err := svc.LoadPlan()
	if err != nil {
		t.Fatalf("读计划失败：%v", err)
	}
	note, status := svc.runTask(context.Background(), task, plan)
	if status != model.AISubtitleStatusSkipped {
		t.Fatalf("状态 = %q（%s），想要 skipped", status, note)
	}
	if !strings.Contains(note, "同目录") {
		t.Fatalf("说明里应指出已有字幕来源：%q", note)
	}

	// 打开覆盖开关后会继续往前走（这里没有 AI 配置，应该失败在"没有 AI 配置"，
	// 而不是又被字幕挡住 —— 用来区分"跳过的原因"）。
	plan.Overwrite = true
	_, status = svc.runTask(context.Background(), task, plan)
	if status != model.AISubtitleStatusFailed {
		t.Fatalf("开了覆盖还停在 %q，说明跳过逻辑把原因搞混了", status)
	}
}
