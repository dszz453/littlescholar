package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// MappingHandler 资源映射
//
// 用途：把存储源（WebDAV / 网盘）里的一个远端目录绑定到系统的一个内容目录，
// 之后一次「同步」就把该目录下的媒体文件全部登记为资源。
//
// 为什么值得单独做一个概念：NAS 上本来就是按目录整理好的，
// 每次进新片都手动「浏览 → 勾选 → 导入」既繁琐又容易漏。
// 映射把"这个远端目录对应系统里哪个目录"这件事**持久化**下来，
// 变成一个可以反复执行、可回看的操作。
type MappingHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
	importSvc *service.ImportService
}

func NewMappingHandler(db *gorm.DB, encryptor *util.Encryptor) *MappingHandler {
	return &MappingHandler{
		db:        db,
		encryptor: encryptor,
		importSvc: service.NewImportService(db),
	}
}

// List 映射列表
//
// 支持按账号、按系统目录过滤：
//
//	· accountId  —— 「资源接入」页关心"这个网盘挂了哪些目录"；
//	· categoryId —— 「目录管理」页关心"这个目录挂了哪条映射"，
//	  这样它才能在编辑目录时就地放一个「立即同步」按钮，
//	  而不必让用户跑去「资源映射」页找那一条。
func (h *MappingHandler) List(c *gin.Context) {
	var list []model.ResourceMapping
	q := h.db.Preload("Account").Preload("Category")
	if t := c.Query("accountId"); t != "" {
		q = q.Where("account_id = ?", t)
	}
	if t := c.Query("categoryId"); t != "" {
		q = q.Where("category_id = ?", t)
	}
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	h.fillResourceCounts(list)
	util.OK(c, list)
}

// fillResourceCounts 给每条映射补上"当前导入了几个资源"。
//
// 用一次 GROUP BY 而不是逐条 Count —— 映射页每次加载都会调它，
// 逐条查在映射多起来之后就是几十次往返。
//
// ⚠️ 聚合结果**必须用 Rows() 手工按序扫**，不能写成
//
//	Select("mapping_id, COUNT(*) AS n").Scan(&[]struct{...})
//
// 那种写法在本仓库踩过坑：一旦列名和自定义结构的字段名对不上，gorm 是
// **静默丢弃**该列（不报错、不警告），拿到一片零值。而且 sqlite（单测）
// 与 postgres（线上）对列名大小写的处理不同，症状正好是
// "单测全绿、线上整列为 0"。见 project memory 的后端铁律第 3 条。
//
// 还有一处同样致命：必须 Model(&model.Resource{}) 而不是 Table("resources")。
// 只有 Model() 会自动补 `deleted_at IS NULL` —— 用 Table() 会把用户刚刚
// 「取消」掉的资源也数进去，列表里明明写着 26、点进去只有 20。
func (h *MappingHandler) fillResourceCounts(list []model.ResourceMapping) {
	if len(list) == 0 {
		return
	}
	ids := make([]uint64, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}

	rows, err := h.db.Model(&model.Resource{}).
		Select("mapping_id, COUNT(*)").
		Where("mapping_id IN ?", ids).
		Group("mapping_id").Rows()
	if err != nil {
		log.Printf("[mapping] 统计映射资源数失败: %v", err)
		return
	}
	defer rows.Close()

	counts := make(map[uint64]int64, len(list))
	for rows.Next() {
		var id uint64
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			log.Printf("[mapping] 读取映射资源数失败: %v", err)
			return
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		log.Printf("[mapping] 遍历映射资源数失败: %v", err)
	}
	for i := range list {
		list[i].ResourceCount = counts[list[i].ID]
	}
}

type mappingRequest struct {
	Name          string `json:"name"`
	AccountID     uint64 `json:"accountId" binding:"required"`
	RemoteDirID   string `json:"remoteDirId"`
	RemoteDirName string `json:"remoteDirName"`
	CategoryID    uint64 `json:"categoryId" binding:"required"`
	// 用指针是为了区分"没传"和"传了 false" —— 布尔零值没法区分，
	// 直接写 bool 会导致更新时把 recursive 静默改成 false。
	Recursive *bool `json:"recursive"`
	Enabled   *bool `json:"enabled"`
}

// Create 新建映射
func (h *MappingHandler) Create(c *gin.Context) {
	var req mappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误：需要 accountId 与 categoryId")
		return
	}
	if err := h.validateRefs(req.AccountID, req.CategoryID); err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	m := &model.ResourceMapping{
		Name:          req.Name,
		AccountID:     req.AccountID,
		RemoteDirID:   req.RemoteDirID,
		RemoteDirName: req.RemoteDirName,
		CategoryID:    req.CategoryID,
		Recursive:     boolOr(req.Recursive, true),
		Enabled:       boolOr(req.Enabled, true),
	}
	if m.Name == "" {
		m.Name = h.autoName(req.AccountID, req.RemoteDirID)
	}
	if err := h.db.Create(m).Error; err != nil {
		util.ServerError(c, "创建失败")
		return
	}
	util.OK(c, gin.H{"id": m.ID})
}

