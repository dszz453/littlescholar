package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TranscodeGC 转码产物垃圾回收
//
// 为什么必须要有这一层：
//
//	一个 720p 的 HLS 产物动辄 700MB~2GB，而它是**永久驻留**的。
//	孩子看 30 部动画片就能吃掉 20GB；若不设上限，跑几个月必然写满磁盘，
//	而磁盘写满是「静默恶化」——先表现为转码失败，然后连数据库都写不进去。
//
// 淘汰策略是「先按年龄、再按容量」的两道闸：
//
//	1. TTL 闸：闲置超过 CacheTTLDays 的产物直接清掉。
//	   判断依据是 last_access_at 而不是 created_at——孩子反复看的那几部不该被清理。
//	2. 容量闸：TTL 清理后若仍超 CacheMaxGB，按 last_access_at 升序（最久未用优先）淘汰，
//	   直到降到上限的 90%。留 10% 余量是为了避免频繁触发（每次清理都留一点缓冲，
//	   而不是刚好卡在上限反复扫）。
//
// 安全性约束（这几条不能省）：
//   - 绝不删除非当前 outDir 下的任何路径（防越界误删）
//   - 不删除正在运行/pending 的任务的产物目录
//   - 不删除物理目录不存在但数据库有记录的情况（清掉记录即可，不算错误）
//   - 清理动作全程记日志，便于事后审计
type TranscodeGC struct {
	db      *gorm.DB
	logger  *zap.Logger
	outDir  string
	maxGB   int
	ttlDays int

	// 串行化：定时任务可能与管理员的「立即清理」并发触发
	mu sync.Mutex
}

// NewTranscodeGC 创建 GC
func NewTranscodeGC(db *gorm.DB, logger *zap.Logger, outDir string, maxGB, ttlDays int) *TranscodeGC {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TranscodeGC{
		db:      db,
		logger:  logger,
		outDir:  outDir,
		maxGB:   maxGB,
		ttlDays: ttlDays,
	}
}

// GCResult 一次清理的结果
type GCResult struct {
	// Scanned 扫描到的产物任务数
	Scanned int `json:"scanned"`
	// RemovedByTTL 因闲置过久被清理的数量
	RemovedByTTL int `json:"removedByTtl"`
	// RemovedBySize 因容量超限被清理的数量
	RemovedBySize int `json:"removedBySize"`
	// PurgedRecords 物理删除的陈旧 evicted 记录数（不涉及磁盘）
	PurgedRecords int `json:"purgedRecords"`
	// FreedBytes 释放的字节数
	FreedBytes int64 `json:"freedBytes"`
	// FreedMB 释放的 MB（便于展示）
	FreedMB int64 `json:"freedMb"`
	// RemainingBytes 清理后仍占用的字节数
	RemainingBytes int64 `json:"remainingBytes"`
	// Errors 清理过程中遇到的非致命错误
	Errors []string `json:"errors,omitempty"`
	// Skipped 因正在运行而跳过的任务 ID
	Skipped []uint64 `json:"skipped,omitempty"`
}

