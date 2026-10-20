package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/subtitle"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 采集器的端到端用例。
//
// 用 **本地硬盘适配器**（DiskLocal）跑真实链路：真落文件、真 ListDir、
// 真 GetDirectLink、真转码成 VTT。比 mock 一个适配器更能证明"这条路是通的"，
// 而且磁盘上的东西是可控的 —— 能造出"同目录有两份字幕、AI 又不确定"这种
// 线上很难凑出来的局面。
type collectFixture struct {
	db    *gorm.DB
	enc   *util.Encryptor
	store *subtitle.Store
	root  string
	accID uint64
}

func newCollectFixture(t *testing.T, files map[string]string) *collectFixture {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("建目录失败：%v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("写文件失败：%v", err)
		}
	}

	db, err := gorm.Open(sqlite.Open("file:collect"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Resource{}, &model.ResourceSubtitle{},
		&model.DiskAccount{}); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	enc, err := util.NewEncryptor("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("构造加密器失败：%v", err)
	}
	// 本地硬盘适配器不用 cookie，但字段不能为空 —— 解密空串会报错，
	// 所以老老实实加密一个空串进去（这也是生产上接本地硬盘时的真实形态）。
	blob, err := enc.Encrypt("")
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	acc := model.DiskAccount{
		DiskType:        string(adapter.DiskLocal),
		CookieEncrypted: blob,
		Extra:           map[string]interface{}{"rootPath": root},
	}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatalf("建账号失败：%v", err)
	}
	return &collectFixture{db: db, enc: enc, store: subtitle.NewStore(db), root: root, accID: acc.ID}
}

// 一条视频资源，fileID/ownerDirID 都是相对 root 的斜杠路径。
func (f *collectFixture) addResource(t *testing.T, title, ext, fileID, dirID string) model.Resource {
	t.Helper()
	acc := f.accID
	dir := dirID
	r := model.Resource{
		Title:         title,
		Ext:           ext,
		MediaType:     model.MediaTypeVideo,
		DiskType:      string(adapter.DiskLocal),
		DiskAccountID: &acc,
		FileID:        fileID,
		OwnerDirID:    dir,
	}
	if err := f.db.Create(&r).Error; err != nil {
		t.Fatalf("建资源失败：%v", err)
	}
	return r
}

const srtBody = "1\n00:00:01,000 --> 00:00:03,000\n小猪佩奇你好\n\n2\n00:00:04,000 --> 00:00:06,000\n我们一起玩吧\n"

// 主链路：同目录有同名字幕 → 转成 VTT → 写进永久存储。
func TestCollectorStoresSiblingSubtitle(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4": "not-a-real-video",
		"第01集.srt": srtBody,
		"第02集.srt": srtBody, // 别的集，不该被认领
	})
	r := f.addResource(t, "第01集", "mp4", "/第01集.mp4", "/")

	c := NewSubtitleCollector(f.db, f.enc, f.store, nil)
	stat := c.RunOnce(context.Background())

	if stat.Stored != 1 {
		t.Fatalf("应该入库 1 条，实际 %+v", stat)
	}
	row, err := f.store.Get(r.ID)
	if err != nil || row == nil {
		t.Fatalf("没读到字幕：%v / %v", row, err)
	}
	if !strings.HasPrefix(row.VTT, "WEBVTT") {
		t.Fatalf("没转成 WebVTT：%q", row.VTT)
	}
	// ⚠️ 断言真实中文出现在正文里：只断言"有内容"的话，
	// 编码转换整个坏掉（GBK 当 UTF-8 解）也能通过。
	if !strings.Contains(row.VTT, "小猪佩奇") {
		t.Fatalf("正文里没有正确的中文，可能是编码转换坏了：%q", row.VTT)
	}
	if row.Cues != 2 {
		t.Fatalf("条数算错了：%d，期望 2", row.Cues)
	}
	if row.Origin != "sibling" {
		t.Fatalf("来源标记不对：%q", row.Origin)
	}
}

// 置信度不够就不入库 —— 错字幕比没字幕更伤。
//
// 这条要靠 AI 捏一个低分出来：同目录的两份字幕相似度都是 90（chs/cht），
// 本地判断分不开，于是交给 AI；AI 说不确定（30 分），采集器就必须放弃。
func TestCollectorRejectsLowConfidence(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4":     "not-a-real-video",
		"第01集.chs.srt": srtBody,
		"第01集.cht.srt": srtBody,
	})
	r := f.addResource(t, "第01集", "mp4", "/第01集.mp4", "/")

	chat := func(ctx context.Context, system, user string) (string, error) {
		return `{"index":0,"confidence":30,"reason":"看不出是哪个版本"}`, nil
	}
	c := NewSubtitleCollector(f.db, f.enc, f.store, func() subtitle.ChatFunc { return chat })
	stat := c.RunOnce(context.Background())

	if stat.Stored != 0 {
		t.Fatalf("低置信度不该入库，实际 %+v", stat)
	}
	if row, _ := f.store.Get(r.ID); row != nil {
		t.Fatalf("低置信度的字幕被存下来了：%+v", row.Source)
	}
}

// 反过来：AI 给了高分，就该存 —— 否则"门槛"这条断言可能只是因为整条链路都不工作。
func TestCollectorStoresHighConfidenceFromAI(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4":     "not-a-real-video",
		"第01集.chs.srt": srtBody,
		"第01集.cht.srt": srtBody,
	})
	r := f.addResource(t, "第01集", "mp4", "/第01集.mp4", "/")

	chat := func(ctx context.Context, system, user string) (string, error) {
		return `{"index":0,"confidence":92,"reason":"简体更合适"}`, nil
	}
	c := NewSubtitleCollector(f.db, f.enc, f.store, func() subtitle.ChatFunc { return chat })
	stat := c.RunOnce(context.Background())

	if stat.Stored != 1 {
		t.Fatalf("高置信度应该入库，实际 %+v", stat)
	}
	row, _ := f.store.Get(r.ID)
	if row == nil || row.Confidence != 92 {
		t.Fatalf("入库结果不对：%+v", row)
	}
}

