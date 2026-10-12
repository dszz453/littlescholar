package model

import "time"

// Role 角色
const (
	RoleAdmin = "admin" // 管理员
	RoleKid   = "kid"   // 孩子账号
)

// 用户状态
const (
	UserStatusDisabled = 0
	UserStatusEnabled  = 1
)

// User 用户（管理员 + 孩子账号统一表）
type User struct {
	Base
	Username     string     `gorm:"size:50;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:200;not null" json:"-"`
	Nickname     string     `gorm:"size:50" json:"nickname"`
	Role         string     `gorm:"size:20;default:'kid';index" json:"role"`
	Avatar       string     `gorm:"size:300" json:"avatar"`
	Grade        string     `gorm:"size:20" json:"grade"`      // 年级，供 AI 分析
	Age          int        `json:"age"`
	Status       int8       `gorm:"default:1;index" json:"status"`
	ExpiresAt    *time.Time `json:"expiresAt"` // 账号整体有效期，nil = 永久
	LastLoginAt  *time.Time `json:"lastLoginAt"`

	Grants []UserCategoryGrant `gorm:"foreignKey:UserID" json:"grants,omitempty"`
}

func (User) TableName() string { return "users" }

// IsExpired 判断账号是否过期
func (u *User) IsExpired() bool {
	if u.ExpiresAt == nil {
		return false
	}
	return u.ExpiresAt.Before(time.Now())
}

// IsActive 账号是否可用
func (u *User) IsActive() bool {
	return u.Status == UserStatusEnabled && !u.IsExpired()
}

// UserCategoryGrant 用户-目录授权
type UserCategoryGrant struct {
	Base
	UserID      uint64     `gorm:"index;not null" json:"userId"`
	CategoryID  uint64     `gorm:"index;not null" json:"categoryId"`
	CanDownload bool       `gorm:"default:true" json:"canDownload"`
	ExpiresAt   *time.Time `json:"expiresAt"` // 该目录授权的有效期，nil = 永久

	Category *Category `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
}

func (UserCategoryGrant) TableName() string { return "user_category_grants" }

// IsExpired 授权是否过期
func (g *UserCategoryGrant) IsExpired() bool {
	if g.ExpiresAt == nil {
		return false
	}
	return g.ExpiresAt.Before(time.Now())
}
