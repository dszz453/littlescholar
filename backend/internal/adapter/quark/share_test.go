package quark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// 这一组测试存在的理由：
//
// 「设置共享」是**完全没法靠返回值自证成功**的一段代码 —— 三步链路里
// 前两步都只回一个 ID，看起来永远是对的；真正决定成败的东西
// （fid_list 传了没有、url_type 是不是"带提取码"那一档、passcode 有没有
// 真的提交上去、回验时用的是不是服务端给的那个码）全都藏在**请求参数**里。
// 所以断言必须落在请求上，只看返回值的话，把 url_type 写成 1（公开链接）
// 这种真错也能一路绿灯。
//
// 用 httptest 而不是打真实接口，也是同一个道理的另一面：driveBase 必须
// 能替换，否则这些"断言请求"的检查会变成对生产环境的随机探针。

// recorded 记录一次收到的请求。
type recorded struct {
	Method string
	Path   string
	Query  map[string]string
	Body   map[string]interface{}
}

// shareStub 一个只认分享三步链路的假夸克服务端。
//
// 每个字段都能被单独改写，用来构造"服务端不按常理出牌"的分支
// （比如自己生成提取码、任务分两次才出 share_id）。
type shareStub struct {
	mu       sync.Mutex
	requests []recorded

	// 行为开关
	CreateCode     int    // /share 的 code，0 = 成功
	CreateMessage  string // /share 的 message
	TaskID         string // /share 回的 task_id，空则直接回 share_id
	ShareIDDirect  string // /share 直接回 share_id（task_id 为空时用）
	TaskCalls      int    // 前 N 次轮询不回 share_id（模拟异步任务还没好）
	ServerPasscode string // /share/password 回的 passcode，空则不回
	TokenCode      int    // sharepage/token 的 code，非 0 表示解锁失败
}

func (s *shareStub) snapshot() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recorded, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *shareStub) record(r *http.Request) map[string]interface{} {
	var body map[string]interface{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	q := map[string]string{}
	for k := range r.URL.Query() {
		q[k] = r.URL.Query().Get(k)
	}
	s.mu.Lock()
	s.requests = append(s.requests, recorded{
		Method: r.Method, Path: r.URL.Path, Query: q, Body: body,
	})
	s.mu.Unlock()
	return body
}

func (s *shareStub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/1/clouddrive/share", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		msg := s.CreateMessage
		if msg == "" {
			msg = "success"
		}
		data := map[string]interface{}{}
		if s.TaskID != "" {
			data["task_id"] = s.TaskID
		}
		if s.ShareIDDirect != "" {
			data["share_id"] = s.ShareIDDirect
		}
		writeJSON(w, 200, map[string]interface{}{
			"status": 200, "code": s.CreateCode, "message": msg, "data": data,
		})
	})

	mux.HandleFunc("/1/clouddrive/task", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		s.mu.Lock()
		calls := s.TaskCalls
		if calls > 0 {
			s.TaskCalls--
		}
		s.mu.Unlock()
		if calls > 0 {
			// 任务在跑：有 data 但没有 share_id
			writeJSON(w, 200, map[string]interface{}{
				"status": 200, "code": 0, "message": "success",
				"data": map[string]interface{}{"status": 1, "message": "处理中"},
			})
			return
		}
		writeJSON(w, 200, map[string]interface{}{
			"status": 200, "code": 0, "message": "success",
			"data": map[string]interface{}{"status": 2, "share_id": "share-abc"},
		})
	})

	mux.HandleFunc("/1/clouddrive/share/password", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		data := map[string]interface{}{
			"share_url": "https://pan.quark.cn/s/deadbeef0011",
		}
		if s.ServerPasscode != "" {
			data["passcode"] = s.ServerPasscode
		}
		writeJSON(w, 200, map[string]interface{}{
			"status": 200, "code": 0, "message": "success", "data": data,
		})
	})

	// 回验走路：getShareToken 打的就是这个端点
	mux.HandleFunc("/1/clouddrive/share/sharepage/token", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if s.TokenCode != 0 {
			writeJSON(w, 200, map[string]interface{}{
				"status": 200, "code": s.TokenCode, "message": "需要提取码",
			})
			return
		}
		writeJSON(w, 200, map[string]interface{}{
			"status": 200, "code": 0, "message": "success",
			"data": map[string]interface{}{"stoken": "stoken-1"},
		})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, httpStatus int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(v)
}

