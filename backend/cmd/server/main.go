package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/cache"
	"github.com/littlescholar/core/internal/config"
	"github.com/littlescholar/core/internal/handler"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/router"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/transcode"
	"github.com/littlescholar/core/internal/util"
	"github.com/littlescholar/core/internal/version"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	// 网盘适配器：通过 init() 自注册到 adapter.Registry
	_ "github.com/littlescholar/core/internal/adapter/aliyun"
	_ "github.com/littlescholar/core/internal/adapter/baidu"
	_ "github.com/littlescholar/core/internal/adapter/quark"
	_ "github.com/littlescholar/core/internal/adapter/tianyi"
	_ "github.com/littlescholar/core/internal/adapter/uc"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径")
	flag.Parse()

	startedAt := time.Now()

	// 1. 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 2. 初始化日志
	logger, err := initLogger(cfg.Log.Level)
	if err != nil {
		log.Fatalf("初始化日志失败: %v", err)
	}
	defer logger.Sync()

	logger.Info("小书童学习系统启动中...",
		// 版本进日志：容器日志是服务器上唯一"随手就能看"的东西，
		// 有了这一行，`docker logs ls-core | head -1` 就能回答"这台跑的是哪一版"。
		zap.String("version", version.Full()),
		zap.String("mode", cfg.Server.Mode),
		zap.String("db", cfg.Database.Driver),
	)

	// 3. 初始化数据库
	db, err := model.InitDB(cfg.Database.Driver, cfg.Database.DSN(), cfg.Server.Mode == "debug")
	if err != nil {
		logger.Fatal("数据库初始化失败", zap.Error(err))
	}
	logger.Info("数据库连接成功，迁移完成")

	// 3.1 确保存储目录存在
	//
	// 这一步不能省：router 里的 r.Static("/media", ...) 与 r.Static("/transcode", ...)
	// 在目录不存在时会让 Gin 在启动阶段直接 panic。
	// 容器首次启动时挂载的卷是空的，所以必须由程序自己创建。
	for _, dir := range []string{cfg.Storage.MediaDir, cfg.Storage.TranscodeDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			logger.Fatal("创建存储目录失败", zap.String("dir", dir), zap.Error(err))
		}
	}
	logger.Info("存储目录就绪",
		zap.String("media", cfg.Storage.MediaDir),
		zap.String("transcode", cfg.Storage.TranscodeDir),
	)

	// 网盘适配器自检
	{
		names := make([]string, 0, len(adapter.List()))
		for _, a := range adapter.List() {
			names = append(names, fmt.Sprintf("%s(%s)", a.Type(), a.Name()))
		}
		logger.Info("网盘适配器已注册",
			zap.Int("count", len(names)),
			zap.Strings("adapters", names),
		)
	}

	// 4. 初始化工具
	jwtManager := util.NewJWTManager(
		cfg.JWT.Secret, cfg.JWT.Issuer,
		cfg.JWT.AccessExpireHrs, cfg.JWT.RefreshExpireHrs,
	)
	encryptor, err := util.NewEncryptor(cfg.Security.CookieEncryptKey)
	if err != nil {
		logger.Fatal("初始化加密器失败", zap.Error(err))
	}

	// 5. 种子数据
	adminUser := envOr("ADMIN_USERNAME", "admin")
	adminPass := envOr("ADMIN_PASSWORD", "admin123")
	hash, err := util.HashPassword(adminPass)
	if err != nil {
		logger.Fatal("生成管理员密码失败", zap.Error(err))
	}
	if err := model.SeedDefaultData(db, adminUser, hash); err != nil {
		logger.Error("初始化默认数据失败", zap.Error(err))
	}

	// 6. 初始化缓存（Redis 为可选加速层，连不上会自动降级）
	rdb := cache.New(cfg.Redis)
	if rdb.Enabled() {
		logger.Info("Redis 缓存已启用", zap.String("addr", cfg.Redis.Addr()))
	} else if cfg.Redis.Enabled {
		logger.Warn("Redis 配置为启用但连接失败，已降级为无缓存模式",
			zap.String("addr", cfg.Redis.Addr()))
	} else {
		logger.Info("Redis 缓存未启用（配置 redis.enabled=false）")
	}
	defer rdb.Close()

	// 7. 初始化服务
	permService := service.NewPermissionService(db).WithCache(rdb)

	// 7.0 外部服务地址：数据库配置优先于配置文件
	//
	// 每个 key 用独立的结构体变量查，不复用同一个 sc。
	// 复用会让上一次查询的值残留下来，一旦某次查询出错但 err 判断写漏，
	// 就会静默使用错值——这种 bug 极难排查。
	sysConf := func(key, fallback string) string {
		var row model.SystemConfig
		if err := db.Where("key = ?", key).First(&row).Error; err == nil && row.Value != "" {
			return row.Value
		}
		return fallback
	}

	pansouURL := sysConf("pansou_url", cfg.External.PansouURL)
	pansouToken := sysConf("pansou_token", cfg.External.PansouToken)
	pansouClient := service.NewPansouClient(pansouURL, pansouToken)
	logger.Info("PanSou 客户端已初始化", zap.String("url", pansouURL))

	// PanCheck 作为存活检测的降级引擎。未配置也不影响启动——
	// 巡检服务会在只有单一引擎时自动退化为只用 PanSou。
	pancheckURL := sysConf("pancheck_url", cfg.External.PancheckURL)
	pancheckPass := sysConf("pancheck_password", cfg.External.PancheckPass)
	pancheckClient := service.NewPancheckClient(pancheckURL, pancheckPass)
	logger.Info("PanCheck 客户端已初始化", zap.String("url", pancheckURL))

	// 7. 启动任务工作池
	pool := task.NewWorkerPool(3, 500)
	pool.Start()
	defer pool.Stop()

	// 7.1 初始化转码引擎
	//
	// 注意：转码不复用上面的 WorkerPool。该池的 runJob 硬编码 10 分钟超时，
	// 而转码常常跑半小时以上，复用会导致任务被中途掐断。
	// 转码有独立的并发闸门（sem），由 Engine 自己管理。
	var transcodeSvc *service.TranscodeService
	if cfg.Transcode.Enabled {
		tcEngine := transcode.New(cfg.Transcode.FFmpegPath, cfg.Transcode.Concurrency)
		if tcEngine.Available() {
			verCtx, verCancel := context.WithTimeout(context.Background(), 10*time.Second)
			ver, verErr := tcEngine.Version(verCtx)
			verCancel()
			if verErr != nil {
				logger.Warn("ffmpeg 版本探测失败", zap.Error(verErr))
			}
			logger.Info("转码引擎已就绪",
				zap.String("ffmpeg", ver),
				zap.Int("concurrency", tcEngine.Concurrency()),
			)
		} else {
			logger.Warn("转码已配置启用，但未找到 ffmpeg，转码功能将不可用",
				zap.String("ffmpeg_path", cfg.Transcode.FFmpegPath))
		}

		transcodeSvc = service.NewTranscodeService(service.TranscodeDeps{
			DB:           db,
			Engine:       tcEngine,
			Acc:          task.NewAccelerator(16),
			Encryptor:    encryptor,
			Logger:       logger,
			OutputDir:    cfg.Storage.TranscodeDir,
			CacheMaxGB:   cfg.Transcode.CacheMaxGB,
			CacheTTLDays: cfg.Transcode.CacheTTLDays,
		})

		// 上个进程如果被强杀（容器重启 / OOM），正在跑的转码会在
		// transcode_tasks 里留下一条永远不动的 pending/running 记录，
		// 让对应资源此后一直显示"转码中"。趁着本进程刚起来、引擎还是空的，
		// 把它清干净（详见 RecoverStaleTasks 的注释）。
		transcodeSvc.RecoverStaleTasks()

		// 产物缓存上限 / TTL 的启动期自检
		if cfg.Transcode.CacheMaxGB <= 0 && cfg.Transcode.CacheTTLDays <= 0 {
			logger.Warn("转码产物缓存未设上限（cache_max_gb 与 cache_ttl_days 均为 0），" +
				"长期运行可能写满磁盘，建议至少配置其中一项")
		} else {
			logger.Info("转码产物缓存策略",
				zap.Int("maxGB", cfg.Transcode.CacheMaxGB),
				zap.Int("ttlDays", cfg.Transcode.CacheTTLDays),
			)
		}
	} else {
		logger.Info("转码功能未启用（配置 transcode.enabled=false）")
	}

	// 7.2 转码产物定时清理
	//
	// 6 小时一次：清理是低频维护动作，太频繁只是白白遍历目录。
	// 首次执行延迟 10 分钟，避开启动时的初始化高峰。
	// stopGC 挂在 main 的 defer 上，随第 10 步的优雅退出而结束。
	if transcodeSvc != nil && transcodeSvc.Enabled() {
		stopGC := service.StartGCJanitor(transcodeSvc, logger, 6*time.Hour, 10*time.Minute)
		defer stopGC()
	}

	// 7.3 资源存活巡检
	//
	// 巡检的意义：分享链接失效是常态，而失效的资源在孩子端表现为
	// "点开播不了"。与其等用户来报，不如后台先扫出来。
	aliveSvc := service.NewResourceAliveService(service.AliveDeps{
		DB:            db,
		Pansou:        pansouClient,
		Pancheck:      pancheckClient,
		Logger:        logger,
		BatchSize:     cfg.Alive.BatchSize,
		Interval:      time.Duration(cfg.Alive.IntervalSeconds) * time.Second,
		DeadThreshold: cfg.Alive.DeadThreshold,
		StaleHours:    cfg.Alive.StaleHours,
	})

	if aliveSvc.Enabled() {
		logger.Info("资源巡检服务已就绪",
			zap.Int("batchSize", cfg.Alive.BatchSize),
			zap.Int("deadThreshold", cfg.Alive.DeadThreshold),
			zap.Int("staleHours", cfg.Alive.StaleHours),
		)
		// 每天一次，扫「超过 staleHours 未检查」的资源。
		// 首次延迟 30 分钟：巡检会向外发大量请求，避开启动期的初始化和转码首启。
		if cfg.Alive.Enabled {
			stopAlive := service.StartAliveScheduler(aliveSvc, logger, 24*time.Hour, 30*time.Minute)
			defer stopAlive()
		} else {
			logger.Info("资源巡检定时任务未启用（配置 alive.enabled=false），仍可手动触发")
		}
	} else {
		logger.Warn("资源巡检不可用：未配置任何检测引擎")
	}

	// 8. 装配路由
	//
	// AI / Plan 两个 handler 先在这里建好再交给路由：PlanHandler 的
	// 周奖状定时任务要在下面挂到优雅退出链上，main 必须持有同一实例。
	aiHandler := handler.NewAIHandler(db, encryptor).WithPerm(permService)
	planHandler := handler.NewPlanHandler(db, permService, aiHandler)
	digestHandler := handler.NewDigestHandler(db, permService, aiHandler).WithLogger(logger)
	organizeHandler := handler.NewOrganizeHandler(db, permService, aiHandler).WithLogger(logger).WithEncryptor(encryptor)
	if n, parseErr := strconv.Atoi(sysConf("digest_daily_limit", "")); parseErr == nil && n > 0 {
		digestHandler.DailyLimit = n
	}

	rdeps := &router.Deps{
		DB:        db,
		Cfg:       cfg,
		Logger:    logger,
		JWT:       jwtManager,
		Encryptor: encryptor,
		Perm:      permService,
		Pansou:    pansouClient,
		Pancheck:  pancheckClient,
		Alive:     aliveSvc,
		Pool:      pool,
		Transcode: transcodeSvc,
		StartedAt: startedAt,
		AI:        aiHandler,
		Plan:      planHandler,
		Digest:    digestHandler,
		Organize:  organizeHandler,
	}
	tengine := router.Setup(rdeps)
	engine := tengine
	// 字幕后台采集：从"资源所在目录"里找同名字幕，分不开时让 AI 选，
	// 命中就存进 resource_subtitles（永久）。启动后延迟 3 分钟先跑第一轮，
	// 之后每 6 小时一轮（见 SubtitleCollector.Start）。
	if rdeps.SubtitleCollector != nil {
		rdeps.SubtitleCollector.Start(6 * time.Hour)
	}

	// 8.0.1 AI 批量写字幕：每分钟看一眼计划时段（见 AISubtitleService.Tick）。
	// 不用固定周期：时段/开关改了要立刻生效，重启服务才能生效的配置
	// 会让人以为"没做"。
	if rdeps.AISubtitle != nil {
		rdeps.AISubtitle.Start()
	}

	// 8.1 每周 AI 奖状
	//
	// 每天扫一次：打卡在任意时刻发生，每天检查能保证"本周达标"最迟第二天
	// 就有奖状。首次延迟 5 分钟，避开启动高峰（此刻还在跑首次转码自检等）。
	// 生成是幂等的（同一用户同一周只发一张），重复跑不会重复颁奖。
	stopAward := planHandler.StartAwardScheduler(logger, 24*time.Hour, 5*time.Minute)
	defer stopAward()

	// 8.2 小书童的知识沉淀
	//
	// 每天把「目录里都有什么」读成人话。首次延迟 8 分钟，排在奖状之后 ——
	// 两件事都要调 AI，错开可以避免启动瞬间并发打满额度。
	// 内容指纹没变就跳过，所以稳定运行后每天实际只重算变化了的那几个目录。
	if sysConf("digest_enabled", "true") == "true" {
		stopDigest := digestHandler.StartDigestScheduler(logger, 24*time.Hour, 8*time.Minute)
		defer stopDigest()
	} else {
		logger.Info("知识沉淀定时任务未启用（system_configs.digest_enabled=false），仍可在后台手动刷新")
	}

	// 8.3 自动共享补齐
	//
	// 存在的理由：PRO 的转存链路要求资源上必须有 shareUrl，缺了整个播放
	// 就停在"缺少分享链接"；而资源是**持续新增**的（每次同步、每次新挂
	// 映射都会带来一批没分享的）。指望家长记得回后台点按钮不现实，
	// 结果就是孩子时不时遇到"这集播不了"，而原因在后台。
	//
	// 这里**再建一个** ShareHandler 实例是安全的：它只持有 db 与 encryptor，
	// 没有任何自有状态，与 router.Setup 内部那个功能完全等价。
	// （那个实例没导出，为了拿它去改 Deps 反而会把装配关系搞复杂。）
	//
	// 节奏：每 6 小时补一次，首轮延迟 15 分钟 —— 避开启动期（此刻还在跑
	// 首次转码自检、巡检等），也不至于让别人安装完等半天才看到分享。
	shareAutoHandler := handler.NewShareHandler(db, encryptor)
	if sysConf("share_auto_enabled", "true") == "true" {
		stopShareAuto := service.StartShareAutoScheduler(
			func(ctx context.Context) (service.ShareAutoStat, error) {
				r, err := shareAutoHandler.Auto(ctx)
				return service.ShareAutoStat{
					Total:   r.Total,
					Created: r.Created,
					Skipped: r.Skipped,
					Failed:  r.Failed,
				}, err
			},
			logger, 6*time.Hour, 15*time.Minute)
		defer stopShareAuto()
		logger.Info("自动共享任务已就绪（每 6 小时补齐一次）",
			zap.String("config", "system_configs.share_auto_enabled"))
	} else {
		logger.Info("自动共享未启用（system_configs.share_auto_enabled=false），仍可在后台手动触发")
	}

	// 9. 启动 HTTP 服务
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      engine,
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 0, // 播放流式响应不设超时
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		logger.Info("HTTP 服务已启动", zap.String("addr", addr))
		printBanner(addr, adminUser, adminPass)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("HTTP 服务启动失败", zap.Error(err))
		}
	}()

	// 10. 优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("正在关闭服务...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("服务关闭异常", zap.Error(err))
	}
	logger.Info("服务已退出")
}

func initLogger(level string) (*zap.Logger, error) {
	var cfg zap.Config
	if level == "debug" {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}
	cfg.Encoding = "console"
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func printBanner(addr, user, pass string) {
	// 版本号**刻意放在方框外面**。
	//
	// 那个框的宽度是手工数好的（中文/全角占两列，ASCII 占一列），
	// 而 Version 会随版本升级变长（1.9.0 → 1.10.0 多一列）。一旦放回框里，
	// 每升一次版本右边框就歪一列，而 %-Ns 补不回来 —— 它按 rune 数补，
	// 中文一律当 1 列。放外面则永远整齐。
	fmt.Println()
	fmt.Printf("  小书童 · 孩子学习系统 %s\n", version.Full())
	fmt.Println("  ╔══════════════════════════════════════════╗")
	fmt.Printf("  ║  服务地址: http://localhost%-13s ║\n", addr)
	fmt.Printf("  ║  后台账号: %-30s ║\n", user)
	fmt.Printf("  ║  后台密码: %-30s ║\n", pass)
	fmt.Println("  ╚══════════════════════════════════════════╝")
	fmt.Println()
}
