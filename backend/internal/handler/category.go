package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// CategoryHandler 目录管理
type CategoryHandler struct {
	db   *gorm.DB
	perm *service.PermissionService
}

func NewCategoryHandler(db *gorm.DB, perm *service.PermissionService) *CategoryHandler {
	return &CategoryHandler{db: db, perm: perm}
}

// IconLibrary 内置图标库（子供儿童化）
var IconLibrary = []map[string]string{
	{"key": "english", "name": "英语", "emoji": "🔤"},
	{"key": "chinese", "name": "语文", "emoji": "📖"},
	{"key": "math", "name": "数学", "emoji": "🔢"},
	{"key": "science", "name": "科学", "emoji": "🔬"},
	{"key": "story", "name": "故事", "emoji": "📚"},
	{"key": "music", "name": "音乐", "emoji": "🎵"},
	{"key": "art", "name": "美术", "emoji": "🎨"},
	{"key": "sport", "name": "运动", "emoji": "⚽"},
	{"key": "book", "name": "书本", "emoji": "📕"},
	{"key": "star", "name": "星星", "emoji": "⭐"},
	{"key": "moon", "name": "月亮", "emoji": "🌙"},
	{"key": "sun", "name": "太阳", "emoji": "☀️"},
	{"key": "calculator", "name": "计算器", "emoji": "🧮"},
	{"key": "pencil", "name": "铅笔", "emoji": "✏️"},
	{"key": "globe", "name": "地球", "emoji": "🌍"},
	{"key": "rocket", "name": "火箭", "emoji": "🚀"},
	{"key": "dino", "name": "恐龙", "emoji": "🦕"},
	{"key": "cat", "name": "小猫", "emoji": "🐱"},
	{"key": "panda", "name": "熊猫", "emoji": "🐼"},
	{"key": "rabbit", "name": "小兔", "emoji": "🐰"},
	{"key": "apple", "name": "苹果", "emoji": "🍎"},
	{"key": "gift", "name": "礼物", "emoji": "🎁"},
	{"key": "trophy", "name": "奖杯", "emoji": "🏆"},
	{"key": "video", "name": "视频", "emoji": "🎬"},
	{"key": "audio", "name": "音频", "emoji": "🎧"},
	{"key": "pdf", "name": "文档", "emoji": "📄"},
	{"key": "folder", "name": "文件夹", "emoji": "📁"},
}

// Icons 获取图标库
func (h *CategoryHandler) Icons(c *gin.Context) {
	util.OK(c, IconLibrary)
}

// Tree 获取完整目录树
func (h *CategoryHandler) Tree(c *gin.Context) {
	withCount := c.Query("withResourceCount") == "true"

	var cats []model.Category
	if err := h.db.Order("level asc, sort_order asc, id asc").Find(&cats).Error; err != nil {
		util.ServerError(c, "查询目录失败")
		return
	}

	list := make([]*model.Category, len(cats))
	for i := range cats {
		list[i] = &cats[i]
	}

	// 统计资源数
	if withCount {
		type cntRow struct {
			CategoryID uint64
			Total      int64
		}
		var rows []cntRow
		h.db.Model(&model.Resource{}).
			Select("category_id, count(*) as total").
			Group("category_id").Scan(&rows)
		countMap := make(map[uint64]int64, len(rows))
		for _, r := range rows {
			countMap[r.CategoryID] = r.Total
		}
		// 累加子目录数量到父级
		for _, cat := range list {
			cat.ResourceCount = countMap[cat.ID]
		}
		accumulateCounts(list, countMap)
	}

	tree := model.BuildTree(list, nil)
	util.OK(c, tree)
}

// accumulateCounts 把子孙资源数累加到父级
func accumulateCounts(list []*model.Category, countMap map[uint64]int64) {
	byID := make(map[uint64]*model.Category, len(list))
	for _, c := range list {
		byID[c.ID] = c
	}
	for _, c := range list {
		cur := c
		for cur.ParentID != nil {
			parent, ok := byID[*cur.ParentID]
			if !ok {
				break
			}
			parent.ResourceCount += c.ResourceCount
			cur = parent
		}
	}
}

