package model

import "time"

// 资源状态
const (
	ResourceStatusAlive   = "alive"
	ResourceStatusDead    = "dead"
	ResourceStatusUnknown = "unknown"
)

// 媒体类型
const (
	MediaTypeVideo = "video"
	MediaTypeAudio = "audio"
	MediaTypePDF   = "pdf"
	MediaTypeImage = "image"
	MediaTypeOther = "other"
)

// Resource 资源
type Resource struct {
	Base
	CategoryID  uint64 `gorm:"index;not null" json:"categoryId"`
	Title       string `gorm:"size:300;not null;index" json:"title"`
	Description string `gorm:"type:text" json:"description"`

	// 网盘来源
	DiskType string `gorm:"size:20;index" json:"diskType"` // quark/baidu/uc/tianyi
	ShareURL string `gorm:"size:800" json:"shareUrl"`
	Passcode string `gorm:"size:50" json:"passcode"`
	ShareID  string `gorm:"size:200;index" json:"shareId"` // 分享ID，用于去重

	// ShareExpireAt 分享在网盘那边的失效时间（Unix 秒）。
	//
	// 0 = **永久有效**，或"不知道"（本字段是后加的，存量行是 NULL/0）。
	//
	// 为什么要记：网盘的分享默认会过期（夸克 30 天）。过期之后
	// share_url 仍然躺在库里、看着一切正常，但孩子端点播时网盘回
	// "分享已失效" —— 而自动补齐任务按"已经有 share_url 就跳过"处理，
	// 于是**永远不会去修**，孩子端从此一直播不了，且没人知道原因。
	// 记下失效时间，自动任务就能把"已经过期的"当成"没分享"重新补上。
	ShareExpireAt int64 `json:"shareExpireAt"`

	// 转存后的信息
	DiskAccountID *uint64 `gorm:"index" json:"diskAccountId"` // 转存到哪个账号
	// OwnerDirID / FileID 对网盘是短 ID，但对 WebDAV 是**相对路径**，
	// NAS 上的中文片名路径很容易超过 200 字符，故放宽到 500。
	OwnerDirID string `gorm:"size:500" json:"ownerDirId"` // 网盘内所在目录ID
	FileID     string `gorm:"size:500" json:"fileId"`     // 网盘内文件ID

	// MappingID 这条资源是由哪个「资源映射」同步进来的（0 = 手动导入/搜索收录）。
	//
	// 为什么必须落一列而不是靠 (账号 + 目录) 现推：
	// 删除映射时用户**期望**资源跟着消失，但同一个远端目录完全可能被
	// 先后映射两次（线上就发生过：夸克映射一次、WebDAV 又映射一次，
	// 孩子端于是出现 26 集重复列表）。只按账号+目录去推，第二次映射
	// 会把第一次的记录一并算进去，删多了没法收拾。
	//
	// 有了它，"删映射时连带清理资源"才能精确到只动这一条映射导入的那些。
	MappingID uint64 `gorm:"index" json:"mappingId"`

	// 元信息
	MediaType string `gorm:"size:20;index" json:"mediaType"`
	// Ext 文件扩展名（小写、不含点，如 "rmvb" / "mp4"）。
	//
	// 为什么必须单独存一列：Title 在入库时被刻意去掉了扩展名（列表更干净），
	// 而"能不能直接交给浏览器播"完全取决于**容器格式** —— 拿 Title 判后缀
	// 永远判不出来，结果 RMVB/AVI/MKV 这类浏览器原生不支持的资源从不触发
	// 转码，一路直链送到 <video> 必然报错，前端只能提示"播放地址可能已过期"。
	Ext       string `gorm:"size:20" json:"ext"`
	Cover     string `gorm:"size:800" json:"cover"`
	FileCount int    `json:"fileCount"`
	TotalSize int64  `json:"totalSize"`
	Duration  int    `json:"duration"` // 秒

	// DirectPlay 强制走直链播放，跳过转码。
	//
	// 用于「搜一搜 · 搜全网」的即时转存播放：用户点了就要看到画面，
	// 而转码要跑几分钟。这类资源宁可先用网盘直链播着（后端代理转发、
	// 支持 Range 拖动），也不该让用户对着"转码中"等。
	DirectPlay bool `gorm:"default:false" json:"directPlay"`

	// 状态
	Status        string     `gorm:"size:20;default:'unknown';index" json:"status"`
	LastCheckedAt *time.Time `json:"lastCheckedAt"`

	// 巡检辅助
	//
	// ConsecutiveFailures：连续失效次数。单次判定的可靠性有限
	// （对面服务抖动、网盘限流都会导致误判），所以只有**连续 N 次**判失效
	// 才真正标记为 dead。这样能把偶发的假阳性挡在外面。
	ConsecutiveFailures int `gorm:"default:0" json:"consecutiveFailures"`
	// CheckFailCount 检测过程本身出错的次数（区别于"资源失效"）
	CheckFailCount int `gorm:"default:0" json:"checkFailCount"`

	// 标签 & 排序
	Tags      StringArray `gorm:"type:text" json:"tags"`
	SortOrder int         `gorm:"default:0" json:"sortOrder"`
	ViewCount int64       `gorm:"default:0" json:"viewCount"`

	Category *Category `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
}

func (Resource) TableName() string { return "resources" }

// PlayRecord 播放记录（断点续播）
type PlayRecord struct {
	Base
	UserID     uint64    `gorm:"index:idx_user_res,unique;not null" json:"userId"`
	ResourceID uint64    `gorm:"index:idx_user_res,unique;not null" json:"resourceId"`
	FileID     string    `gorm:"size:200" json:"fileId"` // 资源内具体文件
	Position   int       `json:"position"`               // 当前进度（秒）
	Duration   int       `json:"duration"`               // 总时长（秒）
	Percentage float64   `json:"percentage"`             // 完成百分比
	Finished   bool      `gorm:"default:false" json:"finished"`
	PlayedAt   time.Time `json:"playedAt"`
}

func (PlayRecord) TableName() string { return "play_records" }
