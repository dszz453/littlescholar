package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 「设置共享」接口层的测试。
//
// 这一层的错误几乎都是**编排**错误，而不是网盘协议错误：分错组、漏切块、
// 把已经有效的链接又作废一次、失败之后照样写库…… 这些都不需要真网盘
// 就能测出来，而且真网盘反而会把它们盖住（一次调用真的成功了，
// 你根本不知道它是"该调 3 次调了 1 次"还是"该跳过却调了"）。
//
// 所以这里用假适配器，断言全部落在**适配器被怎么调用**和**库里写了什么**上。

// ============ 假适配器 ============

type fakeShareCall struct {
	Title      string
	Fids       []string
	Passcode   string
	ExpireDays int
	Cookie     string
}

type fakeShareAdapter struct {
	mu    sync.Mutex
	calls []fakeShareCall
	fail  error

	// failOnce 前 N 次调用返回失败（之后恢复正常）。
	//
	// 专门用来测「一批失败不能让补齐卡在原地」：失败的资源 share_url 仍是空的，
	// 下一批按同样条件还会被选中，不排除就会在同一批上空转。
	failOnce int

	// failFids 只要这一批里含这些 fid 就报错（**永远**失败，不是失败一次）。
	//
	// 用来模拟真实里最常见的那种失败：某个文件已被删掉/被风控，重试多少次
	// 都不会好。配合 failOnce 用能区分"失败后重试成功"和"永远失败"两种情形 ——
	// 后者才是会把补齐卡死的那一类。
	failFids map[string]bool

	// shareFiles 模拟"分享里到底有什么"：shareURL -> fid 集合。
	//
	// 校验接口（Verify）的价值全在于"分享根目录里的 fid 与资源的 fileId
	// 对不上"这种情况。要测它，假适配器就必须**记住**每次分享提交了哪些
	// fid，而不是回一个恒定的列表 —— 恒定的列表会让"对得上"和"对不上"
	// 两种情况都测不出来。
	shareFiles map[string]map[string]bool
	// dropFid 从分享内容里悄悄扣掉一个 fid，用来模拟"看起来建成了、
	// 孩子端却找不到"的故障。
	dropFid string
}

var shareFake = &fakeShareAdapter{}

func (f *fakeShareAdapter) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.fail = nil
	f.failOnce = 0
	f.failFids = map[string]bool{}
	f.shareFiles = map[string]map[string]bool{}
	f.dropFid = ""
}

func (f *fakeShareAdapter) callList() []fakeShareCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeShareCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeShareAdapter) Type() adapter.DiskType { return "fake-share" }
func (f *fakeShareAdapter) Name() string           { return "测试网盘(共享)" }

func (f *fakeShareAdapter) GetQRCode(context.Context) (*adapter.QRCode, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) PollQRCode(context.Context, string) (*adapter.LoginResult, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) ValidateCookie(context.Context, *adapter.Credential) (*adapter.AccountInfo, error) {
	return &adapter.AccountInfo{Nickname: "测试账号"}, nil
}
func (f *fakeShareAdapter) ListDir(context.Context, *adapter.Credential, string) ([]adapter.FileItem, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) MakeDir(context.Context, *adapter.Credential, string, string) (string, error) {
	return "", adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) DeleteFile(context.Context, *adapter.Credential, []string) error {
	return adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) Rename(context.Context, *adapter.Credential, string, string) error {
	return adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) ParseShare(context.Context, *adapter.Credential, string, string) (*adapter.ShareInfo, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) SaveToDir(context.Context, *adapter.Credential, *adapter.ShareInfo, string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) Transfer(context.Context, *adapter.Credential, string, string, string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) GetDirectLink(context.Context, *adapter.Credential, string) (*adapter.DirectLink, error) {
	return nil, adapter.ErrNotImplemented
}
func (f *fakeShareAdapter) GetDownloadInfo(context.Context, *adapter.Credential, string) (*adapter.DirectLink, error) {
	return nil, adapter.ErrNotImplemented
}