// AppTree 获取当前用户可访问的目录树（C 端）
func (h *CategoryHandler) AppTree(c *gin.Context) {
	user := currentUser(c)
	var cats []model.Category
	if err := h.db.Order("level asc, sort_order asc, id asc").Find(&cats).Error; err != nil {
		util.ServerError(c, "查询目录失败")
		return
	}
	list := make([]*model.Category, len(cats))
	for i := range cats {
		list[i] = &cats[i]
	}
	tree := model.BuildTree(list, nil)

	filtered, err := h.perm.FilterAccessibleTree(user, tree)
	if err != nil {
		util.ServerError(c, "权限过滤失败")
		return
	}

	// 附带资源数
	var rows []struct {
		CategoryID uint64
		Total      int64
	}
	h.db.Model(&model.Resource{}).Select("category_id, count(*) as total").Group("category_id").Scan(&rows)
	countMap := make(map[uint64]int64)
	for _, r := range rows {
		countMap[r.CategoryID] = r.Total
	}
	applyCounts(filtered, countMap)

	util.OK(c, filtered)
}

func applyCounts(nodes []*model.Category, countMap map[uint64]int64) int64 {
	var total int64
	for _, n := range nodes {
		childTotal := applyCounts(n.Children, countMap)
		n.ResourceCount = countMap[n.ID] + childTotal
		total += n.ResourceCount
	}
	return total
}

type createCategoryRequest struct {
	ParentID    *uint64 `json:"parentId"`
	Name        string  `json:"name" binding:"required"`
	Icon        string  `json:"icon"`
	IconURL     string  `json:"iconUrl"`
	Color       string  `json:"color"`
	Description string  `json:"description"`
	SortOrder   int     `json:"sortOrder"`
	IsPublic    bool    `json:"isPublic"`
}

// Create 创建目录
func (h *CategoryHandler) Create(c *gin.Context) {
	var req createCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "目录名称不能为空")
		return
	}

	cat := &model.Category{
		ParentID:    req.ParentID,
		Name:        req.Name,
		Icon:        req.Icon,
		IconURL:     req.IconURL,
		Color:       req.Color,
		Description: req.Description,
		SortOrder:   req.SortOrder,
		IsPublic:    req.IsPublic,
		Level:       1,
	}

	parentPath := ""
	if req.ParentID != nil {
		var parent model.Category
		if err := h.db.First(&parent, *req.ParentID).Error; err != nil {
			util.BadRequest(c, "父目录不存在")
			return
		}
		parentPath = parent.Path
		cat.Level = parent.Level + 1
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(cat).Error; err != nil {
			return err
		}
		path := model.BuildPath(parentPath, cat.ID)
		return tx.Model(cat).Update("path", path).Error
	})
	if err != nil {
		util.ServerError(c, "创建目录失败")
		return
	}
	h.perm.InvalidateAll() // 目录树已变，权限计算结果需重算
	h.db.First(cat, cat.ID)
	util.OK(c, cat)
}

type updateCategoryRequest struct {
	Name        *string `json:"name"`
	Icon        *string `json:"icon"`
	IconURL     *string `json:"iconUrl"`
	Color       *string `json:"color"`
	Description *string `json:"description"`
	SortOrder   *int    `json:"sortOrder"`
	IsPublic    *bool   `json:"isPublic"`
}

// Update 更新目录
func (h *CategoryHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的目录 ID")
		return
	}
	var cat model.Category
	if err := h.db.First(&cat, id).Error; err != nil {
		util.NotFound(c, "目录不存在")
		return
	}
	var req updateCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Icon != nil {
		updates["icon"] = *req.Icon
	}
	if req.IconURL != nil {
		updates["icon_url"] = *req.IconURL
	}
	if req.Color != nil {
		updates["color"] = *req.Color
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.IsPublic != nil {
		updates["is_public"] = *req.IsPublic
	}
	if len(updates) == 0 {
		util.OK(c, cat)
		return
	}
	if err := h.db.Model(&cat).Updates(updates).Error; err != nil {
		util.ServerError(c, "更新失败")
		return
	}
	// 公开性变更会影响所有用户的可见范围，需全量失效；
	// 其余字段（名称/图标/颜色等）不影响权限，无需清缓存
	if req.IsPublic != nil {
		h.perm.InvalidateAll()
	}
	h.db.First(&cat, id)
	util.OK(c, cat)
}

