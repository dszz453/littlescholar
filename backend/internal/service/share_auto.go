package service

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// ShareAutoStat 自动共享一轮的统计。
type ShareAutoStat struct {
	Total   int
	Created int
	Skipped int
	Failed  int
}

// ShareAutoFunc 执行一轮自动共享。
//
// 用函数类型而不是接口去引用 handler 包：`handler` 大量 import `service`
// （ai.go / alive.go / category.go 都是），反过来 import 就成环了。
// 这里真正关心的只有"跑一轮、给我四个数字"，函数签名表达得最直接。
type ShareAutoFunc func(ctx context.Context) (ShareAutoStat, error)

// StartShareAutoScheduler 定期为「挂过映射但还没分享」的资源补建分享。
//
// # 为什么需要它，而不是只在后台点按钮
//
// PRO 的转存链路要求资源上必须有 shareUrl，缺了整个播放就停在
// "缺少分享链接"。而资源是**持续新增**的 —— 每次同步、每次新挂映射
// 都会带来一批没分享的。指望家长记得回后台点按钮，结果就是孩子
// 时不时遇到"这集播不了"，而原因在后台、和孩子一点关系都没有。
//
// # 与 StartGCJanitor 刻意不同的一处
//
// 首轮延迟结束后**先立刻跑一次**，再进 ticker。
// `time.NewTicker` 不会立即触发，若直接进 select，注释里写的
// "15 分钟后跑第一轮"实际会变成 "interval + 15 分钟"，而这段时间里
// 日志一行都没有 —— 这是本项目踩过的坑（"首轮延迟 8 分钟"变成
// "24 小时 8 分钟"且毫无痕迹）。
func StartShareAutoScheduler(run ShareAutoFunc, logger *zap.Logger,
	interval, firstDelay time.Duration) func() {

	stopCh := make(chan struct{})

	go func() {
		// 顶层兜底：这个 goroutine 里任何未捕获的 panic 都会带走整个进程。
		defer func() {
			if r := recover(); r != nil {
				logger.Error("自动共享调度器 panic", zap.Any("recover", r))
			}
		}()

		// 首次延迟，可被 stop 提前打断
		if firstDelay > 0 {
			select {
			case <-time.After(firstDelay):
			case <-stopCh:
				return
			}
		}

		runOnce := func() {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("自动共享任务 panic", zap.Any("recover", r))
				}
			}()

			// 单轮封顶：网盘挂住时不能把这个 goroutine 永久占住，
			// 否则后续每一轮都会排在它后面，看起来像"任务再没跑过"。
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			st, err := run(ctx)
			if err != nil {
				logger.Warn("自动共享失败", zap.Error(err))
				return
			}
			if st.Created == 0 && st.Failed == 0 {
				// 没有要补的属于**正常状态**（补完就不再产生），
				// 用 Debug 打，免得每天一条"成功 0 条"把有用日志顶掉。
				logger.Debug("自动共享：没有需要补分享的资源",
					zap.Int("total", st.Total))
				return
			}
			logger.Info("自动共享完成",
				zap.Int("total", st.Total),
				zap.Int("created", st.Created),
				zap.Int("skipped", st.Skipped),
				zap.Int("failed", st.Failed),
			)
		}

		runOnce()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()

	return func() { close(stopCh) }
}
