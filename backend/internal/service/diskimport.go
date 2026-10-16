package service

import (
	"context"
	"log"
	"path"
	"strings"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
)

// 导入上限。
//
// DefaultMaxImportFiles 存在的意义是防止误选 NAS 根目录把整盘几万个文件
// 灌进库 —— 那既拖垮接口，也会让后台资源列表卡死。
const (
	DefaultMaxImportFiles = 2000
	// maxImportDepth 递归深度上限，防软链接成环导致无限递归。
	maxImportDepth = 16
)

// mediaFileExts 可播放 / 可阅读的扩展名白名单。
//
// 导入时必须过滤：NAS 上每个视频旁边通常还有 .nfo / .srt / .torrent /
// 缩略图等附属文件，全登记成资源的话，孩子端点开就是乱码或空白。
var mediaFileExts = map[string]bool{
	// 视频
	"mp4": true, "mkv": true, "avi": true, "mov": true, "wmv": true,
	"flv": true, "rmvb": true, "rm": true, "ts": true, "m3u8": true,
	"webm": true, "m4v": true, "mpg": true, "mpeg": true, "3gp": true,
	// 音频
	"mp3": true, "wav": true, "flac": true, "aac": true, "m4a": true,
	"ogg": true, "wma": true, "ape": true,
	// 文档 / 图片
	"pdf": true,
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true,
}

// ExtOfName 取小写扩展名，没有则返回空串。
func ExtOfName(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// needsTranscodeExts 浏览器**原生放不出来**的容器格式。
//
// 这张表是"要不要转码"的判据核心：命中就必须转码，否则 <video>
// 只会抛 MediaError(code=4) 然后前端弹一句毫无信息量的提示。
//
// 有意不包含的：
//   - mp4 / m4v / webm / m3u8 —— 浏览器原生支持，转码纯属浪费 CPU；
//   - mp3 / m4a / flac / wav —— 音频同理；
//   - mov —— 部分情况是 h264 可以直接播，但线上实测有大量 mov 用的是
//     非浏览器编码，保险起见仍走转码（与改动前的行为保持一致）。
var needsTranscodeExts = map[string]bool{
	"mkv": true, "avi": true, "rmvb": true, "rm": true, "flv": true,
	"wmv": true, "mov": true, "ts": true, "mpg": true, "mpeg": true,
	"3gp": true, "vob": true, "f4v": true, "asf": true, "m2ts": true,
	"mts": true, "divx": true, "xv": true, "ogm": true, "mxf": true,
}

// knownExts 我们"认得出来"的格式集合 = 可导入白名单 ∪ 需转码容器表。
//
// 它的作用是给"从文本里猜后缀"这件事加一道闸：标题里出现小数点太常见了
// （"神奇之钥 第2季.13"、"Biff and Chip (1.2)"），盲猜会得到一个
// 毫无意义的"扩展名"，更糟的是它会**把 FileID 里真正正确的后缀挡在后面**。
// 只在认得出来时才采信，才是安全的。
var knownExts = func() map[string]bool {
	m := make(map[string]bool, len(mediaFileExts)+len(needsTranscodeExts))
	for k := range mediaFileExts {
		m[k] = true
	}
	for k := range needsTranscodeExts {
		m[k] = true
	}
	return m
}()

// NeedsTranscodeExt 判断该扩展名是否必须转码才能给浏览器播。
func NeedsTranscodeExt(ext string) bool { return needsTranscodeExts[ext] }

// ResourceExt 推断资源的真实文件扩展名（小写、不含点）。
//
// 三级取值，缺一不可：
//
//  1. res.Ext —— 新数据。入库时显式写入，最可靠（自己写的，直接采信）。
//  2. res.Title —— 老数据兜底。有些来源（手工建资源、老的分享导入）
//     没有剥后缀，标题本身就是带扩展名的。
//  3. res.FileID —— 老数据的最后一道。WebDAV / 本地硬盘的 FileID 就是
//     **带扩展名的相对路径**，是唯一还留着格式信息的地方；
//     网盘（夸克/UC/百度）的 FileID 是不含点的短 ID，自然取不到后缀。
//
// 第 2、3 步都要求候选值在 knownExts 里，理由见 knownExts 的注释。
func ResourceExt(res *model.Resource) string {
	if res == nil {
		return ""
	}
	if e := strings.ToLower(strings.TrimSpace(res.Ext)); e != "" {
		return e
	}
	if e := ExtOfName(res.Title); knownExts[e] {
		return e
	}
	if e := ExtOfName(res.FileID); knownExts[e] {
		return e
	}
	return ""
}

// IsMediaFile 判断是否属于要导入的媒体类型。
func IsMediaFile(name string) bool { return mediaFileExts[ExtOfName(name)] }

// GuessMediaType 按扩展名猜媒体类型。
//
// 猜不出时按视频处理 —— 这是历史行为（前端播放器对未知类型就是走视频），
// 改动会让一些没有扩展名的资源突然变成"不支持的类型"。
func GuessMediaType(title string) string {
	return MediaTypeByExt(ExtOfName(title))
}

// MediaTypeByExt 按扩展名判媒体类型。
//
// 从 GuessMediaType 里抽出来，是为了让**排序**用的分类（service.MediaRank）
// 与**播放**用的分类共用同一张表 —— 两边各写一份迟早会漂移，
// 表现就是"列表里排前面的打不开、能播的排在后面"。
func MediaTypeByExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), ".")) {
	case "mp3", "wav", "flac", "aac", "m4a", "ogg", "wma", "ape":
		return model.MediaTypeAudio
	case "pdf":
		return model.MediaTypePDF
	case "jpg", "jpeg", "png", "gif", "webp", "bmp":
		return model.MediaTypeImage
	default:
		return model.MediaTypeVideo
	}
}

