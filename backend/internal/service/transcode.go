package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/transcode"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 转码失败原因分类（写入 TranscodeTask.Message，前端可据此给出可操作提示）
const (
	transcodeMsgNoDirectLink = "无法获取直链，请检查网盘账号 Cookie 是否有效"
	transcodeMsgNoFFmpeg     = "服务器未安装 ffmpeg，转码功能不可用"
	transcodeMsgBusy         = "当前转码任务已满，已排队等待"
)

// TranscodeService 转码编排
//
// 职责边界：
//   - 决定「要不要转码」「转哪一档」（业务判断）
//   - 把「怎么转」交给 transcode.Engine（纯技术实现）
//   - 状态落库，让前端能轮询进度
//
// 与 Engine 的分工是刻意的：Engine 不含任何数据库依赖，
// 可以单独做单元测试；Service 不含 ffmpeg 参数细节。
type TranscodeService struct {
	db        *gorm.DB
	engine    *transcode.Engine
	acc       *task.Accelerator
	encryptor EncryptorLike
	logger    *zap.Logger

	// 下载源文件的临时目录
	tmpDir string
	// 转码产物根目录
	outDir string

	// 产物垃圾回收，与 service 同生命周期
	gc *TranscodeGC

	// 正在运行的任务，避免同一资源被重复提交
	inflight sync.Map // resourceID(uint64) → taskID(uint64)
}

// EncryptorLike 转码服务需要的解密能力
//
// 用接口而非直接依赖 util.Encryptor，是为了让 service 包不反向依赖 util 的具体实现，
// 也便于测试时注入假实现。
type EncryptorLike interface {
	Decrypt(ciphertext string) (string, error)
}

// TranscodeDeps 依赖
type TranscodeDeps struct {
	DB        *gorm.DB
	Engine    *transcode.Engine
	Acc       *task.Accelerator
	Encryptor EncryptorLike
	Logger    *zap.Logger
	TempDir   string
	OutputDir string

	// 产物缓存上限（GB）与闲置天数（天），透传给 GC。0 表示该项不限制。
	CacheMaxGB   int
	CacheTTLDays int
}

// NewTranscodeService 创建转码服务
func NewTranscodeService(d TranscodeDeps) *TranscodeService {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	tmp := d.TempDir
	if tmp == "" {
		tmp = filepath.Join(os.TempDir(), "littlescholar-transcode-src")
	}

	s := &TranscodeService{
		db:        d.DB,
		engine:    d.Engine,
		acc:       d.Acc,
		encryptor: d.Encryptor,
		logger:    logger,
		tmpDir:    tmp,
		outDir:    d.OutputDir,
	}

	// GC 始终构造：即使不设上限，也能清掉「产物已被手工删除」的僵尸记录
	s.gc = NewTranscodeGC(d.DB, logger, d.OutputDir, d.CacheMaxGB, d.CacheTTLDays)

	return s
}

// Enabled 转码是否可用（引擎存在且 ffmpeg 就位）
func (s *TranscodeService) Enabled() bool {
	return s != nil && s.engine != nil && s.engine.Available()
}

// Status 运行时状态，供系统状态接口展示
func (s *TranscodeService) Status() map[string]interface{} {
	if s == nil || s.engine == nil {
		return map[string]interface{}{"enabled": false}
	}
	out := map[string]interface{}{
		"enabled":     s.engine.Available(),
		"concurrency": s.engine.Concurrency(),
		"running":     s.engine.RunningCount(),
	}
	// 缓存占用与策略一并带出，后台页面无需再发一次请求
	if s.gc != nil {
		out["cacheMaxGb"] = s.gc.maxGB
		out["cacheTtlDays"] = s.gc.ttlDays
	}
	used := s.DiskUsage()
	out["diskUsedBytes"] = used
	out["diskUsedMb"] = used / (1024 * 1024)
	return out
}

