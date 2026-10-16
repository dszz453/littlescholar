package handler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// ShareHandler 负责「设置共享」：把后台网盘账号里的资源变成一条分享链接。
//
// 为什么需要它（整条链路的缺口就在这）：
//
//	资源是映射进**后台那个网盘账号**的（resources.disk_account_id + file_id），
//	而孩子端 PRO 用的是**另一个**账号（家长自己扫码接入）。两个账号之间
//	没有任何合法通道，唯一的办法就是后台把资源分享出来、孩子端转存进去 ——
//	PRO 那条链路（解析分享 → 按 fileId 挑文件 → 转存 → 取直链 → 加速）
//	早就写完了，一直在等 resources.shareURL/passcode 有值。
//
// 职责边界：本 handler 只负责"创建/清除分享并回写资源"，
// 不碰转存、不碰播放。
type ShareHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
}

// NewShareHandler 创建分享 handler。
func NewShareHandler(db *gorm.DB, enc *util.Encryptor) *ShareHandler {
	return &ShareHandler{db: db, encryptor: enc}
}

// shareChunkSize 一次分享请求里放多少个文件。
//
// 为什么不是"一条资源一条分享"：夸克的 /share 接口本来就收 fid_list，
// 一个目录下几十集动画只需一次调用；逐条建分享会瞬间打出几十个请求，
// 直接撞上限流，而且分享列表会被几十条同名分享塞满。
//
// 为什么又要切块而不是一次全塞：谁都不保证这个接口对 fid_list
// 没有长度上限，超限的报错往往还很含糊。50 是个保守值 —— 覆盖
// 绝大多数"一整季动画"的场景，又不至于把一次请求撑到不可控。
const shareChunkSize = 50

// shareRequest 设置共享的入参。
//
// 三种定位方式二选一（按优先级）：resourceIds / resourceId → mappingId。
type shareRequest struct {
	ResourceID  uint64   `json:"resourceId"`
	ResourceIDs []uint64 `json:"resourceIds"`
	MappingID   uint64   `json:"mappingId"`

	// ExpireDays 有效期：省略/0 = 适配器默认（夸克 30 天）；
	// 负数 = 永久（adapter.ShareExpirePermanent）。
	//
	// 用指针是为了区分"没传"和"传了 0"—— 虽然目前两者含义相同，
	// 但把"没传"当成默认值、把"传了 0"当成"当天过期"这类歧义
	// 值得在类型上就掐掉。
	ExpireDays *int `json:"expireDays"`

	// Passcode 期望的提取码，留空 = 自动生成。
	Passcode string `json:"passcode"`

	// Force 连已经共享过的资源也重新建一条分享。
	//
	// 默认行为（false）是**只补缺**：已经有 share_url 的直接跳过。
	// 这样"同步又新增了几集"之后，用户再点一次按钮只会为新增的那些
	// 建分享，不会把老的链接全部作废 —— 老的链接可能已经被记在
	// 某处、或已经让孩子转过存了。
	Force bool `json:"force"`
}

