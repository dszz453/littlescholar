package subtitle

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var dbSeq int32

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	// ⚠️ 每个测试一个**独立**的内存库名。
	// 之前所有测试共用 `file::memory:?cache=shared`，于是前一个用例建的行
	// 会漏进后一个用例 —— 表现是"软删的资源被捡回来了"这种假故障，
	// 追半天代码才发现是夹具串了。
	dsn := "file:subtest" + strconv.Itoa(int(atomic.AddInt32(&dbSeq, 1))) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Resource{}, &model.ResourceSubtitle{}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return db
}

// ============================================================
//  同目录候选的排序
// ============================================================

// 一个动画目录里通常同时躺着 chs/eng/ass 三份字幕。
// 排序错了就会给孩子配英文字幕 —— 他看不懂，等于没有字幕。
func TestRankSiblingsPrefersChinese(t *testing.T) {
	entries := []SiblingEntry{
		{ID: "e", Name: "第01集.eng.srt"},
		{ID: "a", Name: "第01集.mp4"},
		{ID: "c", Name: "第01集.chs.srt"},
		{ID: "d", Name: "第01集.ass"},
		{ID: "f", Name: "第02集.chs.srt"}, // 别的集，不该出现
		{ID: "g", Name: "readme.txt"},
	}
	got := RankSiblings("第01集.mp4", entries)

	if len(got) == 0 {
		t.Fatal("一个候选都没挑出来")
	}
	if got[0].Name != "第01集.ass" && got[0].Name != "第01集.chs.srt" {
		t.Fatalf("头名应该是无后缀或中文的，实际是 %q", got[0].Name)
	}
	// 视频自己不能被当成字幕
	for _, c := range got {
		if c.Name == "第01集.mp4" {
			t.Fatal("把视频文件本身当成了候选")
		}
		if strings.HasSuffix(c.Name, ".txt") {
			t.Fatal("把非字幕文件当成了候选")
		}
	}
	// 第02集不能混进来（第01 不是 第02 的前缀，MatchScore 会给 0）
	for _, c := range got {
		if strings.Contains(c.Name, "第02集") {
			t.Fatalf("别的集的字幕混进来了：%q", c.Name)
		}
	}
	// 英文必须排在中文后面
	var idxZh, idxEn = -1, -1
	for i, c := range got {
		if strings.Contains(c.Name, "chs") && idxZh < 0 {
			idxZh = i
		}
		if strings.Contains(c.Name, "eng") && idxEn < 0 {
			idxEn = i
		}
	}
	if idxZh >= 0 && idxEn >= 0 && idxZh > idxEn {
		t.Fatalf("英文字幕排在中文字幕前面了：%v", got)
	}
}

// 集数前缀陷阱：第1集 是 第10集 的前缀，反过来认领会配错集。
func TestRankSiblingsPrefixTrap(t *testing.T) {
	entries := []SiblingEntry{
		{ID: "x", Name: "EP1.mp4"},
		{ID: "s10", Name: "EP10.srt"},
	}
	got := RankSiblings("EP1.mp4", entries)
	for _, c := range got {
		if c.Name == "EP10.srt" {
			t.Fatal("EP1 认领了 EP10 的字幕 —— 前缀匹配方向写反了")
		}
	}
}

// ============================================================
//  永久存储
// ============================================================

func TestStoreSaveAndGet(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	if row, err := s.Get(42); err != nil || row != nil {
		t.Fatalf("没存过就该返回 (nil,nil)，实际 %v / %v", row, err)
	}
	if err := s.Save(&model.ResourceSubtitle{
		ResourceID: 42, Source: "同目录字幕 · a.srt", Origin: "sibling",
		Confidence: 100, VTT: "1\n00:00:00.000 --> 00:00:01.000\n你好\n",
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	row, err := s.Get(42)
	if err != nil || row == nil {
		t.Fatalf("读不回来：%v / %v", row, err)
	}
	if got := row.VTT; !strings.HasPrefix(got, "WEBVTT") {
		t.Fatalf("缺 WEBVTT 头，浏览器会整条不认：%q", got[:minInt(len(got), 40)])
	}
	if row.Cues == 0 {
		// Cues 是调用方填的，这里只确认它没被结构体默认值吃掉
		t.Log("Cues 未设置（由采集器填），跳过")
	}
}

// 人工指定的那条不许被后台任务覆盖。
//
// 采集器每 6 小时扫一遍，如果每次都把人工校对过的顶掉，
// 用户就永远固定不了字幕 —— 而且他还会觉得"改了没用"。
func TestStoreDoesNotOverwriteManual(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	if err := s.Save(&model.ResourceSubtitle{
		ResourceID: 7, Source: "手工校对", Origin: "manual", Manual: true,
		Confidence: 100, VTT: "WEBVTT\n\n1\n00:00:00.000 --> 00:00:02.000\n人工版\n",
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	// 后台任务又匹配到一条，想覆盖
	if err := s.Save(&model.ResourceSubtitle{
		ResourceID: 7, Source: "同目录字幕 · auto.srt", Origin: "sibling",
		Confidence: 90, VTT: "WEBVTT\n\n1\n00:00:00.000 --> 00:00:02.000\n自动版\n",
	}); err != nil {
		t.Fatalf("覆盖调用本身不该报错（让路是正常情况）：%v", err)
	}
	row, _ := s.Get(7)
	if row == nil || !strings.Contains(row.VTT, "人工版") {
		t.Fatalf("人工字幕被自动匹配覆盖了：%+v", row)
	}
	if !row.Manual {
		t.Fatal("manual 标记丢了 —— 下一轮采集还会试着覆盖")
	}
}

func TestStoreMissingSkipsOnesWithSubtitle(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	dir := "d1"
	rows := []model.Resource{
		{Title: "有字幕的", MediaType: model.MediaTypeVideo, FileID: "f1", OwnerDirID: dir},
		{Title: "没字幕的", MediaType: model.MediaTypeVideo, FileID: "f2", OwnerDirID: dir},
		{Title: "电子书不算", MediaType: model.MediaTypeEbook, FileID: "f3", OwnerDirID: dir},
		{Title: "没目录的", MediaType: model.MediaTypeVideo, FileID: "f4"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("建资源失败：%v", err)
		}
	}
	if err := s.Save(&model.ResourceSubtitle{
		ResourceID: rows[0].ID, Origin: "sibling", VTT: "WEBVTT\n\n1\n00:00:00.000 --> 00:00:01.000\nx\n",
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	miss, err := s.Missing(50)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	var titles []string
	for _, m := range miss {
		titles = append(titles, m.Title)
	}
	if len(miss) != 1 || miss[0].Title != "没字幕的" {
		t.Fatalf("待配列表不对，实际 %v", titles)
	}
}

// 软删的资源不该再被采集器捡起来。
func TestStoreMissingSkipsDeleted(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)

	dir := "d1"
	r := model.Resource{Title: "已删", MediaType: model.MediaTypeVideo, FileID: "f1", OwnerDirID: dir}
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("建资源失败：%v", err)
	}
	if err := db.Delete(&r).Error; err != nil {
		t.Fatalf("软删失败：%v", err)
	}
	miss, err := s.Missing(50)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if len(miss) != 0 {
		t.Fatalf("软删的资源被捡回来了：%v", len(miss))
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
