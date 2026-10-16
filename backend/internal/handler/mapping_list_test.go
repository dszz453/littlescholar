package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
)

// 映射列表的过滤。
//
// ⚠️ 这**不是**"顺手加个查询参数"。「目录管理」页要能就地放一个「立即同步」按钮，
// 前提是它能回答"这个系统目录挂了哪条映射" —— 没有 categoryId 过滤就只能
// 把用户推去「资源映射」页自己找那一条（那正是用户提的优化点 1）。
//
// 同时钉住两条边界：
//  1. accountId 过滤（老行为）不能被新参数弄坏；
//  2. 两个参数同时给时是**与**关系，不是"后一个覆盖前一个"。
func TestMappingListFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	mustCreate(t, db, &model.ResourceMapping{Name: "A-账号1-目录17", AccountID: 1, CategoryID: 17})
	mustCreate(t, db, &model.ResourceMapping{Name: "B-账号2-目录17", AccountID: 2, CategoryID: 17})
	mustCreate(t, db, &model.ResourceMapping{Name: "C-账号1-目录18", AccountID: 1, CategoryID: 18})

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "不带过滤：全部", query: "", want: 3},
		{name: "按账号", query: "accountId=1", want: 2},
		{name: "按目录", query: "categoryId=17", want: 2},
		{name: "两个一起给是与关系", query: "accountId=1&categoryId=17", want: 1},
		{name: "没有数据的组合返回空", query: "accountId=2&categoryId=18", want: 0},
		// 参数是空串时不该被当成"过滤成空"—— 前端很容易传出 `?categoryId=`，
		// 那种情况下要么回全部、要么回空，但**绝不能**在 SQL 里拼成 `= ''`（类型也不对）。
		{name: "空串参数等于不过滤", query: "categoryId=&accountId=", want: 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("GET", "/api/admin/mappings?"+c.query, nil)
			h.List(ctx)

			if w.Code != 200 {
				t.Fatalf("HTTP %d, body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Code int                     `json:"code"`
				Data []model.ResourceMapping `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("响应不是合法 JSON: %v, body=%s", err, w.Body.String())
			}
			if len(body.Data) != c.want {
				t.Errorf("query=%q 返回 %d 条，期望 %d 条", c.query, len(body.Data), c.want)
			}
			// 过滤生效时逐条核对，避免"数量刚好对但内容错"（比如两个条件被 OR 了）
			for _, m := range body.Data {
				if c.query == "categoryId=17" && m.CategoryID != 17 {
					t.Errorf("categoryId=17 却返回了 category_id=%d 的映射", m.CategoryID)
				}
				if c.query == "accountId=1" && m.AccountID != 1 {
					t.Errorf("accountId=1 却返回了 account_id=%d 的映射", m.AccountID)
				}
			}
		})
	}
}