// shareGroupStat 按网盘账号汇总的执行结果。
type shareGroupStat struct {
	AccountID   uint64 `json:"accountId"`
	AccountName string `json:"accountName"`
	DiskType    string `json:"diskType"`
	Planned     int    `json:"planned"`
	Created     int    `json:"created"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
}

// shareFailure 单条资源的失败原因。
type shareFailure struct {
	ResourceID uint64 `json:"resourceId"`
	Title      string `json:"title"`
	Error      string `json:"error"`
}

// maxShareFailures 响应里最多回多少条失败明细。
//
// 上限的意义不是"省流量"：几千条失败明细会把真正的第一条原因
// （比如账号 cookie 失效）淹掉，而那条才是用户要看的。
const maxShareFailures = 50

// Create 创建分享并回写到资源上。
//
//	POST /api/admin/shares          （?dryRun=1 只预览，不打网盘接口）
func (h *ShareHandler) Create(c *gin.Context) {
	var req shareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数格式错误")
		return
	}
	dryRun := c.Query("dryRun") == "1"

	resources, err := h.collect(req)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	resp := gin.H{
		"dryRun": dryRun,
		"total":  len(resources),
	}
	if len(resources) == 0 {
		resp["message"] = "没有找到符合条件的资源"
		resp["created"], resp["skipped"], resp["failed"] = 0, 0, 0
		resp["groups"], resp["failures"] = []shareGroupStat{}, []shareFailure{}
		util.OK(c, resp)
		return
	}

	groups, failures := h.groupByAccount(resources)

	// 分组阶段被挡下的（存储源资源、缺 fileId、账号不存在…）同样是失败，
	// 必须一起计进 failed。否则会出现"failures 里有 1 条原因、failed 却是 0"
	// 这种自相矛盾的响应，而前端按 failed 判成败，就会把一次什么都没做成的
	// 操作显示成成功。（这条是测试先发现的，不是事后想到的。）
	failed := len(failures)

	// 有效期与提取码是**整批共用**的：一次批量操作里给不同资源
	// 生成不同的提取码，用户面对的就是一堆互不相干的码，
	// 而"一个目录一套码"才是他能记住、能转述给别人的形态。
	opt := adapter.ShareCreateOptions{Passcode: strings.TrimSpace(req.Passcode)}
	if req.ExpireDays != nil {
		opt.ExpireDays = *req.ExpireDays
	}

	var stats []shareGroupStat
	created, skipped := 0, 0

	for _, g := range groups {
		st := shareGroupStat{
			AccountID:   g.accountID,
			AccountName: g.accountName,
			DiskType:    string(g.diskType),
		}

		var todo []model.Resource
		for _, r := range g.resources {
			if !req.Force && strings.TrimSpace(r.ShareURL) != "" {
				st.Skipped++
				skipped++
				continue
			}
			todo = append(todo, r)
		}
		st.Planned = len(todo)

		if len(todo) > 0 && !dryRun {
			nCreated, errs := h.shareInChunks(c.Request.Context(), g, todo, opt)
			st.Created += nCreated
			st.Failed += len(errs)
			created += nCreated
			failed += len(errs)
			failures = append(failures, errs...)
		}
		stats = append(stats, st)
	}

	// 排序只为让输出稳定：map 遍历顺序随机，验收脚本没法断言。
	sort.Slice(stats, func(i, j int) bool { return stats[i].AccountID < stats[j].AccountID })

	resp["created"], resp["skipped"], resp["failed"] = created, skipped, failed
	resp["groups"] = stats
	resp["failures"] = capFailures(failures)
	if dryRun {
		// dryRun 下 created 一定是 0，"计划做多少"才是用户想看的数字。
		resp["message"] = fmt.Sprintf("预览：将为 %d 条资源创建分享，跳过 %d 条已有分享的", countPlanned(stats), skipped)
	} else {
		resp["message"] = fmt.Sprintf("成功 %d 条，跳过 %d 条，失败 %d 条", created, skipped, failed)
	}
	util.OK(c, resp)
}

// shareVerifyItem 单条资源的校验结论。
type shareVerifyItem struct {
	ResourceID uint64 `json:"resourceId"`
	Title      string `json:"title"`
	FileID     string `json:"fileId"`
	Found      bool   `json:"found"`
	Note       string `json:"note,omitempty"`
}

// Verify 校验已建的分享对孩子端是否**真的**可用。
//
//	POST /api/admin/shares/verify   { resourceId | resourceIds | mappingId }
//
// 为什么这是一个独立接口，而不是塞进创建流程里：
// 创建路径上每多一次网络往返，一旦我对网盘某个行为的假设是错的，
// 就会把**本来能用的分享判成失败**（连链接都拿不到）。所以创建保持最小，
// 校验放在旁边按需调用。
//
// 它回答的是一个非常具体、又最容易错的问题：
// **分享根目录里能不能按 fileId 找到这个资源？**
//
// 这是整条加速链路里唯一"看着全对、其实全错"的环节：分享建成了、
// 链接能打开、提取码也对，但分享根目录列出来的文件 ID 与资源自己的
// fileId 对不上时，孩子端会报"分享里没有找到指定的文件" ——
// 那是最不可能靠家长自己查清的故障，因为后台一切正常。
func (h *ShareHandler) Verify(c *gin.Context) {
	var req shareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数格式错误")
		return
	}
	resources, err := h.collect(req)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	items := []shareVerifyItem{}
	unshared := 0

	// 同一批资源往往共用一条分享（整块资源一次 /share），
	// 所以按 (账号, 链接) 去重后再解析 —— 否则 38 条资源会把
	// 同一个分享解析 38 次，既慢又容易被网盘限流。
	type verifyKey struct {
		accountID uint64
		shareURL  string
	}
	pending := map[verifyKey][]model.Resource{}
	var order []verifyKey

	for _, r := range resources {
		if strings.TrimSpace(r.ShareURL) == "" {
			unshared++
			items = append(items, shareVerifyItem{
				ResourceID: r.ID, Title: r.Title, FileID: r.FileID,
				Note: "还没有分享链接，先在「设置共享」里创建",
			})
			continue
		}
		if r.DiskAccountID == nil || *r.DiskAccountID == 0 {
			items = append(items, shareVerifyItem{
				ResourceID: r.ID, Title: r.Title, FileID: r.FileID,
				Note: "资源没有关联网盘账号",
			})
			continue
		}
		k := verifyKey{accountID: *r.DiskAccountID, shareURL: r.ShareURL}
		if _, ok := pending[k]; !ok {
			order = append(order, k)
		}
		pending[k] = append(pending[k], r)
	}

	for _, k := range order {
		group := pending[k]
		found, err := h.listShareFids(c.Request.Context(), k.accountID, group[0])
		if err != nil {
			for _, r := range group {
				items = append(items, shareVerifyItem{
					ResourceID: r.ID, Title: r.Title, FileID: r.FileID,
					Note: "解析分享失败：" + err.Error(),
				})
			}
			continue
		}
		for _, r := range group {
			if _, ok := found[r.FileID]; ok {
				items = append(items, shareVerifyItem{
					ResourceID: r.ID, Title: r.Title, FileID: r.FileID, Found: true,
				})
				continue
			}
			items = append(items, shareVerifyItem{
				ResourceID: r.ID, Title: r.Title, FileID: r.FileID,
				Note: "分享里找不到这个文件 ID —— 孩子端会报「分享里没有找到指定的文件」，" +
					"请重新设置共享",
			})
		}
	}

	okCount, missCount := 0, 0
	for _, it := range items {
		if it.Found {
			okCount++
		} else {
			missCount++
		}
	}
	log.Printf("[share] 校验：共 %d 条，通过 %d，未通过 %d（未共享 %d）",
		len(items), okCount, missCount, unshared)

	out := items
	if len(out) > maxShareFailures {
		out = out[:maxShareFailures]
	}
	util.OK(c, gin.H{
		"checked":   len(items),
		"found":     okCount,
		"missing":   missCount,
		"unshared":  unshared,
		"items":     out,
		"truncated": len(items) > maxShareFailures,
	})
}

// listShareFids 用资源所属账号解析它自己的分享，返回根目录里的文件 ID 集合。
func (h *ShareHandler) listShareFids(ctx context.Context, accountID uint64, sample model.Resource) (map[string]bool, error) {
	var acc model.DiskAccount
	if err := h.db.First(&acc, accountID).Error; err != nil {
		return nil, fmt.Errorf("网盘账号不存在")
	}
	dt := adapter.DiskType(acc.DiskType)
	drv, err := adapter.Get(dt)
	if err != nil {
		return nil, err
	}
	browser, ok := drv.(adapter.ShareBrowser)
	if !ok {
		return nil, fmt.Errorf("%s 不支持解析分享内容", adapter.DiskTypeNames[dt])
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return nil, fmt.Errorf("账号凭据解密失败，请重新接入该账号")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	cred := &adapter.Credential{DiskType: dt, Cookie: cookie, Extra: extra}

	entry, err := browser.ListShareDir(ctx, cred, sample.ShareURL, sample.Passcode, "0")
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(entry.Files))
	for _, f := range entry.Files {
		set[f.ID] = true
	}
	return set, nil
}

// Clear 清除资源上的分享信息。
//
// 只清库里的字段，**不删网盘上的那条分享** —— 我们只知道 share_id，
// 各家网盘取消分享的接口与权限要求都不一样，贸然去调可能删掉
// 用户自己另建的分享。这里如实只负责"后台不再向孩子端提供它"。
func (h *ShareHandler) Clear(c *gin.Context) {
	var req shareRequest
	req.ResourceID = util.GetUintQuery(c, "resourceId", 0)
	req.MappingID = util.GetUintQuery(c, "mappingId", 0)
	req.ResourceIDs = parseIDList(c.Query("resourceIds"))

	resources, err := h.collect(req)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}
	if len(resources) == 0 {
		util.OK(c, gin.H{"cleared": 0})
		return
	}
	rids := make([]uint64, 0, len(resources))
	for _, r := range resources {
		rids = append(rids, r.ID)
	}
	// 逐列写空串而不是 Updates(map) 里省字段：省掉的字段 gorm 不会更新，
	// 上一条分享的 passcode 就会留在库里 —— 表现为"链接没了、码还在"。
	res := h.db.Model(&model.Resource{}).Where("id IN ?", rids).Updates(map[string]interface{}{
		"share_url": "", "passcode": "", "share_id": "",
	})
	if res.Error != nil {
		util.ServerError(c, "清除分享信息失败")
		return
	}
	util.OK(c, gin.H{"cleared": res.RowsAffected})
}

// collect 按入参取出本次要处理的资源。
func (h *ShareHandler) collect(req shareRequest) ([]model.Resource, error) {
	q := h.db.Where("deleted_at IS NULL")

	switch {
	case len(req.ResourceIDs) > 0:
		q = q.Where("id IN ?", req.ResourceIDs)
	case req.ResourceID > 0:
		q = q.Where("id = ?", req.ResourceID)
	case req.MappingID > 0:
		q = q.Where("mapping_id = ?", req.MappingID)
	default:
		return nil, fmt.Errorf("请指定要共享的资源（resourceId / resourceIds / mappingId）")
	}

	var out []model.Resource
	if err := q.Order("id").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("读取资源失败: %w", err)
	}
	return out, nil
}

// accountGroup 同一网盘账号下的一批资源。
//
// 必须按账号分组：分享是**账号内**的行为，用 A 账号的 cookie 去分享
// B 账号的文件必然失败。线上就可能同时存在夸克档案与 WebDAV 备份，
// 混在一起提交会得到一半成功一半失败，而错误信息还看不出分组这件事。
type accountGroup struct {
	accountID   uint64
	accountName string
	diskType    adapter.DiskType
	resources   []model.Resource
}

// groupByAccount 把资源按所属账号分组，顺手把根本不可能共享的挑出来。
func (h *ShareHandler) groupByAccount(resources []model.Resource) ([]*accountGroup, []shareFailure) {
	var failures []shareFailure
	byID := map[uint64]*accountGroup{}
	var order []uint64

	// 账号信息一次查完，避免每条资源一次查询。
	accIDs := make([]uint64, 0, len(resources))
	seenAcc := map[uint64]bool{}
	for _, r := range resources {
		if r.DiskAccountID == nil || *r.DiskAccountID == 0 {
			failures = append(failures, shareFailure{
				ResourceID: r.ID, Title: r.Title,
				Error: "资源没有关联网盘账号（可能是手工收录的），无法创建分享",
			})
			continue
		}
		if !seenAcc[*r.DiskAccountID] {
			seenAcc[*r.DiskAccountID] = true
			accIDs = append(accIDs, *r.DiskAccountID)
		}
	}
	var accs []model.DiskAccount
	if len(accIDs) > 0 {
		h.db.Where("id IN ?", accIDs).Find(&accs)
	}
	accByID := map[uint64]model.DiskAccount{}
	for _, a := range accs {
		accByID[a.ID] = a
	}

	for _, r := range resources {
		if r.DiskAccountID == nil || *r.DiskAccountID == 0 {
			continue // 上面已经记过失败
		}
		acc, ok := accByID[*r.DiskAccountID]
		if !ok {
			failures = append(failures, shareFailure{
				ResourceID: r.ID, Title: r.Title,
				Error: "资源关联的网盘账号已不存在，请重新映射",
			})
			continue
		}
		dt := adapter.DiskType(acc.DiskType)

		// 存储源（WebDAV / 本地硬盘）没有"分享"这个概念。在这里拦住，
		// 用户看到的是"本地硬盘资源无法创建分享"，而不是一个
		// "不支持的网盘类型" 之类的内部说法。
		if adapter.IsStorageType(dt) {
			failures = append(failures, shareFailure{
				ResourceID: r.ID, Title: r.Title,
				Error: fmt.Sprintf("%s资源无法创建分享（孩子端 PRO 只支持网盘转存）", adapter.DiskTypeNames[dt]),
			})
			continue
		}
		if strings.TrimSpace(r.FileID) == "" {
			failures = append(failures, shareFailure{
				ResourceID: r.ID, Title: r.Title,
				Error: "资源没有网盘文件 ID，无法创建分享，请重新同步映射",
			})
			continue
		}
		if _, err := adapter.ShareCreatorOf(dt); err != nil {
			failures = append(failures, shareFailure{
				ResourceID: r.ID, Title: r.Title, Error: err.Error(),
			})
			continue
		}

		g, ok := byID[acc.ID]
		if !ok {
			g = &accountGroup{accountID: acc.ID, accountName: accountLabel(acc), diskType: dt}
			byID[acc.ID] = g
			order = append(order, acc.ID)
		}
		g.resources = append(g.resources, r)
	}

	out := make([]*accountGroup, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, failures
}

// shareInChunks 把一个账号下的资源切块后逐块创建分享。
func (h *ShareHandler) shareInChunks(ctx context.Context, g *accountGroup, todo []model.Resource,
	opt adapter.ShareCreateOptions) (int, []shareFailure) {

	var failures []shareFailure

	creator, err := adapter.ShareCreatorOf(g.diskType)
	if err != nil {
		return 0, failureForAll(todo, err.Error())
	}

	var acc model.DiskAccount
	if err := h.db.First(&acc, g.accountID).Error; err != nil {
		return 0, failureForAll(todo, "网盘账号不存在")
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return 0, failureForAll(todo, "账号凭据解密失败，请重新接入该账号")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	cred := &adapter.Credential{DiskType: g.diskType, Cookie: cookie, Extra: extra}

	created := 0
	for start := 0; start < len(todo); start += shareChunkSize {
		end := start + shareChunkSize
		if end > len(todo) {
			end = len(todo)
		}
		chunk := todo[start:end]

		fids := make([]string, 0, len(chunk))
		ids := make([]uint64, 0, len(chunk))
		for _, r := range chunk {
			fids = append(fids, r.FileID)
			ids = append(ids, r.ID)
		}

		o := opt
		o.Title = shareTitle(chunk, accountLabel(acc))
		res, err := creator.CreateShare(ctx, cred, fids, o)

		if err != nil {
			// 一块失败就是这一块全失败 —— 说清楚这点，否则用户会
			// 以为是"某几个文件有问题"而去逐个排查。
			msg := fmt.Sprintf("本批 %d 条资源创建分享失败：%v", len(chunk), err)
			log.Printf("[share] 账号 %d(%s) 创建分享失败: %v", g.accountID, accountLabel(acc), err)
			for _, r := range chunk {
				failures = append(failures, shareFailure{ResourceID: r.ID, Title: r.Title, Error: msg})
			}
			continue
		}

		// 回写分享信息。这里必须整块写同一条链接：分享的粒度就是
		// "这一块文件"，不是单条资源。
		up := h.db.Model(&model.Resource{}).Where("id IN ?", ids).Updates(map[string]interface{}{
			"share_url": res.ShareURL,
			"passcode":  res.Passcode,
			"share_id":  res.ShareID,
		})
		if up.Error != nil {
			msg := "分享已创建但写入资源失败：" + up.Error.Error()
			log.Printf("[share] 回写资源失败 ids=%v: %v", ids, up.Error)
			for _, r := range chunk {
				failures = append(failures, shareFailure{ResourceID: r.ID, Title: r.Title, Error: msg})
			}
			continue
		}
		log.Printf("[share] 账号 %d(%s) 已为 %d 条资源创建分享 %s（提取码 %s，有效期至 %d）",
			g.accountID, accountLabel(acc), len(chunk), res.ShareURL, res.Passcode, res.ExpireAt)
		created += int(up.RowsAffected)
	}
	return created, failures
}

// shareTitle 给这一批分享起个名字。
//
// 名字会出现在用户的网盘"我的分享"列表里。用单条资源的标题会让
// 几十条分享看起来像几十个孤立文件；用"小书童 · <账号>"这种
// 带来源的名字，用户过几天回来还能认出这是什么、能不能删。
func shareTitle(chunk []model.Resource, accountName string) string {
	if len(chunk) == 1 {
		if t := strings.TrimSpace(chunk[0].Title); t != "" {
			return clip(t, 50)
		}
	}
	name := strings.TrimSpace(accountName)
	if name == "" {
		name = "资源"
	}
	return clip("小书童 · "+name, 50)
}

// clip 按**字符**截断，不是字节。
//
// 中文标题按字节截会把最后一个字切成半个 UTF-8 序列，
// 存进数据库就是乱码 —— 而 Go 的切片不会报错，静默生效。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func failureForAll(rs []model.Resource, msg string) []shareFailure {
	out := make([]shareFailure, 0, len(rs))
	for _, r := range rs {
		out = append(out, shareFailure{ResourceID: r.ID, Title: r.Title, Error: msg})
	}
	return out
}

func capFailures(f []shareFailure) []shareFailure {
	if f == nil {
		return []shareFailure{}
	}
	if len(f) > maxShareFailures {
		return f[:maxShareFailures]
	}
	return f
}

func countPlanned(stats []shareGroupStat) int {
	n := 0
	for _, s := range stats {
		n += s.Planned
	}
	return n
}

// accountLabel 账号的展示名。
//
// 扫码接入的账号可能没有昵称（用户在手机上没有授权昵称，或那一步失败），
// 这时退回"夸克网盘"这类类型名 —— 总比在界面上显示一个空白、
// 让用户以为账号是坏的强。
func accountLabel(acc model.DiskAccount) string {
	if n := strings.TrimSpace(acc.Nickname); n != "" {
		return n
	}
	if n := adapter.DiskTypeNames[adapter.DiskType(acc.DiskType)]; n != "" {
		return n
	}
	return acc.DiskType
}

// parseIDList 解析 "1,2,3" 形式的 ID 列表。
//
// 非法片段直接丢弃、而不是整条报错：这个值来自查询串，多一个逗号
// 不该让整次清理失败。也不会"解析不出来就当成全部" —— 那才是真正
// 危险的方向，所以解析空时返回空列表，由 collect 明确拒绝。
func parseIDList(s string) []uint64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []uint64
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err == nil && v > 0 {
			out = append(out, v)
		}
	}
	return out
}
