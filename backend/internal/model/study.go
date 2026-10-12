package model

import "time"

// StudyRecord 学习行为记录（供 AI 分析）
type StudyRecord struct {
	Base
	UserID       uint64    `gorm:"index:idx_study_user_time;not null" json:"userId"`
	ResourceID   uint64    `gorm:"index" json:"resourceId"`
	CategoryID   uint64    `gorm:"index" json:"categoryId"`
	CategoryName string    `gorm:"size:100" json:"categoryName"`
	ResourceTitle string   `gorm:"size:300" json:"resourceTitle"`

	Action       string    `gorm:"size:30;index" json:"action"` // play_start/play_progress/play_finish/download/search/ai_chat
	Duration     int       `json:"duration"`                    // 本次持续秒数
	Position     int       `json:"position"`
	MediaType    string    `gorm:"size:20" json:"mediaType"`
	HappenedAt   time.Time `gorm:"index:idx_study_user_time" json:"happenedAt"`

	// 用于聚合分析的冗余字段
	StudyDate string `gorm:"size:10;index" json:"studyDate"` // YYYY-MM-DD
	StudyHour int    `gorm:"index" json:"studyHour"`         // 0-23
}

func (StudyRecord) TableName() string { return "study_records" }

// AIConfig AI 接口配置
type AIConfig struct {
	Base
	Name     string `gorm:"size:100;not null" json:"name"`
	Provider string `gorm:"size:30;default:'openai'" json:"provider"` // openai/deepseek/qwen/ollama/custom
	BaseURL  string `gorm:"size:300" json:"baseUrl"`
	APIKeyEncrypted string `gorm:"type:text" json:"-"`
	APIKeyMasked    string `gorm:"size:100" json:"apiKeyMasked"`
	Model      string  `gorm:"size:100" json:"model"`
	Temperature float64 `gorm:"default:0.7" json:"temperature"`
	MaxTokens   int     `gorm:"default:4096" json:"maxTokens"`
	TimeoutSec  int     `gorm:"default:60" json:"timeoutSec"`
	IsDefault   bool    `gorm:"default:false" json:"isDefault"`
	Status      string  `gorm:"size:20;default:'unknown'" json:"status"`
	Remark      string  `gorm:"size:500" json:"remark"`
}

func (AIConfig) TableName() string { return "ai_configs" }

// AIReport AI 生成的学习报告
type AIReport struct {
	Base
	UserID      uint64     `gorm:"index;not null" json:"userId"`
	AIConfigID  uint64     `json:"aiConfigId"`
	PeriodStart time.Time  `json:"periodStart"`
	PeriodEnd   time.Time  `json:"periodEnd"`

	Summary   string      `gorm:"type:text" json:"summary"`
	Highlights StringArray `gorm:"type:text" json:"highlights"`
	Weaknesses StringArray `gorm:"type:text" json:"weaknesses"`
	Suggestions StringArray `gorm:"type:text" json:"suggestions"`
	RawContent string     `gorm:"type:text" json:"rawContent"`

	Stats JSONMap `gorm:"type:text" json:"stats"` // 生成报告时的统计数据快照
}

func (AIReport) TableName() string { return "ai_reports" }

// AIChatSession AI 对话会话
type AIChatSession struct {
	Base
	SessionID string `gorm:"size:64;uniqueIndex" json:"sessionId"`
	UserID    uint64 `gorm:"index" json:"userId"`
	Title     string `gorm:"size:200" json:"title"`
}

func (AIChatSession) TableName() string { return "ai_chat_sessions" }

// AIChatMessage AI 对话消息
type AIChatMessage struct {
	Base
	SessionID string `gorm:"size:64;index" json:"sessionId"`
	Role      string `gorm:"size:20" json:"role"` // user / assistant / system
	Content   string `gorm:"type:text" json:"content"`
	// 附带推荐的资源
	RecommendResourceIDs StringArray `gorm:"type:text" json:"recommendResourceIds"`
}

func (AIChatMessage) TableName() string { return "ai_chat_messages" }

// SystemConfig 系统配置 KV
type SystemConfig struct {
	Base
	Key   string `gorm:"size:100;uniqueIndex;not null" json:"key"`
	Value string `gorm:"type:text" json:"value"`
	Group string `gorm:"size:50;index" json:"group"`
	Remark string `gorm:"size:500" json:"remark"`
}

func (SystemConfig) TableName() string { return "system_configs" }
