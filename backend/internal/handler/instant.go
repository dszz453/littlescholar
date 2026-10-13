package handler

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
)

// ============ 搜一搜 · 搜全网 · 即时转存播放 ============
//
// 这套能力的完整链路是：
//
//	搜全网（PanSou 聚合） → 选中一个分享 → 解析分享内容
//	→ 转存到「系统设置」里配置的默认网盘与目标目录
//	→ 递归找出可播放的媒体文件 → 登记为资源并直接给出播放地址
//
// 设计上刻意"转存 + 登记 + 播放"一次完成，而不是让用户先转存、
// 再回后台导入、再去播放页 —— 那样每多一步都多一个失败点。
//
// 与 xiaoya-alist / 影视类播放器的做法一致：因为网盘的直链大多带
// 防盗链头、且部分需要鉴权，播放在这里统一交回后端的 /api/stream 代理，
// 由代理补头并转发 Range，实现"边下边播、可拖进度"。

// 系统配置键（存在 system_configs，管理后台「系统设置」里可改）
const (
	CfgInstantEnabled    = "instant_enabled"     // 是否开启「搜全网」即时播放
	CfgInstantDiskType   = "instant_disk_type"   // 默认转存网盘类型
	CfgInstantAccountID  = "instant_account_id"  // 默认转存账号 ID
	CfgInstantRemoteDir  = "instant_remote_dir"  // 转存到网盘里的哪个目录（目录 ID，空=根目录）
	CfgInstantCategoryID = "instant_category_id" // 收录到系统里的哪个目录（空=自动用「搜全网」）
)

// instantCategoryName 自动创建的收录目录名
const instantCategoryName = "搜全网"

// cfgValue 读取一条系统配置（不存在返回空串）。
func (h *ResourceHandler) cfgValue(key string) string {
	var row model.SystemConfig
	if err := h.db.Where("key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	return row.Value
}

// cfgUint 读取一条数值型配置。
func (h *ResourceHandler) cfgUint(key string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(h.cfgValue(key)), 10, 64)
	return v
}

// ============ 搜全网 ============