// Update 更新映射
func (h *MappingHandler) Update(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}
	var m model.ResourceMapping
	if err := h.db.First(&m, id).Error; err != nil {
		util.NotFound(c, "映射不存在")
		return
	}
	var req mappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.AccountID > 0 {
		updates["account_id"] = req.AccountID
	}
	if req.CategoryID > 0 {
		updates["category_id"] = req.CategoryID
	}
	// remoteDirId 允许显式改成根目录（空串），所以不判空、直接写。
	updates["remote_dir_id"] = req.RemoteDirID
	updates["remote_dir_name"] = req.RemoteDirName
	if req.Recursive != nil {
		updates["recursive"] = *req.Recursive
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if err := h.db.Model(&m).Updates(updates).Error; err != nil {
		util.ServerError(c, "保存失败")
		return
	}
	util.OKWithMessage(c, "已保存", nil)
}

// Delete 解绑映射。
//
// ============ 资源怎么办：默认一起移除，而且是**软删除** ============
//
// 2026-10-16 用户拍板：解绑映射时，由这条映射导入的资源走**软删除**。
// 这条决定的直接后果有两个，都在下面实现了：
//
//  1. 默认值反转。原先是"默认只删映射、资源保留"，理由是"删资源不可逆"。
//     资源改成软删除之后这个理由不成立了：既然能恢复，就该默认做用户
//     直觉上要的那件事 —— 把资源一起拿掉。保留孤儿资源反而出过事故：
//     用户删掉映射后孩子端依然列着那 26 集，只能读成"删除没生效"。
//     仍然可以用 `?withResources=0` 显式要求"只解绑、资源留着"。
//
//  2. 软删除这一刀要**能被同步正确对待**。资源留着软删行，下次同步
//     如果重新 INSERT，就会堆垃圾行、并且换掉资源 id（孩子的收藏和
//     播放进度全断）。复活的逻辑在 service.ImportService.Register 里，
//     那里有完整说明 —— 改这里时别把那条路径踩断。
//
// 用 Unscoped 查映射是刻意的：对一条**之前只删了映射**的记录，这样还能再补一次
// 「连带清理资源」，不必回滚重建映射。
//
// 注意两条分支对映射都用软删除（与仓库里其他 Delete 一致）：带 withResources
// 时若改成物理删除，会出现"删不删资源"决定了"映射是软删还是硬删"这种
// 与语义无关的差异，日后排查问题时很难解释。
//
// 另有一条针对**历史资源**的兜底，见下面第 2 步：mapping_id 是后加的列，
// 加列之前导入的资源全是 0，光按 mapping_id 清等于什么都没清。
func (h *MappingHandler) Delete(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}

	// 只有显式写 0/false 才是"资源留着"；不传 = 一起移除。
	keepResources := c.Query("withResources") == "0" || c.Query("withResources") == "false"

	if keepResources {
		if err := h.db.Delete(&model.ResourceMapping{}, id).Error; err != nil {
			util.ServerError(c, "删除失败")
			return
		}
		util.OKWithMessage(c, "已解绑映射（已导入的资源保留）", nil)
		return
	}

	// 连带清理：先确认映射存在（含已软删除的），避免对着不存在的 ID 空删一场
	// 却回报"清理成功"。
	var m model.ResourceMapping
	if err := h.db.Unscoped().First(&m, id).Error; err != nil {
		util.NotFound(c, "映射不存在")
		return
	}

	var scoped, legacy int64
	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 1) 精确归属：mapping_id 就是这条映射的那些。
		//
		// 资源走软删除（与「资源列表 → 批量删除」一致），
		// 万一删错了还能从库里捞回来。
		res := tx.Where("mapping_id = ?", m.ID).Delete(&model.Resource{})
		if res.Error != nil {
			return res.Error
		}
		scoped = res.RowsAffected

		// 2) 遗留资源：mapping_id 是后加的列，加之前导入的资源没有归属信息。
		//
		// 这一条是整个修复能不能落地的关键。线上现状就是如此：76 条资源里
		// 全部是历史资源，如果只按第 1 步清，用户依然会看到"删了映射资源还在"
		// —— 只不过这次连提示语都说着"已清理 0 条"，更让人费解。
		//
		// 判定依据是「这条映射的账号 + 目标目录」，对存量资源来说这就是它唯一的
		// 来源信息。代价是可能连带清掉**同一账号同一目录下手动导入**的资源；
		// 权衡后可以接受：真出现那种情况，说明这个目录已经被整条映射覆盖了，
		// 手动导入的那份本来就是重复的。
		//
		// 必须同时兼容 NULL 和 0：AutoMigrate 给已存在的行补列时用的是
		// ALTER TABLE ADD COLUMN，**不回填默认值**，所以历史行是 NULL 而不是 0。
		// 只写 `mapping_id = 0` 在 sqlite（单测）上能过、在 postgres（线上）上
		// 一条都匹配不到 —— 这个坑已经实际踩过一次，别再改回去。
		lg := tx.Where("(mapping_id IS NULL OR mapping_id = 0) AND disk_account_id = ? AND category_id = ?",
			m.AccountID, m.CategoryID).Delete(&model.Resource{})
		if lg.Error != nil {
			return lg.Error
		}
		legacy = lg.RowsAffected

		return tx.Delete(&model.ResourceMapping{}, m.ID).Error
	})
	if err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	removed := scoped + legacy
	// 措辞用「移除」而不是「删除」，并把**可恢复**写进提示：
	// 这里是软删除，重新映射后同步会把资源原样带回来。
	// 说成"彻底删了"会让用户不敢用这个按钮。
	msg := fmt.Sprintf("已解绑映射，并移除 %d 个资源（重新映射同步后可恢复）", removed)
	if legacy > 0 {
		msg = fmt.Sprintf("已解绑映射，并移除 %d 个资源（其中 %d 个为历史资源；重新映射同步后可恢复）",
			removed, legacy)
	}
	util.OKWithMessage(c, msg, gin.H{
		"removedResources": removed,
		"removedScoped":    scoped,
		"removedLegacy":    legacy,
	})
}

