package subtitle

import (
	"errors"
	"sort"
	"strings"

	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
)

// Store 字幕的**永久存储**（表 resource_subtitles）。
//
// 为什么不让播放接口每次现算：现算要打网盘 API（列目录/取直链）、
// 跑 ffprobe、必要时联网搜 —— 一次几秒到几十秒，还全压在播放这个动作上。
// 采集器在后台算一次、存下来，播放时就是一次主键查询。
type Store struct {
	db *gorm.DB
}

// NewStore 构造。db 为 nil 时所有方法安全返回空（未接数据库的场景）。
func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

// ErrNoStore 没有数据库时的统一错误。
var ErrNoStore = errors.New("字幕存储不可用")

// Get 取一条资源的字幕。没有就返回 (nil, nil) —— "没有"不是错误。
func (s *Store) Get(resourceID uint64) (*model.ResourceSubtitle, error) {
	if s == nil || s.db == nil || resourceID == 0 {
		return nil, nil
	}
	var row model.ResourceSubtitle
	err := s.db.Where("resource_id = ?", resourceID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(row.VTT) == "" {
		return nil, nil
	}
	return &row, nil
}

// Save 写入/覆盖一条字幕。
//
// ⚠️ 手工指定的那条不许被后台任务覆盖：采集器会反复扫同一批资源，
// 如果每次都用"自动匹配到的"把人工校对过的顶掉，用户就没法固定字幕了。
func (s *Store) Save(row *model.ResourceSubtitle) error {
	if s == nil || s.db == nil {
		return ErrNoStore
	}
	if row == nil || row.ResourceID == 0 || strings.TrimSpace(row.VTT) == "" {
		return errors.New("字幕内容为空")
	}
	row.VTT = NormalizeNewlines(row.VTT)

	var old model.ResourceSubtitle
	err := s.db.Where("resource_id = ?", row.ResourceID).First(&old).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(row).Error
	}
	if err != nil {
		return err
	}
	if old.Manual && row.Origin != "manual" {
		// 人工的那条优先，后台任务让路（不报错：这是正常情况）
		return nil
	}
	row.ID = old.ID
	row.CreatedAt = old.CreatedAt
	row.Manual = old.Manual || row.Manual
	return s.db.Save(row).Error
}

// Delete 删掉某条资源的字幕（重新匹配用）。
func (s *Store) Delete(resourceID uint64) error {
	if s == nil || s.db == nil {
		return ErrNoStore
	}
	return s.db.Where("resource_id = ?", resourceID).Delete(&model.ResourceSubtitle{}).Error
}

// Stats 已存多少条、总共多少条音视频资源待配。
type Stats struct {
	Stored   int64 `json:"stored"`
	Video    int64 `json:"video"`
	WithSub  int64 `json:"withSub"`
	LastScan string `json:"lastScan"`
}

// Stats 汇总（管理页用）。
func (s *Store) Stats() Stats {
	var st Stats
	if s == nil || s.db == nil {
		return st
	}
	s.db.Model(&model.ResourceSubtitle{}).Count(&st.Stored)
	// ⚠️ 软删的资源不算：Table() 绕不过 gorm 的软删，
	// 所以用 Model() + 正常条件，别用 Raw。
	s.db.Model(&model.Resource{}).
		Where("deleted_at IS NULL").
		Where("COALESCE(media_type,'') IN ?", []string{model.MediaTypeVideo, model.MediaTypeAudio}).
		Count(&st.Video)
	st.WithSub = st.Stored
	return st
}

// Missing 找"还没有字幕"的音视频资源，最新的优先。
//
// limit 卡住的是**每轮**的工作量：每条都要列目录 + 可能取直链，
// 一轮扫太多会把网盘 API 打到限流，也会拖慢正在看的播放。
func (s *Store) Missing(limit int) ([]model.Resource, error) {
	if s == nil || s.db == nil {
		return nil, ErrNoStore
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	var rows []model.Resource
	err := s.db.Model(&model.Resource{}).
		Where("deleted_at IS NULL").
		Where("COALESCE(media_type,'') IN ?", []string{model.MediaTypeVideo, model.MediaTypeAudio}).
		Where("file_id <> ''").
		Where("owner_dir_id IS NOT NULL AND owner_dir_id <> ''").
		// 已经有字幕的跳过（NOT EXISTS 比 LEFT JOIN 更好读，也不受 NULL 影响）
		Where("NOT EXISTS (SELECT 1 FROM resource_subtitles rs WHERE rs.resource_id = resources.id AND rs.vtt <> '')").
		Order("id DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// SiblingCandidate 同一目录下可能是字幕的文件。
type SiblingCandidate struct {
	FileID string `json:"fileId"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	// Score 与视频文件名的相似度（0-100）
	Score int `json:"score"`
}

// RankSiblings 从同目录文件里挑出字幕候选，按相似度降序。
//
// 纯本地判断，不花 AI 额度 —— AI 只在"前两名分不开"时才上（见 collector）。
func RankSiblings(videoName string, entries []SiblingEntry) []SiblingCandidate {
	var out []SiblingCandidate
	for _, e := range entries {
		if e.IsDir || !IsSubtitle(e.Name) {
			continue
		}
		// MatchScore 内部会去掉扩展名，这里直接传原始文件名
		s := MatchScore(videoName, e.Name)
		if s <= 0 {
			continue
		}
		out = append(out, SiblingCandidate{FileID: e.ID, Name: e.Name, Size: e.Size, Score: s})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// SiblingEntry 目录里的一项（与 adapter.FileItem 解耦，方便单测）。
type SiblingEntry struct {
	ID    string
	Name  string
	IsDir bool
	Size  int64
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i:])
	}
	return ""
}

// NormalizeNewlines 统一换行，并在缺 WEBVTT 头时补上。
//
// 采集器写库前都过一遍：VTT 的解析器对缺头/CRLF 很敏感，
// 缺了头浏览器会整条不认，表现是"字幕存下来了但一句都不显示"。
func NormalizeNewlines(vtt string) string {
	s := strings.ReplaceAll(vtt, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimLeft(s, "\uFEFF")
	if !strings.HasPrefix(strings.TrimSpace(s), "WEBVTT") {
		s = "WEBVTT\n\n" + strings.TrimSpace(s) + "\n"
	}
	return s
}

// CountCues 数一下有多少条字幕（管理页/统计用）。
//
// 判定标准就是"含 --> 的时间轴行"：VTT/SRT 都靠它。
// 不能用"空行分段数"——很多字幕每条之间不留空行，会数成 1。
func CountCues(vtt string) int {
	n := 0
	for _, line := range strings.Split(vtt, "\n") {
		if strings.Contains(line, "-->") {
			n++
		}
	}
	return n
}
