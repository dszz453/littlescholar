package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 知识沉淀这组测试守四件事：
//
//  1. 统计范围要含子孙目录 —— 孩子是从一级目录点进来的，父目录没沉淀
//     等于没有。
//  2. 已软删的资源不能算进数量 —— 否则会出现「显示 465 个、点进去 0 个」，
//     这个坑本项目在搜索结果上已经踩过一次。
//  3. 内容没变就不能重复调 AI —— 每天跑一轮，额度是真金白银。
//  4. 范围变空时把沉淀**硬删**掉 —— 软删的行仍占着 category_id 唯一键，
//     之后内容回来时 upsert 会更新那条 deleted_at 非空的行，
//     结果"写进去了但永远读不出来"。

func newDigestTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:digest_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.UserCategoryGrant{}, &model.Category{},
		&model.Resource{}, &model.KnowledgeDigest{}, &model.AIConfig{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// fakeAI 一个只说固定台词的 OpenAI 兼容服务，并记录被调用了几次。
type fakeAI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   int
	reply   string
	lastReq string
}

func newFakeAI(t *testing.T) *fakeAI {
	t.Helper()
	f := &fakeAI{
		reply: `{"intro":"这里有整套牛津树分级读物，适合英语启蒙的孩子按级读下去。",` +
			`"points":["从第 1 级开始，每级读熟再往上"],"keywords":["牛津树","分级阅读"]}`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls++
		f.lastReq = string(raw)
		reply := f.reply
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": reply}},
			},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// seedDigestAI 把 fakeAI 注册成默认 AI 配置。
func seedDigestAI(t *testing.T, db *gorm.DB, enc *util.Encryptor, baseURL string) {
	t.Helper()
	key, err := enc.Encrypt("sk-test")
	if err != nil {
		t.Fatalf("加密测试密钥失败: %v", err)
	}
	mustCreate(t, db, &model.AIConfig{
		Name: "测试 AI", BaseURL: baseURL, APIKeyEncrypted: key,
		Model: "test-model", IsDefault: true, TimeoutSec: 10,
	})
}

func newDigestHandlerForTest(t *testing.T, db *gorm.DB, baseURL string) *DigestHandler {
	t.Helper()
	enc, err := util.NewEncryptor("digest-test-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	if baseURL != "" {
		seedDigestAI(t, db, enc, baseURL)
	}
	aiH := NewAIHandler(db, enc)
	return NewDigestHandler(db, service.NewPermissionService(db), aiH)
}

// seedDigestTree 建两级目录：乐学英语 → 牛津树英语，各挂若干资源。
func seedDigestTree(t *testing.T, db *gorm.DB, childTitles ...string) (root, child model.Category) {
	t.Helper()
	root = model.Category{Name: "乐学英语", IsPublic: true, Level: 1}
	mustCreate(t, db, &root)
	root.Path = fmt.Sprintf("/%d/", root.ID)
	db.Model(&model.Category{}).Where("id = ?", root.ID).Update("path", root.Path)

	child = model.Category{Name: "牛津树英语", ParentID: &root.ID, Level: 2}
	mustCreate(t, db, &child)
	child.Path = root.Path + fmt.Sprintf("%d/", child.ID)
	db.Model(&model.Category{}).Where("id = ?", child.ID).Update("path", child.Path)

	for _, title := range childTitles {
		mustCreate(t, db, &model.Resource{
			CategoryID: child.ID, Title: title, MediaType: model.MediaTypeAudio,
		})
	}
	return
}

func TestDigestScopeIDsCoversDescendants(t *testing.T) {
	db := newDigestTestDB(t)
	root, child := seedDigestTree(t, db, "第 1 集")

	h := newDigestHandlerForTest(t, db, "")
	ids, all := h.digestScopeIDs(&root)
	if all {
		t.Fatal("普通目录不应被当成全站范围")
	}
	if !containsID(ids, root.ID) || !containsID(ids, child.ID) {
		t.Fatalf("范围 %v 应同时包含父目录 %d 与子目录 %d", ids, root.ID, child.ID)
	}
	if n, err := h.countInScope(ids, all); err != nil || n != 1 {
		t.Fatalf("父目录范围应统计到子目录里的 1 个资源，得到 n=%d err=%v", n, err)
	}

	if _, all := h.digestScopeIDs(nil); !all {
		t.Fatal("nil 目录应表示全站范围")
	}
}

func TestDigestCountIgnoresSoftDeleted(t *testing.T) {
	db := newDigestTestDB(t)
	_, child := seedDigestTree(t, db, "第 1 集", "第 2 集")
	h := newDigestHandlerForTest(t, db, "")

	var res model.Resource
	if err := db.Where("category_id = ?", child.ID).First(&res).Error; err != nil {
		t.Fatalf("取测试资源失败: %v", err)
	}
	if err := db.Delete(&model.Resource{}, res.ID).Error; err != nil {
		t.Fatalf("软删资源失败: %v", err)
	}

	ids, all := h.digestScopeIDs(&child)
	n, err := h.countInScope(ids, all)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("软删资源不应计入，期望 1，得到 %d（Table() 绕过软删会得到 2）", n)
	}
}

