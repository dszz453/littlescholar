package model

import "time"

// 网盘账号状态
const (
	DiskAccountValid   = "valid"
	DiskAccountInvalid = "invalid"
	DiskAccountExpired = "expired"
)

// DiskAccount 网盘账号（存储 cookie，加密）
type DiskAccount struct {
	Base
	DiskType string `gorm:"size:20;index;not null" json:"diskType"` // quark/baidu/uc/tianyi/aliyun
	Nickname string `gorm:"size:100" json:"nickname"`
	Avatar   string `gorm:"size:500" json:"avatar"`

	// CookieEncrypted 使用 AES-256-GCM 加密后的 cookie，绝不返回给前端
	CookieEncrypted string `gorm:"type:text" json:"-"`
	// CookiePreview 用于前端展示的脱敏预览
	CookiePreview string `gorm:"size:100" json:"cookiePreview"`

	// 附加凭证（如百度 BDUSS、天翼的 app token）
	Extra JSONMap `gorm:"type:text" json:"-"`

	Status    string     `gorm:"size:20;default:'valid';index" json:"status"`
	LastCheckAt *time.Time `json:"lastCheckAt"`

	CapacityUsed  int64 `json:"capacityUsed"`
	CapacityTotal int64 `json:"capacityTotal"`

	// 是否默认账号（同类型网盘默认使用此账号转存）
	IsDefault bool `gorm:"default:false" json:"isDefault"`
	// 限流配置
	RateLimit int `gorm:"default:2" json:"rateLimit"` // 每秒最大请求数

	Remark string `gorm:"size:500" json:"remark"`
}

func (DiskAccount) TableName() string { return "disk_accounts" }

// QRLoginSession 扫码登录会话（临时，也可存 Redis）
type QRLoginSession struct {
	Base
	SessionID  string     `gorm:"size:64;uniqueIndex" json:"sessionId"`
	DiskType   string     `gorm:"size:20" json:"diskType"`
	Status     string     `gorm:"size:20;default:'waiting'" json:"status"` // waiting/scanned/confirmed/expired/failed
	QRContent  string     `gorm:"size:1000" json:"qrContent"`
	QRImage    string     `gorm:"type:text" json:"qrImage"`
	CookieRaw  string     `gorm:"type:text" json:"-"`   // 临时存放，确认后写入 DiskAccount 并清空
	ExtraRaw   string     `gorm:"type:text" json:"-"`
	AccountID  *uint64    `json:"accountId"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

func (QRLoginSession) TableName() string { return "qr_login_sessions" }