// Sync 立即同步一个映射
func (h *MappingHandler) Sync(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的映射 ID")
		return
	}
	var m model.ResourceMapping
	if err := h.db.First(&m, id).Error; err != nil {
		util.NotFound(c, "映射不存在")
		return
	}
	result := h.syncOne(c.Request.Context(), &m)
	util.OK(c, result)
}

// SyncAll 同步全部启用的映射
func (h *MappingHandler) SyncAll(c *gin.Context) {
	var list []model.ResourceMapping
	h.db.Where("enabled = ?", true).Find(&list)

	created, skipped, failed := 0, 0, 0
	for i := range list {
		r := h.syncOne(c.Request.Context(), &list[i])
		if r.Error != "" {
			failed++
			continue
		}
		created += r.Created
		skipped += r.Skipped
	}
	util.OK(c, gin.H{
		"total": len(list), "created": created,
		"skipped": skipped, "failed": failed,
	})
}

// syncResult 单个映射的同步结果
type syncResult struct {
	MappingID   uint64 `json:"mappingId"`
	Name        string `json:"name"`
	Created     int    `json:"created"`
	Skipped     int    `json:"skipped"`
	SkippedDirs int    `json:"skippedDirs"`
	Truncated   bool   `json:"truncated"`
	Error       string `json:"error,omitempty"`
}

// syncOne 执行一次同步并落库同步快照。
//
// 无论成功失败都写快照：用户在列表里最需要知道的是
// "上次同步是什么时候、有没有报错"，报错信息比成功计数更重要。
func (h *MappingHandler) syncOne(ctx context.Context, m *model.ResourceMapping) syncResult {
	res := syncResult{MappingID: m.ID, Name: m.Name}
	now := time.Now()

	acc, cred, a, err := loadSource(h.db, h.encryptor, m.AccountID)
	if err != nil {
		res.Error = err.Error()
	} else {
		// 递归遍历可能很久（NAS 大目录一次 PROPFIND 几十秒），
		// 超时给到 3 分钟而不是常用值 30s。
		sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()

		targets, stats := service.CollectDiskFiles(sctx, a, cred,
			[]service.CollectItem{{FileID: m.RemoteDirID, IsDir: true}},
			m.Recursive, service.DefaultMaxImportFiles)

		res.SkippedDirs = stats.SkippedDirs
		res.Truncated = stats.Truncated
		if len(targets) == 0 {
			res.Error = "该目录下没有找到可导入的媒体文件"
		} else if created, skipped, rerr := h.importSvc.Register(acc, m.CategoryID, m.ID, targets); rerr != nil {
			res.Error = rerr.Error()
		} else {
			res.Created = created
			res.Skipped = skipped
		}
	}

	updates := map[string]interface{}{
		"last_sync_at":      now,
		"last_sync_created": res.Created,
		"last_sync_skipped": res.Skipped,
		"last_sync_error":   res.Error,
	}
	if err := h.db.Model(m).Updates(updates).Error; err != nil {
		log.Printf("[mapping] 写入同步快照失败 mapping=%d: %v", m.ID, err)
	}
	return res
}

func (h *MappingHandler) validateRefs(accountID, categoryID uint64) error {
	var cnt int64
	h.db.Model(&model.DiskAccount{}).Where("id = ?", accountID).Count(&cnt)
	if cnt == 0 {
		return fmt.Errorf("存储账号不存在")
	}
	h.db.Model(&model.Category{}).Where("id = ?", categoryID).Count(&cnt)
	if cnt == 0 {
		return fmt.Errorf("系统目录不存在")
	}
	return nil
}

// autoName 用户没填名字时，用"账号名 · 远端目录名"拼一个可读的。
func (h *MappingHandler) autoName(accountID uint64, remoteDirID string) string {
	var acc model.DiskAccount
	name := ""
	if h.db.First(&acc, accountID).Error == nil {
		name = acc.Nickname
	}
	dir := remoteDirID
	if dir == "" || dir == "/" {
		dir = "根目录"
	}
	if name == "" {
		return dir
	}
	return name + " · " + dir
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