func TestDigestGenerateThenSkipUnchanged(t *testing.T) {
	db := newDigestTestDB(t)
	_, child := seedDigestTree(t, db, "第 1 集", "第 2 集")
	fake := newFakeAI(t)
	h := newDigestHandlerForTest(t, db, fake.srv.URL)

	digest, generated, err := h.Generate(child.ID, false)
	if err != nil {
		t.Fatalf("首次生成失败: %v", err)
	}
	if !generated || digest == nil {
		t.Fatal("首次生成应产生新沉淀")
	}
	if digest.Intro == "" || len(digest.Points) != 1 {
		t.Fatalf("沉淀内容不完整: %+v", digest)
	}
	if digest.Status != model.DigestStatusReady {
		t.Fatalf("状态应为 ready，得到 %q", digest.Status)
	}
	if digest.CategoryName != "牛津树英语" {
		t.Fatalf("目录名落库错误: %q", digest.CategoryName)
	}
	if n := fake.callCount(); n != 1 {
		t.Fatalf("首次生成应调用 AI 1 次，实际 %d", n)
	}

	// 第二次：内容没变 → 不调 AI、不重算
	_, generated2, err := h.Generate(child.ID, false)
	if err != nil {
		t.Fatalf("第二次生成报错: %v", err)
	}
	if generated2 {
		t.Fatal("内容没变时不应重新生成")
	}
	if n := fake.callCount(); n != 1 {
		t.Fatalf("内容没变时不应再次调用 AI，实际调用 %d 次", n)
	}

	// 内容变了 → 必须重算
	mustCreate(t, db, &model.Resource{CategoryID: child.ID, Title: "第 3 集"})
	if _, generated3, err := h.Generate(child.ID, false); err != nil || !generated3 {
		t.Fatalf("内容变化后应重新生成，generated=%v err=%v", generated3, err)
	}
	if n := fake.callCount(); n != 2 {
		t.Fatalf("内容变化后应再调用一次 AI，实际 %d", n)
	}
}

func TestDigestHardDeletesWhenScopeBecomesEmpty(t *testing.T) {
	db := newDigestTestDB(t)
	_, child := seedDigestTree(t, db, "第 1 集")
	fake := newFakeAI(t)
	h := newDigestHandlerForTest(t, db, fake.srv.URL)

	if _, generated, err := h.Generate(child.ID, false); err != nil || !generated {
		t.Fatalf("准备数据失败: generated=%v err=%v", generated, err)
	}

	// 把这个目录下的资源全部软删 → 范围变空
	if err := db.Where("category_id = ?", child.ID).Delete(&model.Resource{}).Error; err != nil {
		t.Fatalf("软删资源失败: %v", err)
	}
	if _, generated, err := h.Generate(child.ID, false); err != nil || generated {
		t.Fatalf("范围变空时不应生成: generated=%v err=%v", generated, err)
	}

	// 必须是硬删：软删的行还占着 category_id 唯一键
	var left int64
	db.Unscoped().Model(&model.KnowledgeDigest{}).
		Where("category_id = ?", child.ID).Count(&left)
	if left != 0 {
		t.Fatalf("范围变空后沉淀应被硬删，仍剩 %d 行（软删会让后续 upsert 写入不可见的记录）", left)
	}

	// 内容回来 → 必须能重新生成并且**查得到**
	mustCreate(t, db, &model.Resource{CategoryID: child.ID, Title: "第 1 集"})
	if _, generated, err := h.Generate(child.ID, false); err != nil || !generated {
		t.Fatalf("内容恢复后应重新生成: generated=%v err=%v", generated, err)
	}
	var got model.KnowledgeDigest
	if err := db.Where("category_id = ?", child.ID).First(&got).Error; err != nil {
		t.Fatalf("重新生成的沉淀查不到（典型的软删占用唯一键问题）: %v", err)
	}
}

func TestDigestFingerprintTracksInput(t *testing.T) {
	base := digestFingerprint(2, []string{"a"}, []string{"第 1 集", "第 2 集"})
	if got := digestFingerprint(2, []string{"a"}, []string{"第 1 集", "第 2 集"}); got != base {
		t.Fatal("同样的输入应得到同样的指纹")
	}
	if got := digestFingerprint(3, []string{"a"}, []string{"第 1 集", "第 2 集"}); got == base {
		t.Fatal("资源数变化时指纹必须变")
	}
	if got := digestFingerprint(2, []string{"a"}, []string{"第 1 集", "第 3 集"}); got == base {
		t.Fatal("标题变化时指纹必须变")
	}
	if got := digestFingerprint(2, []string{"a", "b"}, []string{"第 1 集", "第 2 集"}); got == base {
		t.Fatal("下级目录变化时指纹必须变")
	}
}

