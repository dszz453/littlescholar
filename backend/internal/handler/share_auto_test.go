package handler

import (
	"context"
	"strconv"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
)

// shareAutoDiskType 是共享测试里注册的假网盘类型（见 share_test.go 的 init）。
const shareAutoDiskType = "fake-share"

// seedMappedResource 建一条资源。
//
// mappingID=0 表示"手动收录/搜索来的"，>0 表示"由某个映射同步进来"。
func seedMappedResource(t *testing.T, db *gorm.DB, accID uint64,
	title, fileID string, mappingID uint64, shareURL string) model.Resource {
	t.Helper()
	a := accID
	r := model.Resource{
		Title:         title,
		FileID:        fileID,
		MappingID:     mappingID,
		ShareURL:      shareURL,
		DiskAccountID: &a,
	}
	mustCreate(t, db, &r)
	return r
}

// TestShareScopeMappedPicksOnlyUnshared 自动任务的范围必须**恰好**是
// 「挂过映射 且 还没有分享」那一批。
//
// 三类必须区分开，任何一类搞错都会出事：
//   - 该补的没补 → 孩子点它就是"缺少分享链接"，而这个报错发生在播放那一刻；
//   - 不该补的补了（手动收录的）→ 平白多出一堆分享；
//   - 已有的被重建 → 家长已经转述出去的链接全部作废、孩子已转存的记录失效。
func TestShareScopeMappedPicksOnlyUnshared(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	shouldShare := seedMappedResource(t, db, acc.ID, "该补的", "fid-1", 7, "")
	alreadyShared := seedMappedResource(t, db, acc.ID, "已有分享", "fid-2", 7,
		"https://pan.example.com/s/old")
	notMapped := seedMappedResource(t, db, acc.ID, "手动收录", "fid-3", 0, "")

	// 第四类：share_url 是 **NULL** 的那批。
	//
	// AutoMigrate 给存量行补列时**不回填默认值**，库里就是 NULL 而不是空串。
	// 只写 `share_url = ''` 会把这些行整批漏掉，而它们恰恰是最需要补的
	// （最老的存量资源）。这里显式置 NULL —— 不置的话 SQLite 会用 Go 零值
	// 插入，测出来永远是空串，这条用例就会一直绿着却什么都没测到。
	nullRow := seedMappedResource(t, db, acc.ID, "NULL 行", "fid-4", 7, "")
	if err := db.Exec("UPDATE resources SET share_url = NULL WHERE id = ?", nullRow.ID).Error; err != nil {
		t.Fatalf("把 share_url 置 NULL 失败: %v", err)
	}

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	if res.Created != 2 {
		t.Errorf("应只补 2 条（空串 + NULL），实得 created=%d（total=%d skipped=%d failed=%d）",
			res.Created, res.Total, res.Skipped, res.Failed)
	}

	var rows []model.Resource
	db.Order("id").Find(&rows)
	byID := map[uint64]model.Resource{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	if got := byID[shouldShare.ID]; got.ShareURL == "" {
		t.Error("该补的没补上 —— 挂着映射却没有分享，PRO 播放会直接报「缺少分享链接」")
	}
	if got := byID[nullRow.ID]; got.ShareURL == "" {
		t.Error("share_url 为 NULL 的那条没补上 —— COALESCE 漏了，最老的存量资源会永远补不到")
	}
	if got := byID[shouldShare.ID]; got.Passcode == "" {
		t.Error("自动共享必须带提取码（用户明确要求「加密码的共享」）")
	}
	if got := byID[alreadyShared.ID]; got.ShareURL != "https://pan.example.com/s/old" {
		t.Errorf("已有分享被改动了，自动任务绝不能重建老链接（实得 %q）", got.ShareURL)
	}
	if got := byID[notMapped.ID]; got.ShareURL != "" {
		t.Errorf("没挂映射的不该被自动共享（实得 %q）", got.ShareURL)
	}
}

// TestAutoSecondRunIsNoop 补完之后再跑一轮，必须一条都不动。
//
// 这条是"循环补"的安全阀：定时任务每 6 小时跑一次，如果每轮都重复创建，
// 用户的网盘里会堆满同名分享。同时也是"补完即停"的证据 ——
// 有了它，才敢把间隔设得很短。
func TestAutoSecondRunIsNoop(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	seedMappedResource(t, db, acc.ID, "该补的", "fid-1", 7, "")

	first, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	if first.Created != 1 {
		t.Fatalf("第一轮应补 1 条，实得 %d", first.Created)
	}

	second, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	if second.Created != 0 {
		t.Errorf("补完之后不该再创建，实得 %d 条（定时任务会每 6 小时跑一次，"+
			"重复创建会让网盘堆满同名分享）", second.Created)
	}
}

// TestAutoIgnoresResourcesWithoutAccount 没有网盘账号的资源不该让整轮失败。
//
// 线上确实存在手工收录、没关联任何网盘账号的资源（DiskAccountID 为空）。
// 它们**不可能**被分享，但这不该导致整批任务报错 —— 否则一条脏数据
// 就能让后面所有资源永远补不到分享。
//
// 这里断言的失败数必须是 0，而不是"≥1 且如实计入 failed"：
// 范围查询里带了 `disk_account_id IN (支持分享的账号)`，NULL 天然不匹配，
// 这些资源**根本不会被选中**。比"选中了再逐条报失败"更好 ——
// 后者会让 failed 永远降不到 0，真正的失败被噪音淹掉。
func TestAutoIgnoresResourcesWithoutAccount(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	orphan := model.Resource{Title: "无账号", FileID: "fid-orphan", MappingID: 9}
	mustCreate(t, db, &orphan)
	ok := seedMappedResource(t, db, acc.ID, "正常", "fid-ok", 7, "")

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("一条脏数据不该让整轮失败: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("正常那条应被补上，实得 created=%d failed=%d", res.Created, res.Failed)
	}
	if res.Failed != 0 {
		t.Errorf("无账号的资源不该被计成失败（会盖住真正的失败），实得 failed=%d", res.Failed)
	}

	var got model.Resource
	db.First(&got, orphan.ID)
	if got.ShareURL != "" {
		t.Errorf("无账号资源竟然被分享了：%q", got.ShareURL)
	}
	var okRow model.Resource
	db.First(&okRow, ok.ID)
	if okRow.ShareURL == "" {
		t.Error("正常资源没补上")
	}
}

// TestShareScopeMappedSkipsTypesWithoutShare 不能分享的网盘类型不该被捞出来。
//
// 本地硬盘、WebDAV 这类存储源的资源同样挂着 mapping_id，但它们**永远**
// 建不出分享。不过滤的话，这些资源每 6 小时被选中一次、每 6 小时失败一次：
// 日志里是假告警，failed 永远降不到 0，真正需要处理的失败反而被淹掉。
func TestShareScopeMappedSkipsTypesWithoutShare(t *testing.T) {
	db := newShareTestDB(t)
	quarkAcc := seedQuarkAccount(t, db, shareAutoDiskType)
	// 本地硬盘：真实存在、但适配器不实现 ShareCreator。
	localAcc := seedQuarkAccount(t, db, "local")
	h := newShareHandler(t, db)

	quarkRes := seedMappedResource(t, db, quarkAcc.ID, "夸克资源", "fid-q", 7, "")
	localRes := seedMappedResource(t, db, localAcc.ID, "本地资源", "fid-l", 7, "")

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("应只补夸克那条，实得 created=%d", res.Created)
	}
	if res.Failed != 0 {
		t.Errorf("本地硬盘那条不该出现在失败里，实得 failed=%d", res.Failed)
	}

	var qr, lr model.Resource
	db.First(&qr, quarkRes.ID)
	db.First(&lr, localRes.ID)
	if qr.ShareURL == "" {
		t.Error("夸克资源没补上")
	}
	if lr.ShareURL != "" {
		t.Errorf("本地硬盘资源被错误地分享了：%q", lr.ShareURL)
	}
}

