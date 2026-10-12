package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 巡检相关错误
var (
	ErrAliveCheckRunning = errors.New("已有巡检任务在执行")
	ErrNoResources       = errors.New("没有需要巡检的资源")
)

// ResourceAliveService 资源存活巡检
//
// 职责：定时把库里的分享链接批量送检，把结果回写资源状态，并在
// 「失效数量异常」时给出信号。
//
// 与 handler 里那个 CheckAlive 的区别（两者并存，不是替换）：
//
//	handler.CheckAlive —— 用户在界面上勾选几条，等结果同步返回。要求快、可控。
//	本服务            —— 后台定时全库扫。要求稳、能降级、失败不静默。
//
// 三个关键设计：
//
//  1. 双引擎降级：优先 PanSou 的 /api/check/links（支持批量），
//     单批失败时退到 PanCheck 逐条检测。两者都不可用则整批标记为「检测失败」，
//     **绝不把"测不出来"写成"资源已失效"**——这是最容易造成误删的坑。
//
//  2. 连续失败才判死：一次判 bad 可能只是对面抖动。要求连续 N 次
//     （默认 2 次）才把状态改成 dead，把这个判断放在服务层而不是调用方，
//     避免每个调用点各写一遍。
//
//  3. 分批 + 进度落库：全库可能有上万条，一次性送检会超时被对面拒；
//     分批处理并把进度写进 AliveCheckTask，管理员能看到"扫到哪了"。
type ResourceAliveService struct {
	db *gorm.DB

	pansou   *PansouClient
	pancheck *PancheckClient
	logger   *zap.Logger

	// batchSize 单批送检数量。PanSou 的检测接口对批量大小有限制，
	// 30 是经验值：够快，又不至于被拒。
	batchSize int
	// interval 每批之间的间隔，避免把对面打挂（也是礼貌）
	interval time.Duration
	// deadThreshold 连续失败几次才判死
	deadThreshold int
	// staleAfter 多久没检查过的资源需要重新检查
	staleAfter time.Duration

	// 同一时刻只允许一轮巡检
	mu      sync.Mutex
	running bool
}

// AliveDeps 依赖
type AliveDeps struct {
	DB       *gorm.DB
	Pansou   *PansouClient
	Pancheck *PancheckClient
	Logger   *zap.Logger

	BatchSize     int
	Interval      time.Duration
	DeadThreshold int
	StaleHours    int
}

// NewResourceAliveService 创建巡检服务
func NewResourceAliveService(d AliveDeps) *ResourceAliveService {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	bs := d.BatchSize
	if bs <= 0 {
		bs = 30
	}
	iv := d.Interval
	if iv <= 0 {
		iv = 2 * time.Second
	}
	dt := d.DeadThreshold
	if dt <= 0 {
		dt = 2
	}
	sh := d.StaleHours
	if sh <= 0 {
		sh = 72 // 3 天
	}
	return &ResourceAliveService{
		db:            d.DB,
		pansou:        d.Pansou,
		pancheck:      d.Pancheck,
		logger:        logger,
		batchSize:     bs,
		interval:      iv,
		deadThreshold: dt,
		staleAfter:    time.Duration(sh) * time.Hour,
	}
}

// InjectPancheck 后置注入 PanCheck（配置可能热更新）
func (s *ResourceAliveService) InjectPancheck(p *PancheckClient) {
	s.pancheck = p
}

// Enabled 是否具备巡检能力（至少有一个检测引擎）
func (s *ResourceAliveService) Enabled() bool {
	if s == nil {
		return false
	}
	return s.pansou != nil || s.pancheck != nil
}