func (f *fakeShareAdapter) CreateShare(_ context.Context, cred *adapter.Credential, fids []string, opt adapter.ShareCreateOptions) (*adapter.ShareResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeShareCall{
		Title: opt.Title, Fids: append([]string(nil), fids...),
		Passcode: opt.Passcode, ExpireDays: opt.ExpireDays, Cookie: cred.Cookie,
	})
	if f.fail != nil {
		return nil, f.fail
	}
	if f.failOnce > 0 {
		f.failOnce--
		return nil, fmt.Errorf("模拟这一批创建分享失败")
	}
	for _, id := range fids {
		if f.failFids[id] {
			return nil, fmt.Errorf("模拟这个文件永远建不成分享（fileid=%s）", id)
		}
	}
	n := len(f.calls)
	url := "https://pan.example.com/s/fake-" + strconv.Itoa(n)
	set := make(map[string]bool, len(fids))
	for _, id := range fids {
		set[id] = true
	}
	// 按 ExpireDays 回对应的失效时间：永久=0，其余给一个未来的固定值。
	// 不跟着入参走的话，"重建后是否变成永久"这件事在库里根本验不出来。
	var expireAt int64 = 1800000000
	if opt.ExpireDays == adapter.ShareExpirePermanent {
		expireAt = 0
	}
	f.shareFiles[url] = set
	return &adapter.ShareResult{
		ShareURL: url,
		Passcode: "p" + strconv.Itoa(n) + "w",
		ShareID:  "sid-" + strconv.Itoa(n),
		ExpireAt: expireAt,
	}, nil
}

// ListShareDir 让假适配器具备解析分享内容的能力（ShareBrowser）。
func (f *fakeShareAdapter) ListShareDir(_ context.Context, _ *adapter.Credential, shareURL, _, pdirFid string) (*adapter.ShareEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set := f.shareFiles[shareURL]
	if set == nil {
		return nil, fmt.Errorf("分享不存在")
	}
	out := make([]adapter.FileItem, 0, len(set))
	for id := range set {
		if f.dropFid != "" && id == f.dropFid {
			continue
		}
		out = append(out, adapter.FileItem{ID: id, Name: "f-" + id})
	}
	return &adapter.ShareEntry{Fid: pdirFid, Files: out}, nil
}

func init() { adapter.Register(shareFake) }

// ============ 测试脚手架 ============

func newShareTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	testDBCounter++
	dsn := fmt.Sprintf("file:share_test_%d?mode=memory&cache=shared", testDBCounter)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Resource{}, &model.DiskAccount{},
		&model.ResourceMapping{}, &model.Category{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return db
}

// seedQuarkAccount 建一个可解密的网盘账号。
//
// 必须是**真加密**的 cookie：ShareHandler 会走 Encryptor.Decrypt，
// 塞个明文进去会得到一个"凭据解密失败"，测出来的就不是编排逻辑了。
func seedQuarkAccount(t *testing.T, db *gorm.DB, diskType string) model.DiskAccount {
	t.Helper()
	enc, err := util.NewEncryptor("test-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	ct, err := enc.Encrypt("__puus=secret-cookie")
	if err != nil {
		t.Fatalf("加密 cookie 失败: %v", err)
	}
	acc := model.DiskAccount{DiskType: diskType, Nickname: "家长夸克", CookieEncrypted: ct}
	mustCreate(t, db, &acc)
	return acc
}

func newShareHandler(t *testing.T, db *gorm.DB) *ShareHandler {
	t.Helper()
	enc, err := util.NewEncryptor("test-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	shareFake.reset()
	return NewShareHandler(db, enc)
}

// callShare 打一次 POST /api/admin/shares。
func callShare(t *testing.T, h *ShareHandler, body map[string]interface{}, query string) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/admin/shares"+query, bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Create(c)

	var env struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return w, env.Data
}

func callShareClear(t *testing.T, h *ShareHandler, query string) map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/api/admin/shares"+query, nil)
	h.Clear(c)

	var env struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return env.Data
}

func numOf(t *testing.T, m map[string]interface{}, key string) int {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("响应里没有 %q: %v", key, m)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%q 不是数字: %#v", key, v)
	}
	return int(f)
}

// reloadResources 按 id 顺序回读资源。
func reloadResources(t *testing.T, db *gorm.DB, ids ...uint64) []model.Resource {
	t.Helper()
	var out []model.Resource
	if err := db.Where("id IN ?", ids).Order("id").Find(&out).Error; err != nil {
		t.Fatalf("回读资源失败: %v", err)
	}
	return out
}

