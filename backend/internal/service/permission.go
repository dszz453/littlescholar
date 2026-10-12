package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/cache"
	"gorm.io/gorm"
)

// permCacheTTL 权限缓存有效期
// 授权变更是低频操作（管理员手动），5 分钟足够安全；
// 且任何授权变更都会主动失效缓存，不依赖 TTL 兜底。
const permCacheTTL = 5 * time.Minute

// PermissionService 权限服务
type PermissionService struct {
	db    *gorm.DB
	ch *cache.Client // 可为 nil，表示不使用缓存
}

func NewPermissionService(db *gorm.DB) *PermissionService {
	return &PermissionService{db: db}
}

// WithCache 注入缓存客户端（链式调用）
func (s *PermissionService) WithCache(c *cache.Client) *PermissionService {
	s.ch = c
	return s
}

// InvalidateUser 失效指定用户的权限缓存
// 授权变更（SetGrants / 删除用户）后必须调用
func (s *PermissionService) InvalidateUser(userID uint64) {
	if s.ch == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.ch.DelPrefix(ctx, cache.KeyPermUser(userID))
}

// InvalidateAll 失效所有用户的权限缓存
// 目录树变动（新增 / 删除 / 移动 / 公开性变更）后必须调用，
// 因为目录层级变化会影响「父目录授权隐式覆盖子目录」的结果
func (s *PermissionService) InvalidateAll() {
	if s.ch == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.ch.DelPrefix(ctx, cache.KeyPermUserPrefix())
}

