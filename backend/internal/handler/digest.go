package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// digestSampleSize 每个范围最多取多少条资源进"候选池"（再由自然序截前 N 条）。
	//
	// 为什么要"先取一批再排序"而不是直接 `ORDER BY title`：
	// 标题里带数字（第 02 集 / 第 10 集），按字符串排会把 10 排在 2 前面，
	// 送进提示词的清单顺序就是乱的；而 PostgreSQL / SQLite 都没有内置的
	// 自然序排序函数。所以先按主键取一段（新增的资源主键最大，天然覆盖
	// "最近同步进来的内容"），再在内存里做自然序排序。
	digestSampleSize = 400
	// digestDescriptionSample 最多带几条描述进提示词（很多资源根本没有描述）
	digestDescriptionSample = 12
	// defaultDigestDailyLimit 每轮最多为多少个范围重新生成沉淀。
	// 每生成一条就是一次 AI 调用，必须有个上限兜住额度。
	defaultDigestDailyLimit = 40
	// digestAITimeout 单次 AI 调用的超时
	digestAITimeout = 90 * time.Second
)

// DigestHandler 小书童的知识沉淀。
type DigestHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	ai     *AIHandler
	logger *zap.Logger

	// DailyLimit 每轮最多生成多少个范围；<=0 用 defaultDigestDailyLimit
	DailyLimit int
}

func NewDigestHandler(db *gorm.DB, perm *service.PermissionService, aiH *AIHandler) *DigestHandler {
	return &DigestHandler{db: db, perm: perm, ai: aiH}
}

// WithLogger 注入日志（异步全量刷新需要它，否则只能丢进 Nop logger）。
func (h *DigestHandler) WithLogger(l *zap.Logger) *DigestHandler {
	h.logger = l
	return h
}

func (h *DigestHandler) log() *zap.Logger {
	if h.logger == nil {
		return zap.NewNop()
	}
	return h.logger
}

// ============ 对外接口 ============

// List 管理端：列出全部沉淀。
func (h *DigestHandler) List(c *gin.Context) {
	var list []model.KnowledgeDigest
	if err := h.db.Order("category_id asc").Find(&list).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	if list == nil {
		list = []model.KnowledgeDigest{}
	}
	util.OK(c, list)
}

type refreshDigestRequest struct {
	// CategoryID 为空/0 表示全量刷新（含全站总览）
	CategoryID *uint64 `json:"categoryId"`
	Force      bool    `json:"force"` // 跳过指纹比对，强制重算
}

// Refresh 管理端：手动触发沉淀生成。
//
// 单目录是同步的（一次 AI 调用几秒，用户可以等）；
// 全量刷新会很久（几十次 AI 调用），所以**立刻返回**、后台慢慢跑 ——
// 让它同步跑的话前端必然超时，用户会以为点了没生效。
func (h *DigestHandler) Refresh(c *gin.Context) {
	var req refreshDigestRequest
	_ = c.ShouldBindJSON(&req)

	if req.CategoryID != nil && *req.CategoryID != model.DigestScopeAll {
		digest, generated, err := h.Generate(*req.CategoryID, req.Force)
		if err != nil {
			util.ServerError(c, "生成失败: "+err.Error())
			return
		}
		util.OKWithMessage(c, digestMessage(generated), gin.H{
			"generated": generated,
			"digest":    digest,
		})
		return
	}

	go h.RunRound(h.log())
	util.OKWithMessage(c, "已开始全量刷新，稍后回本页查看", gin.H{"generated": true})
}

// digestView 孩子端可见的一条沉淀。
type digestView struct {
	CategoryID    uint64     `json:"categoryId"`
	CategoryName  string     `json:"categoryName"`
	Breadcrumb    string     `json:"breadcrumb"`
	Intro         string     `json:"intro"`
	Points        []string   `json:"points"`
	Keywords      []string   `json:"keywords"`
	ResourceCount int        `json:"resourceCount"`
	GeneratedAt   *time.Time `json:"generatedAt"`
}