// callShareVerify 打一次 POST /api/admin/shares/verify。
func callShareVerify(t *testing.T, h *ShareHandler, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/admin/shares/verify", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Verify(c)

	var env struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return env.Data
}

// ============ 用例 ============

// TestShareMappingCoversAllResources 一条映射下的资源应当**一次**分享完。
//
// 这是本设计最核心的一条：一次 /share 调用带上全部 fid，而不是每条资源
// 一次调用。逐条调用不仅会撞限流，还会在用户网盘的"我的分享"里堆出
// 几十条孤立链接。
func TestShareMappingCoversAllResources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	m := model.ResourceMapping{Name: "牛津树-夸克", AccountID: acc.ID, CategoryID: 1}
	mustCreate(t, db, &m)
	var ids []uint64
	for i := 0; i < 3; i++ {
		r := model.Resource{
			Title: fmt.Sprintf("牛津树%02d", i), CategoryID: 1, DiskType: "fake-share",
			DiskAccountID: &acc.ID, FileID: fmt.Sprintf("fid-%d", i), MappingID: m.ID,
		}
		mustCreate(t, db, &r)
		ids = append(ids, r.ID)
	}

	_, data := callShare(t, h, map[string]interface{}{"mappingId": m.ID}, "")
	if got := numOf(t, data, "created"); got != 3 {
		t.Fatalf("created = %d，期望 3（body=%v）", got, data)
	}
	if got := numOf(t, data, "failed"); got != 0 {
		t.Fatalf("failed = %d，期望 0（%v）", got, data["failures"])
	}

	calls := shareFake.callList()
	if len(calls) != 1 {
		t.Fatalf("适配器被调用 %d 次，期望 1 次（整批一次）", len(calls))
	}
	if len(calls[0].Fids) != 3 {
		t.Errorf("一次带着 %d 个 fid，期望 3 个：%v", len(calls[0].Fids), calls[0].Fids)
	}
	if calls[0].Cookie == "" {
		t.Error("传给适配器的凭据是空的 —— 说明账号 cookie 没有解密成功")
	}

	for _, r := range reloadResources(t, db, ids...) {
		if r.ShareURL == "" {
			t.Errorf("资源 %d(%s) 没写 share_url", r.ID, r.Title)
		}
		if r.Passcode == "" {
			t.Errorf("资源 %d(%s) 没写 passcode", r.ID, r.Title)
		}
		if r.ShareID == "" {
			t.Errorf("资源 %d(%s) 没写 share_id", r.ID, r.Title)
		}
	}
}

// TestShareOnlyFillsGaps 默认只补缺：已经有分享的**一个字段都不动**。
//
// 理由是可逆性：老链接可能已经发给过别人、或已经被孩子端转过存，
// 每次点按钮都把它重建一遍，等于把别人手上的东西悄悄作废。
func TestShareOnlyFillsGaps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	old := model.Resource{
		Title: "已经共享过的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-old",
		ShareURL: "https://pan.example.com/s/old", Passcode: "old1", ShareID: "sid-old",
	}
	mustCreate(t, db, &old)
	fresh := model.Resource{
		Title: "还没共享的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-new",
	}
	mustCreate(t, db, &fresh)

	_, data := callShare(t, h, map[string]interface{}{"mappingId": 0, "resourceIds": []uint64{old.ID, fresh.ID}}, "")
	if got := numOf(t, data, "created"); got != 1 {
		t.Fatalf("created = %d，期望 1", got)
	}
	if got := numOf(t, data, "skipped"); got != 1 {
		t.Fatalf("skipped = %d，期望 1", got)
	}

	calls := shareFake.callList()
	if len(calls) != 1 || len(calls[0].Fids) != 1 || calls[0].Fids[0] != "fid-new" {
		t.Fatalf("适配器应只为新资源调用一次，实际 %+v", calls)
	}

	got := reloadResources(t, db, old.ID, fresh.ID)
	if got[0].ShareURL != "https://pan.example.com/s/old" || got[0].Passcode != "old1" {
		t.Errorf("已有分享被改写了：%+v", got[0])
	}
	if got[1].ShareURL == "" {
		t.Error("新资源没有拿到分享")
	}
}

