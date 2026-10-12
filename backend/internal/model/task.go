package model

import "time"

// 转存任务状态机
const (
	TaskStatusPending  = "pending"
	TaskStatusRunning  = "running"
	TaskStatusSuccess  = "success"
	TaskStatusFailed   = "failed"
	TaskStatusCanceled = "canceled"

	// TaskStatusEvicted 产物被 GC 回收（非用户主动取消，也不是失败）。
	//
	// 单独成一个状态而不是复用 canceled，是为了让后台能区分
	// 「我没转成功」和「转过但被清理了」——后者重新播放时会自动重转，
	// 不需要管理员介入，混在一起会误导排查方向。
	TaskStatusEvicted = "evicted"
)

// TransferTask 转存任务
type TransferTask struct {
	Base
	Title         string `gorm:"size:300" json:"title"`
	DiskType      string `gorm:"size:20;index" json:"diskType"`
	ShareURL      string `gorm:"size:800" json:"shareUrl"`
	SharePassword string `gorm:"size:50" json:"sharePassword"`

	DiskAccountID uint64 `gorm:"index" json:"diskAccountId"`
	// 网盘内目标目录
	TargetDirID   string `gorm:"size:200" json:"targetDirId"`
	TargetDirName string `gorm:"size:300" json:"targetDirName"`
	// 转存成功后资源归属的系统目录
	CategoryID uint64 `gorm:"index" json:"categoryId"`

	Status   string `gorm:"size:20;default:'pending';index" json:"status"`
	Progress int    `gorm:"default:0" json:"progress"`
	Message  string `gorm:"size:1000" json:"message"`

	// 转存结果
	CreatedResourceID *uint64    `json:"createdResourceId"`
	RetryCount        int        `gorm:"default:0" json:"retryCount"`
	StartedAt         *time.Time `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`

	// 操作人
	OperatorID uint64 `gorm:"index" json:"operatorId"`
}

func (TransferTask) TableName() string { return "transfer_tasks" }

// AliveCheckTask 存活检测任务
//
// 一次巡检的完整记录。为什么不复用 TransferTask 那种「一个资源一条任务」的模式：
// 巡检是「一次跑一批」，按批次记录才有意义（能看到"这轮扫了 300 条、失效 12 条"），
// 逐条建任务会把表撑爆而信息量极低。
type AliveCheckTask struct {
	Base
	Status string `gorm:"size:20;default:'pending';index" json:"status"`

	// Total/Checked 进度；AliveCount/DeadCount/UnknownCount 结果分布
	Total        int `json:"total"`
	Checked      int `json:"checked"`
	AliveCount   int `json:"aliveCount"`
	DeadCount    int `json:"deadCount"`
	UnknownCount int `json:"unknownCount"`

	// Trigger 触发来源：schedule（定时）/ manual（手动）
	Trigger string `gorm:"size:20;default:'manual'" json:"trigger"`
	// Mode 巡检范围：all（全量）/ stale（仅检查过期未检的）
	Mode string `gorm:"size:20;default:'stale'" json:"mode"`

	// FailedBatches 调用检测服务失败的批次数量。
	// 单批失败不该让整轮巡检失败，但要记录下来——
	// 「大量失败」说明对面服务挂了，而不是资源真的失效了，这个区别很关键。
	FailedBatches int `json:"failedBatches"`

	Message    string     `gorm:"size:1000" json:"message"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (AliveCheckTask) TableName() string { return "alive_check_tasks" }

// TranscodeTask 转码任务
type TranscodeTask struct {
	Base
	ResourceID uint64 `gorm:"index" json:"resourceId"`
	FileID     string `gorm:"size:200" json:"fileId"`
	SourcePath string `gorm:"size:800" json:"sourcePath"`
	OutputPath string `gorm:"size:800" json:"outputPath"`

	Status   string `gorm:"size:20;default:'pending';index" json:"status"`
	Progress int    `gorm:"default:0" json:"progress"`
	Message  string `gorm:"size:1000" json:"message"`

	// LastAccessAt 最后一次被播放的时间，GC 的淘汰依据。
	//
	// 刻意不直接用 CreatedAt：孩子反复看的那几部应该留住，
	// 而转码时间早的冷门内容才该被清。播放接口每次命中 HLS 都会刷新它。
	LastAccessAt *time.Time `gorm:"index" json:"lastAccessAt"`

	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (TranscodeTask) TableName() string { return "transcode_tasks" }
