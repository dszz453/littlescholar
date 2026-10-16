package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
)

// 一键解绑全部映射。
//
// 用户第 4 条：「所有映射全部删除要重新映射」。这个入口的危险性在于它一次
// 动的是整张表，所以下面每一件事都得钉死：
//
//  1. dryRun 必须**只读** —— 预览按钮点下去，库里一条都不能变；
//  2. dryRun 的计数必须与真删**完全一致**。预览存在的唯一理由就是让用户
//     在按确定之前知道要动多少东西，数字虚高或虚低等于没有预览；
//  3. withResources 的默认值与其他入口保持一致（默认一起移除，软删除）；
//  4. 手动导入的资源（没有账号归属）在任何情况下都不能被牵连。
func TestMappingDeleteAll(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name          string
		query         string
		wantMappings  int64 // 执行后期望剩下的映射条数
		wantResources int64 // 执行后期望剩下的**可见**资源条数
		wantRemoved   int64 // 响应里的 removedResources
		// 这次调用是否真的动过资源（决定能不能查"软删行还在不在"）：
		// "只解绑"根本不该删资源，"预览"更是只读 —— 拿它们去断言
		// "库里仍有 5 行"会因为正好没删而假通过，掩盖真实缺陷。
		wantRowsTouched bool
	}{
		{name: "默认连带移除资源", query: "", wantMappings: 0, wantResources: 1, wantRemoved: 4, wantRowsTouched: true},
		{name: "显式只解绑，资源保留", query: "?withResources=0", wantMappings: 0, wantResources: 5},
		{name: "预览不落库", query: "?dryRun=1", wantMappings: 2, wantResources: 5, wantRemoved: 4},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newMappingTestDB(t)
			h := &MappingHandler{db: db}

			m1 := model.ResourceMapping{Name: "牛津树-夸克", AccountID: 1, CategoryID: 17}
			m2 := model.ResourceMapping{Name: "RAZ-WebDAV", AccountID: 2, CategoryID: 23}
			mustCreate(t, db, &m1)
			mustCreate(t, db, &m2)

			// 各带 2 条精确归属的资源
			for _, m := range []model.ResourceMapping{m1, m2} {
				for i := 0; i < 2; i++ {
					mustCreate(t, db, &model.Resource{
						Title: fmt.Sprintf("%s-%02d", m.Name, i), CategoryID: m.CategoryID,
						DiskAccountID: u64ptr(m.AccountID), MappingID: m.ID,
					})
				}
			}
			// 1 条手动导入：没有账号归属，任何清理都不该碰它
			mustCreate(t, db, &model.Resource{Title: "手动导入", CategoryID: 17, DiskType: "local"})

			rec := callMappingDeleteAll(t, h, c.query)
			got := decodeDeleteAll(t, rec)

			if got.Mappings != 2 {
				t.Errorf("响应里映射数是 %d，期望 2（全部映射都该被算进来）", got.Mappings)
			}
			if got.RemovedResources != c.wantRemoved {
				t.Errorf("移除资源数 %d，期望 %d", got.RemovedResources, c.wantRemoved)
			}

			var mc int64
			db.Model(&model.ResourceMapping{}).Count(&mc)
			if mc != c.wantMappings {
				t.Errorf("剩余映射 %d 条，期望 %d 条", mc, c.wantMappings)
			}

			var rc int64
			db.Model(&model.Resource{}).Count(&rc)
			if rc != c.wantResources {
				t.Errorf("剩余可见资源 %d 条，期望 %d 条", rc, c.wantResources)
			}

			// 手动导入的那条任何情况下都得在
			var manual int64
			db.Model(&model.Resource{}).Where("title = ?", "手动导入").Count(&manual)
			if manual != 1 {
				t.Errorf("手动导入的资源被误伤了")
			}

			// 被移除的必须是**软删除**（能捞回来），不是物理删除
			if c.wantRowsTouched {
				var all int64
				db.Unscoped().Model(&model.Resource{}).Count(&all)
				if all != 5 {
					t.Errorf("库里总共应仍是 5 行（软删不丢行），实际 %d 行 —— 写成物理删除了？", all)
				}
			}
		})
	}
}