// Run 执行一次清理
//
// 任何单点失败都不应中断整体流程：转码产物清理是「尽力而为」的维护动作，
// 不该因为一个目录权限问题就让整轮清理停摆。
func (g *TranscodeGC) Run() *GCResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	res := &GCResult{}

	// 1. 取所有成功任务的产物
	var tasks []model.TranscodeTask
	if err := g.db.Where("status = ?", model.TaskStatusSuccess).Find(&tasks).Error; err != nil {
		res.Errors = append(res.Errors, "查询任务失败: "+err.Error())
		return res
	}

	// 2. 进入中的任务必须保护，避免删掉正在写的产物
	active := g.activeTaskIDs()

	type entry struct {
		task    model.TranscodeTask
		size    int64
		lastUse time.Time
	}
	var live []entry

	for _, t := range tasks {
		res.Scanned++

		if _, ok := active[t.ID]; ok {
			res.Skipped = append(res.Skipped, t.ID)
			continue
		}

		dir := filepath.Join(g.outDir, strconv.FormatUint(t.ID, 10))
		size, err := dirSize(dir)
		if err != nil {
			// 目录不存在是正常情况（可能已被手工清理），不该报错刷日志
			if !os.IsNotExist(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("任务 %d 统计体积失败: %v", t.ID, err))
			}
			// 物理目录没了：清掉数据库记录，让它下次重新转码
			g.dropRecord(t.ID)
			continue
		}

		live = append(live, entry{
			task:    t,
			size:    size,
			lastUse: lastAccessOf(t),
		})
	}

	// 3. TTL 闸：闲置过久的直接清
	now := time.Now()
	var keep []entry
	if g.ttlDays > 0 {
		ttl := time.Duration(g.ttlDays) * 24 * time.Hour
		for _, e := range live {
			if now.Sub(e.lastUse) > ttl {
				if freed, err := g.removeArtifact(e.task.ID); err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("任务 %d 清理失败: %v", e.task.ID, err))
					keep = append(keep, e)
				} else {
					res.RemovedByTTL++
					res.FreedBytes += freed
				}
				continue
			}
			keep = append(keep, e)
		}
	} else {
		keep = live
	}

	// 4. 容量闸：仍超限则按 LRU 淘汰
	if g.maxGB > 0 {
		var total int64
		for _, e := range keep {
			total += e.size
		}
		limit := int64(g.maxGB) * 1024 * 1024 * 1024

		if total > limit {
			// 最久未用的排前面
			sort.Slice(keep, func(i, j int) bool {
				return keep[i].lastUse.Before(keep[j].lastUse)
			})

			// 留 10% 余量，避免刚好卡在上限导致每轮都要清理
			target := limit * 9 / 10
			remaining := keep
			for len(remaining) > 0 && total > target {
				e := remaining[0]
				freed, err := g.removeArtifact(e.task.ID)
				if err != nil {
					// 删不掉就跳过（可能被占用），但仍从待清理队列移除，
					// 否则会死循环在同一条上
					res.Errors = append(res.Errors, fmt.Sprintf("任务 %d 清理失败: %v", e.task.ID, err))
					remaining = remaining[1:]
					continue
				}
				res.RemovedBySize++
				res.FreedBytes += freed
				total -= freed
				remaining = remaining[1:]
			}
			keep = remaining
		}
	}

	res.FreedMB = res.FreedBytes / (1024 * 1024)

	// 5. 清理陈旧的 evicted 记录
	//
	// evicted 记录保留是为了可追溯（"这个资源转过，后来被回收了"），
	// 但它没有长期价值。留 90 天足够排查任何问题，再久就是纯负担——
	// 表会随"清理→重转"的循环无限增长，而 EnsureHLS 每次都要扫它。
	res.PurgedRecords = g.purgeStaleRecords(90)

	// 6. 统计清理后的实际占用（以磁盘为准，不用推算值）
	var remain int64
	entries, err := os.ReadDir(g.outDir)
	if err == nil {
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			if sz, err := dirSize(filepath.Join(g.outDir, ent.Name())); err == nil {
				remain += sz
			}
		}
	}
	res.RemainingBytes = remain

	if res.RemovedByTTL > 0 || res.RemovedBySize > 0 || res.PurgedRecords > 0 {
		g.logger.Info("转码产物清理完成",
			zap.Int("removedByTTL", res.RemovedByTTL),
			zap.Int("removedBySize", res.RemovedBySize),
			zap.Int("purgedRecords", res.PurgedRecords),
			zap.Int64("freedMB", res.FreedMB),
			zap.Int64("remainingMB", remain/(1024*1024)),
		)
	}

	return res
}

// purgeStaleRecords 物理删除超过 N 天的 evicted 记录
func (g *TranscodeGC) purgeStaleRecords(days int) int {
	if days <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	tx := g.db.Where("status = ? AND created_at < ?", model.TaskStatusEvicted, cutoff).
		Delete(&model.TranscodeTask{})
	if tx.Error != nil {
		g.logger.Warn("清理陈旧 evicted 记录失败", zap.Error(tx.Error))
		return 0
	}
	return int(tx.RowsAffected)
}

// activeTaskIDs 正在运行或待执行的任务 ID 集合
func (g *TranscodeGC) activeTaskIDs() map[uint64]struct{} {
	out := map[uint64]struct{}{}
	var rows []model.TranscodeTask
	g.db.Select("id").Where("status IN ?",
		[]string{model.TaskStatusRunning, model.TaskStatusPending}).Find(&rows)
	for _, r := range rows {
		out[r.ID] = struct{}{}
	}
	return out
}

