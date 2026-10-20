package model

import "time"

// ResourceSubtitle 一条已经配好的字幕（**永久存储**）。
//
// ⚠️ 它和 `/app/data/subtitle/<id>.vtt` 那份缓存是两件事：
//   · 缓存是"这次算出来的结果"，目录一清、容器一换就没了；
//   · 这张表是"匹配到过一次就存下来"，下次播放直接命中，
//     不再打网盘 API、不再联网搜、不再等 ffprobe 探测。
//
// 用户要的「后台自动搜索匹配并存储」落在就是这张表：
// 采集器在后台跑，命中后写这里；播放接口先查这里，再退回缓存/实时解析。
//
// 正文（VTT）直接放库里而不是只放文件：字幕是几十 KB 的纯文本，
// 放库才能跟着备份/迁移一起走，也不会因为清理缓存目录而丢掉。
type ResourceSubtitle struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// ResourceID 一条资源只留一条字幕（唯一索引）。
	// 想要多语言时把唯一索引改成 (resource_id, lang) 即可，现在不做：
	// 孩子端没有语言切换，多存几份只是浪费空间和匹配次数。
	ResourceID uint64 `gorm:"uniqueIndex;not null" json:"resourceId"`

	Lang  string `gorm:"size:32" json:"lang"`
	// Source 给人看的来源，例如「同目录字幕 · EP01.srt」。
	Source string `gorm:"size:255" json:"source"`
	// Origin 给机器看的来源：sibling / embedded / online / manual。
	Origin string `gorm:"size:32" json:"origin"`
	// Confidence 0-100。AI 打分或按名字相似度算，低分不存（宁可没有）。
	Confidence int `json:"confidence"`
	Cues       int `json:"cues"`
	Size       int `json:"size"`

	// VTT 正文。接口不下发（走 /api/subtitle），所以 json:"-"。
	VTT string `gorm:"type:text" json:"-"`

	// Manual 人工指定/上传过：后台任务不许覆盖它。
	Manual bool `json:"manual"`
}

// TableName 表名
func (ResourceSubtitle) TableName() string { return "resource_subtitles" }
