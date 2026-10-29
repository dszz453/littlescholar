package handler

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 音频转码的判定：只看扩展名，不看 media_type。
//
// 这条判据的由来（2026-10-20 线上）：用户报「大猫目录下 wma 格式的音频无法播放」。
// 根因是早年导入的资源 media_type 一律写成 video（那是后补列，存量行没回填），
// 而这里原来写的是 `res.MediaType != audio → return false`：
// 于是一个 wma 被判成"不需要转换"，直链直接进了 <video>，
// Android 的 WebView 解不了 wma —— 孩子看到的就是"点开没反应/放不出来"。
//
// 扩展名才是格式的权威来源：AudioNeedsTranscodeExt 里全是音频专用格式，
// 不存在"把视频误判成音频"的风险。
func TestNeedsConvertUsesExtNotMediaType(t *testing.T) {
	h := &AudioFormatHandler{}
	cases := []struct {
		name string
		res  model.Resource
		want bool
	}{
		{
			"存量行 media_type 还是 video 的 wma",
			model.Resource{MediaType: model.MediaTypeVideo, Ext: "wma"},
			true,
		},
		{
			"media_type 空的 wma",
			model.Resource{Ext: "wma"},
			true,
		},
		{
			"只有标题带后缀（Ext 空）",
			model.Resource{MediaType: model.MediaTypeVideo, Title: "The Red Coat.wma"},
			true,
		},
		{
			"本来就是音频的 wma",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "wma"},
			true,
		},
		{
			"ape / amr 同理",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "ape"},
			true,
		},
		{
			"mp3 浏览器能放，不转",
			model.Resource{MediaType: model.MediaTypeAudio, Ext: "mp3"},
			false,
		},
		{
			"电子书走自己的转换链，别抢",
			model.Resource{MediaType: model.MediaTypeEbook, Ext: "mobi"},
			false,
		},
		{
			"图片走自己的转换链",
			model.Resource{MediaType: model.MediaTypeImage, Ext: "wma"},
			false,
		},
		{
			"格式确实未知：判不了就别乱转",
			model.Resource{MediaType: model.MediaTypeVideo},
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := c.res
			if got := h.NeedsConvert(&res); got != c.want {
				t.Fatalf("NeedsConvert(%+v) = %v, want %v", c.res, got, c.want)
			}
		})
	}
}

func TestNeedsConvertNilSafe(t *testing.T) {
	var h *AudioFormatHandler
	if h.NeedsConvert(&model.Resource{Ext: "wma"}) {
		t.Fatal("nil handler 不该判需要转换")
	}
	if (&AudioFormatHandler{}).NeedsConvert(nil) {
		t.Fatal("nil resource 不该判需要转换")
	}
}

// ---------------------------------------------------------------------------
// 后台预热
//
// 起因（2026-10-29 用户报）：标准版打开「柯林斯大猫 Big Cat」里的音频，
// 页面挂着"正在准备音频"，第一次要等十几秒。根因是 /api/audio/:id
// 把「解析直链 → 从网盘拉源文件 → ffmpeg 转码」全做在**同一次请求**里，
// 缓存命中之后才是毫秒级 —— 也就是说"第一次"永远慢，而第一次恰恰是
// 唯一的体验。预热就是把这十几秒挪到没人看的时候。
// ---------------------------------------------------------------------------