// 预览数必须等于真删数 —— 这是预览功能唯一的存在理由。
//
// 专门为"两条映射指向同一个账号 + 同一个系统目录"造场景：真删时，第一条
// 映射的 legacy 兜底会把那批无归属资源软删掉，第二条再查就是 0 条；而
// dryRun 不落库，如果不去重，同一批 legacy 会被数两遍 —— 用户看到
// "要移除 7 个"，实际只移除 5 个。差 2 个不多，但足够让人不信这个提示。
func TestMappingDeleteAllPreviewMatchesRealRun(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	// 同一个账号 + 同一个目录上挂了两条映射（左树是允许的，虽然少见）
	a := model.ResourceMapping{Name: "牛津树-A", AccountID: 1, CategoryID: 17}
	b := model.ResourceMapping{Name: "牛津树-B", AccountID: 1, CategoryID: 17}
	mustCreate(t, db, &a)
	mustCreate(t, db, &b)

	mustCreate(t, db, &model.Resource{Title: "A的资源", CategoryID: 17, DiskAccountID: u64ptr(1), MappingID: a.ID})
	mustCreate(t, db, &model.Resource{Title: "B的资源", CategoryID: 17, DiskAccountID: u64ptr(1), MappingID: b.ID})
	// 2 条历史资源：无归属，账号 + 目录与上面两条映射都对得上
	for i := 0; i < 2; i++ {
		mustCreate(t, db, &model.Resource{Title: fmt.Sprintf("历史%d", i), CategoryID: 17, DiskAccountID: u64ptr(1)})
	}
	// 置成 NULL（不是 0）—— 线上 AutoMigrate 补列就是这个结果
	if err := db.Exec("UPDATE resources SET mapping_id = NULL WHERE title LIKE '历史%'").Error; err != nil {
		t.Fatalf("置 NULL 失败: %v", err)
	}
	// 别的账号，不该被牵连
	mustCreate(t, db, &model.Resource{Title: "别的账号", CategoryID: 17, DiskAccountID: u64ptr(2)})

	preview := decodeDeleteAll(t, callMappingDeleteAll(t, h, "?dryRun=1"))
	if !preview.DryRun {
		t.Errorf("响应里 dryRun 标记丢了，前端没法区分预览和真删")
	}
	// 1 + 1 + 2（历史只算一次）+ 0（别的账号）= 4
	if preview.RemovedResources != 4 {
		t.Errorf("预览说会移除 %d 个资源，期望 4", preview.RemovedResources)
	}

	// 预览过后库里一点没变
	var afterPreview int64
	db.Model(&model.Resource{}).Count(&afterPreview)
	if afterPreview != 5 {
		t.Fatalf("dryRun 动了数据：可见资源剩 %d 条，期望 5 条", afterPreview)
	}

	real := decodeDeleteAll(t, callMappingDeleteAll(t, h, ""))
	if real.RemovedResources != preview.RemovedResources {
		t.Errorf("预览 %d 个、实删 %d 个 —— 预览虚高或虚低，等于骗用户",
			preview.RemovedResources, real.RemovedResources)
	}
	if real.RemovedScoped != 2 {
		t.Errorf("精确归属清了 %d 条，期望 2", real.RemovedScoped)
	}
	if real.RemovedLegacy != 2 {
		t.Errorf("历史资源清了 %d 条，期望 2（去重后只该数一次）", real.RemovedLegacy)
	}

	var alive int64
	db.Model(&model.Resource{}).Count(&alive)
	if alive != 1 {
		t.Errorf("剩余资源 %d 条，期望 1（只剩别的账号那条）", alive)
	}
}