// EnsureHLS 确保资源有可用的 HLS 产物
//
// 返回 (masterURL, ready, taskID, err)：
//   - 已有完成产物 → (url, true, 0, nil)
//   - 已在进行中   → ("", false, taskID, nil)
//   - 本次新建任务 → ("", false, taskID, nil)
func (s *TranscodeService) EnsureHLS(ctx context.Context, res *model.Resource) (string, bool, uint64, error) {
	if !s.Enabled() {
		return "", false, 0, transcode.ErrDisabled
	}
	if res == nil || res.ID == 0 {
		return "", false, 0, errors.New("资源无效")
	}

	// 1. 已有成功产物 → 直接返回
	var done model.TranscodeTask
	err := s.db.Where("resource_id = ? AND status = ?", res.ID, model.TaskStatusSuccess).
		Order("id desc").First(&done).Error
	if err == nil {
		// 产物可能被人手工清理了，落盘校验一次
		if _, statErr := os.Stat(done.OutputPath); statErr == nil {
			return MasterURLForTask(done.ID), true, done.ID, nil
		}
		s.logger.Warn("转码产物缺失，将重新转码",
			zap.Uint64("taskId", done.ID), zap.String("path", done.OutputPath))
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, 0, err
	}

	// 2. 已有进行中的任务 → 复用，不重复提交
	if v, ok := s.inflight.Load(res.ID); ok {
		return "", false, v.(uint64), nil
	}
	var running model.TranscodeTask
	err = s.db.Where("resource_id = ? AND status IN ?", res.ID,
		[]string{model.TaskStatusPending, model.TaskStatusRunning}).
		Order("id desc").First(&running).Error
	if err == nil {
		return "", false, running.ID, nil
	}

	// 3. 新建任务
	t, err := s.Create(ctx, res)
	if err != nil {
		return "", false, 0, err
	}
	return "", false, t.ID, nil
}

// Create 创建并异步执行转码任务
func (s *TranscodeService) Create(ctx context.Context, res *model.Resource) (*model.TranscodeTask, error) {
	if !s.Enabled() {
		return nil, transcode.ErrDisabled
	}

	// 局部变量刻意不叫 task：本文件已 import "internal/task" 包，
	// 同名局部变量会遮蔽包名，后续若在同作用域引用 task.XxxType 会静默编译失败。
	tt := &model.TranscodeTask{
		ResourceID: res.ID,
		FileID:     res.FileID,
		// SourcePath 记录网盘内的来源标识（分享链接），便于事后追溯；
		// 本地临时源文件路径在 run() 中按 taskID 计算，不落库（它会被删除）。
		SourcePath: res.ShareURL,
		Status:     model.TaskStatusPending,
		Message:    "",
	}
	if err := s.db.Create(tt).Error; err != nil {
		return nil, err
	}

	s.inflight.Store(res.ID, tt.ID)

	// 异步执行，不阻塞请求
	go s.run(tt.ID, res.ID)

	return tt, nil
}

// run 执行转码（在独立 goroutine 中）
func (s *TranscodeService) run(taskID, resourceID uint64) {
	defer s.inflight.Delete(resourceID)

	// 独立 context：不能绑定 HTTP 请求，否则请求结束转码就被取消
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := s.logger.With(
		zap.Uint64("taskId", taskID),
		zap.Uint64("resourceId", resourceID),
	)

	// 槽位获取：满则标记排队，轮询等待
	if err := s.acquireWithWait(ctx); err != nil {
		s.fail(taskID, "等待转码槽位超时: "+err.Error())
		log.Error("获取转码槽位失败", zap.Error(err))
		return
	}
	defer s.engine.Release()

	s.markRunning(taskID)

	var res model.Resource
	if err := s.db.First(&res, resourceID).Error; err != nil {
		s.fail(taskID, "资源不存在")
		return
	}

	// 1. 解析直链
	directURL, headers, err := s.resolveDirectLink(ctx, &res)
	if err != nil {
		s.fail(taskID, transcodeMsgNoDirectLink+": "+err.Error())
		log.Warn("解析直链失败", zap.Error(err))
		return
	}

	// 2. 下载源文件到本地
	srcPath := filepath.Join(s.tmpDir, fmt.Sprintf("%d.src", taskID))
	// 无论成败都要清理临时源文件：转码产物已经独立，源文件没有留存价值
	defer func() {
		if rmErr := os.Remove(srcPath); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn("清理临时源文件失败", zap.String("path", srcPath), zap.Error(rmErr))
		}
	}()

	dlPct := 0
	err = s.acc.Download(ctx, directURL, headers, srcPath, func(p task.DownloadProgress) {
		if p.Total <= 0 {
			return
		}
		pct := int(p.Downloaded * 100 / p.Total)
		// 下载阶段占总进度的前 30%
		mapped := pct * 30 / 100
		if mapped != dlPct {
			dlPct = mapped
			s.setProgress(taskID, mapped, "下载源文件")
		}
	})
	if err != nil {
		s.fail(taskID, "下载源文件失败: "+err.Error())
		log.Warn("下载源文件失败", zap.Error(err))
		return
	}

	// 3. 转码
	s.setProgress(taskID, 30, "转码中")
	duration := res.Duration
	result, err := s.engine.Run(ctx, transcode.Request{
		TaskID:          taskID,
		SourcePath:      srcPath,
		OutputDir:       s.outDir,
		DurationSeconds: duration,
		OnProgress: func(pct int) {
			// 转码阶段占 30%~100%
			s.setProgress(taskID, 30+pct*70/100, "转码中")
		},
	})
	if err != nil {
		s.fail(taskID, "转码失败: "+err.Error())
		log.Error("转码失败", zap.Error(err))
		return
	}

	// 4. 落库
	now := time.Now()
	s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":      model.TaskStatusSuccess,
		"progress":    100,
		"message":     fmt.Sprintf("完成，共 %d 档", len(result.Variants)),
		"output_path": result.MasterPath,
		"finished_at": &now,
	})

	log.Info("转码完成",
		zap.Int("variants", len(result.Variants)),
		zap.Duration("cost", result.Duration),
		zap.String("master", result.MasterPath),
	)
}

