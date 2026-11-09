package handler

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 学习记录上的「所属目录」守两件事：
//
//  1. 多级路径拼对（家长看到「第一册」得知道是哪一套）；
//  2. 上级目录被删掉之后，历史记录不能变成断头路径 —— 那些记录还在，
//     名字却没了，等于把过去的学习痕迹抹掉一半。
func newCategoryPathDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:catpath_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Category{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

func TestResolveCategoryPathsBuildsFullChain(t *testing.T) {
	db := newCategoryPathDB(t)

	root := model.Category{Name: "乐学英语", Level: 1}
	db.Create(&root)
	root.Path = model.BuildPath("", root.ID)
	db.Save(&root)

	mid := model.Category{Name: "新概念英语", Level: 2, ParentID: &root.ID}
	db.Create(&mid)
	mid.Path = model.BuildPath(root.Path, mid.ID)
	db.Save(&mid)

	leaf := model.Category{Name: "第一册", Level: 3, ParentID: &mid.ID}
	db.Create(&leaf)
	leaf.Path = model.BuildPath(mid.Path, leaf.ID)
	db.Save(&leaf)

	got := resolveCategoryPaths(db, []uint64{leaf.ID})
	want := "乐学英语 / 新概念英语 / 第一册"
	if got[leaf.ID] != want {
		t.Fatalf("路径拼错：got=%q want=%q", got[leaf.ID], want)
	}
}

// 上级目录被软删：路径里那一段仍然要有名字。
func TestResolveCategoryPathsKeepsDeletedAncestors(t *testing.T) {
	db := newCategoryPathDB(t)

	root := model.Category{Name: "牛津树", Level: 1}
	db.Create(&root)
	root.Path = model.BuildPath("", root.ID)
	db.Save(&root)

	leaf := model.Category{Name: "Stage 3", Level: 2, ParentID: &root.ID}
	db.Create(&leaf)
	leaf.Path = model.BuildPath(root.Path, leaf.ID)
	db.Save(&leaf)

	// 家长把一级目录删了（软删），子目录和资源都还在
	if err := db.Delete(&model.Category{}, root.ID).Error; err != nil {
		t.Fatalf("软删失败: %v", err)
	}

	got := resolveCategoryPaths(db, []uint64{leaf.ID})
	if got[leaf.ID] != "牛津树 / Stage 3" {
		t.Fatalf("软删的祖先丢了名字：got=%q", got[leaf.ID])
	}
}

// Path 没维护好（历史脏数据）：至少回退到自己这一级，不能返回空。
func TestResolveCategoryPathsFallsBackToSelfName(t *testing.T) {
	db := newCategoryPathDB(t)

	cat := model.Category{Name: "未整理", Level: 1} // Path 留空
	db.Create(&cat)

	got := resolveCategoryPaths(db, []uint64{cat.ID})
	if got[cat.ID] != "未整理" {
		t.Fatalf("空 Path 的回退不对：got=%q", got[cat.ID])
	}
}

// 目录名带尾空格（导入自网盘文件夹名）时，拼出来的路径不能出现双空格。
func TestResolveCategoryPathsTrimsSegments(t *testing.T) {
	db := newCategoryPathDB(t)

	root := model.Category{Name: "柯林斯大猫 Big Cat ", Level: 1}
	db.Create(&root)
	root.Path = model.BuildPath("", root.ID)
	db.Save(&root)

	leaf := model.Category{Name: " 11级", Level: 2, ParentID: &root.ID}
	db.Create(&leaf)
	leaf.Path = model.BuildPath(root.Path, leaf.ID)
	db.Save(&leaf)

	got := resolveCategoryPaths(db, []uint64{leaf.ID})
	if got[leaf.ID] != "柯林斯大猫 Big Cat / 11级" {
		t.Fatalf("段首尾空格没清掉：got=%q", got[leaf.ID])
	}
}

// 传进来的 ID 在库里不存在（资源指向已硬删的目录）→ 不出现在结果里，
// 让调用方回退到记录里冗余的那份名字。
func TestResolveCategoryPathsSkipsUnknownIDs(t *testing.T) {
	db := newCategoryPathDB(t)
	got := resolveCategoryPaths(db, []uint64{999999})
	if len(got) != 0 {
		t.Fatalf("不存在的 ID 不该有结果：%v", got)
	}
	// 空输入也不该查库/panic
	if len(resolveCategoryPaths(db, nil)) != 0 {
		t.Fatal("空输入应返回空 map")
	}
}
