package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 这组测试守两件事：
//
//  1. 孩子端 /app/resources* 三条端点必须按目录权限收口 —— 此前它们
//     完全不过滤，孩子可以用越权的 categoryId 直接读到任意目录的资源。
//  2. 「找一找」要能同时搜到目录与资源，且同样受权限约束。
//
// 注意：测试库是 SQLite，它没有 ILIKE，走的是 LIKE 分支（见 likeOp）。
// PostgreSQL 上 ILIKE + pg_trgm 索引的行为靠验收探针覆盖。

func newSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:search_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserCategoryGrant{}, &model.Category{}, &model.Resource{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// seedSearchFixture 建两个一级目录（公开 / 私有）各挂一个资源。
func seedSearchFixture(t *testing.T, db *gorm.DB) (pubCat, privCat model.Category, pubRes, privRes model.Resource) {
	t.Helper()

	pubCat = model.Category{Name: "乐学英语", IsPublic: true, Level: 1}
	mustCreate(t, db, &pubCat)
	pubCat.Path = fmt.Sprintf("/%d/", pubCat.ID)
	db.Model(&model.Category{}).Where("id = ?", pubCat.ID).Update("path", pubCat.Path)

	privCat = model.Category{Name: "奥数入门", IsPublic: false, Level: 1}
	mustCreate(t, db, &privCat)
	privCat.Path = fmt.Sprintf("/%d/", privCat.ID)
	db.Model(&model.Category{}).Where("id = ?", privCat.ID).Update("path", privCat.Path)

	pubRes = model.Resource{CategoryID: pubCat.ID, Title: "新概念英语第一册", MediaType: model.MediaTypeVideo}
	mustCreate(t, db, &pubRes)

	privRes = model.Resource{CategoryID: privCat.ID, Title: "奥数精讲视频", MediaType: model.MediaTypeVideo}
	mustCreate(t, db, &privRes)

	return
}

func seedKid(t *testing.T, db *gorm.DB) *model.User {
	t.Helper()
	kid := &model.User{
		Username: "kid_test", Role: model.RoleKid, Status: model.UserStatusEnabled,
	}
	mustCreate(t, db, kid)
	return kid
}

func seedAdmin(t *testing.T, db *gorm.DB) *model.User {
	t.Helper()
	admin := &model.User{
		Username: "admin_test", Role: model.RoleAdmin, Status: model.UserStatusEnabled,
	}
	mustCreate(t, db, admin)
	return admin
}

// callResourceGet 以指定用户身份调用某个资源类 handler。
func callResourceGet(t *testing.T, user *model.User, target string, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if user != nil {
		c.Set("user", user)
		c.Set("userID", user.ID)
		c.Set("role", user.Role)
	}
	fn(c)
	return w
}

func newScopedResourceHandler(db *gorm.DB) *ResourceHandler {
	return &ResourceHandler{db: db, perm: service.NewPermissionService(db)}
}

// setParam 手工塞路径参数（gin 只有在真正路由匹配时才填 Params）。
func setParam(c *gin.Context, name, value string) {
	c.Params = append(c.Params, gin.Param{Key: name, Value: value})
}

type listRespData struct {
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	List     []model.Resource `json:"list"`
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listRespData {
	t.Helper()
	var body struct {
		Code int          `json:"code"`
		Data listRespData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("业务码非 0: %d, body=%s", body.Code, rec.Body.String())
	}
	return body.Data
}

// TestResourceListScopedByPermission 未授权孩子只能看到公开目录下的资源。
func TestResourceListScopedByPermission(t *testing.T) {
	db := newSearchTestDB(t)
	_, _, pubRes, _ := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid, "/api/app/resources", h.List)
	data := decodeList(t, rec)
	if len(data.List) != 1 {
		t.Fatalf("未授权孩子看到 %d 条资源，期望 1 条（只应有公开目录里的）: %+v", len(data.List), data.List)
	}
	if data.List[0].ID != pubRes.ID {
		t.Errorf("看到的资源 ID=%d，期望 %d", data.List[0].ID, pubRes.ID)
	}
	if data.Total != 1 {
		t.Errorf("total=%d，期望 1", data.Total)
	}
}

// TestResourceListScopedByExplicitCategoryID 越权指定 categoryId 也拿不到东西。
//
// 这是原 bug 的具体形态：孩子带上私有目录的 categoryId 请求，接口原样返回。
func TestResourceListScopedByExplicitCategoryID(t *testing.T) {
	db := newSearchTestDB(t)
	_, privCat, _, privRes := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid,
		fmt.Sprintf("/api/app/resources?categoryId=%d", privCat.ID), h.List)
	data := decodeList(t, rec)
	if len(data.List) != 0 || data.Total != 0 {
		t.Errorf("越权 categoryId=%d 仍读到 %d 条（资源 id=%v），应被挡住",
			privCat.ID, len(data.List), data.List)
	}
	_ = privRes
}

