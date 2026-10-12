package model

import (
	"fmt"
	"log"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB 初始化数据库连接并执行迁移
func InitDB(driver, dsn string, debug bool) (*gorm.DB, error) {
	logLevel := logger.Warn
	if debug {
		logLevel = logger.Info
	}
	cfg := &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
		NowFunc: func() time.Time {
			return time.Now().Local()
		},
	}

	var db *gorm.DB
	var err error
	switch driver {
	case "sqlite":
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
	case "postgres":
		db, err = gorm.Open(postgres.Open(dsn), cfg)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("数据库连接失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	return db, nil
}

// AllModels 所有需要迁移的模型
func AllModels() []interface{} {
	return []interface{}{
		&User{},
		&UserCategoryGrant{},
		&Category{},
		&Resource{},
		&DiskAccount{},
		&QRLoginSession{},
		&TransferTask{},
		&AliveCheckTask{},
		&TranscodeTask{},
		&PlayRecord{},
		&StudyRecord{},
		&AIConfig{},
		&AIReport{},
		&AIChatSession{},
		&AIChatMessage{},
		&SystemConfig{},
	}
}

// AutoMigrate 自动迁移
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(AllModels()...)
}

// SeedDefaultData 初始化默认数据
func SeedDefaultData(db *gorm.DB, adminUser, adminPassHash string) error {
	// 1. 默认管理员
	var count int64
	db.Model(&User{}).Where("role = ?", RoleAdmin).Count(&count)
	if count == 0 {
		admin := &User{
			Username:     adminUser,
			PasswordHash: adminPassHash,
			Nickname:     "管理员",
			Role:         RoleAdmin,
			Status:       UserStatusEnabled,
		}
		if err := db.Create(admin).Error; err != nil {
			return err
		}
		log.Printf("[db] 已创建默认管理员: %s", adminUser)
	}

	// 2. 默认系统配置
	defaults := map[string]string{
		"pansou_url":             "",
		"pancheck_url":           "",
		"enable_transcode":       "true",
		"transcode_concurrency":  "2",
		"download_accelerate":    "true",
		"download_concurrency":   "16",
		"play_mode_default":      "proxy",
		"site_name":              "小书童学习系统",
	}
	for k, v := range defaults {
		var existed int64
		db.Model(&SystemConfig{}).Where("key = ?", k).Count(&existed)
		if existed == 0 {
			db.Create(&SystemConfig{Key: k, Value: v, Group: "general"})
		}
	}

	// 3. 默认目录示例（仅当没有任何目录时创建）
	var catCount int64
	db.Model(&Category{}).Count(&catCount)
	if catCount == 0 {
		if err := seedCategories(db); err != nil {
			return err
		}
		log.Printf("[db] 已创建默认目录结构")
	}
	return nil
}

// seedCategories 创建示例目录树
func seedCategories(db *gorm.DB) error {
	type seed struct {
		name     string
		icon     string
		color    string
		children []seed
	}
	tree := []seed{
		{
			name: "乐学英语", icon: "english", color: "#4A90D9",
			children: []seed{
				{name: "新概念英语", icon: "book", color: "#5B8FF9"},
				{name: "剑桥少儿英语", icon: "star", color: "#61DDAA"},
				{name: "英语儿歌", icon: "music", color: "#F6BD16"},
			},
		},
		{
			name: "国学经典", icon: "chinese", color: "#D9554A",
			children: []seed{
				{name: "三字经", icon: "book", color: "#F08BB4"},
				{name: "唐诗三百首", icon: "book", color: "#9270CA"},
				{name: "论语", icon: "book", color: "#6DC8EC"},
			},
		},
		{
			name: "开心故事", icon: "story", color: "#F6BD16",
			children: []seed{
				{name: "睡前故事", icon: "moon", color: "#7262FD"},
				{name: "成语故事", icon: "book", color: "#FF9D4D"},
			},
		},
		{
			name: "数学思维", icon: "math", color: "#5AD8A6",
			children: []seed{
				{name: "趣味数学", icon: "calculator", color: "#269A99"},
				{name: "奥数入门", icon: "calculator", color: "#5B8FF9"},
			},
		},
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for i, root := range tree {
			rootCat := &Category{
				Name: root.name, Icon: root.icon, Color: root.color,
				SortOrder: i, Level: 1,
			}
			if err := tx.Create(rootCat).Error; err != nil {
				return err
			}
			rootCat.Path = BuildPath("", rootCat.ID)
			tx.Model(rootCat).Update("path", rootCat.Path)

			for j, child := range root.children {
				childCat := &Category{
					ParentID:  &rootCat.ID,
					Name:      child.name,
					Icon:      child.icon,
					Color:     child.color,
					SortOrder: j,
					Level:     2,
				}
				if err := tx.Create(childCat).Error; err != nil {
					return err
				}
				childCat.Path = BuildPath(rootCat.Path, childCat.ID)
				tx.Model(childCat).Update("path", childCat.Path)
			}
		}
		return nil
	})
}
