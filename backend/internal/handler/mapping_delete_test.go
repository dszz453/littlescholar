package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 删除映射时的资源归属策略。
//
// 背景（线上真实故障）：用户删掉了夸克那条映射，以为那 26 集牛津树会一起消失，
// 结果孩子端依然列着 —— 因为老代码**只删映射不删资源**，而且是从未说明白的
// 隐含行为。资源本身没有任何来源标记，事后连"哪些是这条映射导入的"都无从判断，
// 只能靠 (账号 + 目录) 猜，而同一目录被先后映射两次时猜出来的必然多算。
//
// 所以这里同时锁两件事：
//  1. 默认行为不变（只删映射，资源保留）—— 删资源不该被"取消配置"顺带触发；
//  2. 显式要求时，**只**清理这条映射导入的资源，绝不牵连手动导入的
//     （mapping_id = 0）和别的映射导入的。
func TestMappingDeleteResourcePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		// 第一条映射已在调用前被软删除，用来验证"补一刀"的场景
		preSoftDeleted bool
		withResources  bool
		// 期望：该映射的资源还剩几条（未被软删除）
		wantSurvivors int64
		// 期望：映射本身是否已被删除
		wantMappingGone bool
	}{
		{name: "默认只删映射，资源保留", withResources: false, wantSurvivors: 3, wantMappingGone: true},
		{name: "显式连带清理", withResources: true, wantSurvivors: 0, wantMappingGone: true},
		{name: "映射先前已删，仍可补清理", preSoftDeleted: true, withResources: true, wantSurvivors: 0, wantMappingGone: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newMappingTestDB(t)
			h := &MappingHandler{db: db}

			target := model.ResourceMapping{Name: "牛津树-夸克", AccountID: 1, CategoryID: 17}
			other := model.ResourceMapping{Name: "牛津树-WebDAV", AccountID: 2, CategoryID: 17}
			mustCreate(t, db, &target)
			mustCreate(t, db, &other)

			// 3 条来自 target 映射
			for i := 0; i < 3; i++ {
				mustCreate(t, db, &model.Resource{
					Title: fmt.Sprintf("魔法钥匙%02d", i), CategoryID: 17,
					DiskType: "quark", MappingID: target.ID,
				})
			}
			// 2 条来自另一条映射
			for i := 0; i < 2; i++ {
				mustCreate(t, db, &model.Resource{
					Title: fmt.Sprintf("W%02d", i), CategoryID: 17,
					DiskType: "webdav", MappingID: other.ID,
				})
			}
			// 1 条手动导入：mapping_id = 0，任何映射的清理都不该碰到它
			mustCreate(t, db, &model.Resource{Title: "手动导入的", CategoryID: 17, DiskType: "local"})

			if c.preSoftDeleted {
				if err := db.Delete(&model.ResourceMapping{}, target.ID).Error; err != nil {
					t.Fatalf("预置软删除失败: %v", err)
				}
			}

			rec := callMappingDelete(t, h, fmt.Sprint(target.ID), c.withResources)
			if rec.Code != 200 {
				t.Fatalf("HTTP %d, body=%s", rec.Code, rec.Body.String())
			}

			// 1) 该映射的资源：按预期被清掉或保留
			var survivors int64
			db.Model(&model.Resource{}).Where("mapping_id = ?", target.ID).Count(&survivors)
			if survivors != c.wantSurvivors {
				t.Errorf("mapping_id=%d 残留 %d 条，期望 %d 条", target.ID, survivors, c.wantSurvivors)
			}

			// 2) 别的映射的资源一条都不能少（这是最容易写错的地方）
			var otherAlive int64
			db.Model(&model.Resource{}).Where("mapping_id = ?", other.ID).Count(&otherAlive)
			if otherAlive != 2 {
				t.Errorf("误伤了另一条映射的资源：还剩 %d 条，期望 2", otherAlive)
			}

			// 3) 手动导入的资源必须完好
			var manualAlive int64
			db.Model(&model.Resource{}).Where("mapping_id = 0").Count(&manualAlive)
			if manualAlive != 1 {
				t.Errorf("误伤了手动导入的资源：还剩 %d 条，期望 1", manualAlive)
			}

			// 4) 映射本身的状态（软删除 → 常规查询查不到即为已删）
			var mcount int64
			db.Model(&model.ResourceMapping{}).Where("id = ?", target.ID).Count(&mcount)
			if c.wantMappingGone && mcount != 0 {
				t.Errorf("映射应已被删除，实际还剩 %d 条", mcount)
			}
		})
	}
}

