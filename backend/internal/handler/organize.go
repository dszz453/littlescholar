package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
		"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	// organizeAITimeout 规划阶段单次 AI 调用超时。
	// 比沉淀给得更宽：这里的素材更长、要模型输出结构化方案。
	organizeAITimeout = 150 * time.Second
	// organizeMaxSteps 方案里最多保留多少步（超出直接截断，防模型刷长度）
	organizeMaxSteps = 40
	// organizeMaxMove 单个移动步骤最多几张资源
	organizeMaxMove = 200
	// organizeMaxNewCats 单次方案最多新建多少目录
	organizeMaxNewCats = 12
)

// OrganizeStep 是 AI 整理方案里的一步。
//
// 它同时充当"AI 的输出"和"前端的输入"：前端拿到的步骤可以逐个取消勾选，
// 也可以直接原样提交回来执行。所以每个字段都要能被 JSON 往返，
// 校验结论（Valid / Invalid）也一并放在这里，让界面能直接显示
// "这一步为什么不能做"，而不是执行时才报错。
type OrganizeStep struct {
	Index int    `json:"index"`
	Type  string `json:"type"`

	Ref    string `json:"ref,omitempty"`
	Name   string `json:"name,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Parent string `json:"parent,omitempty"`

	ResourceIDs []uint64 `json:"resourceIds,omitempty"`
	Target      string   `json:"target,omitempty"`

	CategoryID uint64 `json:"categoryId,omitempty"`
	NewName    string `json:"newName,omitempty"`

	RemoteDirID   string `json:"remoteDirId,omitempty"`
	RemoteDirName string `json:"remoteDirName,omitempty"`
	AccountID     uint64 `json:"accountId,omitempty"`

	Reason string `json:"reason,omitempty"`

	// 校验结论
	Valid   bool   `json:"valid"`
	Invalid string `json:"invalidReason,omitempty"`

	// 展示辅助：把 cat:16 / ref:c1 解析成人看得懂的文字
	TargetLabel string   `json:"targetLabel,omitempty"`
	Preview     []string `json:"preview,omitempty"`
}

// OrganizeHandler 管理端的「AI 整理助手」。
type OrganizeHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	ai     *AIHandler
	logger *zap.Logger
	// 用来解开存储账号的凭据，好把选中远端目录的子目录列出来喂给 AI。
	// 为 nil 时跳过这一步（单测场景），整理流程照常可用。
	encryptor *util.Encryptor
}

func NewOrganizeHandler(db *gorm.DB, perm *service.PermissionService, aiH *AIHandler) *OrganizeHandler {
	return &OrganizeHandler{db: db, perm: perm, ai: aiH}
}

func (h *OrganizeHandler) WithLogger(l *zap.Logger) *OrganizeHandler {
	h.logger = l
	return h
}

// WithEncryptor 注入凭据解密器（链式，不动 NewOrganizeHandler 的签名）。
func (h *OrganizeHandler) WithEncryptor(e *util.Encryptor) *OrganizeHandler {
	h.encryptor = e
	return h
}

func (h *OrganizeHandler) log() *zap.Logger {
	if h.logger == nil {
		return zap.NewNop()
	}
	return h.logger
}

// ============ 生成方案 ============

type organizePlanRequest struct {
	CategoryID uint64 `json:"categoryId"`
	// 接入源信息可选：不填就不允许 AI 提出 createMapping
	AccountID       uint64 `json:"accountId"`
	RemoteDirPrefix string `json:"remoteDirPrefix"`
	Instruction     string `json:"instruction"`
	// Refresh 强制重新生成（否则返回上次的方案缓存）
	Refresh bool `json:"refresh"`
}

type organizeScopeInfo struct {
	CategoryID    uint64 `json:"categoryId"`
	Name          string `json:"name"`
	Breadcrumb    string `json:"breadcrumb"`
	ResourceCount int    `json:"resourceCount"`
	CategoryCount int    `json:"categoryCount"`
	AccountName   string `json:"accountName,omitempty"`
}

type organizeStats struct {
	CreateCategory int `json:"createCategory"`
	RenameCategory int `json:"renameCategory"`
	MoveResource   int `json:"moveResource"`
	CreateMapping  int `json:"createMapping"`
	// AffectedResources 会被移动的资源总数（不含重复计数）
	AffectedResources int `json:"affectedResources"`
	Invalid           int `json:"invalid"`
}

type organizePlanResponse struct {
	Scope   organizeScopeInfo `json:"scope"`
	Summary string            `json:"summary"`
	Steps   []OrganizeStep    `json:"steps"`
	Stats   organizeStats     `json:"stats"`
}

// Plan 生成整理方案（只读，不写库）。
func (h *OrganizeHandler) Plan(c *gin.Context) {
	if h.ai == nil {
		util.ServerError(c, "未配置 AI 接口，请先在「AI 配置」里添加")
		return
	}
	var req organizePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.CategoryID == 0 {
		util.BadRequest(c, "请选择要整理的目录")
		return
	}

	oc, err := h.buildContext(req.CategoryID, req.AccountID, req.RemoteDirPrefix)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}
	if oc.resourceCount == 0 {
		util.BadRequest(c, "这个目录下还没有内容，没什么可整理的")
		return
	}

	client, _, err := h.ai.DefaultClient()
	if err != nil {
		util.ServerError(c, "AI 不可用: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), organizeAITimeout)
	defer cancel()

	var plan ai.OrganizePlanResult
	if err := client.ChatJSON(ctx, ai.BuildOrganizePrompt(ai.OrganizeData{
		ScopeID:          oc.scopeID,
		ScopeName:        oc.scopeName,
		Breadcrumb:       oc.breadcrumb,
		Categories:       oc.catRefs,
		Resources:        oc.resRefs,
		CanCreateMapping: oc.accountID > 0,
		AccountID:        oc.accountID,
		RemoteDirPrefix:  oc.remotePrefix,
		RemoteDirs:       oc.remoteDirs,
		Instruction:      req.Instruction,
	}), &plan); err != nil {
		util.ServerError(c, "AI 生成方案失败: "+err.Error())
		return
	}
	ai.NormalizeOrganizePlan(&plan)

	steps := h.toSteps(plan.Steps)
	steps = h.validateSteps(steps, oc)

	resp := organizePlanResponse{
		Scope: organizeScopeInfo{
			CategoryID:    oc.scopeID,
			Name:          oc.scopeName,
			Breadcrumb:    oc.breadcrumb,
			ResourceCount: oc.resourceCount,
			CategoryCount: len(oc.catRefs),
			AccountName:   oc.accountName,
		},
		Summary: strings.TrimSpace(plan.Summary),
		Steps:   steps,
		Stats:   h.statsOf(steps),
	}
	util.OK(c, resp)
}

// ============ 执行方案 ============

type organizeApplyRequest struct {
	CategoryID      uint64         `json:"categoryId"`
	Steps           []OrganizeStep `json:"steps"`
	DryRun          bool           `json:"dryRun"`
	AccountID       uint64         `json:"accountId"`
	RemoteDirPrefix string         `json:"remoteDirPrefix"`
}

type organizeStepResult struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	// CreatedID 新建目录 / 映射时返回新记录的主键，方便前端跳转
	CreatedID uint64 `json:"createdId,omitempty"`
}

type organizeApplyResponse struct {
	DryRun  bool                 `json:"dryRun"`
	Applied int                  `json:"applied"`
	Failed  int                  `json:"failed"`
	Results []organizeStepResult `json:"results"`
}

// Apply 执行方案。
//
// 关键安全约束：**不信任前端提交的步骤**。每次都用请求里的 categoryId
// 重新构建范围上下文并重新校验一遍，否则改一下请求体就能拿这个接口
// 去移动任意目录下的资源。
func (h *OrganizeHandler) Apply(c *gin.Context) {
	var req organizeApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.CategoryID == 0 {
		util.BadRequest(c, "请选择要整理的目录")
		return
	}
	if len(req.Steps) == 0 {
		util.BadRequest(c, "没有要执行的步骤")
		return
	}
	if len(req.Steps) > organizeMaxSteps {
		req.Steps = req.Steps[:organizeMaxSteps]
	}

	oc, err := h.buildContext(req.CategoryID, req.AccountID, req.RemoteDirPrefix)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	steps := h.toStepsFromDTO(req.Steps)
	steps = h.validateSteps(steps, oc)

	resp := organizeApplyResponse{DryRun: req.DryRun, Results: []organizeStepResult{}}
	if req.DryRun {
		for _, s := range steps {
			message := "可以执行"
			if !s.Valid {
				message = s.Invalid
			}
			resp.Results = append(resp.Results, organizeStepResult{
				Index: s.Index, Type: s.Type, OK: s.Valid, Message: message,
			})
		}
		util.OK(c, resp)
		return
	}

	refs := map[string]uint64{}
	changedCategory := false

	for _, s := range steps {
		if !s.Valid {
			resp.Failed++
			resp.Results = append(resp.Results, organizeStepResult{
				Index: s.Index, Type: s.Type, OK: false, Message: s.Invalid,
			})
			continue
		}

		msg, createdID, execErr := h.execStep(s, refs, oc)
		if execErr != nil {
			resp.Failed++
			resp.Results = append(resp.Results, organizeStepResult{
				Index: s.Index, Type: s.Type, OK: false, Message: execErr.Error(),
			})
			continue
		}
		resp.Applied++
		resp.Results = append(resp.Results, organizeStepResult{
			Index: s.Index, Type: s.Type, OK: true, Message: msg, CreatedID: createdID,
		})
		if s.Type == ai.OrganizeTypeCreateCategory || s.Type == ai.OrganizeTypeRenameCategory {
			changedCategory = true
		}
	}

	// 目录层级变了 → 父目录授权的隐式覆盖范围也变了，必须清权限缓存
	if changedCategory && h.perm != nil {
		h.perm.InvalidateAll()
	}
	util.OK(c, resp)
}

// execStep 执行单步。
//
// 每步一个事务：任何一步失败都只影响它自己，后面的步骤继续 ——
// 整理是"能改一点是一点"的场景，因为第 5 步失败就把前 4 步回滚，
// 用户会觉得整个功能不可靠（而且前 4 步本来也是他确认要做的）。
func (h *OrganizeHandler) execStep(step OrganizeStep, refs map[string]uint64, oc *organizeContext) (string, uint64, error) {
	switch step.Type {
	case ai.OrganizeTypeCreateCategory:
		return h.execCreateCategory(step, refs, oc)
	case ai.OrganizeTypeRenameCategory:
		return h.execRenameCategory(step, oc)
	case ai.OrganizeTypeMoveResource:
		return h.execMoveResource(step, refs, oc)
	case ai.OrganizeTypeCreateMapping:
		return h.execCreateMapping(step, refs, oc)
	}
	return "", 0, fmt.Errorf("不支持的步骤类型: %s", step.Type)
}

func (h *OrganizeHandler) execCreateCategory(step OrganizeStep, refs map[string]uint64, oc *organizeContext) (string, uint64, error) {
	parentID, err := h.resolveParent(step.Parent, refs, oc)
	if err != nil {
		return "", 0, err
	}
	var parent model.Category
	if err := h.db.First(&parent, parentID).Error; err != nil {
		return "", 0, errors.New("父目录不存在")
	}

	var maxSort int
	// 用 Row() 而不是 Scan(&int) 之外的自定义结构：聚合别名配自定义结构
	// 会被 GORM 按模型 schema 静默丢列（本项目踩过的坑）。
	h.db.Model(&model.Category{}).Where("parent_id = ?", parentID).
		Select("coalesce(max(sort_order), 0)").Row().Scan(&maxSort)

	cat := &model.Category{
		ParentID:  &parentID,
		Name:      step.Name,
		Icon:      step.Icon,
		Level:     parent.Level + 1,
		SortOrder: maxSort + 1,
		// 新目录沿用父目录的公开性：在公开目录下建一个私有子目录，
		// 孩子会看到父目录"少了一块"，比全公开更好理解
		IsPublic: parent.IsPublic,
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(cat).Error; err != nil {
			return err
		}
		return tx.Model(cat).Update("path", model.BuildPath(parent.Path, cat.ID)).Error
	})
	if err != nil {
		return "", 0, err
	}

	if step.Ref != "" {
		refs[step.Ref] = cat.ID
	}
	return fmt.Sprintf("已新建目录「%s」", cat.Name), cat.ID, nil
}

func (h *OrganizeHandler) execRenameCategory(step OrganizeStep, oc *organizeContext) (string, uint64, error) {
	var cat model.Category
	if err := h.db.First(&cat, step.CategoryID).Error; err != nil {
		return "", 0, errors.New("目录不存在")
	}
	old := cat.Name
	if err := h.db.Model(&model.Category{}).Where("id = ?", cat.ID).
		Update("name", step.NewName).Error; err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("「%s」已改名为「%s」", old, step.NewName), cat.ID, nil
}

func (h *OrganizeHandler) execMoveResource(step OrganizeStep, refs map[string]uint64, oc *organizeContext) (string, uint64, error) {
	targetID, err := h.resolveTarget(step.Target, refs, oc)
	if err != nil {
		return "", 0, err
	}
	err = h.db.Model(&model.Resource{}).Where("id IN ?", step.ResourceIDs).
		Update("category_id", targetID).Error
	if err != nil {
		return "", 0, err
	}
	label := oc.nameOf(targetID)
	return fmt.Sprintf("已移动 %d 个内容到「%s」", len(step.ResourceIDs), label), targetID, nil
}

func (h *OrganizeHandler) execCreateMapping(step OrganizeStep, refs map[string]uint64, oc *organizeContext) (string, uint64, error) {
	targetID, err := h.resolveTarget(step.Target, refs, oc)
	if err != nil {
		return "", 0, err
	}
	accountID := step.AccountID
	if accountID == 0 {
		accountID = oc.accountID
	}
	name := step.RemoteDirName
	if name == "" {
		name = oc.nameOf(targetID) + " 映射"
	}
	m := &model.ResourceMapping{
		Name:          name,
		AccountID:     accountID,
		RemoteDirID:   step.RemoteDirID,
		RemoteDirName: step.RemoteDirName,
		CategoryID:    targetID,
		Recursive:     true,
		Enabled:       true,
	}
	if err := h.db.Create(m).Error; err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("已登记映射「%s」→「%s」", step.RemoteDirID, oc.nameOf(targetID)), m.ID, nil
}

// ============ 引用解析 ============

func (h *OrganizeHandler) resolveParent(ref string, refs map[string]uint64, oc *organizeContext) (uint64, error) {
	switch {
	case ref == "" || ref == "scope":
		return oc.scopeID, nil
	case strings.HasPrefix(ref, "ref:"):
		id, ok := refs[strings.TrimPrefix(ref, "ref:")]
		if !ok {
			return 0, fmt.Errorf("引用的新建目录 %s 尚不存在（步骤顺序不对？）", ref)
		}
		return id, nil
	case strings.HasPrefix(ref, "cat:"):
		id, err := parseCatRef(ref)
		if err != nil {
			return 0, err
		}
		return id, nil
	}
	return 0, fmt.Errorf("无法识别的目录引用: %s", ref)
}

func (h *OrganizeHandler) resolveTarget(ref string, refs map[string]uint64, oc *organizeContext) (uint64, error) {
	if ref == "" || ref == "scope" {
		return oc.scopeID, nil
	}
	return h.resolveParent(ref, refs, oc)
}

func parseCatRef(ref string) (uint64, error) {
	var id uint64
	if _, err := fmt.Sscanf(strings.TrimPrefix(ref, "cat:"), "%d", &id); err != nil || id == 0 {
		return 0, fmt.Errorf("无法识别的目录引用: %s", ref)
	}
	return id, nil
}

// ============ 上下文与校验 ============

type organizeContext struct {
	scopeID    uint64
	scopeName  string
	breadcrumb string
	accountID  uint64
	accountName string
	remotePrefix string
	// remoteDirs 选中远端目录的直接子目录；remoteDirIDs 是它们的 id 集合，
	// 用来判断 AI 填的 remoteDirId 是不是我们给过它的那些。
	remoteDirs  []ai.OrganizeRemoteRef
	remoteDirIDs map[string]bool

	scopeIDs map[uint64]bool
	catByID  map[uint64]model.Category
	// resByID 只装**范围内**的资源：越界的 ID 查不到，天然被拒
	resByID map[uint64]model.Resource
	// siblingNames[parentID] = 该父目录下已有的小写名字集合
	siblingNames map[uint64]map[string]bool

	resourceCount int
	catRefs       []ai.OrganizeCatRef
	resRefs       []ai.OrganizeResRef
}

func (oc *organizeContext) nameOf(id uint64) string {
	if id == oc.scopeID {
		return oc.scopeName
	}
	if c, ok := oc.catByID[id]; ok {
		return c.Name
	}
	return fmt.Sprintf("#%d", id)
}

// buildContext 装载整理范围的全部上下文。
//
// 范围 = 选定目录 + 全部子孙。所有校验都以这份上下文为准，
// 因此"越界"不是靠字符串比对挡住的，而是根本查不到那条记录。
func (h *OrganizeHandler) buildContext(categoryID, accountID uint64, remotePrefix string) (*organizeContext, error) {
	var root model.Category
	if err := h.db.First(&root, categoryID).Error; err != nil {
		return nil, errors.New("目录不存在")
	}

	ids := categorySubtreeIDs(h.db, &root)
	scope := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		scope[id] = true
	}

	oc := &organizeContext{
		scopeID:      root.ID,
		scopeName:    root.Name,
		accountID:    accountID,
		remotePrefix: strings.TrimSpace(remotePrefix),
		scopeIDs:     scope,
		catByID:      map[uint64]model.Category{},
		resByID:      map[uint64]model.Resource{},
		siblingNames: map[uint64]map[string]bool{},
		remoteDirIDs: map[string]bool{},
	}

	// 把选中远端目录的**直接子目录**列出来。
	//
	// 这是「自动匹配」能不能成立的关键一步：没有这份清单，AI 只知道一个
	// 目录 id，对里面有什么一无所知，只能瞎编 remoteDirId，然后被校验全部拒掉，
	// 用户看到的就是"它从来不建议映射"。
	//
	// 失败一律**不阻断**整理：退回"没有远端清单"，此时提示词会明确要求
	// AI 不要提 createMapping —— 宁可少给建议，也不能让它编造路径。
	if h.encryptor != nil && accountID > 0 {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, cred, a, err := loadSource(h.db, h.encryptor, accountID)
			if err != nil {
				h.log().Warn("整理助手：加载接入源失败", zap.Error(err))
				return
			}
			items, err := a.ListDir(ctx, cred, oc.remotePrefix)
			if err != nil {
				h.log().Warn("整理助手：列远端目录失败", zap.Error(err))
				return
			}
			for _, it := range items {
				if !it.IsDir {
					continue
				}
				if len(oc.remoteDirs) >= ai.OrganizeMaxRemoteDirs {
					break
				}
				oc.remoteDirs = append(oc.remoteDirs, ai.OrganizeRemoteRef{ID: it.ID, Name: it.Name})
				oc.remoteDirIDs[it.ID] = true
			}
		}()
	}

	// 目录：范围内全部 + 面包屑所需的祖先
	var cats []model.Category
	h.db.Select("id, name, path, parent_id, level").Find(&cats)
	nameByID := make(map[uint64]string, len(cats))
	for _, c := range cats {
		nameByID[c.ID] = c.Name
	}
	for _, c := range cats {
		if !scope[c.ID] {
			continue
		}
		oc.catByID[c.ID] = c
		pid := uint64(0)
		if c.ParentID != nil {
			pid = *c.ParentID
		}
		if _, ok := oc.siblingNames[pid]; !ok {
			oc.siblingNames[pid] = map[string]bool{}
		}
		oc.siblingNames[pid][strings.ToLower(c.Name)] = true
	}
	// scope 根自己的"父级名字集合"用来查重名
	if root.ParentID != nil {
		if _, ok := oc.siblingNames[*root.ParentID]; !ok {
			oc.siblingNames[*root.ParentID] = map[string]bool{}
		}
	} else if _, ok := oc.siblingNames[0]; !ok {
		oc.siblingNames[0] = map[string]bool{}
	}

	var resources []model.Resource
	h.db.Select("id, title, category_id").Where("category_id IN ?", ids).Find(&resources)
	direct := map[uint64]int{}
	for _, r := range resources {
		oc.resByID[r.ID] = r
		direct[r.CategoryID]++
	}
	oc.resourceCount = len(resources)

	for _, c := range cats {
		if !scope[c.ID] {
			continue
		}
		pid := uint64(0)
		if c.ParentID != nil {
			pid = *c.ParentID
		}
		oc.catRefs = append(oc.catRefs, ai.OrganizeCatRef{
			ID: c.ID, Name: c.Name, ParentID: pid, Direct: direct[c.ID],
		})
	}
	for _, r := range resources {
		oc.resRefs = append(oc.resRefs, ai.OrganizeResRef{
			ID: r.ID, Title: r.Title, CategoryID: r.CategoryID,
		})
	}
	oc.breadcrumb = breadcrumbOf(root.Path, nameByID)

	if accountID > 0 {
		var acc model.DiskAccount
		if err := h.db.First(&acc, accountID).Error; err != nil {
			return nil, errors.New("接入源账号不存在")
		}
		oc.accountName = acc.Nickname
		if oc.accountName == "" {
			oc.accountName = acc.DiskType
		}
	}
	return oc, nil
}

// toSteps 把 AI 的原始输出转成带序号的 DTO。
func (h *OrganizeHandler) toSteps(in []ai.OrganizeStepResult) []OrganizeStep {
	out := make([]OrganizeStep, 0, len(in))
	for i, s := range in {
		out = append(out, OrganizeStep{
			Index: i, Type: s.Type,
			Ref: s.Ref, Name: s.Name, Icon: s.Icon, Parent: s.Parent,
			ResourceIDs: s.ResourceIDs, Target: s.Target,
			CategoryID: s.CategoryID, NewName: s.NewName,
			RemoteDirID: s.RemoteDirID, RemoteDirName: s.RemoteDirName,
			Reason: s.Reason,
		})
	}
	return out
}

// toStepsFromDTO 处理"前端回传"的形态（字段已经和 DTO 一致，只需补序号与清理旧结论）。
func (h *OrganizeHandler) toStepsFromDTO(in []OrganizeStep) []OrganizeStep {
	out := make([]OrganizeStep, 0, len(in))
	for i := range in {
		s := in[i]
		s.Index = i
		s.Valid = false
		s.Invalid = ""
		s.TargetLabel = ""
		s.Preview = nil
		out = append(out, s)
	}
	return out
}

// validateSteps 逐条校验并给出人话原因。
//
// 校验是"按顺序、带记忆"的：ref 必须在前面的步骤里定义过，
// 同一个资源不能被移动两次，同一个父目录下不能出现重名。
// 这些都是模型很容易犯、但执行起来会直接破坏数据结构的错。
func (h *OrganizeHandler) validateSteps(steps []OrganizeStep, oc *organizeContext) []OrganizeStep {
	declaredRefs := map[string]bool{}
	movedRes := map[uint64]int{}
	newCatCount := 0
	// 本次方案中新出现的目录名，用于查重（键是解析后的父目录 ID 或 ref 名）
	plannedNames := map[string]bool{}

	for i := range steps {
		s := &steps[i]
		switch s.Type {
		case ai.OrganizeTypeCreateCategory:
			if newCatCount >= organizeMaxNewCats {
				h.invalidate(s, fmt.Sprintf("一次最多新建 %d 个目录", organizeMaxNewCats))
				continue
			}
			if s.Name == "" {
				h.invalidate(s, "缺少目录名称")
				continue
			}
			if len([]rune(s.Name)) > 50 {
				h.invalidate(s, "目录名称太长")
				continue
			}
			if s.Ref == "" {
				s.Ref = fmt.Sprintf("c%d", i)
			}
			if declaredRefs[s.Ref] {
				h.invalidate(s, fmt.Sprintf("ref 名 %s 重复了", s.Ref))
				continue
			}
			parentID, parentKey, err := h.checkParent(s.Parent, declaredRefs, oc)
			if err != nil {
				h.invalidate(s, err.Error())
				continue
			}
			// 严格重名检查（对已有目录 + 本次计划里已排的目录）
			key := fmt.Sprintf("%s|%s", parentKey, strings.ToLower(s.Name))
			if plannedNames[key] || (parentID > 0 && oc.siblingNames[parentID][strings.ToLower(s.Name)]) {
				h.invalidate(s, fmt.Sprintf("「%s」下已经有同名的「%s」了", oc.nameOf(parentID), s.Name))
				continue
			}
			if s.Icon != "" && !knownIconKeys[s.Icon] {
				s.Icon = "folder"
			}
			if s.Icon == "" {
				s.Icon = "folder"
			}
			plannedNames[key] = true
			declaredRefs[s.Ref] = true
			newCatCount++
			s.Valid = true
			s.TargetLabel = oc.nameOf(parentID)

		case ai.OrganizeTypeRenameCategory:
			cat, ok := oc.catByID[s.CategoryID]
			if !ok || s.CategoryID == oc.scopeID {
				h.invalidate(s, "只能给范围内的子目录改名")
				continue
			}
			if s.NewName == "" {
				h.invalidate(s, "缺少新名称")
				continue
			}
			if s.NewName == cat.Name {
				h.invalidate(s, "名字没有变化")
				continue
			}
			pid := uint64(0)
			if cat.ParentID != nil {
				pid = *cat.ParentID
			}
			key := fmt.Sprintf("cat:%d|%s", pid, strings.ToLower(s.NewName))
			if oc.siblingNames[pid][strings.ToLower(s.NewName)] || plannedNames[key] {
				h.invalidate(s, fmt.Sprintf("同级下已经有叫「%s」的目录了", s.NewName))
				continue
			}
			plannedNames[key] = true
			s.Valid = true
			s.TargetLabel = fmt.Sprintf("%s → %s", cat.Name, s.NewName)

		case ai.OrganizeTypeMoveResource:
			if len(s.ResourceIDs) == 0 {
				h.invalidate(s, "没有指定要移动的内容")
				continue
			}
			if len(s.ResourceIDs) > organizeMaxMove {
				h.invalidate(s, fmt.Sprintf("一步最多移动 %d 个内容", organizeMaxMove))
				continue
			}
			targetID, label, err := h.checkTarget(s.Target, declaredRefs, oc)
			if err != nil {
				h.invalidate(s, err.Error())
				continue
			}
			okIDs := make([]uint64, 0, len(s.ResourceIDs))
			alreadyThere := 0
			duplicated := false
			for _, id := range s.ResourceIDs {
				r, exist := oc.resByID[id]
				if !exist {
					continue // 不在范围内的 ID 直接丢掉，不报错打断整步
				}
				if movedRes[id] > 0 {
					duplicated = true
					continue
				}
				if r.CategoryID == targetID {
					alreadyThere++
				}
				okIDs = append(okIDs, id)
			}
			if len(okIDs) == 0 {
				if duplicated {
					h.invalidate(s, "这些内容已经在其他步骤里被移动过了")
				} else if alreadyThere > 0 {
					h.invalidate(s, "这些内容已经在这个目录里了")
				} else {
					h.invalidate(s, "没有找到可移动的内容")
				}
				continue
			}
			s.ResourceIDs = okIDs
			for _, id := range okIDs {
				movedRes[id] = i + 1
			}
			s.Valid = true
			s.TargetLabel = label
			s.Preview = resPreview(oc, okIDs)

		case ai.OrganizeTypeCreateMapping:
			if oc.accountID == 0 {
				h.invalidate(s, "没有指定接入源账号，无法新建映射")
				continue
			}
			if s.RemoteDirID == "" {
				h.invalidate(s, "缺少远端目录路径")
				continue
			}
			if !oc.remoteDirAllowed(s.RemoteDirID) {
				h.invalidate(s, "这个远端目录不在给出的清单里")
				continue
			}
			_, label, err := h.checkTarget(s.Target, declaredRefs, oc)
			if err != nil {
				h.invalidate(s, err.Error())
				continue
			}
			var dup int64
			h.db.Model(&model.ResourceMapping{}).
				Where("account_id = ? AND remote_dir_id = ?", oc.accountID, s.RemoteDirID).
				Count(&dup)
			if dup > 0 {
				h.invalidate(s, "这个远端目录已经登记过映射了")
				continue
			}
			s.AccountID = oc.accountID
			s.Valid = true
			s.TargetLabel = label

		default:
			h.invalidate(s, "不认识的步骤类型")
		}
	}
	return steps
}

// remoteDirAllowed 判断 AI 填的远端目录可不可信。
//
// 老写法是「必须以 remotePrefix 开头」，因为那时用户在界面上手填一个路径前缀。
// 现在用户是在目录树里选一个具体目录，传下来的是它的 id —— 夸克是 fs_id、
// 本地硬盘/WebDAV 是路径。fs_id 是随机数字，**父子之间没有前缀关系**，
// 对这类源拿前缀判断等于把 AI 的建议全拒掉。
//
// 所以改成三条并列：
//  1. 命中我们提供给 AI 的清单 → 放行（这是主路径）；
//  2. 等于 selected 目录本身 → 放行（用户直接选了这一层）；
//  3. 以它为前缀 → 放行（路径型 id 能靠前缀判断后代，覆盖本地/WebDAV 的深层目录）。
// 既没有清单也没有前缀时不做限制，保持旧行为。
func (oc *organizeContext) remoteDirAllowed(id string) bool {
	if id == "" {
		return false
	}
	if oc.remoteDirIDs[id] {
		return true
	}
	if oc.remotePrefix == "" {
		return len(oc.remoteDirIDs) == 0
	}
	return id == oc.remotePrefix || strings.HasPrefix(id, oc.remotePrefix)
}

func (h *OrganizeHandler) invalidate(s *OrganizeStep, reason string) {
	s.Valid = false
	s.Invalid = reason
	s.TargetLabel = ""
	s.Preview = nil
}

func (h *OrganizeHandler) checkParent(ref string, declared map[string]bool, oc *organizeContext) (uint64, string, error) {
	switch {
	case ref == "" || ref == "scope":
		return oc.scopeID, "scope", nil
	case strings.HasPrefix(ref, "ref:"):
		name := strings.TrimPrefix(ref, "ref:")
		if !declared[name] {
			return 0, "", fmt.Errorf("引用的新建目录 %s 还没有被创建", name)
		}
		// 新建目录的父目录在本次方案里，重名只与计划内其它新建项比
		return 0, "ref:" + name, nil
	case strings.HasPrefix(ref, "cat:"):
		id, err := parseCatRef(ref)
		if err != nil {
			return 0, "", err
		}
		if !oc.scopeIDs[id] {
			return 0, "", errors.New("父目录不在整理范围内")
		}
		return id, fmt.Sprintf("cat:%d", id), nil
	}
	return 0, "", fmt.Errorf("无法识别的目录引用: %s", ref)
}

func (h *OrganizeHandler) checkTarget(ref string, declared map[string]bool, oc *organizeContext) (uint64, string, error) {
	switch {
	case ref == "" || ref == "scope":
		return oc.scopeID, oc.scopeName, nil
	case strings.HasPrefix(ref, "ref:"):
		name := strings.TrimPrefix(ref, "ref:")
		if !declared[name] {
			return 0, "", fmt.Errorf("引用的新建目录 %s 还没有被创建", name)
		}
		// 执行时能通过 refs 解析到真实 ID；校验阶段只确认"它存在"
		return 0, "新建目录 " + name, nil
	case strings.HasPrefix(ref, "cat:"):
		id, err := parseCatRef(ref)
		if err != nil {
			return 0, "", err
		}
		if !oc.scopeIDs[id] {
			return 0, "", errors.New("目标目录不在整理范围内")
		}
		return id, oc.nameOf(id), nil
	}
	return 0, "", fmt.Errorf("无法识别的目录引用: %s", ref)
}

func (h *OrganizeHandler) statsOf(steps []OrganizeStep) organizeStats {
	var st organizeStats
	seen := map[uint64]bool{}
	for _, s := range steps {
		if !s.Valid {
			st.Invalid++
			continue
		}
		switch s.Type {
		case ai.OrganizeTypeCreateCategory:
			st.CreateCategory++
		case ai.OrganizeTypeRenameCategory:
			st.RenameCategory++
		case ai.OrganizeTypeMoveResource:
			st.MoveResource++
			for _, id := range s.ResourceIDs {
				if !seen[id] {
					seen[id] = true
					st.AffectedResources++
				}
			}
		case ai.OrganizeTypeCreateMapping:
			st.CreateMapping++
		}
	}
	return st
}

// resPreview 取前几个资源标题，让用户在勾选时能看出这一步到底动了什么。
func resPreview(oc *organizeContext, ids []uint64) []string {
	const max = 5
	out := make([]string, 0, max)
	for _, id := range ids {
		if len(out) >= max {
			out = append(out, fmt.Sprintf("…等 %d 个", len(ids)))
			break
		}
		if r, ok := oc.resByID[id]; ok {
			out = append(out, r.Title)
		}
	}
	return out
}

// knownIconKeys 目录图标库的合法取值集合（来自 CategoryHandler.IconLibrary）。
var knownIconKeys = func() map[string]bool {
	m := make(map[string]bool, len(IconLibrary))
	for _, ic := range IconLibrary {
		if k, ok := ic["key"]; ok && k != "" {
			m[k] = true
		}
	}
	return m
}()