// TestResourceListAdminUnrestricted 管理员不受限。
func TestResourceListAdminUnrestricted(t *testing.T) {
	db := newSearchTestDB(t)
	seedSearchFixture(t, db)
	admin := seedAdmin(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, admin, "/api/app/resources", h.List)
	if data := decodeList(t, rec); len(data.List) != 2 {
		t.Errorf("管理员看到 %d 条资源，期望 2 条", len(data.List))
	}
}

// TestResourceDetailScoped 资源详情会带出 ShareURL/FileID，越权必须 404。
func TestResourceDetailScoped(t *testing.T) {
	db := newSearchTestDB(t)
	_, _, pubRes, privRes := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid, fmt.Sprintf("/api/app/resources/%d", privRes.ID),
		func(c *gin.Context) { setParam(c, "id", fmt.Sprint(privRes.ID)); h.Detail(c) })
	if code := respCode(t, rec); code != 404 {
		t.Errorf("越权读私有资源详情应返回 404，实际 code=%d, body=%s", code, rec.Body.String())
	}

	rec = callResourceGet(t, kid, fmt.Sprintf("/api/app/resources/%d", pubRes.ID),
		func(c *gin.Context) { setParam(c, "id", fmt.Sprint(pubRes.ID)); h.Detail(c) })
	if code := respCode(t, rec); code != 0 {
		t.Errorf("读公开资源详情应成功，实际 code=%d, body=%s", code, rec.Body.String())
	}
}

// TestFacetsScoped 分布统计不得泄露隐藏目录的资源规模。
func TestFacetsScoped(t *testing.T) {
	db := newSearchTestDB(t)
	seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid, "/api/app/resources/facets", h.Facets)
	var body struct {
		Code int `json:"code"`
		Data struct {
			Total     int64 `json:"total"`
			DiskTypes []struct {
				Name  string `json:"name"`
				Total int64  `json:"total"`
			} `json:"diskTypes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("业务码非 0: %d", body.Code)
	}
	if body.Data.Total != 1 {
		t.Errorf("facets 合计 %d，期望 1（只算公开目录里的）", body.Data.Total)
	}
	var sum int64
	for _, d := range body.Data.DiskTypes {
		sum += d.Total
	}
	if sum != body.Data.Total {
		t.Errorf("diskTypes 明细合计 %d != total %d", sum, body.Data.Total)
	}
}

type searchRespData struct {
	Keyword    string               `json:"keyword"`
	Categories []searchCategoryItem `json:"categories"`
	Resources  listRespData         `json:"resources"`
}

func decodeSearch(t *testing.T, rec *httptest.ResponseRecorder) searchRespData {
	t.Helper()
	var body struct {
		Code int            `json:"code"`
		Data searchRespData `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, rec.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("业务码非 0: %d, body=%s", body.Code, rec.Body.String())
	}
	return body.Data
}

// TestSearchAllHitsCategoriesAndResources 「找一找」一次命中目录与资源。
func TestSearchAllHitsCategoriesAndResources(t *testing.T) {
	db := newSearchTestDB(t)
	pubCat, _, pubRes, _ := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid, "/api/app/search?keyword=%E8%8B%B1%E8%AF%AD", h.SearchAll) // 英语
	data := decodeSearch(t, rec)

	if len(data.Categories) != 1 {
		t.Fatalf("命中目录 %d 个，期望 1 个：%+v", len(data.Categories), data.Categories)
	}
	if data.Categories[0].ID != pubCat.ID {
		t.Errorf("命中的目录 ID=%d，期望 %d", data.Categories[0].ID, pubCat.ID)
	}
	if data.Categories[0].ResourceCount != 1 {
		t.Errorf("目录 resourceCount=%d，期望 1", data.Categories[0].ResourceCount)
	}

	if len(data.Resources.List) != 1 || data.Resources.List[0].ID != pubRes.ID {
		t.Errorf("命中资源 %+v，期望只有 id=%d", data.Resources.List, pubRes.ID)
	}
}

// TestSearchAllIgnoresSoftDeletedResources 目录资源数必须排除软删资源。
//
// 线上真实故障：资源表 963 行里 517 行是软删的，而统计走的是
// Table("resources") —— 它绕过 gorm 的软删除条件，于是「牛津树英语」
// 显示 465 个资源、孩子点进去一条都没有。数量与内容对不上比不显示更糟，
// 孩子会以为"没了/坏了"。
//
// 这条测试守的是：聚合查询也必须继承 gorm 的软删语义。
func TestSearchAllIgnoresSoftDeletedResources(t *testing.T) {
	db := newSearchTestDB(t)
	pubCat, _, pubRes, _ := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	// 再挂两个同关键词的资源，然后软删掉
	for _, title := range []string{"英语儿歌A", "英语儿歌B"} {
		r := model.Resource{CategoryID: pubCat.ID, Title: title, MediaType: model.MediaTypeAudio}
		mustCreate(t, db, &r)
		if err := db.Delete(&model.Resource{}, r.ID).Error; err != nil {
			t.Fatalf("软删资源失败: %v", err)
		}
	}

	rec := callResourceGet(t, kid, "/api/app/search?keyword=%E8%8B%B1%E8%AF%AD", h.SearchAll)
	data := decodeSearch(t, rec)

	if len(data.Categories) != 1 {
		t.Fatalf("命中目录 %d 个，期望 1 个：%+v", len(data.Categories), data.Categories)
	}
	if got := data.Categories[0].ResourceCount; got != 1 {
		t.Errorf("目录 resourceCount=%d，期望 1（软删资源不应计入）", got)
	}
	if data.Resources.Total != 1 || len(data.Resources.List) != 1 {
		t.Errorf("资源命中 total=%d list=%d，期望各为 1（软删资源不应出现）",
			data.Resources.Total, len(data.Resources.List))
	}
	if len(data.Resources.List) == 1 && data.Resources.List[0].ID != pubRes.ID {
		t.Errorf("命中资源 id=%d，期望 %d", data.Resources.List[0].ID, pubRes.ID)
	}
}