type moveCategoryRequest struct {
	TargetParentID *uint64 `json:"targetParentId"`
	SortOrder      int     `json:"sortOrder"`
}

// Move 移动目录（同时更新整棵子树的 path / level）
func (h *CategoryHandler) Move(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的目录 ID")
		return
	}
	var req moveCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	var cat model.Category
	if err := h.db.First(&cat, id).Error; err != nil {
		util.NotFound(c, "目录不存在")
		return
	}

	// 不能移动到自己或自己的子孙下
	if req.TargetParentID != nil {
		if *req.TargetParentID == id {
			util.BadRequest(c, "不能移动到自身")
			return
		}
		var target model.Category
		if err := h.db.First(&target, *req.TargetParentID).Error; err != nil {
			util.BadRequest(c, "目标父目录不存在")
			return
		}
		if len(target.Path) > len(cat.Path) && target.Path[:len(cat.Path)] == cat.Path {
			util.BadRequest(c, "不能移动到自己的子目录下")
			return
		}
	}

	oldPath := cat.Path
	newParentPath := ""
	newLevel := 1
	if req.TargetParentID != nil {
		var parent model.Category
		h.db.First(&parent, *req.TargetParentID)
		newParentPath = parent.Path
		newLevel = parent.Level + 1
	}
	newPath := model.BuildPath(newParentPath, cat.ID)

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&cat).Updates(map[string]interface{}{
			"parent_id":  req.TargetParentID,
			"path":       newPath,
			"level":      newLevel,
			"sort_order": req.SortOrder,
		}).Error; err != nil {
			return err
		}
		// 更新所有子孙的 path 和 level
		var descendants []model.Category
		if err := tx.Where("path LIKE ?", oldPath+"%").Find(&descendants).Error; err != nil {
			return err
		}
		for i := range descendants {
			if descendants[i].ID == cat.ID {
				continue
			}
			newDescPath := newPath + descendants[i].Path[len(oldPath):]
			newDescLevel := descendants[i].Level + (newLevel - cat.Level)
			if err := tx.Model(&descendants[i]).Updates(map[string]interface{}{
				"path":  newDescPath,
				"level": newDescLevel,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		util.ServerError(c, "移动失败")
		return
	}
	h.perm.InvalidateAll() // 层级变化影响父目录授权的隐式覆盖范围
	util.OKWithMessage(c, "移动成功", nil)
}

// Delete 删除目录
func (h *CategoryHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的目录 ID")
		return
	}
	cascade := c.Query("cascade") == "true"

	var cat model.Category
	if err := h.db.First(&cat, id).Error; err != nil {
		util.NotFound(c, "目录不存在")
		return
	}

	// 检查是否有子目录
	var childCount int64
	h.db.Model(&model.Category{}).Where("parent_id = ?", id).Count(&childCount)
	if childCount > 0 && !cascade {
		util.BadRequest(c, "该目录下存在子目录，请先处理子目录或使用级联删除")
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		ids := []uint64{id}
		if cascade {
			var descendants []struct{ ID uint64 }
			tx.Model(&model.Category{}).Where("path LIKE ?", cat.Path+"%").Select("id").Find(&descendants)
			for _, d := range descendants {
				ids = append(ids, d.ID)
			}
		}
		// 解除资源关联（把资源移到无目录状态或直接删除，这里选择删除关联并置空）
		if err := tx.Model(&model.Resource{}).Where("category_id IN ?", ids).
			Update("category_id", 0).Error; err != nil {
			return err
		}
		if err := tx.Where("category_id IN ?", ids).Delete(&model.UserCategoryGrant{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Delete(&model.Category{}).Error
	})
	if err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	h.perm.InvalidateAll() // 目录已删，清全部用户缓存
	util.OKWithMessage(c, "删除成功", nil)
}

// currentUser 从上下文取用户
func currentUser(c *gin.Context) *model.User {
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}