// TestShareForceRebuilds force=1 时才重建已经共享过的。
func TestShareForceRebuilds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	old := model.Resource{
		Title: "换一条新的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-old",
		ShareURL: "https://pan.example.com/s/old", Passcode: "old1",
	}
	mustCreate(t, db, &old)

	_, data := callShare(t, h, map[string]interface{}{
		"resourceId": old.ID, "force": true, "passcode": "k3m9", "expireDays": adapter.ShareExpirePermanent,
	}, "")
	if got := numOf(t, data, "created"); got != 1 {
		t.Fatalf("created = %d，期望 1", got)
	}

	calls := shareFake.callList()
	if len(calls) != 1 {
		t.Fatalf("适配器调用次数 = %d", len(calls))
	}
	if calls[0].Passcode != "k3m9" {
		t.Errorf("传给适配器的提取码 = %q，期望请求里指定的 k3m9", calls[0].Passcode)
	}
	if calls[0].ExpireDays != adapter.ShareExpirePermanent {
		t.Errorf("有效期 = %d，期望透传 %d", calls[0].ExpireDays, adapter.ShareExpirePermanent)
	}
	got := reloadResources(t, db, old.ID)[0]
	if got.ShareURL == "https://pan.example.com/s/old" {
		t.Error("force 下应当写入新链接，实际还是老的")
	}
}

// TestShareChunksLargeSets 大目录必须切块，且一块都不能漏。
func TestShareChunksLargeSets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	const total = shareChunkSize*2 + 7
	var ids []uint64
	for i := 0; i < total; i++ {
		r := model.Resource{
			Title: fmt.Sprintf("第%03d集", i), CategoryID: 1, DiskType: "fake-share",
			DiskAccountID: &acc.ID, FileID: fmt.Sprintf("fid-%03d", i),
		}
		mustCreate(t, db, &r)
		ids = append(ids, r.ID)
	}

	_, data := callShare(t, h, map[string]interface{}{"resourceIds": ids}, "")
	if got := numOf(t, data, "created"); got != total {
		t.Fatalf("created = %d，期望 %d（%v）", got, total, data["failures"])
	}

	calls := shareFake.callList()
	if len(calls) != 3 {
		t.Fatalf("适配器调用次数 = %d，期望 3 块", len(calls))
	}
	wantSizes := []int{shareChunkSize, shareChunkSize, 7}
	for i, want := range wantSizes {
		if len(calls[i].Fids) != want {
			t.Errorf("第 %d 块带着 %d 个 fid，期望 %d", i, len(calls[i].Fids), want)
		}
	}

	var missing int64
	db.Model(&model.Resource{}).Where("id IN ? AND (share_url IS NULL OR share_url = '')", ids).Count(&missing)
	if missing != 0 {
		t.Errorf("还有 %d 条资源没拿到分享", missing)
	}
}

// TestShareDryRunTouchesNothing 预览不调用网盘、也不写库。
func TestShareDryRunTouchesNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	var ids []uint64
	for i := 0; i < 4; i++ {
		r := model.Resource{
			Title: fmt.Sprintf("预览%02d", i), CategoryID: 1, DiskType: "fake-share",
			DiskAccountID: &acc.ID, FileID: fmt.Sprintf("fid-%d", i),
		}
		mustCreate(t, db, &r)
		ids = append(ids, r.ID)
	}

	_, data := callShare(t, h, map[string]interface{}{"resourceIds": ids}, "?dryRun=1")
	if got := numOf(t, data, "created"); got != 0 {
		t.Errorf("预览却报了 created=%d", got)
	}
	if len(shareFake.callList()) != 0 {
		t.Error("预览不该调用网盘接口")
	}
	var still int64
	db.Model(&model.Resource{}).Where("id IN ? AND (share_url IS NULL OR share_url = '')", ids).Count(&still)
	if int(still) != len(ids) {
		t.Errorf("预览却改了 %d 条资源", len(ids)-int(still))
	}

	// 预览要能回答"这次会做多少" —— 否则它就没用了。
	groups, ok := data["groups"].([]interface{})
	if !ok || len(groups) != 1 {
		t.Fatalf("groups 形态不对: %#v", data["groups"])
	}
	g0, _ := groups[0].(map[string]interface{})
	if planned := int(g0["planned"].(float64)); planned != len(ids) {
		t.Errorf("预览 planned = %d，期望 %d", planned, len(ids))
	}
}