// withStub 起一个假服务端并把 driveBase 指过去。
func withStub(t *testing.T, s *shareStub) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)

	old := driveBase
	driveBase = srv.URL + "/1/clouddrive"
	t.Cleanup(func() { driveBase = old })
}

func testCred() *adapter.Credential {
	return &adapter.Credential{DiskType: adapter.DiskQuark, Cookie: "__puus=test"}
}

// TestCreateShareHappyPath 走通整条链路，并逐个断言三个请求的参数。
func TestCreateShareHappyPath(t *testing.T) {
	s := &shareStub{TaskID: "task-1"}
	withStub(t, s)

	q := New()
	res, err := q.CreateShare(context.Background(), testCred(), []string{"fid-1", "fid-2"}, adapter.ShareCreateOptions{
		Title:      "牛津树英语",
		Passcode:   "k3m9",
		ExpireDays: 30,
	})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if res.ShareURL != "https://pan.quark.cn/s/deadbeef0011" {
		t.Errorf("ShareURL = %q，期望服务端回的那条", res.ShareURL)
	}
	if res.Passcode != "k3m9" {
		t.Errorf("Passcode = %q，期望我们提交的 k3m9", res.Passcode)
	}
	if res.ShareID != "share-abc" {
		t.Errorf("ShareID = %q，期望从 /task 拿到的 share-abc", res.ShareID)
	}
	if res.ExpireAt <= time.Now().Unix() {
		t.Errorf("ExpireAt = %d，期望是未来时刻", res.ExpireAt)
	}

	reqs := s.snapshot()
	if len(reqs) != 4 {
		t.Fatalf("请求数 = %d，期望 4（建分享 / 取任务 / 兑换链接 / 回验）", len(reqs))
	}

	// ① 建分享
	create := reqs[0]
	if create.Path != "/1/clouddrive/share" {
		t.Errorf("第 1 步打到 %s", create.Path)
	}
	if got := create.Body["fid_list"]; got == nil {
		t.Fatal("第 1 步没提交 fid_list —— 没有它服务端不知道分享什么")
	} else {
		list, _ := got.([]interface{})
		if len(list) != 2 || list[0] != "fid-1" || list[1] != "fid-2" {
			t.Errorf("fid_list = %v，期望 [fid-1 fid-2] 且顺序不变", got)
		}
	}
	// url_type=2 才是"带提取码的私密链接"。写成 1 会得到一条公开链接，
	// 而返回值里一样有 share_url，只从结果看完全正常 —— 这正是本测试的重点。
	if got := create.Body["url_type"]; got != float64(2) {
		t.Errorf("url_type = %v，期望 2（私密链接，带提取码）", got)
	}
	if got := create.Body["expired_type"]; got != float64(quarkExpire30Days) {
		t.Errorf("expired_type = %v，期望 %d（30 天）", got, quarkExpire30Days)
	}
	if got := create.Body["passcode"]; got != "k3m9" {
		t.Errorf("passcode = %v，期望提交 k3m9", got)
	}
	if got := create.Body["title"]; got != "牛津树英语" {
		t.Errorf("title = %v", got)
	}
	if create.Query["pr"] != "ucpro" || create.Query["fr"] != "pc" {
		t.Errorf("第 1 步缺少 pr/fr 参数: %v", create.Query)
	}

	// ② 取任务：task_id 必须走 query（写成路径参数会恒 404）
	task := reqs[1]
	if task.Path != "/1/clouddrive/task" {
		t.Errorf("第 2 步打到 %s", task.Path)
	}
	if task.Query["task_id"] != "task-1" {
		t.Errorf("第 2 步 task_id = %q，期望走 query 传 task-1", task.Query["task_id"])
	}

	// ③ 兑换链接：只传 share_id，不该再传一遍 passcode
	pw := reqs[2]
	if pw.Path != "/1/clouddrive/share/password" {
		t.Errorf("第 3 步打到 %s", pw.Path)
	}
	if pw.Body["share_id"] != "share-abc" {
		t.Errorf("第 3 步 share_id = %v", pw.Body["share_id"])
	}
	if _, ok := pw.Body["passcode"]; ok {
		t.Error("第 3 步重复提交了 passcode —— 提取码只在第 1 步定，两处传同一个值迟早会对不上")
	}

	// ④ 回验：必须带我们即将写进库的那个提取码
	v := reqs[3]
	if v.Path != "/1/clouddrive/share/sharepage/token" {
		t.Errorf("第 4 步打到 %s", v.Path)
	}
	if v.Body["pwd_id"] != "deadbeef0011" {
		t.Errorf("回验 pwd_id = %v，期望从 share_url 里提出来的 deadbeef0011", v.Body["pwd_id"])
	}
	if v.Body["passcode"] != "k3m9" {
		t.Errorf("回验 passcode = %v，期望 k3m9", v.Body["passcode"])
	}
}