// acquireWithWait 获取槽位，满则每隔几秒重试，最长等 30 分钟
//
// 用轮询而非阻塞等待，是因为 Engine 的 sem 是带缓冲 channel，
// 直接阻塞会让大量 goroutine 堆积；轮询能让失败可观测、可超时。
func (s *TranscodeService) acquireWithWait(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Minute)
	for {
		err := s.engine.Acquire(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, transcode.ErrBusy) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("排队超过 30 分钟")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// resolveDirectLink 解析资源直链，返回 URL 与请求所需 headers
func (s *TranscodeService) resolveDirectLink(ctx context.Context, res *model.Resource) (string, map[string]string, error) {
	if res.DiskAccountID == nil || *res.DiskAccountID == 0 {
		return "", nil, errors.New("资源未关联网盘账号")
	}
	if res.FileID == "" {
		return "", nil, errors.New("资源缺少文件 ID")
	}

	var acc model.DiskAccount
	if err := s.db.First(&acc, *res.DiskAccountID).Error; err != nil {
		return "", nil, errors.New("网盘账号不存在")
	}

	cookie, err := s.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return "", nil, errors.New("凭据解密失败")
	}

	extra := make(map[string]string, len(acc.Extra))
	for k, v := range acc.Extra {
		if str, ok := v.(string); ok {
			extra[k] = str
		}
	}

	a, err := adapter.Get(adapter.DiskType(res.DiskType))
	if err != nil {
		return "", nil, err
	}

	cred := &adapter.Credential{
		DiskType: adapter.DiskType(res.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}

	link, err := a.GetDirectLink(ctx, cred, res.FileID)
	if err != nil {
		return "", nil, err
	}
	if link == nil || link.URL == "" {
		return "", nil, errors.New("直链为空")
	}

	// 多数网盘直链需要带 Referer/UA，否则 403
	headers := map[string]string{}
	if link.Headers != nil {
		for k, v := range link.Headers {
			headers[k] = v
		}
	}
	if _, ok := headers["User-Agent"]; !ok {
		headers["User-Agent"] = defaultUA
	}
	return link.URL, headers, nil
}

const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// 状态更新辅助

func (s *TranscodeService) markRunning(taskID uint64) {
	now := time.Now()
	s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":     model.TaskStatusRunning,
		"started_at": &now,
	})
}

func (s *TranscodeService) setProgress(taskID uint64, pct int, msg string) {
	if pct < 0 {
		pct = 0
	}
	if pct > 99 {
		pct = 99
	}
	s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"progress": pct,
		"message":  msg,
	})
}

func (s *TranscodeService) fail(taskID uint64, msg string) {
	// 错误信息可能很长（含 ffmpeg 输出），按模型定义截断
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	now := time.Now()
	s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":      model.TaskStatusFailed,
		"message":     msg,
		"finished_at": &now,
	})
}

// Cancel 取消任务
func (s *TranscodeService) Cancel(taskID uint64) error {
	var t model.TranscodeTask
	if err := s.db.First(&t, taskID).Error; err != nil {
		return err
	}
	if t.Status != model.TaskStatusRunning && t.Status != model.TaskStatusPending {
		return fmt.Errorf("任务状态为 %s，无法取消", t.Status)
	}

	// kill 掉 ffmpeg 进程；pending 状态的任务没有进程，靠 db 标记拦截
	killed := s.engine.Cancel(taskID)

	now := time.Now()
	msg := "已取消"
	if !killed && t.Status == model.TaskStatusPending {
		msg = "已在排队中被取消"
	}
	return s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":      model.TaskStatusCanceled,
		"message":     msg,
		"finished_at": &now,
	}).Error
}

// MasterURLForTask 由任务 ID 拼出 master.m3u8 的 URL
//
// 输出目录约定为 <outDir>/<taskID>/master.m3u8，
// 而 /transcode 静态路由指向 outDir，故 URL 为 /transcode/<taskID>/master.m3u8。
//
// 刻意不做「从磁盘路径反推 taskID」——那依赖目录名恰好等于 ID 的隐含约定，
// 跨平台路径分隔符差异也会让 filepath.Base 的结果变得不可预期。
func MasterURLForTask(taskID uint64) string {
	return "/transcode/" + strconv.FormatUint(taskID, 10) + "/master.m3u8"
}

