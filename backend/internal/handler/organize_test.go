package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// AI 整理助手这组测试守的是"AI 可以乱说，但不能乱动数据"：
//
//  1. 越界的资源/目录必须被拒 —— 模型不可能知道真实主键，它一定会编。
//  2. ref 必须按顺序声明 —— 否则 moveResource 会落到一个还不存在的目录上。
//  3. 同一个资源不能被移动两次 —— 否则最终落点取决于步骤顺序，不可预测。
//  4. 校验必须**不依赖前端提交的 valid 字段** —— 请求体是可以被改的。

func newOrganizeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:organize_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserCategoryGrant{}, &model.Category{},
		&model.Resource{}, &model.AIConfig{}, &model.DiskAccount{},
		&model.ResourceMapping{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// seedOrganizeTwoRoots 建两个互不相干的一级目录：
// 「乐学英语」（含子目录「牛津树英语」，里面放资源）与「奥数入门」（放一个资源）。
// 后者用来验证"越界会怎样"。
func seedOrganizeTwoRoots(t *testing.T, db *gorm.DB) (root, child, other model.Category) {
	t.Helper()
	root = model.Category{Name: "乐学英语", IsPublic: true, Level: 1}
	mustCreate(t, db, &root)
	root.Path = fmt.Sprintf("/%d/", root.ID)
	db.Model(&model.Category{}).Where("id = ?", root.ID).Update("path", root.Path)

	child = model.Category{Name: "牛津树英语", ParentID: &root.ID, Level: 2, IsPublic: true}
	mustCreate(t, db, &child)
	child.Path = root.Path + fmt.Sprintf("%d/", child.ID)
	db.Model(&model.Category{}).Where("id = ?", child.ID).Update("path", child.Path)

	other = model.Category{Name: "奥数入门", IsPublic: true, Level: 1}
	mustCreate(t, db, &other)
	other.Path = fmt.Sprintf("/%d/", other.ID)
	db.Model(&model.Category{}).Where("id = ?", other.ID).Update("path", other.Path)

	for i, title := range []string{"牛津树第 1 集", "牛津树第 2 集", "牛津树第 3 集"} {
		mustCreate(t, db, &model.Resource{
			CategoryID: child.ID, Title: title, MediaType: model.MediaTypeAudio, SortOrder: i,
		})
	}
	mustCreate(t, db, &model.Resource{
		CategoryID: other.ID, Title: "奥数精讲", MediaType: model.MediaTypeVideo,
	})
	return
}

func newOrganizeHandlerForTest(t *testing.T, db *gorm.DB, baseURL string) *OrganizeHandler {
	t.Helper()
	enc, err := util.NewEncryptor("organize-test-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	if baseURL != "" {
		seedDigestAI(t, db, enc, baseURL)
	}
	aiH := NewAIHandler(db, enc)
	return NewOrganizeHandler(db, service.NewPermissionService(db), aiH)
}

func callPostJSON(t *testing.T, target string, body interface{}, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw, _ := json.Marshal(body)
	c.Request = httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	fn(c)
	return w
}

type organizeAPIBody struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) organizeAPIBody {
	t.Helper()
	var b organizeAPIBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	return b
}

func resourcesInCategory(t *testing.T, db *gorm.DB, catID uint64) []model.Resource {
	t.Helper()
	var out []model.Resource
	db.Where("category_id = ?", catID).Order("id asc").Find(&out)
	return out
}

// ---------- 校验层 ----------

func TestOrganizeRejectsOutOfScopeResource(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, other := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, err := h.buildContext(child.ID, 0, "")
	if err != nil {
		t.Fatalf("构建上下文失败: %v", err)
	}

	var outside model.Resource
	if err := db.Where("category_id = ?", other.ID).First(&outside).Error; err != nil {
		t.Fatalf("取越界资源失败: %v", err)
	}

	steps := h.validateSteps([]OrganizeStep{{
		Index: 0, Type: "moveResource", ResourceIDs: []uint64{outside.ID}, Target: "scope",
	}}, oc)

	if steps[0].Valid {
		t.Fatal("范围外的资源必须被拒绝，否则改一下请求体就能搬走任意目录的内容")
	}
	if steps[0].Invalid == "" {
		t.Fatal("拒绝时要给出人能看懂的原因")
	}
}