// TestCreateShareUsesServerPasscode 服务端自己生成提取码时，必须存服务端那个。
//
// 参考实现里明确写着私密链接的"提取码由服务端自动生成后返回"。
// 如果我们只把自己提交的值写进库，后台显示"已共享"、孩子端一转存
// 却报提取码错误 —— 而且从后台页面完全看不出来。
func TestCreateShareUsesServerPasscode(t *testing.T) {
	s := &shareStub{TaskID: "task-1", ServerPasscode: "Zx7q"}
	withStub(t, s)

	res, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"},
		adapter.ShareCreateOptions{Passcode: "k3m9", ExpireDays: 30})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if res.Passcode != "Zx7q" {
		t.Fatalf("Passcode = %q，期望以服务端返回的 Zx7q 为准", res.Passcode)
	}
	// 回验必须用**服务端那个**，而不是我们提交的那个 —— 否则这条回验
	// 只是在验证一个没人会用的码，等于没验。
	reqs := s.snapshot()
	last := reqs[len(reqs)-1]
	if last.Body["passcode"] != "Zx7q" {
		t.Errorf("回验用的是 %v，期望服务端给的 Zx7q", last.Body["passcode"])
	}
}

// TestCreateShareEmptyPasscodeGeneratesOne 不传提取码时自动生成一个合法值，
// 且真正提交上去的就是它。
func TestCreateShareEmptyPasscodeGeneratesOne(t *testing.T) {
	s := &shareStub{TaskID: "task-1"}
	withStub(t, s)

	res, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"},
		adapter.ShareCreateOptions{})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if len(res.Passcode) != 4 {
		t.Fatalf("自动生成的提取码 = %q，期望 4 位", res.Passcode)
	}
	reqs := s.snapshot()
	if got := reqs[0].Body["passcode"]; got != res.Passcode {
		t.Errorf("提交的 passcode = %v，与返回的 %q 不一致", got, res.Passcode)
	}
	// 默认有效期是 30 天
	if got := reqs[0].Body["expired_type"]; got != float64(quarkExpire30Days) {
		t.Errorf("默认 expired_type = %v，期望 %d", got, quarkExpire30Days)
	}
}

// TestCreateSharePermanent 永久有效走 expired_type=1，且 ExpireAt 为 0。
func TestCreateSharePermanent(t *testing.T) {
	s := &shareStub{TaskID: "task-1"}
	withStub(t, s)

	res, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"},
		adapter.ShareCreateOptions{ExpireDays: adapter.ShareExpirePermanent})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if res.ExpireAt != 0 {
		t.Errorf("ExpireAt = %d，永久应为 0", res.ExpireAt)
	}
	if got := s.snapshot()[0].Body["expired_type"]; got != float64(quarkExpirePermanent) {
		t.Errorf("expired_type = %v，期望 %d（永久）", got, quarkExpirePermanent)
	}
}

