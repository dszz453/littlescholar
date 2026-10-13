package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 转存的职责边界：**只**把分享里的文件落进网盘目录，不负责"资源归属"。
//
// 为什么值得单独锁一条测试：`processTransfer` 里曾经有一段"转存成功后顺手
// 建一条 Resource"的逻辑，看起来是顺手的好事，实际制造了本项目里最麻烦的
// 一类问题 —— 资源有两个写入方（转存 + 映射同步），而转存写进去的那条
// 不带 mapping_id，事后谁也说不清它是哪条映射导入的，"删了映射资源还在"
// 就是这么长出来的。归属统一交给 ResourceMapping 之后，这个坑必须永久堵上。
func TestTransferDoesNotOwnResourceAttribution(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// ---- 1) 接口层：带了 categoryId 也必须被忽略，目录名要能落库 ----
	t.Run("请求里的 categoryId 被忽略", func(t *testing.T) {
		db := newTransferTestDB(t)
		// 池子不 Start()：任务只会进队列，不会真去外呼网盘。
		h := NewTransferHandler(db, mustEncryptor(t), task.NewWorkerPool(1, 10))

		body, _ := json.Marshal(map[string]interface{}{
			"diskAccountId": 1,
			"targetDirId":   "fid-l1",
			"targetDirName": "牛津树1-9童老师 / L1",
			// 老前端/第三方客户端可能还在传这个字段，不能因此就把归属写进去
			"categoryId": 17,
			"items": []map[string]string{{
				"shareUrl": "https://pan.quark.cn/s/abc",
				"diskType": "fake-transfer",
				"title":    "牛津树（童）L1",
			}},
		})

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/admin/transfer/batch", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.BatchCreate(c)

		if code := respCode(t, w); code != 0 {
			t.Fatalf("批量转存失败: code=%d body=%s", code, w.Body.String())
		}

		var got model.TransferTask
		if err := db.Last(&got).Error; err != nil {
			t.Fatalf("回读任务失败: %v", err)
		}
		if got.TargetDirID != "fid-l1" {
			t.Errorf("target_dir_id = %q，期望 fid-l1", got.TargetDirID)
		}
		// 目录名要落库：否则「转存任务」列表里只有一串没意义的 fid，
		// 用户根本没法确认自己选的是哪个目录 —— 这正是目录选不生效那类
		// bug 长期没被发现的原因。
		if got.TargetDirName != "牛津树1-9童老师 / L1" {
			t.Errorf("target_dir_name = %q，期望面包屑", got.TargetDirName)
		}
		if got.CategoryID != 0 {
			t.Errorf("category_id = %d，转存不该写资源归属，期望 0", got.CategoryID)
		}
	})

	// ---- 2) 执行层：即便任务里已被塞了 category_id，也不许造资源 ----
	t.Run("转存成功不建资源记录", func(t *testing.T) {
		db := newTransferTestDB(t)
		enc := mustEncryptor(t)

		cookie, err := enc.Encrypt("fake-cookie")
		if err != nil {
			t.Fatalf("加密失败: %v", err)
		}
		acc := model.DiskAccount{DiskType: "fake-transfer", Nickname: "测试账号", CookieEncrypted: cookie}
		mustCreate(t, db, &acc)

		// 故意带上 category_id，模拟"历史遗留的调用方"。
		tk := model.TransferTask{
			Title: "牛津树（童）L1", DiskType: "fake-transfer",
			ShareURL: "https://pan.quark.cn/s/abc", DiskAccountID: acc.ID,
			TargetDirID: "fid-l1", TargetDirName: "牛津树1-9童老师 / L1",
			CategoryID: 17, Status: model.TaskStatusPending,
		}
		mustCreate(t, db, &tk)

		h := NewTransferHandler(db, enc, task.NewWorkerPool(1, 10))
		if err := h.processTransfer(context.Background(), tk.ID); err != nil {
			t.Fatalf("转存执行失败: %v", err)
		}

		var after model.TransferTask
		if err := db.First(&after, tk.ID).Error; err != nil {
			t.Fatalf("回读任务失败: %v", err)
		}
		if after.Status != model.TaskStatusSuccess {
			t.Fatalf("任务状态 = %q，期望 success（message=%s）", after.Status, after.Message)
		}
		if after.CreatedResourceID != nil {
			t.Errorf("created_resource_id 应为空，实际 %d", *after.CreatedResourceID)
		}

		var resCount int64
		db.Model(&model.Resource{}).Count(&resCount)
		if resCount != 0 {
			t.Errorf("转存不该登记资源，实际写了 %d 条", resCount)
		}
	})
}

// fakeTransferAdapter 只实现转存链路要用到的两个方法，其余一律 ErrNotImplemented。
//
// 用假适配器而不是真夸克：这条测试要锁的是**职责边界**，不是网盘协议。
// 真适配器会把网络抖动带进来，而边界一旦失守是静默的（多写一条资源），
// 更需要一条断网也能跑的测试盯着。
type fakeTransferAdapter struct{}

func (fakeTransferAdapter) Type() adapter.DiskType { return "fake-transfer" }
func (fakeTransferAdapter) Name() string           { return "测试网盘" }

func (fakeTransferAdapter) GetQRCode(context.Context) (*adapter.QRCode, error) {
	return nil, adapter.ErrNotImplemented
}
func (fakeTransferAdapter) PollQRCode(context.Context, string) (*adapter.LoginResult, error) {
	return nil, adapter.ErrNotImplemented
}
func (fakeTransferAdapter) ValidateCookie(context.Context, *adapter.Credential) (*adapter.AccountInfo, error) {
	return &adapter.AccountInfo{Nickname: "测试账号"}, nil
}
func (fakeTransferAdapter) ListDir(context.Context, *adapter.Credential, string) ([]adapter.FileItem, error) {
	return nil, adapter.ErrNotImplemented
}
func (fakeTransferAdapter) MakeDir(context.Context, *adapter.Credential, string, string) (string, error) {
	return "", adapter.ErrNotImplemented
}
func (fakeTransferAdapter) DeleteFile(context.Context, *adapter.Credential, []string) error {
	return adapter.ErrNotImplemented
}
func (fakeTransferAdapter) Rename(context.Context, *adapter.Credential, string, string) error {
	return adapter.ErrNotImplemented
}
func (fakeTransferAdapter) ParseShare(context.Context, *adapter.Credential, string, string) (*adapter.ShareInfo, error) {
	return &adapter.ShareInfo{Title: "牛津树（童）L1", RootID: "root-1"}, nil
}
func (fakeTransferAdapter) SaveToDir(context.Context, *adapter.Credential, *adapter.ShareInfo, string) (*adapter.SaveResult, error) {
	return &adapter.SaveResult{Count: 9, Message: "已转存 9 个文件"}, nil
}
func (fakeTransferAdapter) Transfer(context.Context, *adapter.Credential, string, string, string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}
func (fakeTransferAdapter) GetDirectLink(context.Context, *adapter.Credential, string) (*adapter.DirectLink, error) {
	return nil, adapter.ErrNotImplemented
}
func (fakeTransferAdapter) GetDownloadInfo(context.Context, *adapter.Credential, string) (*adapter.DirectLink, error) {
	return nil, adapter.ErrNotImplemented
}

func init() { adapter.Register(fakeTransferAdapter{}) }

func mustEncryptor(t *testing.T) *util.Encryptor {
	t.Helper()
	enc, err := util.NewEncryptor("test-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return enc
}

func newTransferTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:transfer_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.DiskAccount{}, &model.TransferTask{}, &model.Resource{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}