// TestResourceListIgnoresSoftDeleted 列表同理：软删资源不能被列出来。
func TestResourceListIgnoresSoftDeleted(t *testing.T) {
	db := newSearchTestDB(t)
	pubCat, _, pubRes, _ := seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	gone := model.Resource{CategoryID: pubCat.ID, Title: "已下架的视频", MediaType: model.MediaTypeVideo}
	mustCreate(t, db, &gone)
	if err := db.Delete(&model.Resource{}, gone.ID).Error; err != nil {
		t.Fatalf("软删资源失败: %v", err)
	}

	rec := callResourceGet(t, kid, fmt.Sprintf("/api/app/resources?categoryId=%d", pubCat.ID), h.List)
	page := decodeList(t, rec)

	if page.Total != 1 {
		t.Errorf("total=%d，期望 1（软删资源不应计入）", page.Total)
	}
	if len(page.List) != 1 || page.List[0].ID != pubRes.ID {
		t.Errorf("列表=%+v，期望只有 id=%d", page.List, pubRes.ID)
	}
}


func TestSearchAllScopedByPermission(t *testing.T) {
	db := newSearchTestDB(t)
	seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	// 「奥数」既是私有目录名，也是私有资源名的前缀
	rec := callResourceGet(t, kid, "/api/app/search?keyword=%E5%A5%A5%E6%95%B0", h.SearchAll)
	data := decodeSearch(t, rec)

	if len(data.Categories) != 0 {
		t.Errorf("未授权孩子搜到私有目录：%+v", data.Categories)
	}
	if len(data.Resources.List) != 0 || data.Resources.Total != 0 {
		t.Errorf("未授权孩子搜到私有资源：%+v", data.Resources.List)
	}
}

// TestSearchAllEmptyKeyword 空关键词返回空结构而不是报错。
func TestSearchAllEmptyKeyword(t *testing.T) {
	db := newSearchTestDB(t)
	seedSearchFixture(t, db)
	kid := seedKid(t, db)
	h := newScopedResourceHandler(db)

	rec := callResourceGet(t, kid, "/api/app/search?keyword=", h.SearchAll)
	data := decodeSearch(t, rec)
	if data.Keyword != "" || len(data.Categories) != 0 || data.Resources.Total != 0 {
		t.Errorf("空关键词应返回空结果，实际 %+v", data)
	}
	// 前端会直接 .map() 结果，nil 切片会炸 —— 必须是空数组
	if data.Categories == nil {
		t.Error("categories 是 nil，期望空数组")
	}
}

// TestLikeOpPicksDialect SQLite 不能用 ILIKE，否则整个搜索直接语法错。
func TestLikeOpPicksDialect(t *testing.T) {
	db := newSearchTestDB(t)
	if got := likeOp(db); got != "LIKE" {
		t.Errorf("sqlite 方言下 likeOp=%q，期望 LIKE", got)
	}
	if got := likeOp(nil); got != "LIKE" {
		t.Errorf("nil db 下 likeOp=%q，期望 LIKE（保守退回）", got)
	}
}

// TestBreadcrumbOf 物化路径 → 面包屑。
func TestBreadcrumbOf(t *testing.T) {
	names := map[uint64]string{1: "乐学英语", 12: "新概念英语", 35: "第一册", 99: "孤儿目录"}

	cases := []struct {
		path string
		want string
	}{
		{"/1/", "乐学英语"},
		{"/1/12/", "乐学英语 / 新概念英语"},
		{"/1/12/35/", "乐学英语 / 新概念英语 / 第一册"},
		{"", ""},
		// 路径里出现查不到的 ID（目录已删、路径未更新）直接跳过，不留空洞
		{"/1/77/12/", "乐学英语 / 新概念英语"},
	}
	for _, tc := range cases {
		if got := breadcrumbOf(tc.path, names); got != tc.want {
			t.Errorf("breadcrumbOf(%q)=%q，期望 %q", tc.path, got, tc.want)
		}
	}
}