// TestWarmOncePicksOnlyConvertible 预热只该碰"真的需要转、且还没转"的资源。
//
// 这条断言的价值不在功能本身，而在**选错候选的代价**：预热是无人盯的后台
// 任务，选错了不会报错，只会悄悄把网盘 API 打满、把磁盘吃满、
// 或者对着已失效的资源一遍遍重试。
func TestWarmOncePicksOnlyConvertible(t *testing.T) {
	db := newAudioTestDB(t)
	dir := t.TempDir()

	// 1) 存量行：media_type 还是 video，只能靠 ext=wma 认出来；没有转存信息 → 注定转失败
	wmaVideo := model.Resource{CategoryID: 1, Title: "Big Cat - The Red Coat",
		MediaType: model.MediaTypeVideo, Ext: "wma", DiskType: "quark"}
	// 2) mp3 浏览器能放，不该进候选
	mp3 := model.Resource{CategoryID: 1, Title: "song", MediaType: model.MediaTypeAudio, Ext: "mp3"}
	// 3) 已判失效的资源不碰（否则每轮都在打它的网盘接口）
	dead := model.Resource{CategoryID: 1, Title: "gone", MediaType: model.MediaTypeAudio,
		Ext: "wma", Status: model.ResourceStatusDead}
	// 4) ext 空、只有标题带后缀 → SQL 靠 media_type=audio 兜进来，再由 NeedsConvert 认
	titleOnly := model.Resource{CategoryID: 1, Title: "Pink Level.wma", MediaType: model.MediaTypeAudio}
	// 5) 产物已经在盘上 → 只算缓存，绝不重转
	cached := model.Resource{CategoryID: 1, Title: "cached", MediaType: model.MediaTypeAudio, Ext: "wma"}

	for _, r := range []*model.Resource{&wmaVideo, &mp3, &dead, &titleOnly, &cached} {
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("造资源失败: %v", err)
		}
	}
	// fileExists 要求 size > 0，空文件不算命中缓存
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.m4a", cached.ID)),
		[]byte("fake-aac"), 0o644); err != nil {
		t.Fatalf("造产物失败: %v", err)
	}

	h := NewAudioFormatHandler(db, nil, nil, "ffmpeg", dir, "secret")
	stat := h.WarmOnce(context.Background())

	if stat.Cached != 1 {
		t.Fatalf("已有产物应算 1 条缓存，实际 %d（%s）", stat.Cached, stat)
	}
	// 该转的只有 wmaVideo 与 titleOnly 两条；mp3 不缺、dead 被 SQL 滤掉
	if stat.Todo != 2 {
		t.Fatalf("候选应为 2 条（wma 存量行 + 只有标题带后缀），实际 %d（%s）", stat.Todo, stat)
	}
	// 这两条都没有转存信息 → 取不到直链，算"跳过"而不是"失败"：
	// 它们本来就走 external 兜底，预热对它们没有意义，混进 Failed 只会淹掉真故障。
	if stat.Converted != 0 {
		t.Fatalf("没有转存信息不该算转好，实际 Converted=%d", stat.Converted)
	}
	if stat.Skipped != 2 {
		t.Fatalf("两条都该记为跳过，实际 Skipped=%d（%s）", stat.Skipped, stat)
	}
	if stat.Failed != 0 {
		t.Fatalf("跳过不该记成失败，实际 Failed=%d（%s）", stat.Failed, stat)
	}
}

// TestWarmOnceStopsRetryingBroken 坏资源重试两次就放弃。
//
// 为什么需要：预热每 15 分钟一轮，不去重的话，一条早就失效的 wma 会
// 永远排在最前面（id 最小），每轮都先花时间去解析直链 —— 网盘那边的
// 请求量就是这么堆起来的，而它永远不会成功。
func TestWarmOnceStopsRetryingBroken(t *testing.T) {
	db := newAudioTestDB(t)
	dir := t.TempDir()

	// ⚠️ 必须带上 DiskAccountID，否则会走"没有转存信息 → 跳过"那条路，
	// 这条测试就变成永远绿的空断言。它同时钉住的是 2026-10-29 那个真实故障：
	// 预热查询里多写了一个 `Select(...)`，`disk_account_id` 没被查出来 →
	// 每一条都被判成"缺少转存信息"，整轮 93 条全"失败"、产物一条没多。
	accID := uint64(1) // 库里并没有这个账号 → 解析直链时必然报"账号不存在"
	broken := model.Resource{CategoryID: 1, Title: "Big Cat - Broken",
		MediaType: model.MediaTypeAudio, Ext: "wma", DiskType: "quark",
		DiskAccountID: &accID, FileID: "fid-broken"}
	if err := db.Create(&broken).Error; err != nil {
		t.Fatalf("造资源失败: %v", err)
	}

	h := NewAudioFormatHandler(db, nil, nil, "ffmpeg", dir, "secret")
	for i := 1; i <= 2; i++ {
		if got := h.WarmOnce(context.Background()); got.Failed != 1 {
			t.Fatalf("第 %d 轮应尝试并失败 1 次，实际 %s", i, got)
		}
	}
	// 第三轮起不再碰它：仍然统计"待转"，但不再发请求
	third := h.WarmOnce(context.Background())
	if third.Failed != 0 {
		t.Fatalf("失败两次后不该再重试，实际 Failed=%d（%s）", third.Failed, third)
	}
	if third.Todo != 1 {
		t.Fatalf("仍应统计为待转，实际 Todo=%d（%s）", third.Todo, third)
	}
}

func newAudioTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:audiofmt_%s?mode=memory&cache=shared", url.QueryEscape(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Category{}, &model.Resource{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}