func TestOrganizeRejectsOutOfScopeTargetCategory(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, other := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, err := h.buildContext(child.ID, 0, "")
	if err != nil {
		t.Fatalf("构建上下文失败: %v", err)
	}
	var res model.Resource
	db.Where("category_id = ?", child.ID).First(&res)

	steps := h.validateSteps([]OrganizeStep{{
		Index: 0, Type: "moveResource",
		ResourceIDs: []uint64{res.ID},
		Target:      fmt.Sprintf("cat:%d", other.ID),
	}}, oc)
	if steps[0].Valid {
		t.Fatal("目标目录在范围外时必须拒绝")
	}
}

func TestOrganizeRefMustBeDeclaredBeforeUse(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, _ := h.buildContext(child.ID, 0, "")
	var res model.Resource
	db.Where("category_id = ?", child.ID).First(&res)

	// 先引用、后创建 —— 顺序反了
	steps := h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "moveResource", ResourceIDs: []uint64{res.ID}, Target: "ref:c1"},
		{Index: 1, Type: "createCategory", Ref: "c1", Name: "第一级", Parent: "scope"},
	}, oc)

	if steps[0].Valid {
		t.Fatal("引用一个尚未创建的目录必须被拒绝")
	}
	if steps[1].Valid != true {
		t.Fatalf("创建步骤本身是合法的，不该被连坐: %s", steps[1].Invalid)
	}
}

func TestOrganizeRejectsMovingSameResourceTwice(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, _ := h.buildContext(child.ID, 0, "")
	var res model.Resource
	db.Where("category_id = ?", child.ID).First(&res)

	steps := h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "createCategory", Ref: "c1", Name: "甲", Parent: "scope"},
		{Index: 1, Type: "createCategory", Ref: "c2", Name: "乙", Parent: "scope"},
		{Index: 2, Type: "moveResource", ResourceIDs: []uint64{res.ID}, Target: "ref:c1"},
		{Index: 3, Type: "moveResource", ResourceIDs: []uint64{res.ID}, Target: "ref:c2"},
	}, oc)

	if !steps[2].Valid {
		t.Fatalf("第一次移动应当合法: %s", steps[2].Invalid)
	}
	if steps[3].Valid {
		t.Fatal("同一个资源被移动两次必须拒绝（否则最终落点取决于顺序）")
	}
}

func TestOrganizeRejectsDuplicateCategoryName(t *testing.T) {
	db := newOrganizeTestDB(t)
	root, _, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, _ := h.buildContext(root.ID, 0, "")

	// 根目录下已经有「牛津树英语」了
	steps := h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "createCategory", Ref: "c1", Name: "牛津树英语", Parent: "scope"},
	}, oc)
	if steps[0].Valid {
		t.Fatal("同级重名目录必须被拒绝")
	}

	// 同一个方案里两次建同名目录也不行
	steps = h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "createCategory", Ref: "c1", Name: "第一级", Parent: "scope"},
		{Index: 1, Type: "createCategory", Ref: "c2", Name: "第一级", Parent: "scope"},
	}, oc)
	if !steps[0].Valid {
		t.Fatalf("第一个应当是合法的: %s", steps[0].Invalid)
	}
	if steps[1].Valid {
		t.Fatal("同一方案内重名也必须拒绝")
	}
}

func TestOrganizeRejectsRenameToUnchangedName(t *testing.T) {
	db := newOrganizeTestDB(t)
	root, child, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, _ := h.buildContext(root.ID, 0, "")
	steps := h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "renameCategory", CategoryID: child.ID, NewName: child.Name},
	}, oc)
	if steps[0].Valid {
		t.Fatal("改成同名是无意义操作，应被过滤掉")
	}
}

func TestOrganizeRejectsMappingWithoutAccount(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	oc, _ := h.buildContext(child.ID, 0, "")
	steps := h.validateSteps([]OrganizeStep{
		{Index: 0, Type: "createMapping", RemoteDirID: "/动画/汪汪队", Target: "scope"},
	}, oc)
	if steps[0].Valid {
		t.Fatal("没有接入源账号时不该允许新建映射")
	}
}

