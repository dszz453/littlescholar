package handler

import (
	"strconv"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// 网盘直链缓存。
//
// # 为什么必须有它
//
// 播放器取一个视频不是"一个请求"，而是**一串**：
//
//	HEAD 探大小 → GET Range: bytes=0- → 播到某处 GET Range: bytes=N- …
//	拖动进度又是两三个 Range 请求
//
// 而每次请求都会走到 `GetDirectLink` —— 那是**一次真实的网盘 API 调用**
// （夸克/UC 大概 200~800ms，风控时更久）。于是：
//
//	· 点开视频要先等一次 API 才拿到第一个字节（"打开视频比较慢"）
//	· 拖动进度、切码率都要再等一次（表现为"一拖就卡"）
//	· 同一集被多个请求重复解析，还容易触发网盘限流
//
// 直链本身有 30 分钟有效期，完全可以在进程内复用十几分钟。
//
// # 为什么不能无限复用
//
// 直链绑的是**取链时的那份 cookie 快照**。夸克的 `__puus` 约 3 小时过期，
// 过期后列目录/取直链仍然 200，只有 CDN 真取字节才回 403/412 ——
// 所以缓存条目除了按时间过期，还要能"被上游拒绝时立刻作废并重取"。
type linkCacheEntry struct {
	link    *adapter.DirectLink
	expires time.Time
}

var directLinks = struct {
	mu sync.Mutex
	m  map[string]linkCacheEntry
}{m: make(map[string]linkCacheEntry)}

// linkTTL 直链在进程内的最长复用时间。
//
// 10 分钟是个折中：比它短，一集动画片（20 分钟）播到后半段就要重新取链；
// 比它长，账号 cookie 被刷新后旧链的失效概率上升。
const linkTTL = 10 * time.Minute

// linkCacheKey 缓存键。
//
// 必须带账号：同一个 fileID 在不同账号下是两份不同的文件
// （比如两个孩子各有一份），串用了会播错人的片子。
func linkCacheKey(accountID uint64, fileID string) string {
	return strconv.FormatUint(accountID, 10) + "|" + fileID
}

func getCachedLink(key string) (*adapter.DirectLink, bool) {
	directLinks.mu.Lock()
	defer directLinks.mu.Unlock()
	e, ok := directLinks.m[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(e.expires) {
		delete(directLinks.m, key)
		return nil, false
	}
	return e.link, true
}

func putCachedLink(key string, link *adapter.DirectLink) {
	if link == nil || link.URL == "" {
		return
	}
	ttl := linkTTL
	// 直链自己带过期时间时以它为准，并且提前 1 分钟弃用：
	// 掐着秒用会撞上"刚过期的链"，表现是个别分片 403。
	if link.ExpiresAt > 0 {
		if d := time.Until(time.Unix(link.ExpiresAt, 0)) - time.Minute; d < ttl {
			ttl = d
		}
	}
	if ttl <= 0 {
		return
	}
	directLinks.mu.Lock()
	directLinks.m[key] = linkCacheEntry{link: link, expires: time.Now().Add(ttl)}
	directLinks.mu.Unlock()
}

func dropCachedLink(key string) {
	directLinks.mu.Lock()
	delete(directLinks.m, key)
	directLinks.mu.Unlock()
}

// ---- 容器格式探测的"失败记忆" ----

// 有些源根本探不出格式（上游不回 Content-Type，或 HEAD 被拒）。
// 没有记忆的话，**每次点开都要先去探一次**（一次真实的 Range 请求，
// 1~3 秒），表现就是"打开视频比较慢"，而且这个延迟永远存在。
//
// 记 30 分钟足够：格式不会在半小时内变，而源恢复后最多等半小时。
const probeMissTTL = 30 * time.Minute

var probeMiss = struct {
	mu sync.Mutex
	m  map[uint64]time.Time
}{m: make(map[uint64]time.Time)}

// probeMissRecently 该资源最近探测失败过。
func probeMissRecently(resourceID uint64) bool {
	probeMiss.mu.Lock()
	defer probeMiss.mu.Unlock()
	at, ok := probeMiss.m[resourceID]
	if !ok {
		return false
	}
	if time.Since(at) > probeMissTTL {
		delete(probeMiss.m, resourceID)
		return false
	}
	return true
}

// markProbeMiss 记一次探测失败。
//
// 顺手做容量兜底：这张表只在进程内活着，但资源多时不能无限长。
func markProbeMiss(resourceID uint64) {
	probeMiss.mu.Lock()
	defer probeMiss.mu.Unlock()
	if len(probeMiss.m) > 5000 {
		cut := time.Now().Add(-probeMissTTL)
		for k, v := range probeMiss.m {
			if v.Before(cut) {
				delete(probeMiss.m, k)
			}
		}
	}
	probeMiss.m[resourceID] = time.Now()
}

// clearProbeMiss 探测成功后清掉记忆（下次若再次为空会重新探）。
func clearProbeMiss(resourceID uint64) {
	probeMiss.mu.Lock()
	delete(probeMiss.m, resourceID)
	probeMiss.mu.Unlock()
}
