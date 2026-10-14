package handler

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// likeOp 返回当前方言下"大小写不敏感的模糊匹配"操作符。
//
// 为什么要区分，而不是统一写 ILIKE：
//   - PostgreSQL 的 LIKE 区分大小写，ILIKE 不区分；且只有 ILIKE 能命中
//     `gin (title gin_trgm_ops)` 索引（配合 InitDB 里建的 pg_trgm 扩展）。
//   - SQLite（单测环境）**根本没有 ILIKE**，写了会直接报语法错。
//     它的 LIKE 对 ASCII 本就大小写不敏感，行为上与 ILIKE 等价。
func likeOp(db *gorm.DB) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return "ILIKE"
	}
	return "LIKE"
}

// scopedCategoryIDs 求当前请求可见的目录 ID 集合。
//
// 返回 unrestricted=true 表示不受限（管理员，可见全部）。
//
// 背景：`/app/resources`、`/app/resources/facets`、`/app/resources/:id`
// 三条路由此前**完全没有权限过滤** —— router 里的注释「权限在 AppTree
// 层面已过滤」只对目录树成立，资源接口本身可以被孩子用任意 categoryId
// 直接读。收口必须复用 AppTree 用的同一个 PermissionService，否则
// 目录树看得见/看不见与资源读得到/读不到会两套口径。
func (h *ResourceHandler) scopedCategoryIDs(c *gin.Context) (ids []uint64, unrestricted bool, err error) {
	if util.IsAdmin(c) {
		return nil, true, nil
	}
	user := currentUser(c)
	if user == nil {
		// 理论上 app 组必经 RequireActiveUser、user 一定存在。
		// 真走到这里说明中间件链被改动过 —— 按"什么都看不到"处理，
		// 安全优先，不因为配置疏漏而泄露数据。
		return []uint64{}, false, nil
	}
	ids, all, err := h.perm.AccessibleCategoryIDs(user)
	if err != nil {
		return nil, false, err
	}
	return ids, all, nil
}

// allowCategoryID 判断目录是否在可见集合内
func allowCategoryID(ids []uint64, unrestricted bool, categoryID uint64) bool {
	if unrestricted {
		return true
	}
	for _, id := range ids {
		if id == categoryID {
			return true
		}
	}
	return false
}

// ResourceHandler 资源管理
type ResourceHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	pansou *service.PansouClient
	// pancheck 存活性检测（PanCheck）。配置了它就用它 —— 它对平台的
	// 识别比 PanSou 的通用探测准，也是用户在「系统设置」里指定的那一个。
	pancheck *service.PancheckClient
	// encryptor 与 importSvc 仅在「从存储源导入」时需要：
	// 要列出 WebDAV 目录内容就必须先解密账号凭据。
	// 其余接口不碰它们。
	encryptor *util.Encryptor
	importSvc *service.ImportService
}

func NewResourceHandler(db *gorm.DB, perm *service.PermissionService, pansou *service.PansouClient,
	pancheck *service.PancheckClient, encryptor *util.Encryptor) *ResourceHandler {
	return &ResourceHandler{
		db:        db,
		perm:      perm,
		pansou:    pansou,
		pancheck:  pancheck,
		encryptor: encryptor,
		importSvc: service.NewImportService(db),
	}
}

// checkLinks 统一的链接存活性检测入口。
//
// 优先 PanCheck，未配置或调用失败时回退 PanSou 的检测接口。
// 返回 map[归一化URL]CheckResult —— 按 URL 建索引是刻意的：
// 两个引擎返回的都是链接字符串，而搜索结果里的链接常带参数，
// 直接用原串匹配会大面积对不上。
//
// 失败时返回空 map 而不是 error：有效性只是"锦上添花"的标注，
// 不该因为检测引擎挂了就让整个搜索失败。
func (h *ResourceHandler) checkLinks(ctx context.Context, items []service.CheckLinkItem) map[string]service.CheckResult {
	out := make(map[string]service.CheckResult, len(items))
	if len(items) == 0 {
		return out
	}

	if h.pancheck != nil && h.pancheck.Configured() {
		links := make([]string, 0, len(items))
		for _, it := range items {
			links = append(links, it.URL)
		}
		if states, err := h.pancheck.Check(ctx, links); err == nil {
			for _, it := range items {
				key := service.NormalizeShareURL(it.URL)
				state := states[key]
				if state == "" {
					state = service.AliveUncertain
				}
				out[key] = service.CheckResult{
					URL: it.URL, NormalizedURL: key,
					State: state, Summary: service.AliveSummary(state),
				}
			}
			return out
		}
	}

	if h.pansou != nil {
		if results, err := h.pansou.CheckLinks(ctx, items); err == nil {
			for _, r := range results {
				key := service.NormalizeShareURL(r.URL)
				out[key] = service.CheckResult{
					URL: r.URL, NormalizedURL: key,
					State: r.State, Summary: r.Summary,
				}
			}
		}
	}
	return out
}

