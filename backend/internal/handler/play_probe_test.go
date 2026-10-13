package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// probeExtFromLink 是"第一次播放就能判对格式"的关键一环。
//
// 它要解决的是：存量网盘资源 Ext 为空（标题被剥后缀、FileID 是短 hash），
// 于是 rmvb 被当成"浏览器能播"代理出去，用户看到"播放地址可能已过期"。
// 这里在上游侧把格式问出来，让 NeedsTranscode 在第一次请求时就是对的。
func TestProbeExtFromLink(t *testing.T) {
	var gotRange, gotUA, gotCookie string

	realmedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotUA = r.Header.Get("User-Agent")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/vnd.rn-realmedia-vbr")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(".RMF"))
	}))
	defer realmedia.Close()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer failing.Close()

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!DOCTYPE html>"))
	}))
	defer html.Close()

	// 1) 正常识别 + 请求头必须带上
	ext := probeExtFromLink(context.Background(), &adapter.DirectLink{
		URL:     realmedia.URL + "/kids/ep13",
		Headers: adapter.MediaHeaders("Quark/1.0", "https://pan.quark.cn/", "ck=1"),
	})
	if ext != "rmvb" {
		t.Fatalf("期望 rmvb，得到 %q", ext)
	}
	if gotRange != "bytes=0-63" {
		t.Errorf("必须只取头部 64 字节，实际 Range=%q", gotRange)
	}
	if gotUA == "" {
		t.Error("必须带上适配器给的 User-Agent（CDN 会校验）")
	}
	if gotCookie == "" {
		t.Error("必须带上 Cookie —— 夸克 CDN 缺它直接 412")
	}

	// 2) 上游 4xx（例如直链失效）→ 不猜格式
	if ext := probeExtFromLink(context.Background(), &adapter.DirectLink{URL: failing.URL}); ext != "" {
		t.Errorf("上游 412 时不应猜出格式，得到 %q", ext)
	}

	// 3) 上游返回 HTML 错误页 → 不能把 text/html 当成格式
	if ext := probeExtFromLink(context.Background(), &adapter.DirectLink{URL: html.URL}); ext != "" {
		t.Errorf("HTML 错误页不应产生扩展名，得到 %q", ext)
	}

	// 4) 本地硬盘是 file:// ，不需要（也不应该）发请求
	if ext := probeExtFromLink(context.Background(), &adapter.DirectLink{URL: "file:///media/local/a.mp4"}); ext != "" {
		t.Errorf("file:// 不应探测，得到 %q", ext)
	}

	// 5) 空值防御
	if ext := probeExtFromLink(context.Background(), nil); ext != "" {
		t.Errorf("nil link 应返回空，得到 %q", ext)
	}
	if ext := probeExtFromLink(context.Background(), &adapter.DirectLink{}); ext != "" {
		t.Errorf("空 URL 应返回空，得到 %q", ext)
	}
}

var testDBCounter int

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// DSN 带自增后缀：sqlite 的 `cache=shared` 内存库在**进程内**是共享的，
	// 写死名字会让多个测试互相看到对方的表。
	testDBCounter++
	dsn := fmt.Sprintf("file:handler_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Resource{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// learnContainerType 是"老资源自愈"的写入口，两条护栏必须守住：
// 认不出格式就不写；已经有格式信息就不覆盖。
func TestLearnContainerTypeGuards(t *testing.T) {
	db := newTestDB(t)
	h := &StreamHandler{db: db}

	blank := &model.Resource{Title: "神奇之钥第2季13集", MediaType: model.MediaTypeVideo}
	if err := db.Create(blank).Error; err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	// WebDAV 的 FileID 就是带后缀的路径，比 CDN 的 MIME 权威
	hasExt := &model.Resource{Title: "看磁带", Ext: "avi", FileID: "/kids/a.avi", MediaType: model.MediaTypeVideo}
	if err := db.Create(hasExt).Error; err != nil {
		t.Fatalf("插入失败: %v", err)
	}

	// 1) 空的应该被补上
	h.learnContainerType(blank, "application/vnd.rn-realmedia-vbr")
	var afterBlank model.Resource
	if err := db.First(&afterBlank, blank.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if afterBlank.Ext != "rmvb" {
		t.Errorf("空 Ext 应被补成 rmvb，实际 %q", afterBlank.Ext)
	}

	// 2) 已有值不能被覆盖
	//
	// 回读必须用**新的**变量：gorm 的 First(&dst, pk) 会把 dst 里非零的主键
	// 也拼进 WHERE，复用上一次的 after 会得到 "id = 2 AND id = 1" 而查不到。
	h.learnContainerType(hasExt, "application/vnd.rn-realmedia-vbr")
	var afterHasExt model.Resource
	if err := db.First(&afterHasExt, hasExt.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if afterHasExt.Ext != "avi" {
		t.Errorf("已有 Ext 不应被覆盖，实际 %q", afterHasExt.Ext)
	}

	// 3) 认不出的 Content-Type 什么都不写（比如 HTML 错误页）
	other := &model.Resource{Title: "无格式", MediaType: model.MediaTypeVideo}
	if err := db.Create(other).Error; err != nil {
		t.Fatalf("插入失败: %v", err)
	}
	h.learnContainerType(other, "text/html; charset=utf-8")
	var afterOther model.Resource
	if err := db.First(&afterOther, other.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if afterOther.Ext != "" {
		t.Errorf("认不出时不应写入，实际 %q", afterOther.Ext)
	}
}
