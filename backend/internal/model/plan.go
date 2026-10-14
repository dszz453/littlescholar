package model

// 学习计划状态
const (
	StudyPlanStatusActive    = "active"    // 进行中
	StudyPlanStatusFinished  = "finished"  // 已结束（过了 end_date）
	StudyPlanStatusCancelled = "cancelled" // 已取消
)

// StudyPlan 学习计划。
//
// 一句话定义：**在某个日期区间内，要求孩子每天在指定目录下学习不少于 X 分钟**。
//
// 为什么 category 要用 ID + Name 两个字段：
// 目录改完名之后，历史计划里仍要能显示当初那个名字，否则孩子回看
// 「我这周的计划」会看到一堆对不上的目录名。
type StudyPlan struct {
	Base
	UserID uint64 `gorm:"index;not null" json:"userId"`

	Title      string `gorm:"size:200" json:"title"`      // 展示用标题，留空则回落到目录名
	CategoryID uint64 `gorm:"index;not null" json:"categoryId"`
	CategoryName string `gorm:"size:100" json:"categoryName"`

	// StartDate / EndDate 都是本地时区的 YYYY-MM-DD 字符串。
	//
	// 跟 StudyRecord.StudyDate 同一套口径：字符串字典序即时间序，
	// 区间比较不用做任何时区转换，也避开「纯日期串 vs 带时区时间戳」
	// 的隐式转换（这个坑在本项目已经踩过一次）。
	StartDate string `gorm:"size:10;index;not null" json:"startDate"`
	EndDate   string `gorm:"size:10;index;not null" json:"endDate"`

	// MinMinutes 每天最低学习分钟数
	MinMinutes int `gorm:"default:20" json:"minMinutes"`

	Status string `gorm:"size:20;default:'active';index" json:"status"`
}

func (StudyPlan) TableName() string { return "study_plans" }

// StudyCheckIn 每日打卡记录。
//
// 一个计划一天最多一条（唯一索引保证），重复"打卡"是更新而不是插入 ——
// 否则孩子反复进出计划页会刷出一堆重复记录，周统计直接失真。
type StudyCheckIn struct {
	Base
	UserID uint64 `gorm:"index:idx_checkin_unique,unique;not null" json:"userId"`
	PlanID uint64 `gorm:"index:idx_checkin_unique,unique;not null" json:"planId"`
	// StudyDate YYYY-MM-DD
	StudyDate string `gorm:"size:10;index:idx_checkin_unique,unique" json:"studyDate"`

	Minutes   int  `json:"minutes"`   // 达标当刻统计到的当日分钟数
	Completed bool `gorm:"default:false;index" json:"completed"`
}

func (StudyCheckIn) TableName() string { return "study_check_ins" }

// StudyAward 每周 AI 奖状。
//
// 口径是"用户 + 一周一张"，不绑具体计划 —— 需求说的是"每一周坚持下来
// 给一个 AI 奖状"，那是给孩子的整体肯定，不是给某个计划的。
// 一周内同时有几个计划的打卡天数一起算。
type StudyAward struct {
	Base
	UserID uint64 `gorm:"index:idx_award_unique,unique;not null" json:"userId"`
	// WeekStart 周一，WeekEnd 周日，均为 YYYY-MM-DD
	WeekStart string `gorm:"size:10;index:idx_award_unique,unique" json:"weekStart"`
	WeekEnd   string `gorm:"size:10" json:"weekEnd"`

	DaysChecked  int `json:"daysChecked"`  // 本周打卡达标天数
	TotalMinutes int `json:"totalMinutes"` // 本周累计学习分钟数

	// 以下四项目标是"一张好看的奖状"所需的全部展示素材
	Title   string `gorm:"size:200" json:"title"`  // AI 起的称号，如「小小英语探险家」
	Emoji   string `gorm:"size:20" json:"emoji"`   // 奖章表情
	Content string `gorm:"type:text" json:"content"` // AI 写的颁奖词
	Color   string `gorm:"size:30" json:"color"`   // 奖状主色

	AIConfigID uint64 `json:"aiConfigId"` // 生成它的 AI 配置（便于排查）
}

func (StudyAward) TableName() string { return "study_awards" }