// List 资源列表
func (h *ResourceHandler) List(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	categoryID := util.GetUintQuery(c, "categoryId", 0)
	keyword := c.Query("keyword")
	diskType := c.Query("diskType")
	status := c.Query("status")
	includeSub := c.Query("includeSub") == "true"

	q := h.db.Model(&model.Resource{})

	// 权限收口：孩子只应看到自己有权限的目录下的资源。
	// 管理员 unrestricted=true，不加任何限制。
	scopeIDs, unrestricted, err := h.scopedCategoryIDs(c)
	if err != nil {
		util.ServerError(c, "权限校验失败")
		return
	}
	if !unrestricted {
		if len(scopeIDs) == 0 {
			util.Page(c, 0, page, pageSize, []model.Resource{})
			return
		}
		q = q.Where("category_id IN ?", scopeIDs)
	}

	if categoryID > 0 {
		if includeSub {
			var cat model.Category
			if err := h.db.First(&cat, categoryID).Error; err == nil {
				var ids []uint64
				h.db.Model(&model.Category{}).Where("path LIKE ?", cat.Path+"%").Pluck("id", &ids)
				ids = append(ids, categoryID)
				q = q.Where("category_id IN ?", ids)
			}
		} else {
			q = q.Where("category_id = ?", categoryID)
		}
	}
	if keyword != "" {
		q = q.Where("title "+likeOp(h.db)+" ?", "%"+keyword+"%")
	}
	if diskType != "" {
		q = q.Where("disk_type = ?", diskType)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}

	var total int64
	q.Count(&total)

	var list []model.Resource
	if err := q.Preload("Category").Order("sort_order asc, id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.Page(c, total, page, pageSize, list)
}

// Facets 资源分布统计。
//
// GET /app/resources/facets?keyword=&categoryId=
//
// 给「搜系统」做网盘筛选用的：只列出**当前结果里真正存在**的网盘类型
// 及其数量。如果不管有没有内容都把 7 种网盘全列出来，孩子点进去
// 只会看到空白页，反而更困惑。
func (h *ResourceHandler) Facets(c *gin.Context) {
	keyword := c.Query("keyword")
	categoryID := util.GetUintQuery(c, "categoryId", 0)

	q := h.db.Model(&model.Resource{})

	// 与 List 同一套权限收口：facets 会泄露"某个目录下有多少资源"，
	// 不做过滤的话越权用户能靠数量推断出隐藏目录的内容规模。
	scopeIDs, unrestricted, err := h.scopedCategoryIDs(c)
	if err != nil {
		util.ServerError(c, "权限校验失败")
		return
	}
	if !unrestricted {
		if len(scopeIDs) == 0 {
			util.OK(c, gin.H{"total": 0, "diskTypes": []gin.H{}})
			return
		}
		q = q.Where("category_id IN ?", scopeIDs)
	}

	if keyword != "" {
		q = q.Where("title "+likeOp(h.db)+" ?", "%"+keyword+"%")
	}
	if categoryID > 0 {
		q = q.Where("category_id = ?", categoryID)
	}

	// ⚠️ 不用 Model().Select("... AS total").Scan(&自定义结构)：
	// gorm 会拿 Model 的 schema 核对返回列，别名 total 不在 Resource
	// 字段里就被静默丢弃（Total 恒为 0、不报错，SQLite 上还看不出来）。
	// 用 Rows() 手工按序扫描，只认 SELECT 顺序，与驱动无关。
	rows, err := q.Select("disk_type, count(*) as total").Group("disk_type").Rows()
	if err != nil {
		util.ServerError(c, "统计失败")
		return
	}
	defer rows.Close()

	list := make([]gin.H, 0, 8)
	var total int64
	for rows.Next() {
		var (
			diskType string
			cnt      int64
		)
		if err := rows.Scan(&diskType, &cnt); err != nil {
			util.ServerError(c, "统计失败")
			return
		}
		total += cnt
		if diskType == "" {
			// 手工创建、没有绑定网盘的资源归到"系统资源"
			list = append(list, gin.H{"diskType": "", "name": "系统资源", "total": cnt})
			continue
		}
		name := adapter.DiskTypeNames[adapter.DiskType(diskType)]
		if name == "" {
			name = diskType
		}
		list = append(list, gin.H{"diskType": diskType, "name": name, "total": cnt})
	}
	util.OK(c, gin.H{"total": total, "diskTypes": list})
}

// Detail 资源详情
func (h *ResourceHandler) Detail(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	var res model.Resource
	if err := h.db.Preload("Category").First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}

	// 权限收口：资源详情会带出 ShareURL/FileID 等可直接取流的信息，
	// 比列表更敏感，必须逐条校验所属目录的可见性。
	scopeIDs, unrestricted, err := h.scopedCategoryIDs(c)
	if err != nil {
		util.ServerError(c, "权限校验失败")
		return
	}
	if !allowCategoryID(scopeIDs, unrestricted, res.CategoryID) {
		util.NotFound(c, "资源不存在")
		return
	}

	util.OK(c, res)
}

