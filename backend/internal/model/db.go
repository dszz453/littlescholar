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

	// 全文检索基础设施（仅 PostgreSQL）。
	//
	// 失败只警告不返回错误：索引纯粹是性能优化，没有它「找一找」的结果
	// 依然正确，只是 `ILIKE '%kw%'` 会退化成全表扫。为了一个可选优化
	// 让整个服务起不来，代价不对等。
	if driver == "postgres" {
		if err := ensureSearchIndexes(db); err != nil {
			log.Printf("[db] 警告: 全文检索索引创建失败（搜索仍可用，但会变慢）: %v", err)
		}
	}
	return db, nil
}

// ensureSearchIndexes 为「找一找」准备 pg_trgm 全文检索索引。
//
// 为什么是 pg_trgm 而不是 tsvector：
//   - tsvector 依赖分词，而中文没有内置分词器（zhparser / pg_jieba 都要
//     自己编译 PostgreSQL 镜像），成本高、收益不确定。
//   - pg_trgm 用三元组做相似匹配，`ILIKE '%关键词%'` 能直接命中 GIN 索引，
//     中英文都有效，且**不需要改表结构**。
//
// 注意这是原生 DDL —— GORM 的 struct tag 表达不了 `USING gin (...)` 这种
// 表达式索引，只能手工执行。sqlite（单测环境）没有 pg_trgm，故只对
// postgres 调用。
func ensureSearchIndexes(db *gorm.DB) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE INDEX IF NOT EXISTS idx_resources_title_trgm ON resources USING gin (title gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS idx_resources_desc_trgm ON resources USING gin (description gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS idx_categories_name_trgm ON categories USING gin (name gin_trgm_ops)`,
		`CREATE INDEX IF NOT EXISTS idx_categories_desc_trgm ON categories USING gin (description gin_trgm_ops)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	log.Printf("[db] 全文检索索引已就绪（pg_trgm + GIN）")
	return nil
}

// AllModels 所有需要迁移的模型
func AllModels() []interface{} {
	return []interface{}{
		&User{},
		&UserCategoryGrant{},
		&Category{},
		&Resource{},
		&ResourceMapping{},
		&DiskAccount{},
		&QRLoginSession{},
		&TransferTask{},
		&AliveCheckTask{},
		&TranscodeTask{},
		&PlayRecord{},
		&StudyRecord{},
		&StudyPlan{},
		&StudyCheckIn{},
		&StudyAward{},
		&KnowledgeDigest{},
		&ResourceSubtitle{},
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
		"pansou_url":            "",
		"pancheck_url":          "",
		"enable_transcode":      "true",
		"transcode_concurrency": "2",
		"download_accelerate":   "true",
		"download_concurrency":  "16",
		// 外部加速代理（对应参考包里的 aliproxy）：留空则不启用。
		// 支持 http://host:port?url={url} 模板写法，也支持直接作为前缀拼接。
		"accel_proxy": "",
		// 分块并发的连接数（自研「非会员加速」）。
		// 4 是提速与账号风控之间的折中：参考包里明确提醒，并发开太大
		// 会被网盘限速甚至封号。配 0 或 1 即关闭、退回单流，上限 32。
		"multi_conn":        "4",
		"play_mode_default": "proxy",
		"site_name":         "小书童学习系统",
		// 小书童的知识沉淀：每天把目录读成人话。
		// daily_limit 是每轮最多重算多少个范围（= 多少次 AI 调用），
		// 用来兜住额度；内容没变化的目录会自动跳过，不占额度。
		"digest_enabled":     "true",
		"digest_daily_limit": "40",
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
