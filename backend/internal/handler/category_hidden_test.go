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

// 这一组守的是「孩子端不该出现「搜全网」这类容器目录」这条规则。
//
// Hidden 的语义刻意收得很窄：**只从孩子端目录树里摘掉**。资源归属、权限判定、
// 站内搜索可见性统统不受影响 —— 因为「搜全网」的内容孩子仍然要能搜到、能播，
// 只是它不该作为一块"学习板块"出现在首页。
//
// 两个最容易写错的地方都锁成了断言：
//
//  1. 摘掉的是**整棵子树**，不是那一层。父节点不在列表里，BuildTree 自然
//     也递归不到它，这一点必须验证（否则会出现"父没了、子飘到根上"）。
//  2. 存量行里 hidden 是 **NULL** 而不是 false —— AutoMigrate 补列不回填
//     默认值，本项目在 resources.mapping_id 上已经踩过一次。
//     所以查询条件必须写 COALESCE(hidden,false)；只写 hidden = false 会让
//     所有老目录一起从孩子端消失。这里用显式 UPDATE ... SET hidden = NULL
//     来复现那个状态。
func newCategoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:category_test_%d?mode=memory&cache=shared", testDBCounter)
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

// seedCategory 建一个目录并把 path 补好（AppTree 不依赖 path，但保持一致）。
func seedCategory(t *testing.T, db *gorm.DB, name string, parent *model.Category, hidden bool) *model.Category {
	t.Helper()
	cat := &model.Category{Name: name, IsPublic: true, Hidden: hidden, Level: 1}
	parentPath := ""
	if parent != nil {
		pid := parent.ID
		cat.ParentID = &pid
		cat.Level = parent.Level + 1
		parentPath = parent.Path
	}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	db.Model(cat).Update("path", model.BuildPath(parentPath, cat.ID))
	db.First(cat, cat.ID)
	return cat
}

func callAppTree(t *testing.T, db *gorm.DB, user *model.User) []*model.Category {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewCategoryHandler(db, service.NewPermissionService(db))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/app/categories/tree", nil)
	c.Set("user", user)
	h.AppTree(c)
	if w.Code != http.StatusOK {
		t.Fatalf("AppTree 返回 %d：%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []*model.Category `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v（%s）", err, w.Body.String())
	}
	return resp.Data
}

func flattenNames(nodes []*model.Category) []string {
	var out []string
	var walk func([]*model.Category)
	walk = func(ns []*model.Category) {
		for _, n := range ns {
			out = append(out, n.Name)
			walk(n.Children)
		}
	}
	walk(nodes)
	return out
}

func TestAppTreeExcludesHiddenSubtree(t *testing.T) {
	db := newCategoryTestDB(t)
	admin := &model.User{Username: "admin", Role: model.RoleAdmin, Status: model.UserStatusEnabled}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}

	keep := seedCategory(t, db, "乐学英语", nil, false)
	container := seedCategory(t, db, "搜全网", nil, true)
	seedCategory(t, db, "自动收录A", container, false)
	seedCategory(t, db, "自动收录B", container, false)

	tree := callAppTree(t, db, admin)
	names := flattenNames(tree)

	if !containsName(names, keep.Name) {
		t.Errorf("普通目录应当出现在孩子端目录树里，实际只有 %v", names)
	}
	if containsName(names, container.Name) {
		t.Errorf("被隐藏的目录不该出现：%v", names)
	}
	for _, n := range []string{"自动收录A", "自动收录B"} {
		if containsName(names, n) {
			t.Errorf("隐藏目录的**子目录**也要一并消失（%s 漏出来了）：%v", n, names)
		}
	}
}

func TestAppTreeKeepsLegacyRowsWithNullHidden(t *testing.T) {
	db := newCategoryTestDB(t)
	admin := &model.User{Username: "admin", Role: model.RoleAdmin, Status: model.UserStatusEnabled}
	db.Create(admin)

	legacy := seedCategory(t, db, "存量目录", nil, false)
	hidden := seedCategory(t, db, "新隐藏目录", nil, true)

	// 复现 AutoMigrate 补列后的真实状态：老行的 hidden 是 NULL 而不是 false。
	if err := db.Exec("UPDATE categories SET hidden = NULL WHERE id = ?", legacy.ID).Error; err != nil {
		t.Fatalf("置 NULL 失败: %v", err)
	}

	tree := callAppTree(t, db, admin)
	names := flattenNames(tree)

	if !containsName(names, legacy.Name) {
		t.Errorf("hidden 为 NULL 的存量目录必须照常可见（这就是 COALESCE 的意义），实际：%v", names)
	}
	if containsName(names, hidden.Name) {
		t.Errorf("显式隐藏的目录仍不该出现：%v", names)
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
