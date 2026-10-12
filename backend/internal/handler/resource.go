package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// ResourceHandler 资源管理
type ResourceHandler struct {
	db     *gorm.DB
	perm   *service.PermissionService
	pansou *service.PansouClient
	// encryptor 与 importSvc 仅在「从存储源导入」时需要：
	// 要列出 WebDAV 目录内容就必须先解密账号凭据。
	// 其余接口不碰它们。
	encryptor *util.Encryptor
	importSvc *service.ImportService
}

func NewResourceHandler(db *gorm.DB, perm *service.PermissionService, pansou *service.PansouClient, encryptor *util.Encryptor) *ResourceHandler {
	return &ResourceHandler{
		db:        db,
		perm:      perm,
		pansou:    pansou,
		encryptor: encryptor,
		importSvc: service.NewImportService(db),
	}
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
		q = q.Where("title LIKE ?", "%"+keyword+"%")
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
		checkCtx, checkCancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		if results, err := h.pansou.CheckLinks(checkCtx, checkItems); err == nil {
			stateMap := make(map[string]service.CheckResult, len(results))
			for _, r := range results {
				stateMap[normalizeURL(r.URL)] = r
			}
			for i := range pageItems {
				if r, ok := stateMap[normalizeURL(pageItems[i].URL)]; ok {
					pageItems[i].Alive = r.State
					pageItems[i].AliveSummary = r.Summary
				}
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

	created, skipped, err := h.importSvc.Register(acc, req.CategoryID, targets)
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