type createResourceRequest struct {
	CategoryID  uint64   `json:"categoryId" binding:"required"`
	Title       string   `json:"title" binding:"required"`
	Description string   `json:"description"`
	DiskType    string   `json:"diskType"`
	ShareURL    string   `json:"shareUrl"`
	Passcode    string   `json:"passcode"`
	Cover       string   `json:"cover"`
	MediaType   string   `json:"mediaType"`
	Tags        []string `json:"tags"`
}

// Create 新增资源
func (h *ResourceHandler) Create(c *gin.Context) {
	var req createResourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "标题和目录不能为空")
		return
	}
	res := &model.Resource{
		CategoryID:  req.CategoryID,
		Title:       req.Title,
		Description: req.Description,
		DiskType:    req.DiskType,
		ShareURL:    req.ShareURL,
		Passcode:    req.Passcode,
		Cover:       req.Cover,
		MediaType:   orDefaultMedia(req.MediaType),
		// 手工建的资源也要记扩展名：转码判定靠它，不能只在导入链路上写。
		Ext:         service.ExtOfName(req.Title),
		Tags:        model.StringArray(req.Tags),
		Status:      model.ResourceStatusUnknown,
	}
	if err := h.db.Create(res).Error; err != nil {
		util.ServerError(c, "创建失败")
		return
	}
	util.OK(c, gin.H{"id": res.ID})
}

// BatchDelete 批量删除
func (h *ResourceHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []uint64 `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		util.BadRequest(c, "请选择要删除的资源")
		return
	}
	if err := h.db.Where("id IN ?", req.IDs).Delete(&model.Resource{}).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "删除成功", nil)
}

// Move 移动资源到其他目录
func (h *ResourceHandler) Move(c *gin.Context) {
	var req struct {
		IDs        []uint64 `json:"ids" binding:"required"`
		CategoryID uint64   `json:"categoryId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	if err := h.db.Model(&model.Resource{}).Where("id IN ?", req.IDs).
		Update("category_id", req.CategoryID).Error; err != nil {
		util.ServerError(c, "移动失败")
		return
	}
	util.OKWithMessage(c, "移动成功", nil)
}