// TestCreateShareRejectsUnknownExpiry 不认识的有效期直接拒绝，不发任何请求。
//
// "就近取一档"看着无害，但会让用户按错误的假设做续期安排。
func TestCreateShareRejectsUnknownExpiry(t *testing.T) {
	s := &shareStub{TaskID: "task-1"}
	withStub(t, s)

	_, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"},
		adapter.ShareCreateOptions{ExpireDays: 7})
	if err == nil {
		t.Fatal("期望拒绝 7 天，却成功了")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Errorf("错误信息应带上用户选的天数，实际: %v", err)
	}
	if n := len(s.snapshot()); n != 0 {
		t.Errorf("请求数 = %d，期望 0（参数就不合法，不该打扰服务端）", n)
	}
}

// TestCreateSharePollTask /task 要多次轮询才出 share_id。
func TestCreateSharePollTask(t *testing.T) {
	s := &shareStub{TaskID: "task-1", TaskCalls: 2}
	withStub(t, s)

	res, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"}, adapter.ShareCreateOptions{})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if res.ShareID != "share-abc" {
		t.Fatalf("ShareID = %q", res.ShareID)
	}
	if n := len(s.snapshot()); n != 6 {
		t.Errorf("请求数 = %d，期望 6（1 建 + 3 轮询 + 1 兑换 + 1 回验）", n)
	}
}

// TestCreateShareFailsWhenVerifyFails 回验不通过时**必须报错**，不能把
// 一个用不了的链接当成成功返回。
//
// 这是整段代码里唯一一个"宁可失败也不写库"的检查点：库里一旦存下
// 带错提取码的分享，后台页面显示一切正常，故障会推迟到孩子端转存那一刻
// 才爆发，而那时排查的人根本不会怀疑"创建分享"这一步。
func TestCreateShareFailsWhenVerifyFails(t *testing.T) {
	s := &shareStub{TaskID: "task-1", TokenCode: 41013}
	withStub(t, s)

	res, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"},
		adapter.ShareCreateOptions{Passcode: "k3m9"})
	if err == nil {
		t.Fatalf("回验失败却返回成功: %+v", res)
	}
	if res != nil {
		t.Errorf("失败时必须连结果一起置空，否则调用方可能仍去写库: %+v", res)
	}
	if !strings.Contains(err.Error(), "k3m9") {
		t.Errorf("错误信息应带上那个没能通过验证的提取码，实际: %v", err)
	}
}

// TestCreateSharePropagatesAPIError 建分享接口自己报错时，把 code/message
// 原样带出来 —— 否则后台只能显示一句"失败"，用户无从判断是空间不足
// 还是链接数超限。
func TestCreateSharePropagatesAPIError(t *testing.T) {
	s := &shareStub{CreateCode: 23008, CreateMessage: "share count limit"}
	withStub(t, s)

	_, err := New().CreateShare(context.Background(), testCred(), []string{"fid-1"}, adapter.ShareCreateOptions{})
	if err == nil {
		t.Fatal("期望失败")
	}
	for _, want := range []string{"23008", "share count limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q: %v", want, err)
		}
	}
}

// TestCreateShareRejectsEmptyFileIDs 空 fid 不打扰服务端。
func TestCreateShareRejectsEmptyFileIDs(t *testing.T) {
	s := &shareStub{TaskID: "task-1"}
	withStub(t, s)

	for _, ids := range [][]string{nil, {}, {"", "  "}} {
		if _, err := New().CreateShare(context.Background(), testCred(), ids, adapter.ShareCreateOptions{}); err == nil {
			t.Errorf("fid=%v 时应报错", ids)
		}
	}
	if n := len(s.snapshot()); n != 0 {
		t.Errorf("请求数 = %d，期望 0", n)
	}
}