// CollectItem 用户勾选的一项（文件或目录）。
type CollectItem struct {
	FileID string
	Title  string
	Size   int64
	IsDir  bool
}

// CollectStats 展开过程中的统计。
type CollectStats struct {
	SkippedDirs   int  // 勾了目录但没开递归 → 直接跳过
	SkippedNonMed int  // 勾了非媒体文件
	Truncated     bool // 触到 MaxFiles 上限
}

// DiskImportTarget 待登记的文件。
type DiskImportTarget struct {
	FileID string
	Title  string
	Size   int64
}

// CollectDiskFiles 把用户勾选的项展开成待导入的文件列表。
//
// 目录递归在服务端做而不是前端逐层调接口：一个"汪汪队"目录
// 下面可能有几十个子目录，前端逐层拉就是几十次往返，很慢；
// 服务端一次走完还能顺带做去重和上限控制。
func CollectDiskFiles(
	ctx context.Context,
	a adapter.DiskAdapter,
	cred *adapter.Credential,
	items []CollectItem,
	recursive bool,
	maxFiles int,
) ([]DiskImportTarget, CollectStats) {
	if maxFiles <= 0 || maxFiles > DefaultMaxImportFiles {
		maxFiles = DefaultMaxImportFiles
	}

	var (
		targets []DiskImportTarget
		seen    = make(map[string]bool, len(items))
		stats   CollectStats
	)

	add := func(fileID, title string, size int64) {
		if fileID == "" || seen[fileID] {
			return
		}
		if len(targets) >= maxFiles {
			stats.Truncated = true
			return
		}
		seen[fileID] = true
		targets = append(targets, DiskImportTarget{FileID: fileID, Title: title, Size: size})
	}

	var walk func(dirID string, depth int)
	walk = func(dirID string, depth int) {
		if stats.Truncated || depth > maxImportDepth {
			return
		}
		entries, err := a.ListDir(ctx, cred, dirID)
		if err != nil {
			// 单个子目录失败不该让整次导入失败 —— 只记日志、跳过。
			// 否则一个权限受限的子目录会让用户前面选的全都白选。
			log.Printf("[import] 遍历存储源目录失败，已跳过 %s: %v", dirID, err)
			return
		}
		for _, e := range entries {
			if stats.Truncated {
				return
			}
			if e.IsDir {
				if recursive {
					walk(e.ID, depth+1)
				}
				continue
			}
			if !IsMediaFile(e.Name) {
				stats.SkippedNonMed++
				continue
			}
			add(e.ID, e.Name, e.Size)
		}
	}

	for _, it := range items {
		if it.IsDir {
			if !recursive {
				stats.SkippedDirs++
				continue
			}
			walk(it.FileID, 1)
			continue
		}
		name := it.Title
		if name == "" {
			name = path.Base(it.FileID)
		}
		if !IsMediaFile(name) {
			stats.SkippedNonMed++
			continue
		}
		add(it.FileID, name, it.Size)
	}

	return targets, stats
}