// 历史资源（mapping_id=0）的兜底清理。
//
// 这是整个修复的**关键路径**，不能只测新资源：mapping_id 是本次才加的列，
// 加列之前导入的资源全是 0。线上现状就是这么来的 —— 2454 条资源几乎全是 0，
// 如果级联删除只认 mapping_id，用户点完"同时删除已导入的资源"会看到
// "已清理 0 条"，然后孩子端一切照旧，比不改还糟。
//
// 所以兜底按「这条映射的账号 + 目标目录」去认，同时必须守住边界：
// 别的账号、别的目录、以及没有账号的手动导入，一条都不能碰。
func TestMappingDeleteSweepsLegacyResources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	// 待删的映射：账号 1 / 目录 17
	target := model.ResourceMapping{Name: "牛津树-夸克", AccountID: 1, CategoryID: 17}
	mustCreate(t, db, &target)
	// 另一条映射占用了同样的目录，但账号不同 —— 它的历史资源不该被牵连
	sibling := model.ResourceMapping{Name: "牛津树-WebDAV", AccountID: 2, CategoryID: 17}
	mustCreate(t, db, &sibling)

	acc := func(v uint64) *uint64 { return &v }

	// 1 条新式资源：带 mapping_id
	mustCreate(t, db, &model.Resource{Title: "新式", CategoryID: 17, DiskAccountID: acc(1), MappingID: target.ID})
	// 2 条历史资源：无归属，账号+目录都对得上 → 应被兜底清掉
	for i := 0; i < 2; i++ {
		mustCreate(t, db, &model.Resource{
			Title: fmt.Sprintf("历史%02d", i), CategoryID: 17, DiskAccountID: acc(1),
		})
	}
	// 关键：把它们的 mapping_id 置成 **NULL** 而不是留着 GORM 写进去的 0。
	//
	// 线上就是这么来的 —— AutoMigrate 给已存在的行补列走 ALTER TABLE ADD COLUMN，
	// 不回填默认值，历史行因此是 NULL。单测里如果只留 0，就会得出
	// "sqlite 过、postgres 死"的假绿灯（已实际踩过一次）。
	tx := db.Exec("UPDATE resources SET mapping_id = NULL WHERE mapping_id = 0 AND title LIKE '历史%'")
	if tx.Error != nil {
		t.Fatalf("置 NULL 失败: %v", tx.Error)
	}
	// 账号不同 → 保留（这是最容易被写错的一条：少了 disk_account_id 条件就会误删）
	mustCreate(t, db, &model.Resource{Title: "别的账号", CategoryID: 17, DiskAccountID: acc(2)})
	// 目录不同 → 保留
	mustCreate(t, db, &model.Resource{Title: "别的目录", CategoryID: 99, DiskAccountID: acc(1)})
	// 没有账号（手动导入/本地硬盘）→ 保留
	mustCreate(t, db, &model.Resource{Title: "手动导入", CategoryID: 17, DiskType: "local"})

	rec := callMappingDelete(t, h, fmt.Sprint(target.ID), true)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d, body=%s", rec.Code, rec.Body.String())
	}

	// 响应里要能看出"清了几条、其中几条是历史资源"，否则线上排查只能靠猜
	var body struct {
		Code int `json:"code"`
		Data struct {
			RemovedResources int64 `json:"removedResources"`
			RemovedScoped    int64 `json:"removedScoped"`
			RemovedLegacy    int64 `json:"removedLegacy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if body.Data.RemovedScoped != 1 || body.Data.RemovedLegacy != 2 || body.Data.RemovedResources != 3 {
		t.Errorf("清理计数不对：scoped=%d legacy=%d total=%d，期望 1/2/3",
			body.Data.RemovedScoped, body.Data.RemovedLegacy, body.Data.RemovedResources)
	}

	// 边界：只有"这条映射 + 对得上的历史资源"消失，其余 3 条必须还在
	var alive int64
	db.Model(&model.Resource{}).Count(&alive)
	if alive != 3 {
		t.Errorf("剩余资源 %d 条，期望 3（别的账号 / 别的目录 / 手动导入）", alive)
	}
	for _, title := range []string{"别的账号", "别的目录", "手动导入"} {
		var n int64
		db.Model(&model.Resource{}).Where("title = ?", title).Count(&n)
		if n != 1 {
			t.Errorf("「%s」被误删了", title)
		}
	}
}

// 不存在的 ID 不能被静默"成功"——否则前端会提示清理完成，实际什么都没发生。
//
// 注意本仓库的约定：HTTP 状态码恒为 200，业务码放在 body 的 code 字段
// （见 util.Fail）。所以断言要看 code，不能看 HTTP 状态。
func TestMappingDeleteUnknownID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newMappingTestDB(t)
	h := &MappingHandler{db: db}

	rec := callMappingDelete(t, h, "999", true)
	if code := respCode(t, rec); code != 404 {
		t.Errorf("不存在的映射应返回 404，实际 code=%d, body=%s", code, rec.Body.String())
	}

	// 非法 ID 也要挡住
	rec = callMappingDelete(t, h, "abc", true)
	if code := respCode(t, rec); code != 400 {
		t.Errorf("非法 ID 应返回 400，实际 code=%d, body=%s", code, rec.Body.String())
	}
}

// respCode 取出统一响应体里的业务码。
func respCode(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	return body.Code
}

func mustCreate(t *testing.T, db *gorm.DB, v interface{}) {
	t.Helper()
	if err := db.Create(v).Error; err != nil {
		t.Fatalf("插入失败: %v", err)
	}
}

func newMappingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:mapping_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Category{}, &model.Resource{}, &model.ResourceMapping{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

func callMappingDelete(t *testing.T, h *MappingHandler, id string, withResources bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: id}}
	url := "/api/admin/mappings/" + id
	if withResources {
		url += "?withResources=1"
	}
	c.Request = httptest.NewRequest("DELETE", url, nil)
	h.Delete(c)
	return w
}