// CanAccessCategory 判断用户能否访问指定目录
//
// 判定链：
//  1. 管理员 → 放行
//  2. 账号有效（未禁用、未过期）
//  3. 目录公开 → 放行
//  4. 用户对该目录或其**祖先目录**有未过期授权 → 放行
//     （授权父目录即隐式获得其所有子目录的访问权，符合直觉）
func (s *PermissionService) CanAccessCategory(user *model.User, categoryID uint64) (bool, error) {
	if user == nil {
		return false, nil
	}
	if user.Role == model.RoleAdmin {
		return true, nil
	}
	if !user.IsActive() {
		return false, nil
	}

	// 取目标目录及其所有祖先
	var target model.Category
	if err := s.db.First(&target, categoryID).Error; err != nil {
		return false, err
	}
	if target.IsPublic {
		return true, nil
	}

	// 收集自身 + 所有祖先 ID
	ids := ancestorIDs(&target)
	ids = append(ids, target.ID)

	// 查询有效授权
	var count int64
	now := time.Now()
	err := s.db.Model(&model.UserCategoryGrant{}).
		Where("user_id = ? AND category_id IN ?", user.ID, ids).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CanDownloadCategory 判断用户能否下载该目录下的资源
func (s *PermissionService) CanDownloadCategory(user *model.User, categoryID uint64) (bool, error) {
	if user == nil {
		return false, nil
	}
	if user.Role == model.RoleAdmin {
		return true, nil
	}
	if !user.IsActive() {
		return false, nil
	}

	var target model.Category
	if err := s.db.First(&target, categoryID).Error; err != nil {
		return false, err
	}
	if target.IsPublic {
		return true, nil
	}

	ids := ancestorIDs(&target)
	ids = append(ids, target.ID)

	now := time.Now()
	var grants []model.UserCategoryGrant
	if err := s.db.Where("user_id = ? AND category_id IN ?", user.ID, ids).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Find(&grants).Error; err != nil {
		return false, err
	}
	for _, g := range grants {
		if g.CanDownload {
			return true, nil
		}
	}
	return false, nil
}

// AccessibleCategoryIDs 返回用户可访问的所有目录 ID
// 返回 nil 表示可访问全部（管理员）
//
// 命中缓存时直接返回；未命中则查库并回填。
// 这是孩子端最高频的调用（每次进首页都会走），缓存收益明显。
func (s *PermissionService) AccessibleCategoryIDs(user *model.User) ([]uint64, bool, error) {
	if user == nil {
		return nil, false, nil
	}
	if user.Role == model.RoleAdmin {
		return nil, true, nil // 全部可访问
	}
	if !user.IsActive() {
		return []uint64{}, false, nil
	}

	// 1. 尝试命中缓存
	if ids, ok := s.loadCachedIDs(user.ID); ok {
		return ids, false, nil
	}

	// 2. 查库
	ids, err := s.queryAccessibleIDs(user.ID)
	if err != nil {
		return nil, false, err
	}

	// 3. 回填缓存
	s.saveCachedIDs(user.ID, ids)
	return ids, false, nil
}

// queryAccessibleIDs 从数据库计算可访问目录集合
//
// 实现要点：把授权目录与公开目录合并后**一次性**查出所有子孙，
// 避免 per-category 循环查询（原实现的 N+1 问题）。
func (s *PermissionService) queryAccessibleIDs(userID uint64) ([]uint64, error) {
	allowed := make(map[uint64]bool)

	// 收集「根」目录：用户有效授权的 + 所有公开的
	var roots []model.Category

	// 2.1 有效授权对应的目录
	var grants []model.UserCategoryGrant
	if err := s.db.Where("user_id = ?", userID).
		Where("expires_at IS NULL OR expires_at > ?", time.Now()).
		Find(&grants).Error; err != nil {
		return nil, err
	}
	if len(grants) > 0 {
		grantIDs := make([]uint64, 0, len(grants))
		for _, g := range grants {
			grantIDs = append(grantIDs, g.CategoryID)
		}
		var grantedCats []model.Category
		if err := s.db.Where("id IN ?", grantIDs).Find(&grantedCats).Error; err != nil {
			return nil, err
		}
		roots = append(roots, grantedCats...)
	}

	// 2.2 公开目录
	var publics []model.Category
	if err := s.db.Where("is_public = ?", true).Find(&publics).Error; err != nil {
		return nil, err
	}
	roots = append(roots, publics...)

	if len(roots) == 0 {
		return []uint64{}, nil
	}

	// 2.3 构造一次性的 LIKE 条件：path LIKE '/1/%' OR path LIKE '/2/%' ...
	// 注意根目录自身的 path 形如 /1/，其后代形如 /1/12/，用 'path%' 即可覆盖两者
	conds := make([]string, 0, len(roots))
	args := make([]interface{}, 0, len(roots))
	for _, r := range roots {
		allowed[r.ID] = true
		if r.Path == "" {
			continue
		}
		conds = append(conds, "path LIKE ?")
		args = append(args, r.Path+"%")
	}

	if len(conds) > 0 {
		var descendants []struct{ ID uint64 }
		if err := s.db.Model(&model.Category{}).
			Where(strings.Join(conds, " OR "), args...).
			Select("id").Find(&descendants).Error; err != nil {
			return nil, err
		}
		for _, d := range descendants {
			allowed[d.ID] = true
		}
	}

	out := make([]uint64, 0, len(allowed))
	for id := range allowed {
		out = append(out, id)
	}
	return out, nil
}

// loadCachedIDs 读缓存。返回 (ids, hit)
func (s *PermissionService) loadCachedIDs(userID uint64) ([]uint64, bool) {
	if s.ch == nil || !s.ch.Enabled() {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	raw, ok := s.ch.Get(ctx, cache.KeyPermUser(userID))
	if !ok || raw == "" {
		return nil, false
	}
	// 空串代表「无任何可访问目录」（已计算过的结果，需与未命中区分）
	if raw == "-" {
		return []uint64{}, true
	}
	parts := strings.Split(raw, ",")
	ids := make([]uint64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, false // 数据损坏，视为未命中，回退查库
		}
		ids = append(ids, n)
	}
	return ids, true
}

// saveCachedIDs 回填缓存
func (s *PermissionService) saveCachedIDs(userID uint64, ids []uint64) {
	if s.ch == nil || !s.ch.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 空集合用 "-" 表示，与「未命中」区分开，避免缓存穿透
	if len(ids) == 0 {
		s.ch.Set(ctx, cache.KeyPermUser(userID), "-", permCacheTTL)
		return
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	s.ch.Set(ctx, cache.KeyPermUser(userID), strings.Join(parts, ","), permCacheTTL)
}

// FilterAccessibleTree 过滤目录树，只保留用户可访问的节点
func (s *PermissionService) FilterAccessibleTree(user *model.User, tree []*model.Category) ([]*model.Category, error) {
	if user != nil && user.Role == model.RoleAdmin {
		return tree, nil
	}
	ids, all, err := s.AccessibleCategoryIDs(user)
	if err != nil {
		return nil, err
	}
	if all {
		return tree, nil
	}
	allowed := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return filterTree(tree, allowed), nil
}

func filterTree(nodes []*model.Category, allowed map[uint64]bool) []*model.Category {
	var out []*model.Category
	for _, n := range nodes {
		if !allowed[n.ID] {
			continue
		}
		n.Children = filterTree(n.Children, allowed)
		out = append(out, n)
	}
	return out
}

// ancestorIDs 由物化路径解析出所有祖先 ID
// path = /1/12/35/  →  [1, 12]
func ancestorIDs(cat *model.Category) []uint64 {
	var out []uint64
	if cat.Path == "" {
		return out
	}
	var cur uint64
	var sb []byte
	sb = nil
	for i := 0; i < len(cat.Path); i++ {
		ch := cat.Path[i]
		if ch == '/' {
			if len(sb) > 0 {
				cur = parseUint(sb)
				if cur != cat.ID {
					out = append(out, cur)
				}
				sb = sb[:0]
			}
			continue
		}
		sb = append(sb, ch)
	}
	return out
}

func parseUint(b []byte) uint64 {
	var n uint64
	for _, ch := range b {
		if ch < '0' || ch > '9' {
			continue
		}
		n = n*10 + uint64(ch-'0')
	}
	return n
}
