package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMappingRoutesDoNotConflict 验证资源映射的几条路由能共存。
//
// 存在的意义：gin 的路由树在同一层级上混用「静态段」和「参数段」时
// 曾经会直接 panic（`/mappings/sync` 与 `/mappings/:id/sync` 就是这种形态）。
// 这类问题只有在真正注册路由时才暴露，编译期完全看不出来 ——
// 而这个 panic 发生在服务启动时，一旦漏掉就是整个后端起不来。
// 所以这里用最精简的方式把同样的形态注册一遍，作为回归护栏。
func TestMappingRoutesDoNotConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := func(c *gin.Context) { c.Status(http.StatusOK) }

	r.POST("/api/admin/mappings", h)
	r.POST("/api/admin/mappings/sync", h)
	r.POST("/api/admin/mappings/:id/sync", h)
	r.PUT("/api/admin/mappings/:id", h)
	// 同一个方法上「集合」与「集合/:id」并存（DELETE 就是这种形态，
	// 用来支持"解绑全部"）。这条形态要一起守 —— 它是本次新加的，
	// 一旦 gin 哪天不允许这么写，症状是**服务启动即 panic**。
	r.DELETE("/api/admin/mappings", h)
	r.DELETE("/api/admin/mappings/:id", h)
	r.GET("/api/admin/mappings", h)

	// 顺便确认静态段确实优先于参数段被匹配到 ——
	// 否则 /mappings/sync 会被当成 id="sync" 走进同步单个映射的分支。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/mappings/sync", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/mappings/sync 期望 200，实际 %d", w.Code)
	}
}

// TestDeleteAllRouteNotSwallowedByIDRoute 确认 DELETE 上的集合与参数两条路由
// 各自命中自己的处理器。
//
// 为什么值得单独一条：这两条路径长得几乎一样，出问题的方向也对称 ——
// 集合被参数吞掉会变成"解绑全部时 id 解析失败 → 400"，
// 参数被集合吞掉会变成"解绑一条时把整张表清了"。后者是数据事故。
// 用不同的状态码把两个处理器区分开，谁吞了谁一眼看得出。
func TestDeleteAllRouteNotSwallowedByIDRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// 集合：解绑全部
	r.DELETE("/api/admin/mappings", func(c *gin.Context) { c.Status(http.StatusOK) })
	// 参数：解绑一条。故意用 201 做标记，与集合区分
	r.DELETE("/api/admin/mappings/:id", func(c *gin.Context) {
		if c.Param("id") == "" {
			t.Error("参数路由收到了空 id —— 说明它匹配到了集合路径")
		}
		c.Status(http.StatusCreated)
	})

	// 没带 id 的请求必须走"解绑全部"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/admin/mappings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /api/admin/mappings 期望 200（集合/解绑全部），实际 %d", w.Code)
	}

	// 带头 id 的请求必须走"解绑一条"，而不是被集合路由吞掉
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/admin/mappings/12", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("DELETE /api/admin/mappings/12 期望 201（单条），实际 %d —— 大概率被集合路由吞了", w.Code)
	}
}

// TestStreamRouteIsPublic 提醒 /api/stream/:id 不能挂在鉴权中间件下。
//
// <video src> 带不了 Authorization 头，所以这条路由一旦被
// AuthMiddleware 包住，WebDAV 播放必然 401。这里不断言中间件，
// 只确保路由本身注册成功、不 panic。
func TestStreamRouteIsPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/stream/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/stream/12", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/stream/:id 期望 200，实际 %d", w.Code)
	}
}
