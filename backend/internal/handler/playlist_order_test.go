package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
)

// 播放列表的排序，以及「当前资源待在哪儿」。
//
// 用户要的是：列表按标题升序，顺序播放就顺着这个顺序一集接一集。
// 旧实现把当前资源硬排在第一位，于是"下一集"永远是列表里的第二项 ——
// 顺序播放从第二集起就开始绕圈。所以这里同时锁两件事：
//  1. 按标题「数字自然序」升序（1-2 必须排在 1-10 前面）；
//  2. 当前资源留在自己的排序位置上，不占第一位。
func TestPlaylistOrderAndCurrentPosition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)
	h := &PlayHandler{db: db}

	ids := map[string]uint64{}
	for _, title := range []string{
		"牛津树 1-10", "牛津树 1-2", "牛津树 1-1", "牛津树 1-9", "牛津树 1-3",
	} {
		r := &model.Resource{Title: title, CategoryID: 17}
		mustCreate(t, db, r)
		ids[title] = r.ID
	}
	// 别的目录的资源不该混进这个列表
	mustCreate(t, db, &model.Resource{Title: "牛津树 1-4", CategoryID: 18})

	// 从中间那一集进入：当前资源是不是被硬塞到第一位，一眼看得出来
	got := callPlaylist(t, h, ids["牛津树 1-3"])

	want := []string{"牛津树 1-1", "牛津树 1-2", "牛津树 1-3", "牛津树 1-9", "牛津树 1-10"}
	if len(got) != len(want) {
		t.Fatalf("列表长度 %d，期望 %d（其它目录的资源不能混进来）：%v",
			len(got), len(want), playlistTitles(got))
	}
	for i := range want {
		if got[i].Title != want[i] {
			t.Errorf("第 %d 项是 %q，期望 %q（完整结果 %v）",
				i+1, got[i].Title, want[i], playlistTitles(got))
		}
	}
	if got[0].ID == ids["牛津树 1-3"] {
		t.Errorf("当前资源不该被排到第一位 —— 那会让「下一集」永远指向列表第二项")
	}
	if idx := playlistIndexOf(got, ids["牛津树 1-3"]); idx != 2 {
		t.Errorf("当前资源应待在自己的排序位置（第 3 项），实际在第 %d 项", idx+1)
	}
}

// 列表里只有自己时不能报错，也不能凭空多出内容 —— 单集目录很常见。
func TestPlaylistSingleItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)
	h := &PlayHandler{db: db}

	solo := &model.Resource{Title: "独苗", CategoryID: 21}
	mustCreate(t, db, solo)

	got := callPlaylist(t, h, solo.ID)
	if len(got) != 1 || got[0].ID != solo.ID {
		t.Fatalf("单集目录应只返回自己，实际 %v", playlistTitles(got))
	}
}

// 列表被 limit 截断时，当前资源仍必须在列表里。
//
// 否则前端标不出"正在播放"，顺序播放也找不到起点 —— 表现就是
// "点开大目录里的最后一集，播放列表里没有它"。
func TestPlaylistKeepsCurrentWhenTruncated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)
	h := &PlayHandler{db: db}

	// 先塞满 limit 条，且标题都排在目标之前（A… < Z…）
	batch := make([]model.Resource, 0, 50)
	for i := 0; i < playlistLimit; i++ {
		batch = append(batch, model.Resource{Title: fmt.Sprintf("A%04d", i), CategoryID: 23})
		if len(batch) == 50 {
			if err := db.Create(&batch).Error; err != nil {
				t.Fatalf("批量插入失败: %v", err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := db.Create(&batch).Error; err != nil {
			t.Fatalf("批量插入失败: %v", err)
		}
	}

	target := &model.Resource{Title: "Z 最后一集", CategoryID: 23}
	mustCreate(t, db, target)

	got := callPlaylist(t, h, target.ID)
	if len(got) != playlistLimit+1 {
		t.Fatalf("列表长度 %d，期望 %d（截断后也要把当前资源补进来）",
			len(got), playlistLimit+1)
	}
	if got[len(got)-1].ID != target.ID {
		t.Errorf("补进来的当前资源应排在末尾，实际 %v", playlistTitles(got))
	}
}

/* ---------- 测试辅助 ---------- */

type playlistItem struct {
	ID      uint64 `json:"id"`
	Title   string `json:"title"`
	PlayURL string `json:"playUrl"`
}

func callPlaylist(t *testing.T, h *PlayHandler, id uint64) []playlistItem {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
	c.Request = httptest.NewRequest("GET", fmt.Sprintf("/api/app/play/%d/playlist", id), nil)
	h.Playlist(c)

	if w.Code != 200 {
		t.Fatalf("HTTP %d，body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int            `json:"code"`
		Data []playlistItem `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v，body=%s", err, w.Body.String())
	}
	return resp.Data
}

func playlistTitles(items []playlistItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func playlistIndexOf(items []playlistItem, id uint64) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}
