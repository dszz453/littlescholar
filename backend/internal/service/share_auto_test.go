package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestShareAutoSchedulerRunsOnceAfterFirstDelay 首轮延迟一到就该跑**一次**，
// 而不是等到第一个 interval。
//
// 这条钉的是本项目踩过的坑：`time.NewTicker` 不会立即触发，若首轮只靠
// ticker，注释里写的"15 分钟后跑"实际会变成"6 小时 15 分钟后"，
// 而这段时间日志一行都没有 —— 看起来就像任务根本没启动。
//
// interval 特意给 1 小时：如果实现退化成"只靠 ticker"，2 秒内一次都不会跑，
// 用例必红。这是变异验证式写法 —— 断言本身就是"旧代码会挂在哪里"。
func TestShareAutoSchedulerRunsOnceAfterFirstDelay(t *testing.T) {
	var n int32
	run := func(context.Context) (ShareAutoStat, error) {
		atomic.AddInt32(&n, 1)
		return ShareAutoStat{Total: 1, Created: 1}, nil
	}

	stop := StartShareAutoScheduler(run, zap.NewNop(), time.Hour, 20*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&n) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("首轮延迟后应立刻跑 1 次，实得 %d 次（interval=1h 还没到，"+
			"说明首轮退化成了只等 ticker）", got)
	}

	// 再等等，确认"首轮那次"没有顺手把 ticker 的节奏也提前了
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Errorf("interval 还没到就不该再跑，实得 %d 次", got)
	}
}

// TestShareAutoSchedulerStopBeforeFirstRun 首轮延迟期间被 stop，就不该再跑。
//
// 这是优雅退出的要求：进程重启时若还在等首轮延迟，stop 必须能立刻打断它，
// 而不是让 main 的 defer 链卡上一个 firstDelay。
func TestShareAutoSchedulerStopBeforeFirstRun(t *testing.T) {
	var n int32
	run := func(context.Context) (ShareAutoStat, error) {
		atomic.AddInt32(&n, 1)
		return ShareAutoStat{}, nil
	}

	stop := StartShareAutoScheduler(run, zap.NewNop(), time.Hour, 200*time.Millisecond)
	stop()

	time.Sleep(350 * time.Millisecond)
	if got := atomic.LoadInt32(&n); got != 0 {
		t.Errorf("stop 之后不该再执行，实得 %d 次", got)
	}
}

// TestShareAutoSchedulerSurvivesPanic 单轮 panic 不能带走后面几轮。
//
// 定时任务最怕的是"第一次出异常就再也不干活了"，而进程还活得好好的 ——
// 表现是"日志里只有一条 panic，之后什么都没有"，极难发现。
func TestShareAutoSchedulerSurvivesPanic(t *testing.T) {
	var n int32
	run := func(context.Context) (ShareAutoStat, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			panic("模拟第一轮炸了")
		}
		return ShareAutoStat{Total: 1, Created: 0}, nil
	}

	stop := StartShareAutoScheduler(run, zap.NewNop(), 40*time.Millisecond, time.Millisecond)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&n) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&n); got < 2 {
		t.Fatalf("第一轮 panic 之后应继续跑后续轮次，实得 %d 轮", got)
	}
}