// Update 更新资源
func (h *ResourceHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	var req struct {
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		CategoryID  *uint64   `json:"categoryId"`
		Cover       *string   `json:"cover"`
		SortOrder   *int      `json:"sortOrder"`
		Tags        *[]string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.CategoryID != nil {
		updates["category_id"] = *req.CategoryID
	}
	if req.Cover != nil {
		updates["cover"] = *req.Cover
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.Tags != nil {
		updates["tags"] = model.StringArray(*req.Tags)
	}
	if len(updates) == 0 {
		util.OK(c, res)
		return
	}
	if err := h.db.Model(&res).Updates(updates).Error; err != nil {
		util.ServerError(c, "更新失败")
		return
	}
	h.db.First(&res, id)
	util.OK(c, res)
}

// CheckAlive 批量存活检测
func (h *ResourceHandler) CheckAlive(c *gin.Context) {
	var req struct {
		IDs []uint64 `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		util.BadRequest(c, "请选择要检测的资源")
		return
	}
	var resources []model.Resource
	h.db.Where("id IN ?", req.IDs).Find(&resources)

	items := make([]service.CheckLinkItem, 0, len(resources))
	for _, r := range resources {
		if r.ShareURL == "" {
			continue
		}
		items = append(items, service.CheckLinkItem{
			DiskType: r.DiskType, URL: r.ShareURL, Password: r.Passcode,
		})
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	results, err := h.pansou.CheckLinks(ctx, items)
	if err != nil {
		util.ServerError(c, "检测服务调用失败: "+err.Error())
		return
	}

	// 回写状态
	byURL := make(map[string]model.Resource)
	for _, r := range resources {
		byURL[normalizeURL(r.ShareURL)] = r
	}
	now := time.Now()
	updated := 0
	for _, res := range results {
		if r, ok := byURL[normalizeURL(res.URL)]; ok {
			newStatus := mapAliveState(res.State)
			h.db.Model(&model.Resource{}).Where("id = ?", r.ID).Updates(map[string]interface{}{
				"status":          newStatus,
				"last_checked_at": now,
			})
			updated++
		}
	}
	util.OK(c, gin.H{"checked": len(items), "updated": updated, "results": results})
}

// normalizeURL 与 mapAliveState 委托给 service 的实现。
//
// 不在 handler 里各写一份的原因：手动检测（本文件）与定时巡检
// （service/resource_alive.go）必须用同一套判定，否则会出现
// "手动点检测说失效、定时巡检说存活"这种自相矛盾的结果。
func normalizeURL(u string) string { return service.NormalizeShareURL(u) }

func mapAliveState(state string) string { return service.MapAliveState(state) }

func orDefaultMedia(m string) string {
	if m == "" {
		return model.MediaTypeVideo
	}
	return m
}

// ============ 找一找（系统内全文检索）============

// searchCategoryLimit 目录命中上限。
// 目录是"往里走"的入口，没必要翻页；50 条足够覆盖任何合理的关键词。
const searchCategoryLimit = 50

// searchCategoryItem 目录搜索结果项
type searchCategoryItem struct {
	ID            uint64 `json:"id"`
	Name          string `json:"name"`
	Icon          string `json:"icon"`
	Color         string `json:"color"`
	Level         int    `json:"level"`
	Path          string `json:"path"`
	Breadcrumb    string `json:"breadcrumb"`    // 祖先名 / 自身名
	ResourceCount int64  `json:"resourceCount"` // 含子孙
}

// SearchAll 「找一找」：一次请求同时返回命中的**目录**与**资源**。
//
// 与下面 admin 的 Search（PanSou 全网搜）毫无关系 —— 这里只搜系统内
// 已收录的内容，数据边界严格等于孩子的可见目录集合。
// 需求里的"全文检索"在中文场景下的落地方式：
//   - PostgreSQL 走 `ILIKE '%kw%'`，由 InitDB 建的 pg_trgm GIN 索引支撑。
//     不建索引的话前置 `%` 会让 btree 彻底失效、退化成全表扫。
//   - 表结构不变（不引入 tsvector —— 中文没有内置分词，tsvector 用不了）。
//
// 资源匹配 title 或 description；目录匹配 name 或 description。
func (h *ResourceHandler) SearchAll(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("keyword"))
	diskType := c.Query("diskType")
	page, pageSize := util.GetPage(c)

	empty := gin.H{
		"keyword":    keyword,
		"categories": []searchCategoryItem{},
		"resources": util.PageData{
			Total: 0, Page: page, PageSize: pageSize, List: []model.Resource{},
		},
	}
	if keyword == "" {
		util.OK(c, empty)
		return
	}

	scopeIDs, unrestricted, err := h.scopedCategoryIDs(c)
	if err != nil {
		util.ServerError(c, "权限校验失败")
		return
	}
	if !unrestricted && len(scopeIDs) == 0 {
		util.OK(c, empty)
		return
	}

	op := likeOp(h.db)
	like := "%" + keyword + "%"

	// ---- 目录命中 ----
	catQuery := h.db.Model(&model.Category{}).
		Where("name "+op+" ? OR description "+op+" ?", like, like)
	if !unrestricted {
		catQuery = catQuery.Where("id IN ?", scopeIDs)
	}
	var cats []model.Category
	if err := catQuery.Order("level asc, sort_order asc, id asc").
		Limit(searchCategoryLimit).Find(&cats).Error; err != nil {
		util.ServerError(c, "搜索失败")
		return
	}

	// ---- 资源命中 ----
	// 用闭包重复构造，避免 Count 与 Find 互相污染 Statement。
	resBase := func() *gorm.DB {
		q := h.db.Model(&model.Resource{}).
			Where("title "+op+" ? OR description "+op+" ?", like, like)
		if !unrestricted {
			q = q.Where("category_id IN ?", scopeIDs)
		}
		if diskType != "" {
			q = q.Where("disk_type = ?", diskType)
		}
		return q
	}
	var total int64
	resBase().Count(&total)

	list := make([]model.Resource, 0, pageSize)
	if err := resBase().Preload("Category").
		Order("sort_order asc, id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error; err != nil {
		util.ServerError(c, "搜索失败")
		return
	}

	// ---- 目录展示所需的资源数与面包屑 ----
	names, counts := h.categoryMeta(scopeIDs, unrestricted)
	items := make([]searchCategoryItem, 0, len(cats))
	for i := range cats {
		cat := cats[i]
		items = append(items, searchCategoryItem{
			ID: cat.ID, Name: cat.Name, Icon: cat.Icon, Color: cat.Color,
			Level: cat.Level, Path: cat.Path,
			Breadcrumb:    breadcrumbOf(cat.Path, names),
			ResourceCount: counts[cat.ID],
		})
	}

	util.OK(c, gin.H{
		"keyword":    keyword,
		"categories": items,
		"resources": util.PageData{
			Total: total, Page: page, PageSize: pageSize, List: list,
		},
	})
}

// categoryMeta 返回 (id→名称, id→资源数) 两张表，供搜索结果渲染。
//
// 资源数按「直接挂载 + 子孙累加」口径 —— 孩子搜到一个大类时，
// 关心的是它底下总共有多少内容，而不是只有直接挂在它上面的那几个。
func (h *ResourceHandler) categoryMeta(scopeIDs []uint64, unrestricted bool) (map[uint64]string, map[uint64]int64) {
	var cats []model.Category
	h.db.Select("id, name, path").Find(&cats)

	names := make(map[uint64]string, len(cats))
	for _, cat := range cats {
		names[cat.ID] = cat.Name
	}

	// 各目录的直接资源数。用 Rows() 手工扫描 —— Model().Scan(&自定义结构)
	// 会按模型 schema 静默丢弃聚合别名列（详见笔记"GORM 聚合查询的静默丢列"）。
	direct := map[uint64]int64{}
	q := h.db.Table("resources").Select("category_id, count(*) as total")
	if !unrestricted {
		q = q.Where("category_id IN ?", scopeIDs)
	}
	if rows, err := q.Group("category_id").Rows(); err == nil {
		for rows.Next() {
			var (
				cid uint64
				n   int64
			)
			if err := rows.Scan(&cid, &n); err == nil {
				direct[cid] = n
			}
		}
		rows.Close()
	}

	counts := make(map[uint64]int64, len(cats))
	for _, cat := range cats {
		counts[cat.ID] = direct[cat.ID]
	}
	// 子孙累加到祖先：按物化路径前缀判定。目录表只有几十~几百行，
	// O(n²) 完全够用，且不依赖 ParentID 链是否完整。
	for _, child := range cats {
		n := direct[child.ID]
		if n == 0 || child.Path == "" {
			continue
		}
		for _, anc := range cats {
			if anc.ID == child.ID || anc.Path == "" {
				continue
			}
			if strings.HasPrefix(child.Path, anc.Path) {
				counts[anc.ID] += n
			}
		}
	}
	return names, counts
}

// breadcrumbOf 由物化路径（/1/12/35/）拼出「乐学英语 / 新概念英语」。
// 路径里查不到的 ID（目录已删但路径未更新）直接跳过，不产空洞。
func breadcrumbOf(path string, names map[uint64]string) string {
	parts := make([]string, 0, 4)
	var cur uint64
	inNum := false
	for i := 0; i <= len(path); i++ {
		var ch byte
		if i < len(path) {
			ch = path[i]
		}
		if i < len(path) && ch >= '0' && ch <= '9' {
			cur = cur*10 + uint64(ch-'0')
			inNum = true
			continue
		}
		if inNum {
			if n, ok := names[cur]; ok && n != "" {
				parts = append(parts, n)
			}
			cur = 0
			inNum = false
		}
	}
	return strings.Join(parts, " / ")
}

// ============ 搜索 ============

type searchRequest struct {
	Keyword    string   `json:"keyword" binding:"required"`
	DiskTypes  []string `json:"diskTypes"`
	Res        string   `json:"res"`
	Src        string   `json:"src"`
	CheckAlive bool     `json:"checkAlive"`
	Page       int      `json:"page"`
	PageSize   int      `json:"pageSize"`
}

// Search 聚合搜索（对接 PanSou）
func (h *ResourceHandler) Search(c *gin.Context) {
	var req searchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请输入搜索关键词")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	resp, err := h.pansou.Search(ctx, &service.SearchRequest{
		Keyword:   req.Keyword,
		DiskTypes: req.DiskTypes,
		Res:       orDefaultStr(req.Res, "merge"),
		Src:       req.Src,
	})
	if err != nil {
		util.ServerError(c, "搜索失败: "+err.Error())
		return
	}

	// 可选：顺带做存活检测（只检测当前页）
	if req.CheckAlive && len(resp.List) > 0 {
		page, pageSize := req.Page, req.PageSize
		if page < 1 {
			page = 1
		}
		if pageSize <= 0 {
			pageSize = 20
		}
		start := (page - 1) * pageSize
		end := start + pageSize
		if start > len(resp.List) {
			start = len(resp.List)
		}
		if end > len(resp.List) {
			end = len(resp.List)
		}
		pageItems := resp.List[start:end]

		checkItems := make([]service.CheckLinkItem, 0, len(pageItems))
		for _, it := range pageItems {
			checkItems = append(checkItems, service.CheckLinkItem{
				DiskType: it.DiskType, URL: it.URL, Password: it.Password,
			})
		}
		checkCtx, checkCancel := context.WithTimeout(c.Request.Context(), 50*time.Second)
		stateMap := h.checkLinks(checkCtx, checkItems)
		for i := range pageItems {
			if r, ok := stateMap[normalizeURL(pageItems[i].URL)]; ok {
				pageItems[i].Alive = r.State
				pageItems[i].AliveSummary = r.Summary
			}
		}
		checkCancel()
	}

	// 分页返回
	page, pageSize := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start > len(resp.List) {
		start = len(resp.List)
	}
	end := start + pageSize
	if end > len(resp.List) {
		end = len(resp.List)
	}

	util.OK(c, gin.H{
		"keyword":  req.Keyword,
		"total":    resp.Total,
		"costMs":   resp.CostMs,
		"page":     page,
		"pageSize": pageSize,
		"list":     resp.List[start:end],
	})
}

// Import 从搜索结果导入资源
func (h *ResourceHandler) Import(c *gin.Context) {
	var req struct {
		CategoryID uint64 `json:"categoryId" binding:"required"`
		Items      []struct {
			Title    string `json:"title"`
			DiskType string `json:"diskType"`
			ShareURL string `json:"shareUrl"`
			Passcode string `json:"passcode"`
			Note     string `json:"note"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	created, skipped := 0, 0
	for _, it := range req.Items {
		title := orDefaultStr(it.Title, it.Note)
		if title == "" || it.ShareURL == "" {
			skipped++
			continue
		}
		var existed int64
		h.db.Model(&model.Resource{}).Where("share_url = ?", it.ShareURL).Count(&existed)
		if existed > 0 {
			skipped++
			continue
		}
		res := &model.Resource{
			CategoryID: req.CategoryID,
			Title:      title,
			DiskType:   it.DiskType,
			ShareURL:   it.ShareURL,
			Passcode:   it.Passcode,
			MediaType:  model.MediaTypeVideo,
			Ext:        service.ExtOfName(title),
			Status:     model.ResourceStatusUnknown,
		}
		if err := h.db.Create(res).Error; err == nil {
			created++
		} else {
			skipped++
		}
	}
	util.OK(c, gin.H{"created": created, "skipped": skipped})
}

// ============ 从存储源导入资源 ============

type importFromDiskItem struct {
	FileID string `json:"fileId" binding:"required"`
	Title  string `json:"title"`
	Size   int64  `json:"size"`
	IsDir  bool   `json:"isDir"`
}

// ImportFromDisk 把存储源（WebDAV 等）里**已有的文件**登记为系统资源。
//
// 与 Import 的关键差异 ——
//
//	Import         输入是分享链接：文件还不存在，得先转存，
//	               所以只写 ShareURL，FileID 留空；
//	ImportFromDisk 输入是存储源里的实际文件：不需要转存，
//	               直接写 DiskAccountID + FileID。
//
// 播放由 handler.StreamHandler 代理取流，因为 WebDAV 直链离开
// Authorization 头就是 401，浏览器 <video> 又带不了这个头。
//
// 展开与登记的逻辑都在 service.ImportService，与「资源映射」的同步
// 共用同一份实现 —— 两处各写一遍必然会漂移。
func (h *ResourceHandler) ImportFromDisk(c *gin.Context) {
	var req struct {
		CategoryID uint64               `json:"categoryId" binding:"required"`
		AccountID  uint64               `json:"accountId" binding:"required"`
		Items      []importFromDiskItem `json:"items" binding:"required"`
		Recursive  bool                 `json:"recursive"`
		MaxFiles   int                  `json:"maxFiles"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误：需要 categoryId、accountId 与 items")
		return
	}
	if len(req.Items) == 0 {
		util.BadRequest(c, "请至少选择一个文件或目录")
		return
	}

	var cat model.Category
	if err := h.db.First(&cat, req.CategoryID).Error; err != nil {
		util.BadRequest(c, "目标目录不存在")
		return
	}
	acc, cred, a, err := loadSource(h.db, h.encryptor, req.AccountID)
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	items := make([]service.CollectItem, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, service.CollectItem{
			FileID: it.FileID, Title: it.Title, Size: it.Size, IsDir: it.IsDir,
		})
	}

	// 递归可能很久（NAS 大目录一次 PROPFIND 就要几十秒），
	// 所以这里的超时给到 3 分钟，而不是其它接口常用的 30s。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()

	targets, stats := service.CollectDiskFiles(ctx, a, cred, items, req.Recursive, req.MaxFiles)

	maxFiles := req.MaxFiles
	if maxFiles <= 0 || maxFiles > service.DefaultMaxImportFiles {
		maxFiles = service.DefaultMaxImportFiles
	}

	result := service.ImportResult{
		SkippedDirs:   stats.SkippedDirs,
		SkippedNonMed: stats.SkippedNonMed,
		Truncated:     stats.Truncated,
		MaxFiles:      maxFiles,
	}
	if len(targets) == 0 {
		result.Message = "没有找到可导入的媒体文件（视频/音频/PDF/图片）"
		util.OK(c, result)
		return
	}

	// mappingID 传 0：手动「浏览 → 勾选 → 导入」进来的资源不属于任何映射，
	// 不该在将来某次"删映射连带清理"里被误伤。
	created, skipped, err := h.importSvc.Register(acc, req.CategoryID, 0, targets)
	if err != nil {
		util.ServerError(c, "导入失败: "+err.Error())
		return
	}
	result.Created = created
	result.Skipped = skipped
	util.OK(c, result)
}

// SearchHealth 搜索服务健康检查
func (h *ResourceHandler) SearchHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	result := gin.H{"pansouOnline": false}
	if health, err := h.pansou.Health(ctx); err == nil {
		result["pansouOnline"] = true
		result["plugins"] = health["plugin_count"]
		result["channels"] = health["channels_count"]
	}
	util.OK(c, result)
}