// TestAutoDoesNotRetryFailedBatch 建分享失败的那批不能把补齐卡在原地。
//
// 真实场景是"某个文件永远建不成分享"（已被删、被风控），而不是"这次网络抖了"。
// 这类资源的 share_url 永远是空的，每一轮按同样条件都会被选中 ——
// 不做排除的话，它们会把补齐**永久卡住**：每轮都在重试同一批、
// 打几百次网盘接口，排在后面的新资源一条都处理不到。
//
// 造 shareAutoBatch+3 条资源，让前 shareChunkSize 个 fid 永远失败：
//
//	· SkipIDs 生效 → 那 50 条只失败一次，后面 3 条照样补上（failed=50）
//	· SkipIDs 失效 → 第二轮又把 50 条捞回来再失败一次（failed≥100）
//
// 所以 failed 必须**恰好**是 50。
func TestAutoDoesNotRetryFailedBatch(t *testing.T) {
	// 批间 1.5 秒的冷却在这里纯属浪费：这条用例要打 11 批，光睡觉就 16 秒。
	oldCool := shareChunkCooldown
	shareChunkCooldown = 0
	t.Cleanup(func() { shareChunkCooldown = oldCool })

	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	total := shareAutoBatch + 3
	for i := 0; i < total; i++ {
		seedMappedResource(t, db, acc.ID, "r"+strconv.Itoa(i),
			"fid-"+strconv.Itoa(i), 7, "")
	}
	// 前 shareChunkSize 个文件永远建不成分享（一批正好 50 个）。
	shareFake.failFids = map[string]bool{}
	for i := 0; i < shareChunkSize; i++ {
		shareFake.failFids["fid-"+strconv.Itoa(i)] = true
	}

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	wantCreated := total - shareChunkSize
	if res.Created != wantCreated {
		t.Errorf("应补上 %d 条（除永远失败的 %d 条），实得 created=%d（failed=%d）",
			wantCreated, shareChunkSize, res.Created, res.Failed)
	}
	if res.Failed != shareChunkSize {
		t.Errorf("永远失败的那批只该记 %d 条失败；实得 %d —— 多出来的说明"+
			"下一轮又把它们捞回来重试了（补齐会被永久卡在这批上）",
			shareChunkSize, res.Failed)
	}
	if calls := len(shareFake.callList()); calls < 2 {
		t.Errorf("应该至少打了两批，实际只调用了 %d 次", calls)
	}
}