// Status 运行时状态
func (s *ResourceAliveService) Status() map[string]interface{} {
	if s == nil {
		return map[string]interface{}{"enabled": false}
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	return map[string]interface{}{
		"enabled":       s.Enabled(),
		"running":       running,
		"batchSize":     s.batchSize,
		"deadThreshold": s.deadThreshold,
		"staleHours":    int(s.staleAfter / time.Hour),
		"hasPansou":     s.pansou != nil,
		"hasPancheck":   s.pancheck != nil,
	}
}

// Start 启动一轮巡检（异步）
//
// 返回 taskID 供前端轮询。mode:
//   - "stale" 只检查超过 staleAfter 未检查的（默认，日常用）
//   - "all"   全量重检（慎用，全库量大的时候很久）
//   - "dead"  只重检当前标记为失效的（验证是否真的死了，或已恢复）
func (s *ResourceAliveService) Start(trigger, mode string) (uint64, error) {
	if !s.Enabled() {
		return 0, errors.New("未配置检测引擎")
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return 0, ErrAliveCheckRunning
	}
	s.running = true
	s.mu.Unlock()

	resources, err := s.pickResources(mode)
	if err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return 0, err
	}
	if len(resources) == 0 {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return 0, ErrNoResources
	}

	if mode == "" {
		mode = "stale"
	}
	if trigger == "" {
		trigger = "manual"
	}

	now := time.Now()
	task := &model.AliveCheckTask{
		Status:    model.TaskStatusRunning,
		Total:     len(resources),
		Trigger:   trigger,
		Mode:      mode,
		Message:   "巡检中",
		StartedAt: &now,
	}
	if err := s.db.Create(task).Error; err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return 0, fmt.Errorf("创建巡检任务失败: %w", err)
	}

	go s.run(task.ID, resources)

	return task.ID, nil
}