// removeArtifact 删除某任务的产物目录，并把任务标记为 evicted
//
// 返回释放的字节数。
//
// 关键设计：**删目录的同时必须改状态**，不能只删目录。
// 因为 EnsureHLS 是按 `status = success` 找产物的，若记录仍是 success，
// 它每次都会 os.Stat 失败、然后新建一条任务——「清理 → 重转」反复几轮，
// transcode_tasks 表就会堆满永远不会被用到的僵尸记录。
//
// 标记为 evicted 后，EnsureHLS 视其如无物（只查 success），
// 播放时会自然新建一条转码任务，这正是我们想要的语义：
// 「缓存被清掉了，下次看的时候重新生成」。
//
// 越界防护：清理路径必须严格位于 outDir 之下，防止配置写错导致误删。
func (g *TranscodeGC) removeArtifact(taskID uint64) (int64, error) {
	dir := filepath.Join(g.outDir, strconv.FormatUint(taskID, 10))

	cleanOut := filepath.Clean(g.outDir)
	cleanDir := filepath.Clean(dir)
	if cleanOut == "" || cleanDir == cleanOut || !strings.HasPrefix(cleanDir, cleanOut+string(os.PathSeparator)) {
		return 0, fmt.Errorf("路径越界，拒绝删除: %s", dir)
	}

	size, err := dirSize(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录本来就不在了：仍然要修正状态，否则会留下 success 僵尸记录
			g.markEvicted(taskID)
			return 0, nil
		}
		return 0, err
	}

	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}

	g.markEvicted(taskID)
	return size, nil
}

// markEvicted 把已被回收产物的任务标记出来
//
// 只更新仍处于 success 的记录：若任务已被用户取消或已失败，不动它。
func (g *TranscodeGC) markEvicted(taskID uint64) {
	err := g.db.Model(&model.TranscodeTask{}).
		Where("id = ? AND status = ?", taskID, model.TaskStatusSuccess).
		Updates(map[string]interface{}{
			"status":  model.TaskStatusEvicted,
			"message": "产物已被缓存清理回收，下次播放时自动重新转码",
		}).Error
	if err != nil {
		g.logger.Warn("标记产物已回收失败", zap.Uint64("taskId", taskID), zap.Error(err))
	}
}

// dropRecord 处理「数据库标记成功，但磁盘上产物已不存在」的僵尸记录
//
// 与 markEvicted 的区别：
//   - markEvicted：我们自己删的目录 → 状态改 evicted，记录**保留**（有审计价值）
//   - dropRecord ：目录不知何时没了（手工 rm、卷重建）→ 直接删记录
//
// 后者删记录是因为它没有任何可追溯的信息：既没有产物，也不是我们回收的，
// 留着只会让 EnsureHLS 每次都白扫一遍、os.Stat 失败后再新建，
// 久而久之把表撑大。
func (g *TranscodeGC) dropRecord(taskID uint64) {
	err := g.db.Where("id = ? AND status = ?", taskID, model.TaskStatusSuccess).
		Delete(&model.TranscodeTask{}).Error
	if err != nil {
		g.logger.Warn("清理失效转码记录失败", zap.Uint64("taskId", taskID), zap.Error(err))
	}
}

// lastAccessOf 取任务的「最后使用时间」
//
// 优先用 LastAccessAt（播放时刷新），没有则退到 CreatedAt。
// 这样「孩子一直在看的那几部」不会被 TTL 误杀。
func lastAccessOf(t model.TranscodeTask) time.Time {
	if t.LastAccessAt != nil && !t.LastAccessAt.IsZero() {
		return *t.LastAccessAt
	}
	if t.FinishedAt != nil && !t.FinishedAt.IsZero() {
		return *t.FinishedAt
	}
	return t.CreatedAt
}

// dirSize 递归统计目录占用
func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// StartGCJanitor 启动后台定时清理
//
// 返回一个 stop 函数，调用后停止清理循环（main 里用 defer 挂上）。
//
// 用 ticker + select 而不是 time.AfterFunc 递归重排：前者在进程退出时
// 能被 stop 干净打断，后者会留下一个游离的定时器。
//
// 首次执行延迟 firstDelay：避开启动时的初始化高峰，也让日志更易读
// （启动日志和清理日志混在一起会很难排查）。
func StartGCJanitor(svc *TranscodeService, logger *zap.Logger, interval, firstDelay time.Duration) func() {
	if svc == nil || logger == nil {
		return func() {}
	}
	stopCh := make(chan struct{})

	go func() {
		// 首次延迟，可被 stop 提前打断
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
					// 兜底 recover：清理任务出任何 panic 都不该拖垮整个进程
					defer func() {
						if r := recover(); r != nil {
							logger.Error("转码清理任务 panic",
								zap.Any("recover", r))
						}
					}()
					res := svc.RunGC()
					if len(res.Errors) > 0 {
						logger.Warn("转码清理存在非致命错误",
							zap.Strings("errors", res.Errors))
					}
				}()
			}
		}
	}()

	return func() { close(stopCh) }
}