func toDigestView(d *model.KnowledgeDigest) digestView {
	points := []string(d.Points)
	if points == nil {
		points = []string{}
	}
	keywords := []string(d.Keywords)
	if keywords == nil {
		keywords = []string{}
	}
	return digestView{
		CategoryID: d.CategoryID, CategoryName: d.CategoryName,
		Breadcrumb: d.Breadcrumb, Intro: d.Intro,
		Points: points, Keywords: keywords,
		ResourceCount: d.ResourceCount, GeneratedAt: d.GeneratedAt,
	}
}

// Get 孩子端：取某个目录的沉淀。
//
// 没有沉淀时返回 data:null 而不是 404 —— 前端只是"不显示这张卡片"，
// 把它当成错误会让控制台刷满红色，也容易被误当成接口挂了。
func (h *DigestHandler) Get(c *gin.Context) {
	categoryID, ok := util.GetUintParam(c, "categoryId")
	if !ok {
		util.BadRequest(c, "无效的目录 ID")
		return
	}
	if user := currentUser(c); user != nil && !util.IsAdmin(c) {
		allowed, err := h.perm.CanAccessCategory(user, categoryID)
		if err != nil {
			util.ServerError(c, "权限校验失败")
			return
		}
		if !allowed {
			util.OK(c, nil)
			return
		}
	}

	var digest model.KnowledgeDigest
	if err := h.db.Where("category_id = ?", categoryID).First(&digest).Error; err != nil {
		util.OK(c, nil)
		return
	}
	util.OK(c, toDigestView(&digest))
}

// AppList 孩子端：列出可见目录下的沉淀（「小书童的笔记」用）。
func (h *DigestHandler) AppList(c *gin.Context) {
	limit := util.GetIntQuery(c, "limit", 30)
	if limit < 1 || limit > 100 {
		limit = 30
	}

	scopeIDs, all, err := h.accessibleIDs(c)
	if err != nil {
		util.ServerError(c, "权限校验失败")
		return
	}
	if !all && len(scopeIDs) == 0 {
		util.OK(c, []digestView{})
		return
	}

	q := h.db.Model(&model.KnowledgeDigest{}).Where("category_id > ?", model.DigestScopeAll)
	if !all {
		q = q.Where("category_id IN ?", scopeIDs)
	}
	var rows []model.KnowledgeDigest
	if err := q.Order("resource_count desc, category_id asc").Limit(limit).Find(&rows).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}

	out := make([]digestView, 0, len(rows))
	for i := range rows {
		out = append(out, toDigestView(&rows[i]))
	}
	util.OK(c, out)
}

func (h *DigestHandler) accessibleIDs(c *gin.Context) ([]uint64, bool, error) {
	if util.IsAdmin(c) {
		return nil, true, nil
	}
	user := currentUser(c)
	if user == nil {
		return []uint64{}, false, nil
	}
	ids, all, err := h.perm.AccessibleCategoryIDs(user)
	if err != nil {
		return nil, false, err
	}
	return ids, all, nil
}

func digestMessage(generated bool) string {
	if generated {
		return "沉淀已更新"
	}
	return "内容没有变化，已跳过"
}

// ============ 生成 ============