// ---------- 端到端 ----------

// TestOrganizePlanThenApply 走一遍完整链路：
// AI 给方案 → 校验 → 执行 → 落库结果正确（含 ref 解析）。
func TestOrganizePlanThenApply(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, _ := seedOrganizeTwoRoots(t, db)
	fake := newFakeAI(t)

	var ids []uint64
	for _, r := range resourcesInCategory(t, db, child.ID) {
		ids = append(ids, r.ID)
	}

	fake.reply = fmt.Sprintf(`{"summary":"按级别分成两个子目录","steps":[
		{"type":"createCategory","ref":"c1","name":"第一级","icon":"book","parent":"scope","reason":"低级别先读"},
		{"type":"moveResource","resourceIds":[%d,%d],"target":"ref:c1","reason":"都是第一级"},
		{"type":"createCategory","ref":"c2","name":"第二级","icon":"book","parent":"scope","reason":"往后读"},
		{"type":"moveResource","resourceIds":[%d],"target":"ref:c2","reason":"第二级"}
	]}`, ids[0], ids[1], ids[2])

	h := newOrganizeHandlerForTest(t, db, fake.srv.URL)

	// --- 生成方案 ---
	rec := callPostJSON(t, "/api/admin/organize/plan", map[string]interface{}{
		"categoryId": child.ID,
	}, h.Plan)
	body := decodeBody(t, rec)
	if body.Code != 0 {
		t.Fatalf("生成方案失败: %s", body.Message)
	}
	var plan organizePlanResponse
	if err := json.Unmarshal(body.Data, &plan); err != nil {
		t.Fatalf("解析方案失败: %v", err)
	}
	if len(plan.Steps) != 4 {
		t.Fatalf("期望 4 个步骤，得到 %d", len(plan.Steps))
	}
	for _, s := range plan.Steps {
		if !s.Valid {
			t.Fatalf("步骤 %d 应当是合法的: %s", s.Index, s.Invalid)
		}
	}
	if plan.Stats.AffectedResources != 3 {
		t.Fatalf("受影响资源数应为 3，得到 %d", plan.Stats.AffectedResources)
	}

	// --- 执行 ---
	rec = callPostJSON(t, "/api/admin/organize/apply", map[string]interface{}{
		"categoryId": child.ID,
		"steps":      plan.Steps,
	}, h.Apply)
	body = decodeBody(t, rec)
	if body.Code != 0 {
		t.Fatalf("执行失败: %s", body.Message)
	}
	var applied organizeApplyResponse
	if err := json.Unmarshal(body.Data, &applied); err != nil {
		t.Fatalf("解析执行结果失败: %v", err)
	}
	if applied.Applied != 4 || applied.Failed != 0 {
		t.Fatalf("期望 4 成功 0 失败，得到 %+v", applied)
	}

	// --- 验证落库 ---
	var first, second model.Category
	if err := db.Where("name = ? AND parent_id = ?", "第一级", child.ID).First(&first).Error; err != nil {
		t.Fatalf("新建的「第一级」没落库: %v", err)
	}
	if err := db.Where("name = ? AND parent_id = ?", "第二级", child.ID).First(&second).Error; err != nil {
		t.Fatalf("新建的「第二级」没落库: %v", err)
	}
	// 物化路径必须被正确维护，否则权限与搜索都会出问题
	if !strings.HasPrefix(first.Path, child.Path) {
		t.Fatalf("新目录 path=%q 应当是 %q 的子路径", first.Path, child.Path)
	}
	if first.Level != child.Level+1 {
		t.Fatalf("新目录层级应为 %d，得到 %d", child.Level+1, first.Level)
	}
	// 公开性应继承父目录
	if !first.IsPublic {
		t.Fatal("公开目录下新建的子目录应继承公开性")
	}

	if n := len(resourcesInCategory(t, db, first.ID)); n != 2 {
		t.Fatalf("「第一级」应有 2 个资源，得到 %d", n)
	}
	if n := len(resourcesInCategory(t, db, second.ID)); n != 1 {
		t.Fatalf("「第二级」应有 1 个资源，得到 %d", n)
	}
	if n := len(resourcesInCategory(t, db, child.ID)); n != 0 {
		t.Fatalf("原子目录应被搬空，还剩 %d 个", n)
	}
}