// ImportResult 导入结果，直接作为接口返回值。
type ImportResult struct {
	Created       int    `json:"created"`
	Skipped       int    `json:"skipped"`
	SkippedDirs   int    `json:"skippedDirs"`
	SkippedNonMed int    `json:"skippedNonMed"`
	Truncated     bool   `json:"truncated"`
	MaxFiles      int    `json:"maxFiles"`
	Message       string `json:"message,omitempty"`
}

// ImportService 把存储源里的文件登记为系统资源。
type ImportService struct {
	db *gorm.DB
}

func NewImportService(db *gorm.DB) *ImportService { return &ImportService{db: db} }

// Register 登记一批文件。已存在（同账号 + 同 fileId）的跳过。
//
// mappingID 记录这批资源由哪条映射导入（0 = 手动导入/搜索收录）。
// 它决定了"删除映射"时能不能精确地连带清理资源，所以调用方务必传对：
// 同步链路传 m.ID，手动导入传 0。
//
// ============ 软删除的闭环（2026-10-16）============
//
// 资源是**软删除**的（`Base.DeletedAt`），所以库里可能同时存在
// 「同账号 + 同 fileId 的软删行」。这里必须把那种行**复活**，而不是
// 再 INSERT 一条新的 —— 这是用户"取消映射用软删除"这个决定的下半场，
// 缺了它软删除就只是个占位的空话。
//
// 不复活会出两个真实故障：
//
//  1. **库被垃圾行撑爆**。"删映射 → 重新映射 → 同步"是用户明确要走的路
//     （第 4 条要求：全部映射删掉后要重新映射），每走一轮就多一份
//     同 fileId 的死行。走三轮之后同一个文件在库里躺四行，
//     再排查"孩子端怎么多了 26 集"时根本对不上号。
//
//  2. **孩子的痕迹全断**。收藏、播放进度、播放记录都挂在资源 id 上，
//     重新 INSERT 意味着换了个 id —— 资源"回来了"，但读到一半的位置、
//     收藏的标记全没了。复活后 id 不变，一切照旧。
//
// 代价是明确的：**软删的资源会随下一次同步回来**。这是刻意的取舍 ——
// 软删除本身表达的语义就是"可以恢复"，而真正"我不想要这个目录了"
// 应该通过取消映射来表达（映射没了，同步不会再来）。
//
// 复活的资源计入 created：从用户视角"资源回来了"和"新增了资源"
// 是同一件事（孩子端都是凭空多出一集），没必要为此多一个返回值
// 去改两个调用点 + 同步结果结构 + 三处前端文案。
func (s *ImportService) Register(acc *model.DiskAccount, categoryID, mappingID uint64, targets []DiskImportTarget) (int, int, error) {
	if len(targets) == 0 {
		return 0, 0, nil
	}

	// 去重：先一次性查出已存在的 fileId 再比对，避免循环里逐条查库。
	//
	// ⚠️ Unscoped() 是必须的 —— 默认作用域会补 `deleted_at IS NULL`，
	// 于是软删的那些行"查不到"，下面的复活分支永远走不到，
	// 表现就是"软删除看起来生效了，其实只是多堆一行垃圾"。
	fileIDs := make([]string, 0, len(targets))
	for _, t := range targets {
		fileIDs = append(fileIDs, t.FileID)
	}
	var existing []model.Resource
	s.db.Unscoped().Model(&model.Resource{}).
		Where("disk_account_id = ? AND file_id IN ?", acc.ID, fileIDs).
		Find(&existing)

	// 同一个 fileId 在库里可能既有活行又有死行（历史遗留），
	// 活行优先：有活行就按"已存在"跳过，不动死行。
	alive := make(map[string]bool, len(existing))
	dead := make(map[string]uint64, len(existing))
	for i := range existing {
		r := &existing[i]
		if r.DeletedAt.Valid {
			if _, ok := dead[r.FileID]; !ok {
				dead[r.FileID] = r.ID
			}
			continue
		}
		alive[r.FileID] = true
	}

	accID := acc.ID
	created, skipped, restored := 0, 0, 0
	for _, t := range targets {
		if alive[t.FileID] {
			skipped++
			continue
		}

		// 标题去掉扩展名，列表更干净；全空了才退回原名。
		// 扩展名本身不能丢 —— 单独存进 Ext，转码判定全靠它。
		ext := ExtOfName(t.Title)
		title := strings.TrimSuffix(t.Title, path.Ext(t.Title))
		if strings.TrimSpace(title) == "" {
			title = t.Title
		}

		// ---- 复活一条软删的资源（见函数头注释）----
		if id, ok := dead[t.FileID]; ok {
			// ⚠️ 这里必须 Unscoped()：不加的话 gorm 会给 UPDATE 补上
			// `AND deleted_at IS NULL`，于是**一行都更新不到**，
			// 而 RowsAffected=0 又不会报错 —— 复活静默失败，
			// 表现和"跳过了"完全一样，只能靠单测钉住。
			err := s.db.Unscoped().Model(&model.Resource{}).Where("id = ?", id).
				Updates(map[string]interface{}{
					"deleted_at":   nil,
					"category_id":  categoryID,
					"mapping_id":   mappingID,
					"title":        title,
					"media_type":   GuessMediaType(t.Title),
					"ext":          ext,
					"owner_dir_id": path.Dir(t.FileID),
					"file_count":   1,
					"total_size":   t.Size,
					"status":       model.ResourceStatusAlive,
				}).Error
			if err != nil {
				log.Printf("[import] 复活资源失败 %s: %v", t.FileID, err)
				skipped++
				continue
			}
			delete(dead, t.FileID)
			alive[t.FileID] = true
			created++
			restored++
			continue
		}

		res := &model.Resource{
			CategoryID:    categoryID,
			Title:         title,
			DiskType:      acc.DiskType,
			DiskAccountID: &accID,
			FileID:        t.FileID,
			OwnerDirID:    path.Dir(t.FileID),
			MediaType:     GuessMediaType(t.Title),
			Ext:           ext,
			MappingID:     mappingID,
			FileCount:     1,
			TotalSize:     t.Size,
			// 文件是刚从存储源列出来的，存在性是确定的，
			// 不用像分享链接那样留 unknown 等巡检。
			Status: model.ResourceStatusAlive,
		}
		if err := s.db.Create(res).Error; err != nil {
			log.Printf("[import] 登记资源失败 %s: %v", t.FileID, err)
			skipped++
			continue
		}
		created++
	}
	if restored > 0 {
		// 单独记一条日志：同步结果里"新增 26"看不出其中有几条是复活，
		// 而这两件事在排查时意义完全不同（复活 = 之前删过）。
		log.Printf("[import] 复活已软删资源 %d 条（account=%d category=%d mapping=%d）",
			restored, acc.ID, categoryID, mappingID)
	}
	return created, skipped, nil
}