func TestDigestRoundTargetsPropagatesToAncestors(t *testing.T) {
	db := newDigestTestDB(t)
	root, child := seedDigestTree(t, db, "第 1 集")
	_ = child
	h := newDigestHandlerForTest(t, db, "")

	targets := h.roundTargets(20)
	if len(targets) == 0 || targets[0] != model.DigestScopeAll {
		t.Fatalf("全站总览应排在第一位，得到 %v", targets)
	}
	if !containsID(targets, root.ID) {
		t.Fatalf("父目录的子孙有资源时也应成为目标，得到 %v", targets)
	}
}

func TestDigestGetHiddenForInaccessibleCategory(t *testing.T) {
	db := newDigestTestDB(t)
	_, child := seedDigestTree(t, db, "第 1 集")
	fake := newFakeAI(t)
	h := newDigestHandlerForTest(t, db, fake.srv.URL)
	if _, _, err := h.Generate(child.ID, false); err != nil {
		t.Fatalf("准备沉淀失败: %v", err)
	}

	// 私有化这个目录，未授权的孩子就不该再看到它的沉淀
	db.Model(&model.Category{}).Where("id = ?", child.ID).Update("is_public", false)

	kid := seedKid(t, db)

	rec := callResourceGet(t, kid, "/api/app/digests/1", func(c *gin.Context) {
		setParam(c, "categoryId", fmt.Sprintf("%d", child.ID))
		h.Get(c)
	})
	var body struct {
		Code int         `json:"code"`
		Data interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body.Code != 0 {
		t.Fatalf("业务码应为 0，得到 %d", body.Code)
	}
	if body.Data != nil {
		t.Fatalf("未授权目录的沉淀应返回 null，得到 %v", body.Data)
	}

	// 管理员不受限
	admin := seedAdmin(t, db)
	rec2 := callResourceGet(t, admin, "/api/app/digests/1", func(c *gin.Context) {
		setParam(c, "categoryId", fmt.Sprintf("%d", child.ID))
		h.Get(c)
	})
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatalf("管理员响应不是合法 JSON: %v", err)
	}
	if body.Data == nil {
		t.Fatal("管理员应能看到任何目录的沉淀")
	}
}

// TestDigestPromptCarriesTitles 提示词里必须真的带上内容清单，
// 否则 AI 只能靠目录名瞎猜。
func TestDigestPromptCarriesTitles(t *testing.T) {
	db := newDigestTestDB(t)
	_, child := seedDigestTree(t, db, "牛津树第 1 级", "牛津树第 2 级")
	fake := newFakeAI(t)
	h := newDigestHandlerForTest(t, db, fake.srv.URL)

	if _, _, err := h.Generate(child.ID, false); err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	req := fake.lastReq
	for _, want := range []string{"牛津树英语", "牛津树第 1 级", "牛津树第 2 级"} {
		if !strings.Contains(req, want) {
			t.Fatalf("提示词里缺少 %q:\n%s", want, req)
		}
	}
}

func containsID(ids []uint64, want uint64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestDigestSchedulerRunsOnceAfterFirstDelay 锁住「首轮延迟结束后立刻执行一次」。
//
// 曾经的写法是首轮延迟之后只建 ticker 就进 select。ticker 不会立即触发，
// 要等满一个 interval 才有第一次 tick —— 于是「首轮延迟 8 分钟」实际变成
// 「24 小时 8 分钟」，新部署的实例当天一条沉淀都不会生成，
// 而且日志里连一行输出都没有，看着像功能根本没生效。
//
// 断言方式刻意不依赖真实等待 interval：interval 传 1 小时，
// 那么这段时间内唯一可能的触发源就是"首轮延迟后立即执行"。
func TestDigestSchedulerRunsOnceAfterFirstDelay(t *testing.T) {
	db := newDigestTestDB(t)
	seedDigestTree(t, db, "第 1 集", "第 2 集")

	fake := newFakeAI(t)
	h := newDigestHandlerForTest(t, db, fake.srv.URL)

	stop := h.StartDigestScheduler(zap.NewNop(), time.Hour, 50*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Model(&model.KnowledgeDigest{}).Count(&n).Error; err != nil {
			t.Fatalf("统计沉淀失败: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("首轮延迟已过却一条沉淀都没生成：ticker 语义把首轮执行吃掉了")
}