// TranscodeVariant 一档清晰度的描述（供播放接口下发给前端）
type TranscodeVariant struct {
	Name      string `json:"name"`
	Height    int    `json:"height"`
	Playlist  string `json:"playlist"`
	Bandwidth int    `json:"bandwidth"`
}

// variants 档位预设表：与 transcode.DefaultProfiles 保持一致。
//
// 之所以在这里再写一份而不是 import transcode 包：service 包已经 import 了
// transcode（Engine 类型），复用 DefaultProfiles 也可以；但档位带宽是按
// 视频码率 × 1.5 粗估的展示值，属于「播放侧展示」语义，和编码侧参数不是同一件事，
// 放在这里避免两个语义耦合。
var variants = []TranscodeVariant{
	{Name: "360p", Height: 360, Bandwidth: 800_000},
	{Name: "480p", Height: 480, Bandwidth: 1_400_000},
	{Name: "720p", Height: 720, Bandwidth: 2_600_000},
}

// VariantsOf 返回某转码任务的清晰度档位列表。
//
// 档位不落库，直接按输出目录约定扫描：<outDir>/<taskID>/<档位名>/index.m3u8。
// 磁盘是唯一事实来源——目录在就说明这一档真的转出来了，
// 相比在库里维护一张档位表，少了一处可能不同步的状态。
func (s *TranscodeService) VariantsOf(taskID uint64) []TranscodeVariant {
	base := filepath.Join(s.outDir, strconv.FormatUint(taskID, 10))
	out := make([]TranscodeVariant, 0, len(variants))
	for _, v := range variants {
		playlist := filepath.Join(base, v.Name, "index.m3u8")
		if _, err := os.Stat(playlist); err != nil {
			continue
		}
		out = append(out, TranscodeVariant{
			Name:      v.Name,
			Height:    v.Height,
			Playlist:  "/transcode/" + strconv.FormatUint(taskID, 10) + "/" + v.Name + "/index.m3u8",
			Bandwidth: v.Bandwidth,
		})
	}
	return out
}

// Touch 刷新产物访问时间
//
// 播放接口每次命中 HLS 都会调用它。GC 以此做 LRU——「孩子一直在看的那几部」
// 会因为持续被访问而长期留住，冷门内容才会被淘汰。
//
// 失败不返回错误：刷新时间戳是辅助动作，不该因为它而让播放请求失败。
func (s *TranscodeService) Touch(taskID uint64) {
	if s == nil || taskID == 0 {
		return
	}
	now := time.Now()
	if err := s.db.Model(&model.TranscodeTask{}).Where("id = ?", taskID).
		Update("last_access_at", &now).Error; err != nil {
		s.logger.Warn("刷新转码产物访问时间失败",
			zap.Uint64("taskId", taskID), zap.Error(err))
	}
}

// RunGC 执行一次产物体检（供定时任务与管理员手动触发）
func (s *TranscodeService) RunGC() *GCResult {
	if s == nil || s.gc == nil {
		return &GCResult{Errors: []string{"转码未启用，GC 不可用"}}
	}
	return s.gc.Run()
}

// DiskUsage 统计转码目录占用（字节）
func (s *TranscodeService) DiskUsage() int64 {
	if s == nil || s.outDir == "" {
		return 0
	}
	size, err := dirSize(s.outDir)
	if err != nil {
		return 0
	}
	return size
}

// NeedsTranscode 判断某资源是否值得转码
//
// 判定逻辑（保守，宁可不转也不要白转）：
//   - 非视频不转
//   - 已是 HLS（.m3u8）不转，它本来就是流
//   - 常见 MP4 + H.264 组合且时长较短，直链即可播，不转
//   - 其他容器（mkv/avi/rmvb/flv/ts/wmv/mov）或时长很长、体积很大 → 转
func NeedsTranscode(res *model.Resource) bool {
	if res == nil || res.MediaType != model.MediaTypeVideo {
		return false
	}
	title := strings.ToLower(res.Title)
	if strings.HasSuffix(title, ".m3u8") {
		return false
	}
	// 体积超过 300MB 或时长超过 10 分钟，直链拖动体验差，转码收益明显
	if res.TotalSize > 300*1024*1024 || res.Duration > 600 {
		return true
	}
	// 容器格式明确的，按扩展名判断
	for _, ext := range []string{".mkv", ".avi", ".rmvb", ".rm", ".flv", ".wmv", ".mov", ".ts", ".mpg", ".mpeg", ".3gp"} {
		if strings.HasSuffix(title, ext) {
			return true
		}
	}
	return false
}