// pickResources 选出待检资源
func (s *ResourceAliveService) pickResources(mode string) ([]model.Resource, error) {
	q := s.db.Model(&model.Resource{}).Where("share_url <> ''")

	switch mode {
	case "all":
		// 全量：不加时间条件
	case "dead":
		q = q.Where("status = ?", model.ResourceStatusDead)
	default: // stale
		cutoff := time.Now().Add(-s.staleAfter)
		// 从未检查过的（NULL）也要包含进来
		q = q.Where("last_checked_at IS NULL OR last_checked_at < ?", cutoff)
	}

	var out []model.Resource
	// 排序：最久没查的优先。
	//
	// 用 "last_checked_at IS NULL DESC" 而不是 "NULLS FIRST"：
	// 后者在 PostgreSQL 上可用，但 SQLite 对它的支持依赖版本，
	// 而本项目支持 sqlite 驱动（见 config.database.driver）。
	// 用 IS NULL 表达式是两边都稳的写法，代价只是多一个排序键。
	//
	// 这样即使中途中断，也是「老的先被覆盖」而不是每次都从头扫同一批。
	if err := q.Order("last_checked_at IS NULL DESC, last_checked_at ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// run 执行巡检（独立 goroutine）
func (s *ResourceAliveService) run(taskID uint64, resources []model.Resource) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	var (
		checked, alive, dead, unknown, failedBatches int
	)

	for i := 0; i < len(resources); i += s.batchSize {
		end := i + s.batchSize
		if end > len(resources) {
			end = len(resources)
		}
		batch := resources[i:end]

		results, err := s.checkBatch(ctx, batch)
		if err != nil {
			// ctx 已取消/超时：后续批次必然同样失败，直接收尾。
			// 不做这个判断的话，每批都会快速失败并刷一条日志，
			// 上千条资源的场景下会瞬间打爆日志。
			if ctx.Err() != nil {
				checked += len(batch)
				unknown += len(batch)
				s.finish(taskID, checked, alive, dead, unknown, failedBatches,
					"巡检超时中断，已处理 "+fmt.Sprintf("%d/%d", checked, len(resources)))
				return
			}

			// 单批失败不中断整轮——但要计数。
			// 大量失败意味着对面服务挂了，而不是资源失效，这个区别至关重要。
			failedBatches++
			s.logger.Warn("巡检批次失败",
				zap.Uint64("taskId", taskID),
				zap.Int("batchStart", i),
				zap.Error(err))
			checked += len(batch)
			unknown += len(batch)
			// 失败也要更新 last_checked_at？**不更新**。
			// 不更新才能让这批在下一轮被重新捡起来，否则会陷入
			// 「一直失败、但永远不再重试」的静默状态。
			s.updateProgress(taskID, checked, alive, dead, unknown, failedBatches)
			continue
		}

		for _, r := range results {
			switch r.state {
			case model.ResourceStatusAlive:
				alive++
			case model.ResourceStatusDead:
				dead++
			default:
				unknown++
			}
			s.applyResult(r.resourceID, r.state, r.summary)
		}
		checked += len(batch)

		s.updateProgress(taskID, checked, alive, dead, unknown, failedBatches)

		// 批间隔，别把对面打挂
		if end < len(resources) {
			select {
			case <-ctx.Done():
				s.finish(taskID, checked, alive, dead, unknown, failedBatches, "巡检被中断（超时）")
				return
			case <-time.After(s.interval):
			}
		}
	}

	msg := fmt.Sprintf("完成：共 %d 条，存活 %d，失效 %d，未知 %d", checked, alive, dead, unknown)
	if failedBatches > 0 {
		msg += fmt.Sprintf("（%d 个批次调用失败，建议检查检测服务）", failedBatches)
	}
	s.finish(taskID, checked, alive, dead, unknown, failedBatches, msg)

	s.logger.Info("资源巡检完成",
		zap.Uint64("taskId", taskID),
		zap.Int("checked", checked),
		zap.Int("alive", alive),
		zap.Int("dead", dead),
		zap.Int("unknown", unknown),
		zap.Int("failedBatches", failedBatches),
	)
}

// checkResult 单条检测的归一化结果
type checkResult struct {
	resourceID uint64
	state      string // alive / dead / unknown
	summary    string
}

// checkBatch 检测一批资源，带引擎降级
//
// 降级链：PanSou 批量 → 失败则 PanCheck 逐条 → 仍失败则返回 error。
//
// 为什么不只用一个？PanSou 的批量接口快但会整体失败（比如对面重启），
// PanCheck 逐条慢但容错好。组合起来能把「大批量整体失败」的概率降下来。
func (s *ResourceAliveService) checkBatch(ctx context.Context, batch []model.Resource) ([]checkResult, error) {
	if s.pansou != nil {
		items := make([]CheckLinkItem, 0, len(batch))
		for _, r := range batch {
			items = append(items, CheckLinkItem{
				DiskType: r.DiskType,
				URL:      r.ShareURL,
				Password: r.Passcode,
			})
		}

		linkResults, err := s.pansou.CheckLinks(ctx, items)
		if err == nil {
			return s.mapByURL(batch, linkResults), nil
		}
		s.logger.Warn("PanSou 检测失败，尝试 PanCheck 降级", zap.Error(err))
	}

	if s.pancheck != nil {
		return s.checkViaPancheck(ctx, batch)
	}

	return nil, errors.New("无可用的检测引擎")
}

// mapByURL 把 PanSou 的结果按 URL 匹配回资源
//
// PanSou 返回的 URL 可能与库里的不完全一致（多了 query、少了斜杠），
// 所以两边都做归一化后再比。归一化函数与 handler 里那个保持一致语义。
func (s *ResourceAliveService) mapByURL(batch []model.Resource, results []CheckResult) []checkResult {
	byURL := make(map[string]uint64, len(batch))
	for _, r := range batch {
		byURL[normalizeShareURL(r.ShareURL)] = r.ID
	}

	out := make([]checkResult, 0, len(batch))
	matched := make(map[uint64]bool, len(batch))

	for _, res := range results {
		id, ok := byURL[normalizeShareURL(res.URL)]
		if !ok {
			// URL 对不上就用返回值里的 normalized_url 再试一次
			id, ok = byURL[normalizeShareURL(res.NormalizedURL)]
			if !ok {
				continue
			}
		}
		matched[id] = true
		out = append(out, checkResult{
			resourceID: id,
			state:      mapPanSouState(res.State),
			summary:    res.Summary,
		})
	}

	// 对面没返回的资源：状态未知，不能当成失效
	for _, r := range batch {
		if !matched[r.ID] {
			out = append(out, checkResult{
				resourceID: r.ID,
				state:      model.ResourceStatusUnknown,
				summary:    "检测服务未返回该条结果",
			})
		}
	}
	return out
}

// checkViaPancheck 用 PanCheck 逐条检测
func (s *ResourceAliveService) checkViaPancheck(ctx context.Context, batch []model.Resource) ([]checkResult, error) {
	out := make([]checkResult, 0, len(batch))
	okCount := 0

	for _, r := range batch {
		state, err := s.pancheck.CheckOne(ctx, r.DiskType, r.ShareURL)
		if err != nil {
			// 逐条失败：这条算 unknown，继续下一条
			out = append(out, checkResult{
				resourceID: r.ID,
				state:      model.ResourceStatusUnknown,
				summary:    "PanCheck 调用失败: " + err.Error(),
			})
			continue
		}
		okCount++
		out = append(out, checkResult{
			resourceID: r.ID,
			state:      mapPanSouState(state),
			summary:    "PanCheck: " + state,
		})
		// 逐条模式节奏更慢，避免触发风控
		select {
		case <-ctx.Done():
			return out, nil
		case <-time.After(s.interval):
		}
	}

	// 一条都没成功 → 视为整批失败，让上层计入 failedBatches
	if okCount == 0 && len(batch) > 0 {
		return out, errors.New("PanCheck 全部调用失败")
	}
	return out, nil
}

// applyResult 把单条结果写回资源
//
// 这里实现「连续失败才判死」：
//   - 判定失效 → ConsecutiveFailures + 1；达到阈值才把状态改成 dead
//   - 判定存活 → 清零计数，状态置 alive
//   - 未知     → 不动状态，只累加 CheckFailCount（区分"资源问题"和"检测问题"）
func (s *ResourceAliveService) applyResult(resourceID uint64, state, summary string) {
	var r model.Resource
	if err := s.db.First(&r, resourceID).Error; err != nil {
		return
	}
	now := time.Now()

	switch state {
	case model.ResourceStatusAlive:
		s.db.Model(&model.Resource{}).Where("id = ?", resourceID).Updates(map[string]interface{}{
			"status":               model.ResourceStatusAlive,
			"last_checked_at":      now,
			"consecutive_failures": 0,
		})

	case model.ResourceStatusDead:
		failures := r.ConsecutiveFailures + 1
		updates := map[string]interface{}{
			"last_checked_at":      now,
			"consecutive_failures": failures,
		}
		// 只有连续多次失效才真正标记 dead。
		// 未达阈值时保持原状态，但计数器在涨——这样下一轮再失败就会生效。
		if failures >= s.deadThreshold {
			updates["status"] = model.ResourceStatusDead
		}
		s.db.Model(&model.Resource{}).Where("id = ?", resourceID).Updates(updates)

	default: // unknown
		s.db.Model(&model.Resource{}).Where("id = ?", resourceID).Updates(map[string]interface{}{
			"last_checked_at":  now,
			"check_fail_count": gorm.Expr("check_fail_count + 1"),
		})
	}
}

// updateProgress 更新巡检进度
func (s *ResourceAliveService) updateProgress(taskID uint64, checked, alive, dead, unknown, failedBatches int) {
	s.db.Model(&model.AliveCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"checked":        checked,
		"alive_count":    alive,
		"dead_count":     dead,
		"unknown_count":  unknown,
		"failed_batches": failedBatches,
	})
}

