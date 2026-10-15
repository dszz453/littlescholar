package router

import (
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/config"
	"github.com/littlescholar/core/internal/handler"
	"github.com/littlescholar/core/internal/middleware"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Deps 路由依赖
type Deps struct {
	DB        *gorm.DB
	Cfg       *config.Config
	Logger    *zap.Logger
	JWT       *util.JWTManager
	Encryptor *util.Encryptor
	Perm      *service.PermissionService
	Pansou    *service.PansouClient
	Pancheck  *service.PancheckClient
	Alive     *service.ResourceAliveService
	Pool      *task.WorkerPool
	Transcode *service.TranscodeService
	StartedAt time.Time

	// AI / Plan 由 main 预先构造后传入。
	//
	// 为什么不在这里 new：周奖状的定时生成是在 main 里挂到优雅退出链上的
	// （`defer stopAward()`），main 必须拿得到同一个 PlanHandler 实例。
	// 留空时下面会兜底自建，保证单独调用 Setup 的场景（测试）仍可用。
	AI   *handler.AIHandler
	Plan *handler.PlanHandler
	// Digest 同上：每日知识沉淀的定时任务也挂在 main 的优雅退出链上。
	Digest *handler.DigestHandler
	// Organize AI 整理助手（纯请求-响应，没有后台任务，由 router 自行构造即可）
	Organize *handler.OrganizeHandler
}

// Setup 装配路由
func Setup(deps *Deps) *gin.Engine {
	gin.SetMode(ginMode(deps.Cfg.Server.Mode))
	r := gin.New()

	r.Use(middleware.Recovery(deps.Logger))
	r.Use(middleware.AccessLog(deps.Logger))
	r.Use(cors.New(cors.Config{
		AllowAllOrigins:  true,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept"},
		ExposeHeaders:    []string{"Content-Length", "Content-Range", "Accept-Ranges"},
		AllowCredentials: false,
		MaxAge:           12 * time.Hour,
	}))

	// 初始化各 handler
	authH := handler.NewAuthHandler(deps.DB, deps.JWT)
	categoryH := handler.NewCategoryHandler(deps.DB, deps.Perm)
	resourceH := handler.NewResourceHandler(deps.DB, deps.Perm, deps.Pansou, deps.Pancheck, deps.Encryptor)
	userH := handler.NewUserHandler(deps.DB, deps.Perm)
	diskH := handler.NewDiskHandler(deps.DB, deps.Encryptor)
	transferH := handler.NewTransferHandler(deps.DB, deps.Encryptor, deps.Pool)
	aiH := deps.AI
	if aiH == nil {
		aiH = handler.NewAIHandler(deps.DB, deps.Encryptor).WithPerm(deps.Perm)
	}
	planH := deps.Plan
	if planH == nil {
		planH = handler.NewPlanHandler(deps.DB, deps.Perm, aiH)
	}
	digestH := deps.Digest
	if digestH == nil {
		digestH = handler.NewDigestHandler(deps.DB, deps.Perm, aiH)
	}
	organizeH := deps.Organize
	if organizeH == nil {
		organizeH = handler.NewOrganizeHandler(deps.DB, deps.Perm, aiH).WithEncryptor(deps.Encryptor)
	}
	studyH := handler.NewStudyHandler(deps.DB, aiH)
	playH := handler.NewPlayHandler(deps.DB, deps.Perm, deps.Encryptor, deps.Transcode, deps.Cfg.JWT.Secret)
	streamH := handler.NewStreamHandler(deps.DB, deps.Encryptor, deps.Cfg.JWT.Secret)
	systemH := handler.NewSystemHandler(deps.DB, deps.Cfg, deps.Pansou, deps.Pancheck, deps.Pool, deps.StartedAt)
	transcodeH := handler.NewTranscodeHandler(deps.DB, deps.Transcode)
	aliveH := handler.NewAliveHandler(deps.DB, deps.Alive)
	mappingH := handler.NewMappingHandler(deps.DB, deps.Encryptor)

	api := r.Group("/api")

	// 健康检查
	api.GET("/health", func(c *gin.Context) {
		util.OK(c, gin.H{"status": "ok", "time": time.Now()})
	})

	// ============ 认证（公开）============
	api.POST("/auth/refresh", authH.Refresh)
	api.POST("/admin/auth/login", authH.Login(model.RoleAdmin))
	api.POST("/app/auth/login", authH.Login(""))

	// ============ 媒体流代理（公开，靠票据鉴权）============
	//
	// 这个地址会被塞进 <video src="...">，浏览器**无法**为它附加
	// Authorization 头，所以不能挂在 AuthMiddleware 下面 ——
	// 否则 WebDAV / 需鉴权头的存储源必然 401。
	// 鉴权改由 handler.SignStreamTicket 签发的短期票据承担：
	// 票据绑定"资源 ID + 过期时间"，由 JWT secret 做 HMAC，
	// 泄露也只能播那一个资源。
	api.GET("/stream/:id", streamH.Serve)

	// 静态资源（媒体文件）
	r.Static("/media", deps.Cfg.Storage.MediaDir)
	r.Static("/transcode", deps.Cfg.Storage.TranscodeDir)

	// ============ 后台管理（需管理员）============
	admin := api.Group("/admin")
	admin.Use(middleware.AuthMiddleware(deps.JWT))
	admin.Use(middleware.RequireAdmin())
	{
		admin.GET("/auth/profile", authH.Profile)
		admin.POST("/auth/logout", authH.Logout)

		// 目录管理
		admin.GET("/categories/icons", categoryH.Icons)
		admin.GET("/categories/tree", categoryH.Tree)
		admin.POST("/categories", categoryH.Create)
		admin.PUT("/categories/:id", categoryH.Update)
		admin.PUT("/categories/:id/move", categoryH.Move)
		admin.DELETE("/categories/:id", categoryH.Delete)

		// 资源管理
		admin.GET("/resources", resourceH.List)
		admin.GET("/resources/:id", resourceH.Detail)
		admin.POST("/resources", resourceH.Create)
		admin.PUT("/resources/:id", resourceH.Update)
		admin.POST("/resources/import", resourceH.Import)
		// 从存储源（WebDAV 等）导入已有文件为资源 —— 与上面的
		// 分享链接导入不同，这条不需要转存，直接登记账号+文件路径。
		admin.POST("/resources/import-disk", resourceH.ImportFromDisk)
		admin.POST("/resources/check-alive", resourceH.CheckAlive)
		admin.PUT("/resources/move", resourceH.Move)
		admin.DELETE("/resources/batch", resourceH.BatchDelete)

		// 搜索
		admin.POST("/search", resourceH.Search)
		admin.GET("/search/health", resourceH.SearchHealth)

		// 资源接入（网盘 / WebDAV / 本地硬盘统一入口）
		admin.GET("/disk/types", diskH.Types)
		admin.GET("/disk/accounts", diskH.List)
		admin.POST("/disk/accounts/qr", diskH.GetQRCode)
		admin.GET("/disk/accounts/qr/:sessionId", diskH.PollQRCode)
		admin.POST("/disk/accounts/manual", diskH.AddManual)
		admin.PUT("/disk/accounts/:id", diskH.UpdateAccount)
		admin.POST("/disk/accounts/:id/validate", diskH.Validate)
		admin.PUT("/disk/accounts/:id/cookie", diskH.UpdateCookie)
		admin.POST("/disk/accounts/:id/default", diskH.SetDefault)
		admin.DELETE("/disk/accounts/:id", diskH.Delete)
		admin.GET("/disk/accounts/:id/browse", diskH.Browse)
		admin.POST("/disk/accounts/:id/mkdir", diskH.MakeDir)
		admin.PUT("/disk/accounts/:id/rename", diskH.RenameRemote)
		admin.POST("/disk/accounts/:id/delete", diskH.DeleteRemote)

		// 转存
		admin.POST("/transfer", transferH.Create)
		admin.POST("/transfer/batch", transferH.BatchCreate)
		admin.GET("/transfer/tasks", transferH.List)
		admin.POST("/transfer/tasks/:id/retry", transferH.Retry)
		admin.POST("/transfer/tasks/:id/cancel", transferH.Cancel)

		// 用户管理
		admin.GET("/users", userH.List)
		admin.POST("/users", userH.Create)
		admin.PUT("/users/:id", userH.Update)
		admin.PUT("/users/:id/password", userH.ResetPassword)
		admin.GET("/users/:id/grants", userH.GetGrants)
		admin.PUT("/users/:id/grants", userH.SetGrants)
		admin.DELETE("/users/:id", userH.Delete)

		// AI 配置
		admin.GET("/ai/configs", aiH.ListConfigs)
		admin.POST("/ai/configs", aiH.CreateConfig)
		admin.PUT("/ai/configs/:id", aiH.UpdateConfig)
		admin.DELETE("/ai/configs/:id", aiH.DeleteConfig)
		admin.POST("/ai/configs/:id/test", aiH.TestConfig)

		// 知识沉淀（小书童每天把目录读成人话）
		admin.GET("/digests", digestH.List)
		admin.POST("/digests/refresh", digestH.Refresh)

		// AI 整理助手：先出方案（只读），人工确认后再执行
		admin.POST("/organize/plan", organizeH.Plan)
		admin.POST("/organize/apply", organizeH.Apply)

		// 学情分析
		admin.GET("/study/overview", studyH.Overview)
		admin.GET("/study/records", studyH.Records)
		admin.POST("/study/analyze", studyH.Analyze)
		admin.GET("/study/reports", studyH.Reports)
		admin.GET("/study/reports/:id", studyH.ReportDetail)
		admin.DELETE("/study/reports/:id", studyH.DeleteReport)

		// 系统
		admin.GET("/system/config", systemH.GetConfig)
		admin.PUT("/system/config", systemH.UpdateConfig)
		admin.GET("/system/status", systemH.Status)

		// 转码
		admin.GET("/transcode/status", transcodeH.Status)
		admin.POST("/transcode", transcodeH.Create)
		admin.GET("/transcode/tasks", transcodeH.List)
		admin.GET("/transcode/tasks/:id", transcodeH.Detail)
		admin.POST("/transcode/tasks/:id/cancel", transcodeH.Cancel)
		admin.DELETE("/transcode/tasks/:id", transcodeH.Delete)
		admin.POST("/transcode/gc", transcodeH.GC)

		// 资源存活巡检
		admin.GET("/alive/status", aliveH.Status)
		admin.POST("/alive/check", aliveH.Start)
		admin.GET("/alive/tasks", aliveH.Tasks)
		admin.GET("/alive/tasks/:id", aliveH.Detail)
		admin.GET("/alive/dead", aliveH.DeadList)

		// 资源映射：远端存储目录 → 系统目录，同步即批量登记资源
		admin.GET("/mappings", mappingH.List)
		admin.POST("/mappings", mappingH.Create)
		admin.PUT("/mappings/:id", mappingH.Update)
		admin.DELETE("/mappings/:id", mappingH.Delete)
		admin.POST("/mappings/sync", mappingH.SyncAll) // 必须在 /:id 之前，否则会被当成 id
		admin.POST("/mappings/:id/sync", mappingH.Sync)
	}

	// ============ C 端（需登录 + 账号有效）============
	app := api.Group("/app")
	app.Use(middleware.AuthMiddleware(deps.JWT))
	app.Use(middleware.RequireActiveUser(deps.DB))
	{
		app.GET("/auth/profile", authH.Profile)
		app.POST("/auth/logout", authH.Logout)

		app.GET("/categories/tree", categoryH.AppTree)

		// 「找一找」：系统内全文检索，一次返回命中的目录 + 资源。
		// 与 admin 的 /search（PanSou 全网搜）不是一回事。
		app.GET("/search", resourceH.SearchAll)

		// 注意：下面三条**在 handler 内部**做权限收口
		// （scopedCategoryIDs → PermissionService.AccessibleCategoryIDs）。
		// 早先这里写着"权限在 AppTree 层面已过滤"，那只对目录树成立，
		// 资源接口本身可以被孩子用越权的 categoryId 直接读。
		app.GET("/resources", resourceH.List)
		// facets 必须注册在 /:id 之前，否则会被当成资源 ID
		app.GET("/resources/facets", resourceH.Facets)
		app.GET("/resources/:id", resourceH.Detail)

		app.GET("/play/:id", playH.Info)
		app.POST("/play/:id/progress", playH.ReportProgress)
		app.GET("/play/:id/playlist", playH.Playlist)
		app.GET("/play-history", playH.History)

		app.POST("/ai/chat", aiH.Chat)
		app.GET("/study/overview", studyH.Overview)
		// 学习记录明细。孩子端「我的 → 学习记录」用 groupBy=resource
		// 拿按资源聚合后的列表（否则 15 秒一条的心跳会把同一个资源刷屏）。
		app.GET("/study/records", studyH.Records)

		// 学习计划 / 每日打卡 / 每周 AI 奖状
		app.GET("/study/plans", planH.List)
		app.POST("/study/plans", planH.Create)
		app.PUT("/study/plans/:id", planH.Update)
		app.DELETE("/study/plans/:id", planH.Delete)
		// 今日总览：孩子一进计划页就据此决定提示「今天还没打卡」还是「已完成」。
		// 刻意不写成 /study/plans/today —— 那会和上面的 /study/plans/:id
		// 落在同一层，靠"静态段优先于通配符"的隐含规则才不出错，太脆。
		app.GET("/study/today", planH.Today)
		app.GET("/study/awards", planH.Awards)
		app.POST("/study/awards/refresh", planH.RefreshAward)

		// 小书童的知识沉淀：「这个目录里是什么、先学什么」
		app.GET("/digests", digestH.AppList)
		app.GET("/digests/:categoryId", digestH.Get)
	}

	// ============ 播放（后台 + C端通用，需登录）============
	playGroup := api.Group("")
	playGroup.Use(middleware.AuthMiddleware(deps.JWT))
	{
		playGroup.GET("/play/:id", playH.Info)
	}

	// 404
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, util.Response{
			Code: 404, Message: "接口不存在", Data: nil,
		})
	})

	return r
}

func ginMode(mode string) string {
	if mode == "debug" {
		return gin.DebugMode
	}
	return gin.ReleaseMode
}
