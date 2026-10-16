package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============ 「资源软删除」的闭环 ============
//
// 用户 2026-10-16 拍板：取消映射时资源走**软删除**（不是保留孤儿、也不是物理删除）。
// 这条决定要有意义，就必须回答一个问题 ——
// **软删掉的资源，下一次同步会不会又冒出来？**
//
// 答案必须是"会，但**复用原来那一行**"。这个测试就是在钉这件事。
//
// 不这么做的两个真实后果：
//  1. 用户第 4 条明确要走「全部映射删掉 → 重新映射」这条路。每走一轮，
//     如果同步是重新 INSERT，库里就多一份同 fileId 的死行，走三轮 = 四行，
//     排查"孩子端怎么多了 26 集"时对不上号。
//  2. 重新 INSERT 会换 id，而收藏 / 播放进度 / 播放记录全挂在资源 id 上 ——
//     资源"回来了"，但孩子读到一半的位置没了。
//
// 这里最容易写错的一处是 **Unscoped()**：gorm 的软删模型在 Update 时会自动补
// `AND deleted_at IS NULL`，漏掉 Unscoped 就一行都更新不到，**而且不报错**
// （RowsAffected=0 对 gorm 是正常返回）。表现和"跳过"一模一样，
// 只有这类断言能抓住。
func TestRegisterRevivesSoftDeletedResource(t *testing.T) {
	db := newImportTestDB(t)
	svc := NewImportService(db)
	acc := testAccount(7, "quark")

	// 先按正常路径导入一次
	targets := []DiskImportTarget{
		{FileID: "/quark/oxford/01.mp4", Title: "01.mp4", Size: 100},
	}
	created, skipped, err := svc.Register(acc, 17, 42, targets)
	if err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	if created != 1 || skipped != 0 {
		t.Fatalf("首次导入应新增 1 跳过 0，实际 %d/%d", created, skipped)
	}

	var first model.Resource
	if err := db.Unscoped().Where("file_id = ?", "/quark/oxford/01.mp4").First(&first).Error; err != nil {
		t.Fatalf("取首次导入的行失败: %v", err)
	}
	firstID := first.ID

	// ---- 模拟「取消映射（连带移除资源）」：软删除 ----
	if err := db.Delete(&model.Resource{}, firstID).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	// 先确认它真的"看不见"了 —— 否则下面的断言可能只是在测一个没删成功的东西
	var visible int64
	db.Model(&model.Resource{}).Where("id = ?", firstID).Count(&visible)
	if visible != 0 {
		t.Fatalf("软删除后常规查询仍能看到该行，测试前提不成立")
	}

	// ---- 重新映射后同步：应该**复活**那一行 ----
	created, skipped, err = svc.Register(acc, 17, 99, targets) // mappingID 变了
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if created != 1 {
		t.Errorf("复活应对用户表现为「新增 1」，实际 created=%d", created)
	}
	if skipped != 0 {
		t.Errorf("复活不该被算成跳过，实际 skipped=%d", skipped)
	}

	// 1) 库里只有一行（没有堆垃圾行）
	var total int64
	db.Unscoped().Model(&model.Resource{}).Where("file_id = ?", "/quark/oxford/01.mp4").Count(&total)
	if total != 1 {
		t.Errorf("同 fileId 的行数应为 1，实际 %d —— 复活写成了重新 INSERT", total)
	}

	// 2) 还是原来那一行（id 不变 → 收藏/进度不断）
	var revived model.Resource
	if err := db.Where("file_id = ?", "/quark/oxford/01.mp4").First(&revived).Error; err != nil {
		t.Fatalf("复活后的行查不到: %v", err)
	}
	if revived.ID != firstID {
		t.Errorf("复活后 id 变了（%d → %d），孩子的播放进度会断", firstID, revived.ID)
	}
	if revived.DeletedAt.Valid {
		t.Errorf("复活后 deleted_at 仍然有值 —— Unscoped() 漏了？")
	}

	// 3) 归属信息要跟到**这一条**新映射上，否则再删映射时清不掉它
	if revived.MappingID != 99 {
		t.Errorf("复活后 mapping_id 应为 99，实际 %d", revived.MappingID)
	}
	if revived.CategoryID != 17 {
		t.Errorf("复活后 category_id 应为 17，实际 %d", revived.CategoryID)
	}
	if revived.Status != model.ResourceStatusAlive {
		t.Errorf("复活后 status 应为 alive，实际 %s", revived.Status)
	}
}