// finish 标记巡检结束
func (s *ResourceAliveService) finish(taskID uint64, checked, alive, dead, unknown, failedBatches int, msg string) {
	now := time.Now()
	s.db.Model(&model.AliveCheckTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"status":         model.TaskStatusSuccess,
		"checked":        checked,
		"alive_count":    alive,
		"dead_count":     dead,
		"unknown_count":  unknown,
		"failed_batches": failedBatches,
		"message":        msg,
		"finished_at":    &now,
	})
}

// DeadResources 返回当前标记失效的资源（供后台展示与告警）
func (s *ResourceAliveService) DeadResources(limit int) ([]model.Resource, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []model.Resource
	err := s.db.Where("status = ?", model.ResourceStatusDead).
		Order("last_checked_at desc").Limit(limit).Find(&out).Error
	return out, err
}

// ============ 工具函数 ============

// MapAliveState 把检测服务返回的状态映射为资源状态
//
// 导出是为了让 handler 的手动检测复用同一套判定，避免两处各写一份、
// 时间久了语义漂移（比如一处认为 locked 是 dead、另一处认为是 unknown）。
//
// 注意 locked（需要提取码）被归到 unknown 而不是 dead：
// 链接本身很可能是活的，只是这次没带对密码。判死会让用户丢掉一个可用资源，
// 而判 unknown 只是"待确认"，代价小得多。
func MapAliveState(state string) string {
	switch state {
	case "ok":
		return model.ResourceStatusAlive
	case "bad":
		return model.ResourceStatusDead
	case "locked", "unsupported", "uncertain", "":
		return model.ResourceStatusUnknown
	default:
		return model.ResourceStatusUnknown
	}
}