// TestAutoUsesPermanentShare 自动补齐必须建**永久**分享。
//
// 用默认的 30 天，意味着装好一个月后的某天孩子端点播突然全都
// "分享已失效"，而家长完全不知道去哪续 —— 后台每条资源都显示已共享，
// 自动任务也一直在正常跑。永久链接没有"续期"这件事。
func TestAutoUsesPermanentShare(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)
	seedMappedResource(t, db, acc.ID, "该补的", "fid-1", 7, "")

	if _, err := h.Auto(context.Background()); err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	calls := shareFake.callList()
	if len(calls) == 0 {
		t.Fatal("一次创建分享都没发生")
	}
	for i, c := range calls {
		if c.ExpireDays != adapter.ShareExpirePermanent {
			t.Errorf("第 %d 批不是永久分享（ExpireDays=%d）：30 天后会静默失效", i+1, c.ExpireDays)
		}
	}
}

// TestAutoRebuildsExpiredShare 已经过期的分享必须被当成「没分享」重新补。
//
// 这是最容易漏的一类：库里 share_url 好好的、后台看着"已共享"，
// 网盘那边却已经过期 —— 孩子端点它只会得到"分享已失效"。
// 不把过期的算进来，自动任务永远不会修它，而且一条错误日志都没有。
func TestAutoRebuildsExpiredShare(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	expired := seedMappedResource(t, db, acc.ID, "已过期", "fid-old", 7,
		"https://pan.example.com/s/dead")
	db.Model(&model.Resource{}).Where("id = ?", expired.ID).
		Update("share_expire_at", time.Now().Add(-time.Hour).Unix())

	alive := seedMappedResource(t, db, acc.ID, "还有效", "fid-ok", 7,
		"https://pan.example.com/s/alive")
	// +72h：明确"还早"，不该被续期（+24h 正好卡在续期线附近，边界会抖）
	db.Model(&model.Resource{}).Where("id = ?", alive.ID).
		Update("share_expire_at", time.Now().Add(72*time.Hour).Unix())

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("应只重建过期的那 1 条，实得 created=%d（skipped=%d）", res.Created, res.Skipped)
	}

	var gotDead, gotAlive model.Resource
	db.First(&gotDead, expired.ID)
	db.First(&gotAlive, alive.ID)
	if gotDead.ShareURL == "https://pan.example.com/s/dead" {
		t.Error("过期的分享没被重建 —— 孩子点它只会得到「分享已失效」")
	}
	if gotDead.ShareExpireAt != 0 {
		t.Errorf("重建后应记为永久（ShareExpireAt=0），实得 %d", gotDead.ShareExpireAt)
	}
	if gotAlive.ShareURL != "https://pan.example.com/s/alive" {
		t.Errorf("还有效的分享被动了，绝不能重建老链接（实得 %q）", gotAlive.ShareURL)
	}
}

// TestAutoRenewsExpiringShare 快过期的要**提前**重建，不能等它真的死掉。
//
// 自动任务 6 小时一轮。如果只在"已经过期"之后才补，那么过期那一刻起
// 最长有 6 小时孩子端是播不了的 —— 而这完全能避免：提前一天重建时，
// 旧链接本来也快死了，重建没有任何代价。
func TestAutoRenewsExpiringShare(t *testing.T) {
	db := newShareTestDB(t)
	acc := seedQuarkAccount(t, db, shareAutoDiskType)
	h := newShareHandler(t, db)

	soon := seedMappedResource(t, db, acc.ID, "明天就过期", "fid-soon", 7,
		"https://pan.example.com/s/soon")
	db.Model(&model.Resource{}).Where("id = ?", soon.ID).
		Update("share_expire_at", time.Now().Add(12*time.Hour).Unix())

	res, err := h.Auto(context.Background())
	if err != nil {
		t.Fatalf("自动共享失败: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("还有 12 小时过期的应被提前重建，实得 created=%d", res.Created)
	}
	var got model.Resource
	db.First(&got, soon.ID)
	if got.ShareExpireAt != 0 {
		t.Errorf("重建后应是永久（ShareExpireAt=0），实得 %d —— 否则 30 天后又要再来一次", got.ShareExpireAt)
	}
}