// TestOrganizeApplyRevalidatesSubmittedSteps 前端提交的步骤会被重新校验。
//
// 这条守的是"接口不能被当成任意搬运器"：请求体里塞一个范围外的资源 ID，
// 即使把 valid 写成 true，也必须被拒。
func TestOrganizeApplyRevalidatesSubmittedSteps(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, other := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	var outside model.Resource
	db.Where("category_id = ?", other.ID).First(&outside)

	rec := callPostJSON(t, "/api/admin/organize/apply", map[string]interface{}{
		"categoryId": child.ID,
		"steps": []map[string]interface{}{
			{
				"index": 0, "type": "moveResource",
				"resourceIds": []uint64{outside.ID},
				"target":      "scope",
				"valid":       true, // 客户端谎报，必须无效
			},
		},
	}, h.Apply)

	body := decodeBody(t, rec)
	if body.Code != 0 {
		t.Fatalf("业务码应为 0（失败项在 data 里体现），得到 %d: %s", body.Code, body.Message)
	}
	var applied organizeApplyResponse
	json.Unmarshal(body.Data, &applied)
	if applied.Applied != 0 || applied.Failed != 1 {
		t.Fatalf("越界步骤必须失败，得到 %+v", applied)
	}

	var after model.Resource
	db.First(&after, outside.ID)
	if after.CategoryID != other.ID {
		t.Fatalf("越界资源被移动了！category_id 从 %d 变成了 %d", other.ID, after.CategoryID)
	}
}

// TestOrganizeDryRunWritesNothing 试运行不落库。
func TestOrganizeDryRunWritesNothing(t *testing.T) {
	db := newOrganizeTestDB(t)
	_, child, _ := seedOrganizeTwoRoots(t, db)
	h := newOrganizeHandlerForTest(t, db, "")

	var res model.Resource
	db.Where("category_id = ?", child.ID).First(&res)

	rec := callPostJSON(t, "/api/admin/organize/apply", map[string]interface{}{
		"categoryId": child.ID,
		"dryRun":     true,
		"steps": []map[string]interface{}{
			{"index": 0, "type": "createCategory", "ref": "c1", "name": "第一级", "parent": "scope"},
			{"index": 1, "type": "moveResource", "resourceIds": []uint64{res.ID}, "target": "ref:c1"},
		},
	}, h.Apply)

	body := decodeBody(t, rec)
	if body.Code != 0 {
		t.Fatalf("试运行不该报错: %s", body.Message)
	}
	var applied organizeApplyResponse
	json.Unmarshal(body.Data, &applied)
	if !applied.DryRun || applied.Applied != 0 {
		t.Fatalf("试运行不应真的执行，得到 %+v", applied)
	}

	var cnt int64
	db.Model(&model.Category{}).Where("name = ?", "第一级").Count(&cnt)
	if cnt != 0 {
		t.Fatal("试运行不该新建目录")
	}
	var after model.Resource
	db.First(&after, res.ID)
	if after.CategoryID != child.ID {
		t.Fatal("试运行不该移动资源")
	}
}

// TestOrganizePlanRejectsEmptyScope 空目录直接拒绝，不要白白调一次 AI。
func TestOrganizePlanRejectsEmptyScope(t *testing.T) {
	db := newOrganizeTestDB(t)
	root, _, _ := seedOrganizeTwoRoots(t, db)
	fake := newFakeAI(t)
	h := newOrganizeHandlerForTest(t, db, fake.srv.URL)

	// 新建一个没有任何内容的目录
	empty := model.Category{Name: "空目录", ParentID: &root.ID, Level: 2}
	mustCreate(t, db, &empty)

	rec := callPostJSON(t, "/api/admin/organize/plan", map[string]interface{}{
		"categoryId": empty.ID,
	}, h.Plan)
	body := decodeBody(t, rec)
	if body.Code == 0 {
		t.Fatal("空目录应当直接拒绝")
	}
	if n := fake.callCount(); n != 0 {
		t.Fatalf("空目录不该浪费一次 AI 调用，实际调用 %d 次", n)
	}
}
