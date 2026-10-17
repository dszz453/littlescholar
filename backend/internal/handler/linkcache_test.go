package handler

import (
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// TestLinkCacheKeyIncludesAccount 缓存键必须带账号。
//
// 同一个 fileID 在不同账号下是**两份不同的文件**（两个孩子各有一份），
// 键里丢了账号就会串着播别人的片子 —— 而现象只是"偶尔放错一集"。
func TestLinkCacheKeyIncludesAccount(t *testing.T) {
	dropCachedLink(linkCacheKey(1, "f1"))
	dropCachedLink(linkCacheKey(2, "f1"))
	putCachedLink(linkCacheKey(1, "f1"), &adapter.DirectLink{URL: "https://cdn/a"})

	if _, ok := getCachedLink(linkCacheKey(1, "f1")); !ok {
		t.Fatal("同账号应命中")
	}
	if _, ok := getCachedLink(linkCacheKey(2, "f1")); ok {
		t.Error("不同账号不该共用一条直链")
	}
}

// TestLinkCacheHonoursLinkExpiry 直链自己快到期时，缓存不能比它活得久。
//
// 缓存一条 30 秒后失效的直链 10 分钟，表现是"刚打开能播、过一会儿 403"。
func TestLinkCacheHonoursLinkExpiry(t *testing.T) {
	key := linkCacheKey(7, "short")
	dropCachedLink(key)
	exp := time.Now().Add(90 * time.Second)
	putCachedLink(key, &adapter.DirectLink{URL: "https://cdn/short", ExpiresAt: exp.Unix()})

	e, ok := directLinks.m[key]
	if !ok {
		t.Fatal("应写入缓存")
	}
	if e.expires.After(exp) {
		t.Errorf("缓存有效期 %s 超过了直链自身的过期时间 %s",
			e.expires.Format(time.RFC3339), exp.Format(time.RFC3339))
	}

	// 只剩不到 1 分钟就过期的直链**根本不缓存** —— 存进去也就能用一个
	// 几十秒，随即变成 403，收益是负的。
	key2 := linkCacheKey(7, "shorter")
	dropCachedLink(key2)
	putCachedLink(key2, &adapter.DirectLink{
		URL: "https://cdn/shorter", ExpiresAt: time.Now().Add(20 * time.Second).Unix(),
	})
	if _, ok := getCachedLink(key2); ok {
		t.Error("即将过期的直链不该进缓存")
	}
}

// TestLinkCacheDrop 丢掉之后必须真的不再命中（上游 403 后走这条）。
func TestLinkCacheDrop(t *testing.T) {
	key := linkCacheKey(3, "f3")
	putCachedLink(key, &adapter.DirectLink{URL: "https://cdn/x"})
	dropCachedLink(key)
	if _, ok := getCachedLink(key); ok {
		t.Error("drop 之后不该再命中")
	}
}

// TestProbeMissMemory 探不出格式的资源，别每次点开都再探一遍。
//
// 每次探查都是一次真实的网络往返（1~3 秒），没有记忆的话
// "打开视频慢"这个延迟会永久存在。
func TestProbeMissMemory(t *testing.T) {
	if probeMissRecently(4242) {
		t.Fatal("没探过就不该算失败")
	}
	markProbeMiss(4242)
	if !probeMissRecently(4242) {
		t.Error("探失败之后应记住")
	}
	clearProbeMiss(4242)
	if probeMissRecently(4242) {
		t.Error("清掉之后应重新允许探测")
	}
}
