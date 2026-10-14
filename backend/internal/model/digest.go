package model

import "time"

// 知识沉淀状态
const (
	DigestStatusReady = "ready" // 已生成
	DigestStatusStale = "stale" // 内容已变，等待重算（本轮先只用于展示判断）
	DigestStatusFail  = "failed"
)

// DigestScopeAll 表示「全站总览」那条沉淀。
//
// 用 0 而不是 NULL：唯一索引对 NULL 不生效（PostgreSQL / SQLite 都是），
// 而本表的 upsert 完全依赖 `category_id` 唯一约束做幂等，一旦允许 NULL
// 就会出现多条全站沉淀，越攒越多。
const DigestScopeAll uint64 = 0

// KnowledgeDigest 小书童对一个目录（或全站）做的知识沉淀。
//
// 一句话定义：**把某个目录里成百上千个文件名，读成一段"这里有什么、
// 先学什么、重点是什么"的人话**。
//
// 为什么要落库而不是每次实时问 AI：
//   - 孩子点开目录是高频操作，AI 一次要几秒到几十秒，实时算体验不可接受；
//   - 目录内容不是每天都变，每天重算一遍纯属浪费额度。
//
// 于是引入 Fingerprint：把「该范围内资源的 ID + 标题」排序后取哈希。
// 内容没变就直接跳过，有增删改才重新生成。这既保证「每天学习」这件事
// 真的在跑，又不会没变化时空烧 token。
type KnowledgeDigest struct {
	Base
	// CategoryID 目录 ID；DigestScopeAll(0) 表示全站总览
	CategoryID uint64 `gorm:"uniqueIndex;not null" json:"categoryId"`
	// CategoryName 冗余目录名：目录改名后仍能看出这条沉淀当初写的是谁
	CategoryName string `gorm:"size:100" json:"categoryName"`
	// Breadcrumb 生成时的物化路径面包屑，如「乐学英语 / 新概念英语」
	Breadcrumb string `gorm:"size:300" json:"breadcrumb"`

	Intro    string      `gorm:"type:text" json:"intro"`    // 一段介绍（80~150 字）
	Points   StringArray `gorm:"type:text" json:"points"`   // 学习要点 / 推荐顺序
	Keywords StringArray `gorm:"type:text" json:"keywords"` // 关键词，用于展示与检索

	ResourceCount int    `json:"resourceCount"`              // 生成时该范围的资源数
	Fingerprint   string `gorm:"size:64" json:"fingerprint"` // 内容指纹

	Status    string `gorm:"size:20;default:'ready';index" json:"status"`
	LastError string `gorm:"size:500" json:"lastError"`

	AIConfigID  uint64     `json:"aiConfigId"`
	GeneratedAt *time.Time `json:"generatedAt"`
}

func (KnowledgeDigest) TableName() string { return "knowledge_digests" }
