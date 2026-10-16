package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/version"
)

// 后台「系统状态」里那一行版本号必须是**真的**。
//
// 背景：这里曾经写死 `"version": "1.0.0"`（system.go 的 gin.H 字面量），
// 从项目第一个提交（ec9c10f）起一次也没动过 —— 项目改了十几轮，
// 后台显示的版本号从上线第一天就是假的，直到 2026-10-16 用户看着它问
// "后台版本对不对"才被发现。
//
// 断言刻意是"**等于 version.Version**"而不是"不等于空"：
// 后者在有人把它改回任意字面量时照样通过（只要那个字面量非空），
// 而这条测试存在的唯一目的就是拦住"重新写死"。
func TestSystemStatusReportsRealVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := newMappingTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.TransferTask{}, &model.SystemConfig{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// pansou / pancheck 指向一个必然连不上的端口（127.0.0.1:1）。
	// 刻意不 mock 网络：这条测试要验的是"版本从哪里来"，
	// 两个 online 字段是 false 还是 true 无关紧要（不参与断言）。
	h := &SystemHandler{
		db:       db,
		pansou:   service.NewPansouClient("http://127.0.0.1:1", ""),
		pancheck: service.NewPancheckClient("http://127.0.0.1:1", ""),
		pool:     task.NewWorkerPool(1, 10),
		start:    time.Now(),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/admin/system/status", nil)
	h.Status(c)

	if w.Code != 200 {
		t.Fatalf("HTTP %d, body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, w.Body.String())
	}

	got, _ := body.Data["version"].(string)
	if got != version.Version {
		t.Fatalf("版本 = %q, want %q —— 必须来自 internal/version，不能在 handler 里写字面量",
			got, version.Version)
	}

	// commit / buildTime 两个键**必须存在**（值可以为空：本地 go run 没有注入）。
	// 缺键与"空值"在前端都是 undefined，但缺键说明有人漏配了 gin.H ——
	// 那种情况下后台会永远显示"没有构建信息"，而真相是"字段根本没发出来"。
	for _, k := range []string{"commit", "buildTime"} {
		if _, ok := body.Data[k]; !ok {
			t.Fatalf("响应里缺少 %q 字段（前端 buildLabel 会恒为空）", k)
		}
	}
}