// Generate 为某个范围生成（或跳过）沉淀。
//
// 返回 generated=false 的两种情形：
//   - 范围内没有资源（顺带清掉可能残留的旧沉淀）
//   - 资源指纹没变且未要求强制重算
//
// 幂等：靠 knowledge_digests.category_id 的唯一索引做 upsert，
// 同一天跑几轮都不会产出重复记录。
func (h *DigestHandler) Generate(categoryID uint64, force bool) (*model.KnowledgeDigest, bool, error) {
	if h.ai == nil {
		return nil, false, errors.New("未配置 AI 接口")
	}

	var cat *model.Category
	if categoryID != model.DigestScopeAll {
		var c model.Category
		if err := h.db.First(&c, categoryID).Error; err != nil {
			return nil, false, err
		}
		cat = &c
	}

	scopeIDs, all := h.digestScopeIDs(cat)
	scopeName := scopeDisplayName(cat)

	total, err := h.countInScope(scopeIDs, all)
	if err != nil {
		return nil, false, err
	}

	// 空的目录不生成，并且把旧沉淀硬删掉 ——
	// 留着会让前端显示"这里有 465 个内容"而点进去空空如也，比不显示更伤人。
	//
	// ⚠️ 必须 Unscoped()（真删）：本表的幂等靠 category_id 唯一索引做 upsert，
	// 软删的行**仍然占着那个唯一键**，之后内容回来时 upsert 会去更新那条
	// deleted_at 非空的行 —— 数据写进去了，查询却永远查不到。这类
	// "写进去但读不出来"的坑本项目已经踩过一次（resources.mapping_id）。
	if total == 0 {
		h.db.Unscoped().Where("category_id = ?", categoryID).
			Delete(&model.KnowledgeDigest{})
		return nil, false, nil
	}

	childNames, titles, descs := h.material(cat)
	fp := digestFingerprint(total, childNames, titles)

	if !force {
		var existed model.KnowledgeDigest
		if err := h.db.Where("category_id = ?", categoryID).First(&existed).Error; err == nil {
			if existed.Fingerprint == fp && existed.Status == model.DigestStatusReady {
				return &existed, false, nil
			}
		}
	}

	client, cfgID, err := h.ai.DefaultClient()
	if err != nil {
		h.markFailed(categoryID, err)
		return nil, false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), digestAITimeout)
	defer cancel()

	var result ai.DigestResult
	if err := client.ChatJSON(ctx, ai.BuildDigestPrompt(ai.DigestData{
		Name:               scopeName,
		Breadcrumb:         h.breadcrumbOf(cat),
		IsGlobal:           all,
		ResourceCount:      total,
		ChildNames:         childNames,
		Titles:             titles,
		SampleDescriptions: descs,
	}), &result); err != nil {
		h.markFailed(categoryID, err)
		return nil, false, err
	}
	ai.NormalizeDigestResult(&result)
	if strings.TrimSpace(result.Intro) == "" {
		err := errors.New("AI 返回的内容为空")
		h.markFailed(categoryID, err)
		return nil, false, err
	}

	now := time.Now()
	digest := &model.KnowledgeDigest{
		CategoryID:    categoryID,
		CategoryName:  scopeName,
		Breadcrumb:    h.breadcrumbOf(cat),
		Intro:         result.Intro,
		Points:        model.StringArray(result.Points),
		Keywords:      model.StringArray(result.Keywords),
		ResourceCount: total,
		Fingerprint:   fp,
		Status:        model.DigestStatusReady,
		LastError:     "",
		AIConfigID:    cfgID,
		GeneratedAt:   &now,
	}

	err = h.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "category_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"category_name", "breadcrumb", "intro", "points", "keywords",
			"resource_count", "fingerprint", "status", "last_error",
			"ai_config_id", "generated_at", "updated_at",
		}),
	}).Create(digest).Error
	if err != nil {
		return nil, false, err
	}
	return digest, true, nil
}

// markFailed 记录失败，但不覆盖已有内容 ——
// AI 挂一次就把孩子已经能看到的那段介绍清空，得不偿失。
func (h *DigestHandler) markFailed(categoryID uint64, cause error) {
	msg := cause.Error()
	if len([]rune(msg)) > 480 {
		msg = string([]rune(msg)[:480])
	}
	h.db.Model(&model.KnowledgeDigest{}).Where("category_id = ?", categoryID).
		Updates(map[string]interface{}{
			"status": model.DigestStatusFail, "last_error": msg,
		})
}

