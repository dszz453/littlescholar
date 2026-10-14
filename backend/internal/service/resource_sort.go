package service

import (
	"strings"

	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
)

// 孩子端「文件夹内列表」与「播放列表」共用的排序。
//
// 为什么两处必须共用一份实现：它们表达的是同一件事 ——
// "这个目录里内容的先后顺序"。列表负责展示、播放列表负责上一集/下一集，
// 一旦两处各写一套，就会出现"列表里第一个是 A，点播放的下一集却是 B"
// 这种自相矛盾（这个项目已经踩过同口径的坑，见 MEMORY 里的聚合口径那条）。

// SortItem 排序所需的最小字段集。
//
// 只带这几个字段是有意的：列表接口需要**先按这个顺序排好、再分页**，
// 若只排当前页，第 3 页的视频永远上不到第 1 页来。
type SortItem struct {
	ID        uint64
	Title     string
	MediaType string
	Ext       string
}

// Less 音视频优先 → 标题自然升序 → id 升序兜底（保证结果稳定可复现）。
func (s SortItem) Less(o SortItem) bool {
	ra, rb := MediaRank(s.MediaType, s.Ext), MediaRank(o.MediaType, o.Ext)
	if ra != rb {
		return ra < rb
	}
	if c := util.NaturalCompare(s.Title, o.Title); c != 0 {
		return c < 0
	}
	// 同名（很常见：不同目录下都有 "01"）时用 id 兜底，否则 SliceStable
	// 的顺序会随底层查询顺序漂移，分页时可能重复或漏项。
	return s.ID < o.ID
}

// MediaRank 排序权重：音视频 = 0（排前面），其余 = 1。
//
// 判定口径**刻意复用** MediaTypeByExt，与播放时的 GuessMediaType 同一张表 ——
// 否则会出现「排在前面的是 PDF、音视频反而在后面」这种和预期相反的结果。
func MediaRank(mediaType, ext string) int {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if mt == "" {
		// media_type 是 AutoMigrate 补出来的列，存量行可能是空的
		// （见 MEMORY 里"补列不回填默认值"那条），这时退回按扩展名判。
		mt = MediaTypeByExt(ext)
	}
	if mt == model.MediaTypeAudio || mt == model.MediaTypeVideo {
		return 0
	}
	return 1
}