// SearchGlobal 全网聚合搜索（孩子端「搜一搜 · 搜全网」）。
//
// 与后台的 Search 复用同一个 PanSou 客户端，差别只在鉴权层级
// （这里是 C 端登录用户，后台是管理员）与返回字段裁剪。
func (h *ResourceHandler) SearchGlobal(c *gin.Context) {
	var req struct {
		Keyword   string   `json:"keyword" binding:"required"`
		DiskTypes []string `json:"diskTypes"`
		Page      int      `json:"page"`
		PageSize  int      `json:"pageSize"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请输入搜索关键词")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	resp, err := h.pansou.Search(ctx, &service.SearchRequest{
		Keyword:   req.Keyword,
		DiskTypes: req.DiskTypes,
		Res:       "merge",
	})
	if err != nil {
		util.ServerError(c, "搜索失败: "+err.Error())
		return
	}

	page, pageSize := req.Page, req.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 50 {
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

	// 告诉前端"当前配置下能不能一键播放"，避免用户点了才发现
	// 后台根本没配默认转存网盘。
	//
	// availableDiskTypes：有哪些网盘类型已绑定账号 —— 前端据此把
	// "没有对应账号"的结果标灰。搜全网会返回各种网盘的结果，
	// 若用户只绑了夸克，那些百度/115 的结果点了必然失败，
	// 与其让人一次次撞墙，不如提前说清楚。
	util.OK(c, gin.H{
		"keyword":            req.Keyword,
		"total":              resp.Total,
		"costMs":             resp.CostMs,
		"page":               page,
		"pageSize":           pageSize,
		"list":               resp.List[start:end],
		"instantReady":       h.instantReady(),
		"instantTarget":      h.instantTargetLabel(),
		"availableDiskTypes": h.availableDiskTypes(),
	})
}

// availableDiskTypes 返回已绑定可用账号的网盘类型（不含存储源）。
func (h *ResourceHandler) availableDiskTypes() []string {
	var types []string
	h.db.Model(&model.DiskAccount{}).
		Where("status = ?", model.DiskAccountValid).
		Where("disk_type NOT IN ?", adapter.StorageTypes).
		Distinct().Pluck("disk_type", &types)
	if types == nil {
		types = []string{}
	}
	return types
}

// instantEnabled 判断即时转存播放是否可用。
//
// 默认**开启**：这个开关是给"我不希望孩子随手搜到全网资源"的家长
// 准备的刹车，而不是让新装好的人先去翻设置才能用。所以只在被显式
// 配成 false 时才关闭；没配过（空）视为开启，是否有账号则另行判断。
func (h *ResourceHandler) instantEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(h.cfgValue(CfgInstantEnabled)))
	return v != "false" && v != "0"
}

// instantReady 是否具备"一键播放"的条件：开了开关 **且** 有可用账号。
func (h *ResourceHandler) instantReady() bool {
	if !h.instantEnabled() {
		return false
	}
	_, err := h.pickAccount("", 0)
	return err == nil
}

// instantTargetLabel 给前端展示"会转存到哪里"，例如「夸克网盘 · 我的夸克」。
func (h *ResourceHandler) instantTargetLabel() string {
	acc, err := h.pickAccount("", 0)
	if err != nil {
		return ""
	}
	return adapter.DiskTypeNames[adapter.DiskType(acc.DiskType)] + " · " + acc.Nickname
}

// ============ 即时转存播放 ============

type instantPlayRequest struct {
	ShareURL string `json:"shareUrl" binding:"required"`
	Passcode string `json:"passcode"`
	Title    string `json:"title"`
	DiskType string `json:"diskType"`
	Note     string `json:"note"`
}

// InstantPlay 一键把全网搜到的分享转存并立即返回可播放地址。
func (h *ResourceHandler) InstantPlay(c *gin.Context) {
	var req instantPlayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "缺少分享链接")
		return
	}
	if !h.instantEnabled() {
		util.BadRequest(c, "「搜全网」即时播放未开启：请在后台「系统设置」里指定默认转存网盘与目录")
		return
	}

	// 1) 已经转存过的同一分享，直接复用，避免重复占用网盘空间
	var existed model.Resource
	if err := h.db.Where("share_url = ? AND file_id <> ''", strings.TrimSpace(req.ShareURL)).
		First(&existed).Error; err == nil {
		util.OK(c, h.instantPayload(&existed, nil, "已收录，直接播放"))
		return
	}

	// 2) 选账号：优先系统设置里指定的账号，否则该网盘的默认账号，再否则任意可用账号
	acc, err := h.pickAccount(req.DiskType, h.cfgUint(CfgInstantAccountID))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}
	_, cred, a, err := loadSource(h.db, h.encryptor, acc.ID)
	if err != nil {
		util.ServerError(c, "加载账号失败: "+err.Error())
		return
	}

	// 转存 + 递归找文件都可能耗时（网盘转存是异步任务），超时给足 3 分钟
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()

	// 3) 解析分享
	share, err := a.ParseShare(ctx, cred, strings.TrimSpace(req.ShareURL), strings.TrimSpace(req.Passcode))
	if err != nil {
		util.ServerError(c, "解析分享失败: "+err.Error())
		return
	}

	targetDirID := strings.TrimSpace(h.cfgValue(CfgInstantRemoteDir))

	// 4) 转存到目标目录
	if _, err := a.SaveToDir(ctx, cred, share, targetDirID); err != nil {
		util.ServerError(c, "转存失败: "+err.Error())
		return
	}

	// 5) 转存后重新列目录，按名字把"刚转存进来的条目"认出来。
	//
	// 为什么不直接用 SaveToDir 返回的 FileIDs：各家网盘转存后返回的
	// 往往是"分享里的原始 ID"，而落到自己网盘里会生成**新的** ID，
	// 直接拿旧 ID 去取直链必然失败。按文件名匹配是跨网盘最稳的做法。
	items, err := a.ListDir(ctx, cred, targetDirID)
	if err != nil {
		util.ServerError(c, "转存成功但读取目录失败: "+err.Error())
		return
	}
	picked := matchTransferred(share, items)
	if len(picked) == 0 {
		util.ServerError(c, "转存成功，但在目标目录里没有找到对应文件（可能是网盘延迟，请稍后重试）")
		return
	}

	// 6) 递归展开出真正可播放的媒体文件
	targets, _ := service.CollectDiskFiles(ctx, a, cred, picked, true, 200)
	if len(targets) == 0 {
		util.ServerError(c, "转存成功，但里面没有可播放的媒体文件（视频/音频/PDF/图片）")
		return
	}

	best := pickPlayable(targets, req.Title)

	// 7) 登记资源
	cat, err := h.instantCategory()
	if err != nil {
		util.ServerError(c, "获取收录目录失败: "+err.Error())
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Note)
	}
	if title == "" {
		title = stripExt(best.Title)
	}

	accID := acc.ID
	res := &model.Resource{
		CategoryID:    cat.ID,
		Title:         title,
		Description:   "来自「搜一搜 · 搜全网」即时转存",
		DiskType:      acc.DiskType,
		ShareURL:      strings.TrimSpace(req.ShareURL),
		Passcode:      strings.TrimSpace(req.Passcode),
		DiskAccountID: &accID,
		OwnerDirID:    targetDirID,
		FileID:        best.FileID,
		MediaType:     service.GuessMediaType(best.Title),
		TotalSize:     best.Size,
		Status:        model.ResourceStatusAlive,
		DirectPlay:    true, // 立即播放优先，跳过转码
	}
	if err := h.db.Create(res).Error; err != nil {
		util.ServerError(c, "保存资源失败")
		return
	}

	// 附带一份文件清单，供前端"查看"时展示
	files := make([]gin.H, 0, len(targets))
	for i, t := range targets {
		if i >= 50 {
			break
		}
		files = append(files, gin.H{"title": stripExt(t.Title), "size": t.Size})
	}
	util.OK(c, h.instantPayload(res, files, "已转存并开始播放"))
}

// instantPayload 组装返回体。
func (h *ResourceHandler) instantPayload(res *model.Resource, files []gin.H, msg string) gin.H {
	return gin.H{
		"resourceId": res.ID,
		"title":      res.Title,
		"playUrl":    fmt.Sprintf("/api/play/%d", res.ID),
		"mediaType":  res.MediaType,
		"fileCount":  len(files),
		"files":      files,
		"message":    msg,
	}
}

// pickAccount 挑选转存账号。
//
// 优先级：显式指定（且类型匹配）> 该类型的默认账号 > 该类型任意有效账号。
// 显式指定的账号类型不匹配时**静默降级**而不是报错 —— 用户在设置里
// 配的可能是很久以前删掉的账号，这时按类型兜底比直接失败更有用。
func (h *ResourceHandler) pickAccount(diskType string, preferID uint64) (*model.DiskAccount, error) {
	if preferID > 0 {
		var acc model.DiskAccount
		if err := h.db.First(&acc, preferID).Error; err == nil {
			if diskType == "" || acc.DiskType == diskType {
				if adapter.IsSupported(adapter.DiskType(acc.DiskType)) {
					return &acc, nil
				}
			}
		}
	}

	q := h.db.Model(&model.DiskAccount{}).
		Where("status = ?", model.DiskAccountValid).
		// 存储源（WebDAV/本地硬盘）不能用来转存分享，排除掉
		Where("disk_type NOT IN ?", adapter.StorageTypes)
	if diskType != "" {
		q = q.Where("disk_type = ?", diskType)
	}
	var acc model.DiskAccount
	if err := q.Order("is_default desc, id asc").First(&acc).Error; err != nil {
		if diskType != "" {
			return nil, fmt.Errorf("没有可用的%s账号，请先在「资源接入」里绑定", adapter.DiskTypeNames[adapter.DiskType(diskType)])
		}
		return nil, fmt.Errorf("没有可用的网盘账号，请先在「资源接入」里绑定")
	}
	return &acc, nil
}

// instantCategory 取（或创建）收录目录。
func (h *ResourceHandler) instantCategory() (*model.Category, error) {
	if id := h.cfgUint(CfgInstantCategoryID); id > 0 {
		var c model.Category
		if err := h.db.First(&c, id).Error; err == nil {
			return &c, nil
		}
	}
	var c model.Category
	if err := h.db.Where("name = ? AND parent_id IS NULL", instantCategoryName).First(&c).Error; err == nil {
		return &c, nil
	}
	c = model.Category{
		Name:        instantCategoryName,
		Description: "「搜一搜 · 搜全网」连同转存一起收录进来的资源",
		// 公开目录：任何登录用户都能看，否则孩子在孩子端点开自己的
		// 收录结果会被权限拦下，体验上像是"点了没反应"。
		IsPublic: true,
		Level:    1,
	}
	if err := h.db.Create(&c).Error; err != nil {
		return nil, err
	}
	c.Path = model.BuildPath("", c.ID)
	h.db.Model(&c).Update("path", c.Path)
	return &c, nil
}

// matchTransferred 在转存后目录列表里，找出与分享内容同名的条目。
func matchTransferred(share *adapter.ShareInfo, items []adapter.FileItem) []service.CollectItem {
	byName := make(map[string]adapter.FileItem, len(items))
	for _, it := range items {
		byName[it.Name] = it
	}
	out := make([]service.CollectItem, 0, len(share.Files))
	for _, f := range share.Files {
		if it, ok := byName[f.Name]; ok {
			out = append(out, service.CollectItem{
				FileID: it.ID, Title: it.Name, Size: it.Size, IsDir: it.IsDir,
			})
			continue
		}
		// 有些网盘转存后会给名字加上后缀（如 "xxx(1)"），退一步做前缀匹配
		for name, it := range byName {
			if strings.HasPrefix(name, f.Name) {
				out = append(out, service.CollectItem{
					FileID: it.ID, Title: name, Size: it.Size, IsDir: it.IsDir,
				})
				break
			}
		}
	}
	return out
}

// pickPlayable 从候选文件里挑最合适的那个。
//
// 偏好顺序：标题命中关键词 > 视频 > 其他；同类型取体积最大的
// （一集动画里体积最大的通常就是正片，而不是预告或花絮）。
func pickPlayable(targets []service.DiskImportTarget, keyword string) service.DiskImportTarget {
	best := targets[0]
	score := func(t service.DiskImportTarget) int {
		s := 0
		if keyword != "" && strings.Contains(t.Title, keyword) {
			s += 1000
		}
		if service.GuessMediaType(t.Title) == model.MediaTypeVideo {
			s += 100
		}
		return s
	}
	bestScore := score(best)
	for _, t := range targets[1:] {
		s := score(t)
		if s > bestScore || (s == bestScore && t.Size > best.Size) {
			best, bestScore = t, s
		}
	}
	return best
}

// stripExt 去掉文件名末尾的扩展名，用作资源标题。
func stripExt(name string) string {
	base := path.Base(strings.TrimSpace(name))
	ext := path.Ext(base)
	if ext == "" {
		return base
	}
	return strings.TrimSuffix(base, ext)
}