// 空表不能报错，也不能因为"没东西可删"就返回一个含糊的成功。
func TestMappingDeleteAllEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	rec := callMappingDeleteAll(t, h, "")
	if code := respCode(t, rec); code != 0 {
		t.Fatalf("空表解绑全部应成功（code 0），实际 code=%d body=%s", code, rec.Body.String())
	}
	if got := decodeDeleteAll(t, rec); got.Mappings != 0 {
		t.Errorf("空表应报 0 条映射，实际 %d", got.Mappings)
	}
}

// 经**真实 gin 路由**再打一遍。
//
// 前面的用例都是直接调 h.DeleteAll(c)，绕过了路由分发 —— 而"解绑全部"
// 恰恰是这次新增的一条路由，最容易出问题的地方就在分发这一层：
// DELETE /api/admin/mappings 被 /mappings/:id 吞掉（症状是 id 解析失败 → 400），
// 或者反过来把单条解绑吞成"清空整张表"。这两种都只有经真实路由才看得见，
// 直接调处理函数永远测不出来。
func TestMappingDeleteAllThroughRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	m := model.ResourceMapping{Name: "牛津树", AccountID: 1, CategoryID: 17}
	mustCreate(t, db, &m)
	mustCreate(t, db, &model.Resource{
		Title: "第1集", CategoryID: 17, DiskAccountID: u64ptr(1), MappingID: m.ID,
	})

	// 照 router.go 里的注册方式挂一遍（两条路由在同一个 group 上）
	r := gin.New()
	g := r.Group("/api/admin")
	g.DELETE("/mappings", h.DeleteAll)
	g.DELETE("/mappings/:id", h.Delete)

	// 1) 不带 id → 必须走"解绑全部"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/admin/mappings?dryRun=1", nil))
	if code := respCode(t, w); code != 0 {
		t.Fatalf("DELETE /api/admin/mappings 没走到解绑全部：code=%d body=%s", code, w.Body.String())
	}
	if got := decodeDeleteAll(t, w); got.Mappings != 1 || !got.DryRun {
		t.Errorf("解绑全部的预览结果不对：%+v", got)
	}

	// 2) 带 id → 必须走"解绑一条"（不存在的 id 判 404 就是走了这条分支的铁证）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/admin/mappings/999", nil))
	if code := respCode(t, w); code != 404 {
		t.Fatalf("DELETE /api/admin/mappings/999 没走到单条解绑：code=%d body=%s", code, w.Body.String())
	}

	// 上面两次都是只读（一次 dryRun、一次不存在的 id），数据一条都不该动
	var mc, rc int64
	db.Model(&model.ResourceMapping{}).Count(&mc)
	db.Model(&model.Resource{}).Count(&rc)
	if mc != 1 || rc != 1 {
		t.Errorf("只读请求之后数据被改了：映射 %d 条、资源 %d 条，期望各 1 条", mc, rc)
	}
}

type deleteAllResp struct {
	DryRun           bool  `json:"dryRun"`
	Mappings         int64 `json:"mappings"`
	WithResources    bool  `json:"withResources"`
	RemovedResources int64 `json:"removedResources"`
	RemovedScoped    int64 `json:"removedScoped"`
	RemovedLegacy    int64 `json:"removedLegacy"`
}

func decodeDeleteAll(t *testing.T, rec *httptest.ResponseRecorder) deleteAllResp {
	t.Helper()
	var body struct {
		Code int           `json:"code"`
		Data deleteAllResp `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	// ⚠️ 本仓库的成功业务码是 **0**，不是 HTTP 200 —— 错误码才非 0
	// （见 util.OK / util.Fail，HTTP 状态码恒为 200）。
	// 拿 HTTP 状态码去断言会永远通过，等于没测。
	if body.Code != 0 {
		t.Fatalf("业务码 %d（成功应为 0）, body=%s", body.Code, rec.Body.String())
	}
	return body.Data
}

// callMappingDeleteAll 打一次「解绑全部」。query 直接拼在路径后面（含 ?）。
func callMappingDeleteAll(t *testing.T, h *MappingHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/api/admin/mappings"+query, nil)
	h.DeleteAll(c)
	return w
}

func u64ptr(v uint64) *uint64 { return &v }
