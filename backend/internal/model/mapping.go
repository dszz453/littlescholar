package model

import "time"

// ResourceMapping 资源映射
//
// 解决的问题：NAS / Alist 上已经有一整套按目录整理好的视频，
// 每次加一个片子都要手动「浏览 → 选文件 → 导入」太繁琐。
// 映射把「存储源的一个远端目录」和「系统的一个内容目录」绑定起来，
// 之后一次同步就能把目录里的媒体文件全部登记为资源。
//
// 为什么复用 DiskAccount 而不是新建一张存储源表：
// WebDAV 已经作为 adapter.DiskAdapter 接入了账号体系（类型 webdav），
// 浏览目录、解密凭据、取直链这些能力全部现成。再造一张表等于把
// 同一件事描述两遍，两边的状态还会不一致。
type ResourceMapping struct {
	Base
	Name string `gorm:"size:200" json:"name"`

	// 存储源账号（disk_accounts.id，disk_type=webdav 或任意网盘）
	AccountID uint64 `gorm:"index;not null" json:"accountId"`

	// 远端目录：对 WebDAV 是相对根地址的路径（如 "/动画/汪汪队"），
	// 对网盘是目录 ID。空串表示根目录。
	RemoteDirID   string `gorm:"size:500" json:"remoteDirId"`
	RemoteDirName string `gorm:"size:300" json:"remoteDirName"` // 仅用于展示

	// 目标系统目录
	CategoryID uint64 `gorm:"index;not null" json:"categoryId"`

	// Recursive 是否递归子目录。默认开 —— 用户选的多半是
	// "汪汪队" 这种外层目录，具体剧集在子目录里。
	Recursive bool `gorm:"default:true" json:"recursive"`
	Enabled   bool `gorm:"default:true" json:"enabled"`

	// 同步结果快照。
	//
	// 保留它是因为"同步"是个可能耗时几分钟的异步感操作，
	// 用户点完按钮就切走了。有快照才能在列表里回看
	// "上次同步新增了几个、跳过几个、有没有报错"。
	LastSyncAt      *time.Time `json:"lastSyncAt"`
	LastSyncCreated int        `json:"lastSyncCreated"`
	LastSyncSkipped int        `json:"lastSyncSkipped"`
	LastSyncError   string     `gorm:"size:500" json:"lastSyncError"`

	Account  *DiskAccount `gorm:"foreignKey:AccountID" json:"account,omitempty"`
	Category *Category    `gorm:"foreignKey:CategoryID" json:"category,omitempty"`

	// ResourceCount 这条映射**当前**导入了多少条资源（不含已软删除的）。
	//
	// 只读、不进库（gorm:"-"）：它是列表接口现算出来的，存下来只会给自己
	// 制造不一致 —— 资源可以被单条移除、可以被批量删、还能被映射重建时
	// 改变归属，任何一处漏更新，列表上的数字就开始骗人。
	//
	// 存在的意义是让「资源映射」页左树在**展开之前**就能标出
	// "这条映射底下有多少个资源"。没有它，用户只能一个个点开看，
	// 而"这条映射到底倒进来东西没有"恰恰是他最想确认的事。
	ResourceCount int64 `gorm:"-" json:"resourceCount"`
}

func (ResourceMapping) TableName() string { return "resource_mappings" }