// TestShareFailureWritesNothing 网盘失败时**不许**留下半条分享信息。
//
// 库里只要写进一条 share_url，后台页面就会显示"已共享"，
// 而孩子端一到转存就说链接失效 —— 故障被推迟到完全没有上下文的地方。
func TestShareFailureWritesNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")
	shareFake.fail = fmt.Errorf("夸克网盘空间不足，请清理空间后重试")

	r := model.Resource{
		Title: "会失败的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-x",
	}
	mustCreate(t, db, &r)

	_, data := callShare(t, h, map[string]interface{}{"resourceId": r.ID}, "")
	if got := numOf(t, data, "failed"); got != 1 {
		t.Fatalf("failed = %d，期望 1", got)
	}
	got := reloadResources(t, db, r.ID)[0]
	if got.ShareURL != "" || got.Passcode != "" {
		t.Errorf("失败却写了库：share_url=%q passcode=%q", got.ShareURL, got.Passcode)
	}
	fails, _ := data["failures"].([]interface{})
	if len(fails) != 1 {
		t.Fatalf("failures 应有 1 条: %#v", data["failures"])
	}
	f0, _ := fails[0].(map[string]interface{})
	if msg, _ := f0["error"].(string); !strings.Contains(msg, "空间不足") {
		t.Errorf("失败原因没透出来: %#v", f0)
	}
}

// TestShareRejectsStorageTypes WebDAV / 本地硬盘资源要被挡下并说明原因。
//
// 这几类资源在后台里看着和网盘资源一样，用户点"设置共享"只会得到
// 一句"不支持的网盘类型"，而真正该说的是"孩子端 PRO 只支持网盘转存"。
func TestShareRejectsStorageTypes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, string(adapter.DiskWebdav))

	local := model.Resource{
		Title: "NAS上的动画", CategoryID: 1, DiskType: string(adapter.DiskWebdav),
		DiskAccountID: &acc.ID, FileID: "/动画/汪汪队/01.mp4",
	}
	mustCreate(t, db, &local)

	_, data := callShare(t, h, map[string]interface{}{"resourceId": local.ID}, "")
	if got := numOf(t, data, "failed"); got != 1 {
		t.Fatalf("failed = %d，期望 1", got)
	}
	if len(shareFake.callList()) != 0 {
		t.Error("存储源不该被送进创建分享")
	}
	fails, _ := data["failures"].([]interface{})
	f0, _ := fails[0].(map[string]interface{})
	msg, _ := f0["error"].(string)
	if !strings.Contains(msg, "无法创建分享") {
		t.Errorf("错误信息应说明这类资源不支持分享，实际: %q", msg)
	}
}

// TestShareRejectsMissingFileID 手工收录的资源没有 fileId，要给出可行动的提示。
func TestShareRejectsMissingFileID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	r := model.Resource{
		Title: "手工收录的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "",
	}
	mustCreate(t, db, &r)

	_, data := callShare(t, h, map[string]interface{}{"resourceId": r.ID}, "")
	if got := numOf(t, data, "failed"); got != 1 {
		t.Fatalf("failed = %d，期望 1", got)
	}
	fails, _ := data["failures"].([]interface{})
	f0, _ := fails[0].(map[string]interface{})
	if msg, _ := f0["error"].(string); !strings.Contains(msg, "文件 ID") {
		t.Errorf("提示应指向 fileId 缺失，实际: %q", msg)
	}
}

// TestShareRequiresTarget 不指定对象时必须拒绝，不能默默地对全库动手。
func TestShareRequiresTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	seedQuarkAccount(t, db, "fake-share")

	w, _ := callShare(t, h, map[string]interface{}{}, "")
	if code := respCode(t, w); code == 0 {
		t.Fatal("不指定目标时应当报错")
	}
	if len(shareFake.callList()) != 0 {
		t.Error("参数不合法却调用了网盘")
	}
}