// 还活着的资源仍然要跳过 —— 不能因为"顺便支持了复活"就把去重弄丢。
func TestRegisterStillSkipsAliveResource(t *testing.T) {
	db := newImportTestDB(t)
	svc := NewImportService(db)
	acc := testAccount(7, "quark")
	targets := []DiskImportTarget{{FileID: "/q/a.mp4", Title: "a.mp4", Size: 1}}

	if _, _, err := svc.Register(acc, 17, 42, targets); err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	created, skipped, err := svc.Register(acc, 17, 42, targets)
	if err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}
	if created != 0 || skipped != 1 {
		t.Errorf("已存在的资源应 0 新增 / 1 跳过，实际 %d/%d", created, skipped)
	}
	var n int64
	db.Unscoped().Model(&model.Resource{}).Count(&n)
	if n != 1 {
		t.Errorf("不该新增行，实际 %d 行", n)
	}
}

// 复活只认**同一个账号**。
//
// 这条边界很容易写错（图的省事用 file_id 全局匹配），后果是
// "网盘 A 删掉的资源被网盘 B 的同步复活"，而两个账号的 file_id
// 完全可能重名（都是 / 开头的路径）。
func TestRegisterDoesNotReviveOtherAccount(t *testing.T) {
	db := newImportTestDB(t)
	svc := NewImportService(db)

	a := testAccount(1, "quark")
	b := testAccount(2, "webdav")
	targets := []DiskImportTarget{{FileID: "/same/path.mp4", Title: "path.mp4", Size: 1}}

	if _, _, err := svc.Register(a, 17, 42, targets); err != nil {
		t.Fatalf("账号 A 导入失败: %v", err)
	}
	var first model.Resource
	db.Unscoped().Where("disk_account_id = ?", uint64(1)).First(&first)
	if err := db.Delete(&model.Resource{}, first.ID).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	// 账号 B 用同样的 file_id 导入：必须**新建**，不能去复活账号 A 的那一行
	created, _, err := svc.Register(b, 17, 43, targets)
	if err != nil {
		t.Fatalf("账号 B 导入失败: %v", err)
	}
	if created != 1 {
		t.Errorf("账号 B 应新增 1 条，实际 %d", created)
	}

	// 账号 A 的行必须保持软删状态（不能被跨账号的同步悄悄复活）
	var stillDead model.Resource
	if err := db.Unscoped().Where("id = ?", first.ID).First(&stillDead).Error; err != nil {
		t.Fatalf("账号 A 的行不见了: %v", err)
	}
	if !stillDead.DeletedAt.Valid {
		t.Errorf("账号 A 已软删的资源被跨账号同步误复活了")
	}
}

// 复活时标题要重新按**这次**的文件名算（用户可能在网盘上改过名字），
// 扩展名同理 —— Ext 决定要不要转码，拿错了会把 MKV 直链丢给浏览器。
func TestRegisterReviveRefreshesTitleAndExt(t *testing.T) {
	db := newImportTestDB(t)
	svc := NewImportService(db)
	acc := testAccount(7, "quark")

	if _, _, err := svc.Register(acc, 17, 42,
		[]DiskImportTarget{{FileID: "/q/1", Title: "旧名字.mp4", Size: 1}}); err != nil {
		t.Fatalf("首次导入失败: %v", err)
	}
	var first model.Resource
	db.Unscoped().First(&first)
	if err := db.Delete(&model.Resource{}, first.ID).Error; err != nil {
		t.Fatalf("软删除失败: %v", err)
	}

	if _, _, err := svc.Register(acc, 17, 42,
		[]DiskImportTarget{{FileID: "/q/1", Title: "新名字.mkv", Size: 2}}); err != nil {
		t.Fatalf("复活导入失败: %v", err)
	}
	var got model.Resource
	if err := db.Where("id = ?", first.ID).First(&got).Error; err != nil {
		t.Fatalf("复活后的行查不到: %v", err)
	}
	if got.Title != "新名字" {
		t.Errorf("标题应刷新为「新名字」，实际 %q", got.Title)
	}
	if got.Ext != "mkv" {
		t.Errorf("扩展名应刷新为 mkv，实际 %q —— 拿旧 Ext 会把 MKV 直链丢给浏览器", got.Ext)
	}
	if got.TotalSize != 2 {
		t.Errorf("大小应刷新为 2，实际 %d", got.TotalSize)
	}
}

// 一个 in-memory 的干净库。
//
// 用 shared cache + 唯一库名：不然同一个包里的多个用例会互相看见对方的行
// （sqlite 的 `:memory:` 在共享缓存下按名字复用）。
func newImportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	importTestDBCounter++
	dsn := "file:diskimport_test_" + itoa(importTestDBCounter) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Resource{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

var importTestDBCounter int

// DiskAccount 的 ID 来自内嵌的 Base，结构体字面量里**不能**直接写
// promoted 字段（Go 的语法限制），所以单独造一个。
func testAccount(id uint64, diskType string) *model.DiskAccount {
	acc := &model.DiskAccount{DiskType: diskType}
	acc.ID = id
	return acc
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