// NormalizeShareURL 归一化分享链接（导出供 handler 复用）
//
// 去掉无意义的 query 参数，但保留 pwd=（它影响能否访问）。
func NormalizeShareURL(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "?"); i > 0 {
		base := u[:i]
		query := u[i:]
		if strings.Contains(query, "pwd=") {
			return u
		}
		return base
	}
	return u
}

// mapPanSouState 内部别名，保持文件内调用简洁
func mapPanSouState(state string) string { return MapAliveState(state) }

// normalizeShareURL 内部别名
func normalizeShareURL(u string) string { return NormalizeShareURL(u) }

// StartAliveScheduler 启动后台定时巡检
//
// 返回 stop 函数（main 里 defer 挂上）。
//
// 与 GC 的 janitor 结构一致：ticker + select，可被 stopCh 干净打断。
//
// 一个额外的保护：如果上一轮巡检还在跑（比如资源特别多、24 小时没扫完），
// 本轮直接跳过而不是排队。巡检不是"必须按时完成"的任务，
// 堆积多轮反而会发出重复的检测请求，把对面打挂。
func StartAliveScheduler(svc *ResourceAliveService, logger *zap.Logger, interval, firstDelay time.Duration) func() {
	if svc == nil || logger == nil {
		return func() {}
	}
	stopCh := make(chan struct{})

	go func() {
		if firstDelay > 0 {
			select {
			case <-time.After(firstDelay):
			case <-stopCh:
				return
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							logger.Error("资源巡检 panic", zap.Any("recover", r))
						}
					}()

					// 上一轮没跑完就跳过，不排队
					svc.mu.Lock()
					busy := svc.running
					svc.mu.Unlock()
					if busy {
						logger.Info("上轮巡检尚未结束，跳过本轮")
						return
					}

					taskID, err := svc.Start("schedule", "stale")
					if err != nil {
						if errors.Is(err, ErrNoResources) {
							logger.Info("定时巡检：没有需要检查的资源")
							return
						}
						logger.Warn("定时巡检启动失败", zap.Error(err))
						return
					}
					logger.Info("定时巡检已启动", zap.Uint64("taskId", taskID))
				}()
			}
		}
	}()

	return func() { close(stopCh) }
}
