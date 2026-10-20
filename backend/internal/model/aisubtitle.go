package model

import "time"

// AISubtitleTask 一条「把这件资料交给大模型写字幕」的任务。
//
// 为什么任务要落库而不是只放内存队列：
//   - 一批可能上百条，跑一整晚；服务重启 / 容器重建后队列不该凭空消失；
//   - 每条的结果（成功/失败/为什么跳过）需要留下来给管理员看，
//     否则又变成「静默兜底成功、坏了几天才知道」；
//   - 同一件资源只该排一次，靠 resource_id 的唯一索引兜住。
//
// ⚠️ 这里只记**任务状态**，字幕正文仍然落在 resource_subtitles（origin='ai'），
// 播放链路只认那一张表 —— 任务表删了也不影响已经生成的字幕。
type AISubtitleTask struct {
	Base

	// ResourceID 一条资源只排一个任务（唯一索引）。
	ResourceID uint64 `gorm:"uniqueIndex;not null" json:"resourceId"`
	// Title 冗余存一份标题：资源被删/被改名之后，任务列表里还能看出它是谁。
	Title string `gorm:"size:300" json:"title"`

	// Status 见 AISubtitleStatus*。排队/执行中/完成/失败/跳过。
	Status string `gorm:"size:20;index;not null" json:"status"`
	// Attempts 第几次尝试。失败重试会 +1，用来在页面上区分「一次就成」和「折腾了很久」。
	Attempts int `json:"attempts"`
	// Note 最近一次的结果说明（失败原因 / 跳过原因 / 对齐方式）。
	Note string `gorm:"size:500" json:"note"`

	// Model 实际用的模型名（换了模型之后，回看某条字幕是谁写的）。
	Model string `gorm:"size:100" json:"model"`
	// Cues 生成了多少条字幕。
	Cues int `json:"cues"`
	// Align 时间轴怎么来的：speech（按静音切分对齐）/ even（按字数均分）。
	Align string `gorm:"size:20" json:"align"`

	FinishedAt *time.Time `json:"finishedAt"`
}

// TableName 表名
func (AISubtitleTask) TableName() string { return "ai_subtitle_tasks" }

// 任务状态
const (
	// AISubtitleStatusPending 排队中，等计划时段（或手动「立即执行」）。
	AISubtitleStatusPending = "pending"
	// AISubtitleStatusRunning 正在调模型。
	AISubtitleStatusRunning = "running"
	// AISubtitleStatusDone 已生成并入库。
	AISubtitleStatusDone = "done"
	// AISubtitleStatusFailed 调用失败 / 拿不到时长等，可重试。
	AISubtitleStatusFailed = "failed"
	// AISubtitleStatusSkipped 模型明确说"不确定"，或已有字幕且没开覆盖。
	//
	// 单独一个状态而不是混进 failed：这两种情况**重试也没用**
	// （同一份标题问同一家模型，答案不会变），页面不该让人一条条去点重试。
	AISubtitleStatusSkipped = "skipped"
)

// AISubtitlePlan 批量生成字幕的计划（单行，id=1）。
//
// 用户的原话：「可以设置频率，计划，比如每天晚上几点到几点自动生成」。
// 所以这里是**时段**而不是 cron：只在 [StartMinute, EndMinute) 之间干活，
// 窗口内每轮处理 BatchPerRound 条、两条之间喘 GapSeconds 秒。
//
// 时段允许跨零点（22:00 → 06:00）：睡觉时间生成是这个功能最主要的用法，
// 写成"必须在同一天内"等于让主要用法用不了。
type AISubtitlePlan struct {
	ID        uint64    `gorm:"primaryKey" json:"id"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Enabled 总开关。关掉之后队列原样保留，只是不自动消费。
	Enabled bool `json:"enabled"`
	// StartMinute / EndMinute 一天的分钟数 0-1439。结束时间不含。
	StartMinute int `json:"startMinute"`
	EndMinute   int `json:"endMinute"`
	// BatchPerRound 每次唤醒最多处理几条。一条 = 一次大模型调用，
	// 默认 5 条：既能在一晚上推进几十条，又不至于一次把额度打爆。
	BatchPerRound int `json:"batchPerRound"`
	// GapSeconds 两条之间歇多久。给模型接口留余量，也避免瞬时并发。
	GapSeconds int `json:"gapSeconds"`
	// Overwrite 已有字幕的资源也重做。
	//
	// 默认 false：库里那条可能是同目录的原版字幕（时间轴是准的），
	// 用模型按标题复原的版本盖掉它是**降级**。要在页面上明确勾选才覆盖。
	Overwrite bool `json:"overwrite"`
	// Note 附加给模型的提示（比如"这是大猫英语分级阅读三级"）。
	// 有些资源标题只有一册的名字、没有套系名，补一句能明显提高还原准确率。
	Note string `gorm:"size:500" json:"note"`
}

// TableName 表名
func (AISubtitlePlan) TableName() string { return "ai_subtitle_plans" }

// 计划默认值（没有存过配置时用）。
const (
	AISubtitleDefaultStartMinute = 22 * 60 // 22:00
	AISubtitleDefaultEndMinute   = 6 * 60  // 06:00
	AISubtitleDefaultBatch       = 5
	AISubtitleDefaultGapSeconds  = 20
)