// 目录里没有字幕文件：跳过，不该报错、也不该写空记录。
func TestCollectorSkipsWhenNoSubtitle(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4": "not-a-real-video",
		"说明.txt":   "hello",
	})
	r := f.addResource(t, "第01集", "mp4", "/第01集.mp4", "/")

	c := NewSubtitleCollector(f.db, f.enc, f.store, nil)
	stat := c.RunOnce(context.Background())
	if stat.Stored != 0 || stat.Failed != 0 {
		t.Fatalf("没有字幕就该安静跳过，实际 %+v", stat)
	}
	if row, _ := f.store.Get(r.ID); row != nil {
		t.Fatal("没有字幕却写了一条记录")
	}
}

// 目录里没有字幕文件 → 去字幕站搜一份并入库。
//
// 这是线上最普遍的处境：1308 条待配资源里**一个 .srt 都没有**。
// 没有这条兜底，"后台自动配字幕"就是一个永远入库 0 条的摆设。
func TestCollectorFallsBackToOnline(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4": "not-a-real-video",
	})
	r := f.addResource(t, "小猪佩奇 第01集", "mp4", "/第01集.mp4", "/")

	srv := newFakeSubServer(t, "1\n00:00:01,000 --> 00:00:03,000\n小猪佩奇你好\n")

	c := NewSubtitleCollector(f.db, f.enc, f.store, nil)
	online := subtitle.NewOnline("fake-key")
	online.Base = srv
	c.SetOnline(online)

	stat := c.RunOnce(context.Background())
	if stat.Stored != 1 {
		t.Fatalf("应当从网上配到一条，实际 %+v", stat)
	}
	row, err := f.store.Get(r.ID)
	if err != nil || row == nil {
		t.Fatalf("没有入库：%v", err)
	}
	if row.Origin != "online" {
		t.Fatalf("来源标错了：%q", row.Origin)
	}
	if !strings.Contains(row.VTT, "小猪佩奇你好") {
		t.Fatalf("字幕正文不对：%q", row.VTT)
	}
}

// 没配 key 时必须安静跳过：这是"没开这个功能"，不是"出错"。
func TestCollectorOnlineSilentWithoutKey(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4": "not-a-real-video",
	})
	f.addResource(t, "小猪佩奇 第01集", "mp4", "/第01集.mp4", "/")

	c := NewSubtitleCollector(f.db, f.enc, f.store, nil)
	c.SetOnline(subtitle.NewOnline(""))

	stat := c.RunOnce(context.Background())
	if stat.Failed != 0 || stat.Stored != 0 {
		t.Fatalf("没配 key 却报错/入库了：%+v", stat)
	}
}

// 一轮跑着的时候，查进度必须**立刻**返回。
//
// 2026-10-20 的原始实现把 lastStat 也放在 mu 下面：一轮要跑几分钟
// （每条列一遍网盘目录 + 喘 1.2 秒），期间 GET /api/admin/subtitle/stats
// 一直阻塞，管理页看到的是干转圈直到超时 —— 而人只会解读为"采集器挂了"。
// 这里直接占住"轮次锁"来复现那个场景。
func TestLastStatDoesNotBlockWhileRoundRunning(t *testing.T) {
	c := NewSubtitleCollector(nil, nil, nil, nil)

	c.mu.Lock() // 模拟一轮正在跑
	defer c.mu.Unlock()

	done := make(chan CollectStat, 1)
	go func() { done <- c.LastStat() }()

	select {
	case st := <-done:
		_ = st
	case <-time.After(500 * time.Millisecond):
		t.Fatal("一轮在跑的时候 LastStat 阻塞了 —— 管理页查进度会一直转到超时")
	}
}

// 进度要在轮次**结束前**就能看到，不能只在末尾写一次。
func TestCollectorExposesProgressDuringRound(t *testing.T) {
	f := newCollectFixture(t, map[string]string{
		"第01集.mp4": "not-a-real-video",
	})
	f.addResource(t, "第01集", "mp4", "/第01集.mp4", "/")

	c := NewSubtitleCollector(f.db, f.enc, f.store, nil)
	stat := c.RunOnce(context.Background())

	// 扫完的提示要带"分母"，否则看不出这一轮到底走完没有
	if !strings.Contains(stat.LastNote, "1/") {
		t.Fatalf("结论文案里没有进度分母：%q", stat.LastNote)
	}
	if got := c.LastStat(); got.LastNote != stat.LastNote {
		t.Fatalf("LastStat 与返回值不一致：%q vs %q", got.LastNote, stat.LastNote)
	}
}

// newFakeSubServer 一个最小的字幕站：搜得到一条、下载得到 srt 正文。
//
// 直接返回基址，配合 subtitle.Online.Base 用。真站点要 key 要配额，
// 不搭假的，这条兜底路径就只能靠上线手点 —— 而它恰好是线上唯一的出路。
func newFakeSubServer(t *testing.T, srt string) string {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/subtitles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"attributes":{"language":"zh-cn",`+
			`"download_count":1234,"files":[{"file_id":"1","file_name":"a.srt"}]}}]}`)
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"link":"`+srv.URL+`/f"}`)
	})
	mux.HandleFunc("/f", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, srt)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}