// digestFingerprint 内容指纹 = 送进提示词的那份素材的哈希。
//
// 口径刻意选成"提示词输入是否变化"，而不是"库里是否有任何变化"：
// 提示词里只放得下前 N 条标题，第 900 条标题改名根本不会影响 AI 的输出，
// 为此重新调一次 AI 纯属浪费额度。反过来，目录里新增了内容、
// 或者新内容挤进了前 N 条，指纹必然变 —— 该重算的一次不会漏。
func digestFingerprint(total int, childNames, titles []string) string {
	var sb strings.Builder
	sb.WriteString("n=")
	sb.WriteString(strconv.Itoa(total))
	sb.WriteString("\n|c=")
	sb.WriteString(strings.Join(childNames, "\u0001"))
	sb.WriteString("\n|t=")
	sb.WriteString(strings.Join(titles, "\u0001"))
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// scopeDisplayName 全站总览的名字；普通目录用它的真实名字。
func scopeDisplayName(cat *model.Category) string {
	if cat == nil {
		return "整个学习乐园"
	}
	return cat.Name
}

// ============ 素材准备 ============

// categorySubtreeIDs 求「目录自身 + 全部子孙」的 ID 集合。
//
// 用物化路径前缀求子孙，不递归 ParentID 链 —— 路径是建表时就维护好的，
// 一次查询够用；递归要按层查库，层数不定时还得考虑上限。
// 知识沉淀与 AI 整理助手都靠它划定"范围"，两处口径必须一致。
func categorySubtreeIDs(db *gorm.DB, root *model.Category) []uint64 {
	if root == nil {
		return nil
	}
	ids := []uint64{root.ID}
	if root.Path == "" {
		return ids
	}
	var desc []struct{ ID uint64 }
	// Model() 而不是 Table()：软删除条件必须由 GORM 补上
	db.Model(&model.Category{}).Where("path LIKE ?", root.Path+"%").
		Select("id").Find(&desc)
	for _, d := range desc {
		if d.ID != root.ID {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// digestScopeIDs 求「目录自身 + 全部子孙」的 ID 集合。
// cat == nil 表示全站（不按目录过滤）。
func (h *DigestHandler) digestScopeIDs(cat *model.Category) ([]uint64, bool) {
	if cat == nil {
		return nil, true
	}
	return categorySubtreeIDs(h.db, cat), false
}

func (h *DigestHandler) countInScope(ids []uint64, all bool) (int, error) {
	q := h.db.Model(&model.Resource{})
	if !all {
		q = q.Where("category_id IN ?", ids)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// material 准备送进提示词的三样东西：下级目录名、资源标题、少量描述。
func (h *DigestHandler) material(cat *model.Category) (names, titles, descs []string) {
	if cat == nil {
		names = h.topLevelSummary()
	} else {
		var children []model.Category
		h.db.Where("parent_id = ?", cat.ID).Order("sort_order asc, id asc").Find(&children)
		for _, ch := range children {
			names = append(names, ch.Name)
		}
	}

	q := h.db.Model(&model.Resource{})
	if cat != nil {
		q = q.Where("category_id IN ?", h.scopeIDsFor(cat))
	}
	// 取一批候选，再在内存里自然序排序：标题里的集数要按数字比，
	// 直接 ORDER BY title 会把「第 10 集」排到「第 2 集」前面。
	type row struct {
		Title       string
		Description string
	}
	var rows []row
	q.Select("title, description").Order("id desc").Limit(digestSampleSize).Scan(&rows)

	picked := make([]string, 0, len(rows))
	for _, r := range rows {
		t := strings.TrimSpace(r.Title)
		if t == "" {
			continue
		}
		picked = append(picked, t)
		if len(descs) < digestDescriptionSample {
			if d := strings.TrimSpace(r.Description); d != "" {
				descs = append(descs, t+"："+truncateRunesStr(d, 60))
			}
		}
	}

	sort.SliceStable(picked, func(i, j int) bool {
		return util.NaturalLess(picked[i], picked[j])
	})
	if len(picked) > ai.DigestMaxTitles {
		picked = picked[:ai.DigestMaxTitles]
	}
	return names, picked, descs
}

func (h *DigestHandler) scopeIDsFor(cat *model.Category) []uint64 {
	ids, _ := h.digestScopeIDs(cat)
	return ids
}

// topLevelSummary 全站总览用的一级目录摘要（带资源数）。
func (h *DigestHandler) topLevelSummary() []string {
	var roots []model.Category
	h.db.Where("parent_id IS NULL").Order("sort_order asc, id asc").Find(&roots)

	// 与搜索结果、目录树同口径：直接挂载 + 子孙累加
	_, counts := categoryMeta(h.db, nil, true)

	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if n := counts[r.ID]; n > 0 {
			out = append(out, r.Name+"（"+strconv.Itoa(int(n))+" 个）")
		} else {
			out = append(out, r.Name)
		}
	}
	return out
}

func (h *DigestHandler) breadcrumbOf(cat *model.Category) string {
	if cat == nil {
		return "整个学习乐园"
	}
	var cats []model.Category
	h.db.Select("id, name, path").Find(&cats)
	names := make(map[uint64]string, len(cats))
	for _, c := range cats {
		names[c.ID] = c.Name
	}
	return breadcrumbOf(cat.Path, names)
}

func truncateRunesStr(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// ============ 定时任务 ============

// RunRound 扫一轮：为内容最多的若干个范围生成沉淀。
//
// 排序用资源数倒序 —— 内容多的目录最需要一段"人话介绍"，
// 也最可能被孩子点开。每天额度固定，优先花在刀刃上。
func (h *DigestHandler) RunRound(logger *zap.Logger) {
	if logger == nil {
		logger = h.log()
	}
	if h.ai == nil {
		return
	}
	limit := h.DailyLimit
	if limit <= 0 {
		limit = defaultDigestDailyLimit
	}

	targets := h.roundTargets(limit)
	logger.Info("知识沉淀开始", zap.Int("targets", len(targets)))

	updated, skipped, failed := 0, 0, 0
	for _, id := range targets {
		_, generated, err := h.Generate(id, false)
		switch {
		case err != nil:
			failed++
			// AI 不可用是常态（额度/网络），记 warn 下轮再试
			logger.Warn("生成知识沉淀失败", zap.Uint64("categoryId", id), zap.Error(err))
		case generated:
			updated++
		default:
			skipped++
		}
	}
	logger.Info("知识沉淀完成",
		zap.Int("updated", updated), zap.Int("skipped", skipped), zap.Int("failed", failed))
}

// roundTargets 选出本轮要处理的范围（全站总览永远排第一）。
func (h *DigestHandler) roundTargets(limit int) []uint64 {
	var cats []model.Category
	h.db.Select("id, path, parent_id").Find(&cats)

	// 直接挂了资源的目录。Pluck 一次拿全，避免按目录逐个 COUNT。
	var withRes []uint64
	h.db.Model(&model.Resource{}).Where("category_id > 0").
		Distinct("category_id").Pluck("category_id", &withRes)

	byID := make(map[uint64]model.Category, len(cats))
	for _, c := range cats {
		byID[c.ID] = c
	}

	// "有资源"要向上传染给祖先：孩子多半从一级目录点进来，
	// 那儿空着最显眼。
	hit := make(map[uint64]bool, len(withRes))
	for _, id := range withRes {
		hit[id] = true
		cur, ok := byID[id]
		if !ok {
			continue
		}
		for cur.ParentID != nil {
			p, ok := byID[*cur.ParentID]
			if !ok {
				break
			}
			hit[p.ID] = true
			cur = p
		}
	}

	// 按"子孙资源总数"倒序，决定有限的额度先花在谁身上
	_, counts := categoryMeta(h.db, nil, true)
	ids := make([]uint64, 0, len(hit))
	for id := range hit {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ci, cj := counts[ids[i]], counts[ids[j]]
		if ci != cj {
			return ci > cj
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return append([]uint64{model.DigestScopeAll}, ids...)
}

// StartDigestScheduler 每天跑一轮知识沉淀。
// 与 StartAwardScheduler 同一套骨架：首轮延迟 + recover + stopCh。
func (h *DigestHandler) StartDigestScheduler(logger *zap.Logger, interval, firstDelay time.Duration) func() {
	if h == nil || logger == nil {
		return func() {}
	}
	stopCh := make(chan struct{})

	go func() {
		if firstDelay > 0 {
			select {
			case <-time.After(firstDelay):
			case <-stopCh:
				return
			}
		}

		runOnce := func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("知识沉淀 panic", zap.Any("recover", r))
				}
			}()
			h.RunRound(logger)
		}

		// 首轮延迟结束后**立刻跑一次**，再进入周期循环。
		//
		// 只建 ticker 是不够的：ticker 要等满一个 interval 才有第一次 tick，
		// 于是「首轮延迟 8 分钟」实际变成「首轮延迟 24 小时 8 分钟」——
		// 上线当天一条沉淀都不会生成，看起来像功能没生效。
		runOnce()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()
	return func() { close(stopCh) }
}