// TestShareVerifyFindsFids 校验：分享里能按 fileId 找到 → 全通过。
//
// 这条锁的是孩子端加速链路上最关键、也最看不见的假设：
// PRO 解析分享后是**按 fileId**在分享根目录里挑文件的
// （见 littlescholar-edge 的 pickFiles）。分享建成、链接能开、提取码也对，
// 但分享里的 fid 与资源的 fileId 对不上时，孩子端只会说
// "分享里没有找到指定的文件" —— 后台一切正常，家长无从下手。
func TestShareVerifyFindsFids(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	m := model.ResourceMapping{Name: "牛津树", AccountID: acc.ID, CategoryID: 1}
	mustCreate(t, db, &m)
	for i := 0; i < 4; i++ {
		r := model.Resource{
			Title: fmt.Sprintf("第%d集", i), CategoryID: 1, DiskType: "fake-share",
			DiskAccountID: &acc.ID, FileID: fmt.Sprintf("fid-%d", i), MappingID: m.ID,
		}
		mustCreate(t, db, &r)
	}

	// 还没共享就校验：应当如实报告"未共享"，而不是报一堆解析失败。
	before := callShareVerify(t, h, map[string]interface{}{"mappingId": m.ID})
	if got := numOf(t, before, "unshared"); got != 4 {
		t.Errorf("unshared = %d，期望 4", got)
	}

	if _, data := callShare(t, h, map[string]interface{}{"mappingId": m.ID}, ""); numOf(t, data, "created") != 4 {
		t.Fatalf("创建分享失败: %v", data)
	}

	after := callShareVerify(t, h, map[string]interface{}{"mappingId": m.ID})
	if got := numOf(t, after, "found"); got != 4 {
		t.Errorf("found = %d，期望 4（%v）", got, after["items"])
	}
	if got := numOf(t, after, "missing"); got != 0 {
		t.Errorf("missing = %d，期望 0", got)
	}
}

// TestShareVerifyCatchesFidMismatch 分享里的 fid 少了 → 必须报出来。
//
// 这是"分享建成但孩子端用不了"的唯一一种形态，也是本接口存在的理由。
func TestShareVerifyCatchesFidMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	r := model.Resource{
		Title: "对不上的那条", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-x",
	}
	mustCreate(t, db, &r)

	if _, data := callShare(t, h, map[string]interface{}{"resourceId": r.ID}, ""); numOf(t, data, "created") != 1 {
		t.Fatalf("创建分享失败: %v", data)
	}

	// 模拟网盘实际返回的分享内容里没有这个 fid
	shareFake.mu.Lock()
	shareFake.dropFid = "fid-x"
	shareFake.mu.Unlock()

	got := callShareVerify(t, h, map[string]interface{}{"resourceId": r.ID})
	if n := numOf(t, got, "missing"); n != 1 {
		t.Fatalf("missing = %d，期望 1（items=%v）", n, got["items"])
	}
	items, _ := got["items"].([]interface{})
	i0, _ := items[0].(map[string]interface{})
	note, _ := i0["note"].(string)
	if !strings.Contains(note, "找不到") {
		t.Errorf("提示应说明分享里找不到该文件 ID，实际: %q", note)
	}
}

// TestShareVerifyReportsParseFailure 解析失败要如实说明，不能当成"通过"。
func TestShareVerifyReportsParseFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	r := model.Resource{
		Title: "链接已失效的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-y",
		// 手写一条假链接：假适配器里没有它的记录 → ListShareDir 回"分享不存在"
		ShareURL: "https://pan.example.com/s/gone", Passcode: "zzzz",
	}
	mustCreate(t, db, &r)

	got := callShareVerify(t, h, map[string]interface{}{"resourceId": r.ID})
	if n := numOf(t, got, "found"); n != 0 {
		t.Fatalf("found = %d，解析失败却算通过了", n)
	}
	items, _ := got["items"].([]interface{})
	i0, _ := items[0].(map[string]interface{})
	if note, _ := i0["note"].(string); !strings.Contains(note, "解析分享失败") {
		t.Errorf("提示应写明解析失败，实际: %q", note)
	}
}

// TestShareClearOnlyAffectsGiven 清除只动指定范围内、且把三个字段一起清空。
//
// 只清 share_url 而留下 passcode 是很典型的"半个状态"：
// 界面上看不出区别，代码里按 passcode 判空的地方却会走错分支。
func TestShareClearOnlyAffectsGiven(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	m := model.ResourceMapping{Name: "映射A", AccountID: acc.ID, CategoryID: 1}
	mustCreate(t, db, &m)

	mk := func(mappingID uint64, title string) uint64 {
		r := model.Resource{
			Title: title, CategoryID: 1, DiskType: "fake-share",
			DiskAccountID: &acc.ID, FileID: "fid-" + title, MappingID: mappingID,
			ShareURL: "https://pan.example.com/s/x", Passcode: "pw", ShareID: "sid",
		}
		mustCreate(t, db, &r)
		return r.ID
	}
	inA := mk(m.ID, "A里的")
	other := mk(0, "手工的")

	data := callShareClear(t, h, "?mappingId="+strconv.FormatUint(m.ID, 10))
	if got := numOf(t, data, "cleared"); got != 1 {
		t.Fatalf("cleared = %d，期望 1", got)
	}

	got := reloadResources(t, db, inA, other)
	if got[0].ShareURL != "" || got[0].Passcode != "" || got[0].ShareID != "" {
		t.Errorf("A 的资源没被清干净: %+v", got[0])
	}
	if got[1].ShareURL == "" {
		t.Error("清 A 的时候把不在范围内的资源也清了")
	}
}

// TestShareClearCountsOnlyShared 没共享过的资源不该被算进 cleared。
//
// Updates 的影响行数是"匹配行数"，空值覆盖空值也算 1 —— 对一批从没共享过的
// 资源点「清除共享」，接口原样回"已清除 3 条"，用户以为清掉了什么。
// 这里钉住"只数本来就有分享信息的"。
func TestShareClearCountsOnlyShared(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newShareTestDB(t)
	h := newShareHandler(t, db)
	acc := seedQuarkAccount(t, db, "fake-share")

	m := model.ResourceMapping{Name: "映射A", AccountID: acc.ID, CategoryID: 1}
	mustCreate(t, db, &m)

	shared := model.Resource{
		Title: "共享过的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-1", MappingID: m.ID,
		ShareURL: "https://pan.example.com/s/x", Passcode: "pw", ShareID: "sid",
	}
	mustCreate(t, db, &shared)
	// 另外两条只有 passcode / 完全为空 —— 都算"没有分享信息"。
	onlyPass := model.Resource{
		Title: "只有码的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-2", MappingID: m.ID, Passcode: "pw",
	}
	mustCreate(t, db, &onlyPass)
	bare := model.Resource{
		Title: "没共享的", CategoryID: 1, DiskType: "fake-share",
		DiskAccountID: &acc.ID, FileID: "fid-3", MappingID: m.ID,
	}
	mustCreate(t, db, &bare)

	// 清除范围覆盖全部三条，但只有两条"有分享信息" → cleared 必须是 2：
	//   · shared（链接+码+shareId）→ 算
	//   · onlyPass（只剩一个码的**残留**）→ 也算，而且确实被清掉了
	//     （这是"链接没了、码还在"那种半截状态，不清反而留隐患）
	//   · bare（三列全空）→ **不算**，这正是要钉住的那一条
	data := callShareClear(t, h, "?mappingId="+strconv.FormatUint(m.ID, 10))
	if got := numOf(t, data, "cleared"); got != 2 {
		t.Fatalf("cleared = %d，期望 2（shared + 只剩码的残留；bare 不算）", got)
	}

	got := reloadResources(t, db, shared.ID, onlyPass.ID, bare.ID)
	if got[0].ShareURL != "" || got[0].Passcode != "" || got[0].ShareID != "" {
		t.Errorf("shared 没被清干净: %+v", got[0])
	}
	if got[1].Passcode != "" {
		t.Errorf("只剩 passcode 的那条也该被清干净: %+v", got[1])
	}

	// 再来一次：这回一条分享信息都没有了，cleared 必须是 0 而不是 3。
	data2 := callShareClear(t, h, "?mappingId="+strconv.FormatUint(m.ID, 10))
	if got := numOf(t, data2, "cleared"); got != 0 {
		t.Fatalf("第二次 cleared = %d，期望 0（三列都空了却还回 3，就是「匹配行数」那个老毛病）", got)
	}
}
