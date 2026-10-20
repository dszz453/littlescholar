package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/subtitle"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AISubtitleService 用大模型批量写字幕。
//
// 和「字幕采集器」（SubtitleCollector）是两件事，别混：
//   - 采集器解决「旁边有没有现成字幕」：同目录文件 → 内嵌轨 → 字幕站；
//   - 这个服务解决「什么都没有，但模型知道这册书的内容」：
//     把标题给模型，让它复原原文 + 中文对照，再由音频自己算时间轴。
//
// 为什么值得做：用户实测「大猫英语」这类分级读物，模型能准确返回原文和翻译，
// 但网上一个字幕都搜不到。这类材料在库里是大头，靠采集器永远配不上。
//
// ⚠️ 纪律（都是踩过坑换来的）：
//  1. **不确定就返回空**，状态记 skipped。模型编出来的内容比没字幕更糟 ——
//     孩子会照着错的读，家长看不出来；
//  2. **默认不覆盖**已有字幕。库里那条可能是压制组配的原版，时间轴是准的，
//     模型复原的版本只是"文本对、时间轴糙"，覆盖它是降级；
//  3. **任务全部落库**。跑一整晚的东西不能只放内存，重启就消失等于白干；
//  4. **每轮有上限 + 间隔**。一次几十条连续调接口，很容易被限流，
//     也会让孩子正在看的视频缺带宽。
type AISubtitleService struct {
	db    *gorm.DB
	store *subtitle.Store
	ai    *AIHandler

	ffmpeg string
	probe  string

	// dir 生成的 .vtt/.txt 落盘目录（data/subtitle/ai）。
	// cacheDir 是播放链路的缓存目录（data/subtitle）：生成之后要清掉那几条
	// 旧缓存，否则播放器会继续用旧的那份（Serve 先看缓存文件，再看数据库）。
	dir      string
	cacheDir string

	runMu  sync.Mutex
	statMu sync.RWMutex
	stat   AISubtitleStat
}

// AISubtitleStat 当前这一轮跑到哪了（管理页轮询）。
type AISubtitleStat struct {
	At time.Time `json:"at"`
	// State 空闲/运行中/未启用/不在时段/没有 AI 配置。
	State     string     `json:"state"`
	Scanned   int        `json:"scanned"`
	Done      int        `json:"done"`
	Failed    int        `json:"failed"`
	Skipped   int        `json:"skipped"`
	LastNote  string     `json:"lastNote"`
	LastRunAt *time.Time `json:"lastRunAt"`
}

// NewAISubtitleHandler 构造。
//
// 类型叫 Service（它还跑后台计划，不只是 HTTP handler），但构造器沿用
// NewXxxHandler 的命名：scripts/lint_offline.py 的「路由 handler 方法存在性」
// 是拿 `xxxH := handler.NewXxxHandler` 配对的，换个名字这些路由就没人校验了。
//
// store 为空时所有写库动作都会失败（ErrNoStore），所以这里允许为空，
// 但路由装配时一定传真的 —— 单测里可以只测计划/窗口这些纯逻辑。
func NewAISubtitleHandler(db *gorm.DB, store *subtitle.Store, aiH *AIHandler,
	ffmpegPath, dir, cacheDir string) *AISubtitleService {

	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(os.TempDir(), "ls-ai-subtitle")
	}
	_ = os.MkdirAll(dir, 0o755)
	return &AISubtitleService{
		db:       db,
		store:    store,
		ai:       aiH,
		ffmpeg:   strings.TrimSpace(ffmpegPath),
		probe:    ffprobeOf(ffmpegPath),
		dir:      dir,
		cacheDir: cacheDir,
	}
}

// Start 起后台心跳。
//
// 每分钟看一眼：到点了（且开关开着）就消费一批。用"每分钟判断"而不是
// 精确定时器，是为了让「改计划」立刻生效 —— 重启一次服务才能生效的配置，
// 用户会以为没做。
func (s *AISubtitleService) Start() {
	go func() {
		// 启动先等 90 秒：此刻在跑数据库迁移、缓存预热，别去抢
		time.Sleep(90 * time.Second)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			s.Tick(context.Background())
		}
	}()
}

// Tick 一分钟一次的心跳。
func (s *AISubtitleService) Tick(ctx context.Context) {
	plan, err := s.LoadPlan()
	if err != nil {
		s.setStat(AISubtitleStat{At: time.Now(), State: "error", LastNote: "读计划失败：" + err.Error()})
		return
	}
	if !plan.Enabled {
		s.setStat(AISubtitleStat{At: time.Now(), State: "disabled", LastNote: "自动生成未启用"})
		return
	}
	if !InWindow(time.Now(), plan.StartMinute, plan.EndMinute) {
		s.setStat(AISubtitleStat{
			At:       time.Now(),
			State:    "outside",
			LastNote: fmt.Sprintf("不在时段（%s - %s）", MinuteText(plan.StartMinute), MinuteText(plan.EndMinute)),
		})
		return
	}
	s.RunBatch(ctx, plan, false)
}

// InWindow 判断某个时刻是否落在计划时段内。
//
// 跨零点必须支持（22:00 → 06:00）：睡觉时间生成是这个功能的主要用法。
// start == end 视为空窗口而不是"全天" —— 全天会让"我只想夜里跑"的意图
// 悄悄变成白天也在调用模型、扣额度。
func InWindow(t time.Time, startMinute, endMinute int) bool {
	start := normMinute(startMinute)
	end := normMinute(endMinute)
	if start == end {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

func normMinute(m int) int {
	m = m % (24 * 60)
	if m < 0 {
		m += 24 * 60
	}
	return m
}

// MinuteText 分钟数 → "22:00"。
func MinuteText(m int) string {
	m = normMinute(m)
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// LoadPlan 读计划（没有就返回默认值，不落库）。
func (s *AISubtitleService) LoadPlan() (*model.AISubtitlePlan, error) {
	if s == nil || s.db == nil {
		return nil, gorm.ErrInvalidDB
	}
	var plan model.AISubtitlePlan
	err := s.db.First(&plan, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultPlan(), nil
	}
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func defaultPlan() *model.AISubtitlePlan {
	return &model.AISubtitlePlan{
		ID:            1,
		Enabled:       false,
		StartMinute:   model.AISubtitleDefaultStartMinute,
		EndMinute:     model.AISubtitleDefaultEndMinute,
		BatchPerRound: model.AISubtitleDefaultBatch,
		GapSeconds:    model.AISubtitleDefaultGapSeconds,
	}
}

// SavePlan 写入计划（单行 upsert）。
func (s *AISubtitleService) SavePlan(p *model.AISubtitlePlan) error {
	if s == nil || s.db == nil {
		return gorm.ErrInvalidDB
	}
	p.ID = 1
	p.StartMinute = normMinute(p.StartMinute)
	p.EndMinute = normMinute(p.EndMinute)
	if p.BatchPerRound <= 0 || p.BatchPerRound > 50 {
		p.BatchPerRound = model.AISubtitleDefaultBatch
	}
	if p.GapSeconds < 0 || p.GapSeconds > 600 {
		p.GapSeconds = model.AISubtitleDefaultGapSeconds
	}
	p.Note = truncate(p.Note, 400)
	return s.db.Save(p).Error
}

func (s *AISubtitleService) setStat(st AISubtitleStat) {
	s.statMu.Lock()
	s.stat = st
	s.statMu.Unlock()
}

// Stat 最近一次状态。**不阻塞**：一轮要跑好几分钟，
// 拿锁读会把管理页卡到超时（采集器那边踩过一模一样的坑）。
func (s *AISubtitleService) Stat() AISubtitleStat {
	s.statMu.RLock()
	defer s.statMu.RUnlock()
	return s.stat
}

// ============================================================
//  队列与执行
// ============================================================

// EnqueueResult 入队结果。
type EnqueueResult struct {
	Added   int `json:"added"`
	Reset   int `json:"reset"`
	Skipped int `json:"skipped"`
}

// Enqueue 把选中的资源排进队列。
//
// 已存在的任务：
//   - done / skipped / failed → 重置成 pending（用户又点了一次，就再跑一次）；
//   - pending / running → 不动（避免把正在跑的那条打回队列）。
func (s *AISubtitleService) Enqueue(resourceIDs []uint64) (EnqueueResult, error) {
	var res EnqueueResult
	if s == nil || s.db == nil {
		return res, gorm.ErrInvalidDB
	}
	if len(resourceIDs) == 0 {
		return res, fmt.Errorf("没有选择任何文件")
	}
	// 一次别排太多：整晚的额度是一点点用完的，误点"全选"不该直接烧掉几百次调用。
	const maxEnqueue = 500
	if len(resourceIDs) > maxEnqueue {
		resourceIDs = resourceIDs[:maxEnqueue]
	}
	for _, id := range resourceIDs {
		if id == 0 {
			continue
		}
		var r model.Resource
		if err := s.db.First(&r, id).Error; err != nil {
			res.Skipped++
			continue
		}
		if !isAudioVideo(&r) {
			res.Skipped++
			continue
		}
		var task model.AISubtitleTask
		err := s.db.Where("resource_id = ?", id).First(&task).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			t := &model.AISubtitleTask{
				ResourceID: id,
				Title:      r.Title,
				Status:     model.AISubtitleStatusPending,
			}
			if err := s.db.Create(t).Error; err != nil {
				res.Skipped++
				continue
			}
			res.Added++
		case err != nil:
			res.Skipped++
		case task.Status == model.AISubtitleStatusPending || task.Status == model.AISubtitleStatusRunning:
			res.Skipped++
		default:
			updates := map[string]interface{}{
				"status":      model.AISubtitleStatusPending,
				"note":        "已重新排队",
				"title":       r.Title,
				"finished_at": nil,
			}
			if err := s.db.Model(&model.AISubtitleTask{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
				res.Skipped++
				continue
			}
			res.Reset++
		}
	}
	return res, nil
}

func isAudioVideo(r *model.Resource) bool {
	if r == nil {
		return false
	}
	if r.MediaType == model.MediaTypeAudio || r.MediaType == model.MediaTypeVideo {
		return true
	}
	// 存量行的 media_type 可能是空的（后补列），按扩展名再判一次。
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Ext), ".")) {
	case "mp3", "m4a", "aac", "wav", "flac", "ape", "wma", "ogg", "opus", "amr",
		"mp4", "mkv", "avi", "mov", "webm", "flv", "ts", "rmvb", "wmv", "m4v", "mpg", "mpeg":
		return true
	}
	return false
}

// RunBatch 消费一批任务。force=true 时忽略"在不在时段"（管理页的「立即执行」）。
func (s *AISubtitleService) RunBatch(ctx context.Context, plan *model.AISubtitlePlan, force bool) AISubtitleStat {
	if s == nil || s.db == nil || s.store == nil {
		return AISubtitleStat{At: time.Now(), State: "error", LastNote: "服务未初始化"}
	}
	if plan == nil {
		var err error
		plan, err = s.LoadPlan()
		if err != nil {
			return AISubtitleStat{At: time.Now(), State: "error", LastNote: "读计划失败：" + err.Error()}
		}
	}
	if !force && !InWindow(time.Now(), plan.StartMinute, plan.EndMinute) {
		return s.Stat()
	}
	// 同一时刻只跑一批：手动「立即执行」撞上定时任务会变成双倍调用
	if !s.runMu.TryLock() {
		st := s.Stat()
		st.LastNote = "上一批还在跑，稍等"
		return st
	}
	defer s.runMu.Unlock()

	// 上次进程被杀留下的 running 行会永远卡住，超过 30 分钟就算过期
	s.db.Model(&model.AISubtitleTask{}).
		Where("status = ? AND updated_at < ?", model.AISubtitleStatusRunning, time.Now().Add(-30*time.Minute)).
		Update("status", model.AISubtitleStatusPending)

	batch := plan.BatchPerRound
	if batch <= 0 {
		batch = model.AISubtitleDefaultBatch
	}
	var tasks []model.AISubtitleTask
	if err := s.db.Where("status = ?", model.AISubtitleStatusPending).
		Order("id asc").Limit(batch).Find(&tasks).Error; err != nil {
		return AISubtitleStat{At: time.Now(), State: "error", LastNote: "查队列失败：" + err.Error()}
	}

	st := AISubtitleStat{At: time.Now(), State: "running"}
	if len(tasks) == 0 {
		st.State = "idle"
		st.LastNote = "队列是空的"
		s.setStat(st)
		return st
	}
	st.LastNote = fmt.Sprintf("开始生成 %d 条", len(tasks))
	s.setStat(st)

	lastErr := ""
	for i := range tasks {
		if ctx.Err() != nil {
			st.LastNote = "已取消，" + st.LastNote
			break
		}
		task := &tasks[i]
		st.Scanned++
		note, status := s.runTask(ctx, task, plan)
		switch status {
		case model.AISubtitleStatusDone:
			st.Done++
		case model.AISubtitleStatusSkipped:
			st.Skipped++
		default:
			st.Failed++
			lastErr = note
		}
		// 每条之后刷一次：一轮几分钟，只在末尾写一次的话
		// 页面永远显示上一轮的旧数据，分不清"在跑"和"卡死"。
		st.LastNote = fmt.Sprintf("进行中 %d/%d（完成 %d，跳过 %d，失败 %d）",
			st.Scanned, len(tasks), st.Done, st.Skipped, st.Failed)
		if lastErr != "" {
			st.LastNote += "；最新失败：" + lastErr
		}
		s.setStat(st)

		if i < len(tasks)-1 && plan.GapSeconds > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(plan.GapSeconds) * time.Second):
			}
		}
	}
	now := time.Now()
	st.At = now
	st.LastRunAt = &now
	if ctx.Err() == nil {
		st.State = "idle"
	}
	st.LastNote = fmt.Sprintf("本轮 %d 条：完成 %d，跳过 %d，失败 %d",
		st.Scanned, st.Done, st.Skipped, st.Failed)
	if lastErr != "" {
		st.LastNote += "；最新失败：" + lastErr
	}
	s.setStat(st)
	log.Printf("[ai-subtitle] %s", st.LastNote)
	return st
}

// skipError 表示"这条不该重试"：模型明确说不知道 / 已有更好的字幕。
type skipError struct{ reason string }

func (e skipError) Error() string { return e.reason }

// runTask 跑一条任务，返回 (说明, 终态)。
func (s *AISubtitleService) runTask(ctx context.Context, task *model.AISubtitleTask,
	plan *model.AISubtitlePlan) (string, string) {

	s.db.Model(&model.AISubtitleTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status":   model.AISubtitleStatusRunning,
		"attempts": task.Attempts + 1,
	})

	finish := func(status, note string, cues int, align, modelName string) (string, string) {
		now := time.Now()
		s.db.Model(&model.AISubtitleTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"status":      status,
			"note":        truncate(note, 480),
			"cues":        cues,
			"align":       align,
			"model":       modelName,
			"finished_at": now,
		})
		return note, status
	}

	var res model.Resource
	if err := s.db.First(&res, task.ResourceID).Error; err != nil {
		return finish(model.AISubtitleStatusFailed, "资源不存在（可能已被删除）", 0, "", "")
	}
	if strings.TrimSpace(task.Title) == "" {
		s.db.Model(&model.AISubtitleTask{}).Where("id = ?", task.ID).Update("title", res.Title)
	}

	// 已有字幕就别动：默认策略下"sibling/embedded/online"都比模型复原的可信。
	//
	// ⚠️ 人工指定的那条**任何情况下都不覆盖**（Store.Save 也是这么定的），
	// 所以这里必须先判 Manual：不判的话会生成、写文件、标记 done，
	// 但库里纹丝不动 —— 页面说"已完成"，播出来还是旧字幕，最难查的一类不一致。
	if s.store != nil {
		if old, err := s.store.Get(res.ID); err == nil && old != nil {
			if old.Manual {
				return finish(model.AISubtitleStatusSkipped, "人工指定的字幕，不覆盖", 0, "", "")
			}
			if !plan.Overwrite && old.Origin != "ai" {
				return finish(model.AISubtitleStatusSkipped,
					"已有字幕（"+orDefaultStr(old.Source, old.Origin)+"），未开启覆盖", 0, "", "")
			}
		}
	}

	gen, err := s.generate(ctx, &res, plan)
	if err != nil {
		var skip skipError
		if errors.As(err, &skip) {
			return finish(model.AISubtitleStatusSkipped, skip.reason, 0, "", "")
		}
		return finish(model.AISubtitleStatusFailed, err.Error(), 0, "", "")
	}

	// 写永久存储：播放链路只认这张表，写不进去等于白干。
	if err := s.store.Save(&model.ResourceSubtitle{
		ResourceID: res.ID,
		Lang:       gen.lang,
		Source:     "AI 生成 · " + gen.model + " · " + alignText(gen.align),
		Origin:     "ai",
		// 置信度刻意给低：这条是"模型按标题复原的文本"，不是原片字幕。
		// 低于 subtitle.MinConfidence(60)，后续如果有自动校验/人工校对，
		// 它会优先被当成"待确认"的那一类。
		Confidence: 45,
		Cues:       len(gen.cues),
		Size:       len(gen.vtt),
		VTT:        gen.vtt,
	}); err != nil {
		return finish(model.AISubtitleStatusFailed, "入库失败："+err.Error(), 0, "", "")
	}

	s.writeFiles(res.ID, res.Title, gen)
	s.clearPlaybackCache(res.ID)

	note := fmt.Sprintf("%d 条 · %s · %s", len(gen.cues), gen.model, alignText(gen.align))
	return finish(model.AISubtitleStatusDone, note, len(gen.cues), gen.align, gen.model)
}

func alignText(align string) string {
	switch align {
	case "speech":
		return "按停顿对齐"
	case "even":
		return "按字数均分"
	}
	return orDefaultStr(align, "未知对齐")
}

type genResult struct {
	vtt   string
	lang  string
	cues  []subtitle.Cue
	align string
	model string
	dur   int
}

// generate 调模型 + 算时间轴，返回可入库的 VTT。**不落库**（预览也走这里）。
func (s *AISubtitleService) generate(ctx context.Context, res *model.Resource,
	plan *model.AISubtitlePlan) (*genResult, error) {

	cli, err := s.client()
	if err != nil {
		return nil, err
	}
	// 一本绘本的原文 JSON 可能上千字，慢模型 60 秒（配置默认）经常不够。
	// 线上就撞过 deadline exceeded —— 单条失败，重试又要再烧一次额度。
	cli = cli.WithTimeout(180 * time.Second)

	duration := float64(res.Duration)
	// 音频地址：本地硬盘给 file://，网盘给带 headers 的直链。
	// 没有直链也能工作 —— 只是时间轴只能按字数估。
	speechInput, headers := func() (string, map[string]string) {
		enc := s.encryptor()
		if enc == nil {
			return "", nil
		}
		link, lerr := resolveDirectLinkFor(s.db, enc, res)
		if lerr != nil || link == nil || link.URL == "" {
			return "", nil
		}
		if strings.HasPrefix(link.URL, "file://") {
			return fileURLToPath(link.URL), link.Headers
		}
		return link.URL, link.Headers
	}()
	if duration <= 0 && speechInput != "" {
		duration = subtitle.ProbeDuration(ctx, s.probe, speechInput, headers)
		// 顺手把时长写回资源：这个字段以前一直是 0，播放页也因此没有总时长
		if duration > 0 {
			s.db.Model(&model.Resource{}).Where("id = ?", res.ID).Update("duration", int(duration+0.5))
			res.Duration = int(duration + 0.5)
		}
	}

	// 目录名是重要的匹配要素：分级读物的套系/级别只写在目录上，
	// 标题往往只有一册的名字。所以分类路径和远端目录都带上。
	extra := ""
	if plan != nil {
		extra = plan.Note
	}
	system, user := subtitle.BuildScriptPrompt(subtitle.ScriptContext{
		Title:       res.Title,
		Category:    s.categoryPath(res.CategoryID),
		RemoteDir:   remoteDirHint(res.OwnerDirID),
		Ext:         service.ResourceExt(res),
		DurationSec: res.Duration,
		Extra:       extra,
	})
	raw, err := cli.Chat(ctx, []ai.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
	if err != nil {
		return nil, fmt.Errorf("大模型调用失败：%w", err)
	}
	script, err := subtitle.ParseScript(raw)
	if err != nil {
		return nil, fmt.Errorf("解析模型返回失败：%w", err)
	}
	lines := subtitle.PrepareLines(script.Lines)
	if len(lines) == 0 {
		reason := strings.TrimSpace(script.Reason)
		if reason == "" {
			reason = "模型表示不确定这份资料的内容"
		}
		return nil, skipError{reason: "模型没有给内容：" + reason}
	}

	// 句子数量级明显不对（4 分钟的绘本只回 1~2 句）时，宁可跳过：
	// 那种字幕只在开头闪一下，家长会以为"字幕功能坏了"，还会写进库里。
	if subtitle.ScriptLooksIncomplete(lines, duration) {
		return nil, skipError{reason: fmt.Sprintf(
			"模型只给出 %d 句，与 %.0f 秒的时长明显不成比例（可能只记得开头），已跳过",
			len(lines), duration)}
	}

	// 有音频就读一遍停顿位置；读不到就退回按字数均分（AlignCues 内部处理）。
	var spans []subtitle.Span
	if speechInput != "" {
		spans = subtitle.DetectSpeech(ctx, s.ffmpeg, speechInput, headers, duration)
	}
	cues := subtitle.AlignCues(lines, duration, spans)
	if len(cues) == 0 {
		return nil, fmt.Errorf("生成时间轴失败")
	}
	// 页面上的"按停顿对齐 / 按字数均分"要跟实际行为一致（同一套判定）。
	align := subtitle.AlignmentMode(lines, duration, spans)
	durInt := int(duration + 0.5)
	return &genResult{
		vtt:   subtitle.BuildVTT(cues),
		lang:  orDefaultStr(strings.TrimSpace(script.Lang), "en"),
		cues:  cues,
		align: align,
		model: cli.Model(),
		dur:   durInt,
	}, nil
}

// categoryPath 把分类树拼成「英语 / 分级阅读 / 大猫英语三级」。
//
// 分类的 Path 是物化路径（/1/12/35/），一次 IN 查询就能把整条链取回来。
// 只给叶子名是不够的：叶子往往叫「三级」，看不出是哪套书。
func (s *AISubtitleService) categoryPath(catID uint64) string {
	if catID == 0 {
		return ""
	}
	var cat model.Category
	if err := s.db.Select("id", "name", "path").First(&cat, catID).Error; err != nil {
		return ""
	}
	ids := parsePathIDs(cat.Path)
	if len(ids) == 0 {
		return cat.Name
	}
	var rows []model.Category
	s.db.Select("id", "name").Where("id IN ?", ids).Find(&rows)
	names := make(map[uint64]string, len(rows))
	for _, r := range rows {
		names[r.ID] = r.Name
	}
	out := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if n := strings.TrimSpace(names[id]); n != "" {
			out = append(out, n)
		}
	}
	// Path 通常不含自己（建路径时 id 还没生成），兜底补上叶子名
	if len(out) == 0 || out[len(out)-1] != cat.Name {
		out = append(out, cat.Name)
	}
	return strings.Join(out, " / ")
}

// parsePathIDs 解析物化路径里的 id 序列。
func parsePathIDs(path string) []uint64 {
	var ids []uint64
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		v, err := strconv.ParseUint(seg, 10, 64)
		if err != nil || v == 0 {
			continue
		}
		ids = append(ids, v)
	}
	return ids
}

// remoteDirHint 从远端目录信息里取出"人能看懂的那部分"。
//
// 同一列在不同存储源里含义不同：
//   - WebDAV / 本地硬盘：相对路径（/大猫英语/三级），是有用的匹配要素；
//   - 网盘：一串短 ID，对模型毫无意义，给出去只会干扰判断。
//
// 所以只在它看起来像路径（含 `/` 且不是纯 ID）时才用，并且只取最后两层 ——
// 再上层通常是挂载点或账号目录。
func remoteDirHint(dir string) string {
	s := strings.TrimSpace(dir)
	if s == "" || !strings.Contains(s, "/") {
		return ""
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// 纯 ID 段（长十六进制）不要：那是网盘目录 ID，不是名字
		if p == "" || looksLikeDirID(p) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, " / ")
}

// looksLikeDirID 判断一段是不是"ID 而不是名字"。
//
// 条件故意收得紧：长度 >= 16 且全是十六进制字符。中文目录名、带空格的
// 英文目录名都不可能满足，所以不会把正常目录误判掉。
func looksLikeDirID(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func (s *AISubtitleService) client() (*ai.Client, error) {
	if s.ai == nil {
		return nil, fmt.Errorf("AI 服务未初始化")
	}
	cli, _, err := s.ai.DefaultClient()
	if err != nil || cli == nil {
		return nil, fmt.Errorf("没有可用的 AI 配置，请先在「AI 接口配置」里添加（并设为默认）")
	}
	return cli, nil
}

func (s *AISubtitleService) encryptor() *util.Encryptor {
	if s.ai == nil {
		return nil
	}
	return s.ai.encryptor
}

// writeFiles 把产物落到磁盘。
//
// 用户要的「存在文件夹」就是这里：`.vtt` 给播放器/其它工具，`.txt` 是
// 逐行的中英对照，可以直接打印或贴到别处。数据库里那份才是权威，
// 这里的文件丢了不影响播放（下次生成会重写）。
func (s *AISubtitleService) writeFiles(id uint64, title string, gen *genResult) {
	if gen == nil {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		log.Printf("[ai-subtitle] 建目录失败：%v", err)
		return
	}
	base := strconv.FormatUint(id, 10)
	writeAtomic(filepath.Join(s.dir, base+".vtt"), gen.vtt)

	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(orDefaultStr(title, base))
	b.WriteString("\n# 由大模型生成（")
	b.WriteString(gen.model)
	b.WriteString(" · ")
	b.WriteString(alignText(gen.align))
	b.WriteString("），仅供学习参考\n\n")
	for _, c := range gen.cues {
		b.WriteString(c.Text)
		if strings.TrimSpace(c.Trans) != "" {
			b.WriteString("\n")
			b.WriteString(c.Trans)
		}
		b.WriteString("\n\n")
	}
	writeAtomic(filepath.Join(s.dir, base+".txt"), b.String())
}

func writeAtomic(path, content string) {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// clearPlaybackCache 清掉播放链路的旧缓存。
//
// Serve 的顺序是「缓存文件 → 数据库 → 负缓存」，所以生成新字幕后
// 必须把 <id>.vtt（旧的同目录/联网字幕）和 <id>.none（上次判定"没有"）
// 一起删掉，否则孩子端还在用旧的，页面上看起来"生成了没生效"。
func (s *AISubtitleService) clearPlaybackCache(id uint64) {
	if strings.TrimSpace(s.cacheDir) == "" {
		return
	}
	base := strconv.FormatUint(id, 10)
	for _, ext := range []string{".vtt", ".source", ".none"} {
		_ = os.Remove(filepath.Join(s.cacheDir, base+ext))
	}
}

// ============================================================
//  管理接口
// ============================================================

// AdminPlan GET /api/admin/ai/subtitles/plan
//
// 一次把页面要的东西全给齐：计划、当前状态、各状态的任务数、有没有 AI 配置。
// 分成四个接口会让页面首屏发四次请求，而且它们本来就要一起看。
func (s *AISubtitleService) AdminPlan(c *gin.Context) {
	plan, err := s.LoadPlan()
	if err != nil {
		util.ServerError(c, "读取计划失败")
		return
	}
	counts := map[string]int64{}
	for _, st := range []string{
		model.AISubtitleStatusPending, model.AISubtitleStatusRunning,
		model.AISubtitleStatusDone, model.AISubtitleStatusFailed, model.AISubtitleStatusSkipped,
	} {
		var n int64
		s.db.Model(&model.AISubtitleTask{}).Where("status = ?", st).Count(&n)
		counts[st] = n
	}
	_, _, aiErr := func() (*ai.Client, uint64, error) {
		if s.ai == nil {
			return nil, 0, fmt.Errorf("未初始化")
		}
		return s.ai.DefaultClient()
	}()

	util.OK(c, gin.H{
		"plan":         plan,
		"stat":         s.Stat(),
		"counts":       counts,
		"inWindow":     InWindow(time.Now(), plan.StartMinute, plan.EndMinute),
		"aiConfigured": aiErr == nil,
		"startText":    MinuteText(plan.StartMinute),
		"endText":      MinuteText(plan.EndMinute),
	})
}

type savePlanRequest struct {
	Enabled       bool   `json:"enabled"`
	StartMinute   int    `json:"startMinute"`
	EndMinute     int    `json:"endMinute"`
	BatchPerRound int    `json:"batchPerRound"`
	GapSeconds    int    `json:"gapSeconds"`
	Overwrite     bool   `json:"overwrite"`
	Note          string `json:"note"`
}

// AdminSavePlan PUT /api/admin/ai/subtitles/plan
func (s *AISubtitleService) AdminSavePlan(c *gin.Context) {
	var req savePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}
	if normMinute(req.StartMinute) == normMinute(req.EndMinute) {
		util.BadRequest(c, "开始和结束时间不能相同（等于没有时段）")
		return
	}
	p := &model.AISubtitlePlan{
		ID:            1,
		Enabled:       req.Enabled,
		StartMinute:   req.StartMinute,
		EndMinute:     req.EndMinute,
		BatchPerRound: req.BatchPerRound,
		GapSeconds:    req.GapSeconds,
		Overwrite:     req.Overwrite,
		Note:          req.Note,
	}
	if err := s.SavePlan(p); err != nil {
		util.ServerError(c, "保存失败")
		return
	}
	util.OKWithMessage(c, "已保存", gin.H{
		"startText": MinuteText(p.StartMinute),
		"endText":   MinuteText(p.EndMinute),
	})
}

// AdminCandidates GET /api/admin/ai/subtitles/candidates
//
// 勾选文件用的列表。只列音视频，带上"有没有字幕 / 在队列里是什么状态"，
// 否则管理员得在资源列表和这个页面之间来回切。
func (s *AISubtitleService) AdminCandidates(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	if pageSize > 100 {
		pageSize = 100
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	onlyMissing := c.Query("onlyMissing") == "true"
	categoryID := util.GetUintQuery(c, "categoryId", 0)

	q := s.db.Model(&model.Resource{}).
		Where("COALESCE(media_type,'') IN ?", []string{model.MediaTypeVideo, model.MediaTypeAudio})
	if keyword != "" {
		q = q.Where("title "+likeOp(s.db)+" ?", "%"+keyword+"%")
	}
	if categoryID > 0 {
		q = q.Where("category_id = ?", categoryID)
	}
	if onlyMissing {
		q = q.Where("NOT EXISTS (SELECT 1 FROM resource_subtitles rs WHERE rs.resource_id = resources.id AND rs.vtt <> '')")
	}
	var total int64
	q.Count(&total)

	var rows []model.Resource
	if err := q.Preload("Category").Order("id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}

	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	subs := map[uint64]model.ResourceSubtitle{}
	tasks := map[uint64]model.AISubtitleTask{}
	if len(ids) > 0 {
		var subRows []model.ResourceSubtitle
		s.db.Where("resource_id IN ?", ids).Find(&subRows)
		for _, r := range subRows {
			subs[r.ResourceID] = r
		}
		var taskRows []model.AISubtitleTask
		s.db.Where("resource_id IN ?", ids).Find(&taskRows)
		for _, t := range taskRows {
			tasks[t.ResourceID] = t
		}
	}

	list := make([]gin.H, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		item := gin.H{
			"id":         r.ID,
			"title":      r.Title,
			"mediaType":  r.MediaType,
			"ext":        r.Ext,
			"duration":   r.Duration,
			"categoryId": r.CategoryID,
		}
		if r.Category != nil {
			item["categoryName"] = r.Category.Name
		}
		if sub, ok := subs[r.ID]; ok {
			item["hasSubtitle"] = true
			item["subtitleSource"] = sub.Source
			item["subtitleOrigin"] = sub.Origin
		} else {
			item["hasSubtitle"] = false
		}
		if t, ok := tasks[r.ID]; ok {
			item["taskStatus"] = t.Status
			if t.Note != "" {
				item["taskNote"] = t.Note
			}
		}
		list = append(list, item)
	}
	util.Page(c, total, page, pageSize, list)
}

type enqueueRequest struct {
	ResourceIDs []uint64 `json:"resourceIds"`
}

// AdminEnqueue POST /api/admin/ai/subtitles/tasks
func (s *AISubtitleService) AdminEnqueue(c *gin.Context) {
	var req enqueueRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.ResourceIDs) == 0 {
		util.BadRequest(c, "请选择要生成字幕的文件")
		return
	}
	res, err := s.Enqueue(req.ResourceIDs)
	if err != nil {
		util.ServerError(c, "入队失败："+err.Error())
		return
	}
	util.OKWithMessage(c, fmt.Sprintf("已排队 %d 条（重置 %d，跳过 %d）", res.Added, res.Reset, res.Skipped), res)
}

// AdminTasks GET /api/admin/ai/subtitles/tasks
func (s *AISubtitleService) AdminTasks(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	if pageSize > 100 {
		pageSize = 100
	}
	status := strings.TrimSpace(c.Query("status"))
	q := s.db.Model(&model.AISubtitleTask{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Count(&total)
	var rows []model.AISubtitleTask
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.Page(c, total, page, pageSize, rows)
}

// AdminRetry POST /api/admin/ai/subtitles/tasks/:id/retry
func (s *AISubtitleService) AdminRetry(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	var task model.AISubtitleTask
	if err := s.db.First(&task, id).Error; err != nil {
		util.NotFound(c, "任务不存在")
		return
	}
	if task.Status == model.AISubtitleStatusRunning {
		util.BadRequest(c, "这条正在生成，稍等")
		return
	}
	s.db.Model(&model.AISubtitleTask{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status": model.AISubtitleStatusPending,
		"note":   "手动重试",
	})
	util.OKWithMessage(c, "已重新排队", nil)
}

// AdminDeleteTask DELETE /api/admin/ai/subtitles/tasks/:id
//
// 只删任务，**不删已生成的字幕**：任务表是"这次排队的记录"，
// 字幕是已经交付给孩子的内容，两者生命周期不同。
func (s *AISubtitleService) AdminDeleteTask(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的任务 ID")
		return
	}
	if err := s.db.Unscoped().Delete(&model.AISubtitleTask{}, id).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "已移除", nil)
}

type clearTasksRequest struct {
	Status string `json:"status"`
}

// AdminClearTasks POST /api/admin/ai/subtitles/tasks/clear
//
// 清理已完成/跳过的记录。只允许清这两种终态：pending/running 被误清
// 等于把排好的队弄丢了，而 failed 留着还能一键重试。
func (s *AISubtitleService) AdminClearTasks(c *gin.Context) {
	var req clearTasksRequest
	_ = c.ShouldBindJSON(&req)
	if req.Status != model.AISubtitleStatusDone && req.Status != model.AISubtitleStatusSkipped {
		util.BadRequest(c, "只能清理「已完成」或「已跳过」的记录")
		return
	}
	res := s.db.Unscoped().Where("status = ?", req.Status).Delete(&model.AISubtitleTask{})
	util.OKWithMessage(c, fmt.Sprintf("已清理 %d 条", res.RowsAffected), gin.H{"removed": res.RowsAffected})
}

// AdminRun POST /api/admin/ai/subtitles/run
//
// 立即跑一批（不受时段限制）。异步：一批可能要几分钟，
// 同步等会让管理页转圈到超时，前端还以为失败了。
func (s *AISubtitleService) AdminRun(c *gin.Context) {
	plan, err := s.LoadPlan()
	if err != nil {
		util.ServerError(c, "读取计划失败")
		return
	}
	if !s.runMu.TryLock() {
		util.OK(c, gin.H{"started": false, "message": "上一批还在跑"})
		return
	}
	// TryLock 只是为了立刻给前端一个准确答复；真正的互斥在 RunBatch 里。
	s.runMu.Unlock()
	go s.RunBatch(context.Background(), plan, true)
	util.OKWithMessage(c, "已开始生成，进度看下方状态", gin.H{"started": true})
}

type previewRequest struct {
	ResourceID uint64 `json:"resourceId"`
}

// AdminPreview POST /api/admin/ai/subtitles/preview
//
// 只生成、不入库、不落盘。管理员可以先看一眼模型给的内容对不对 ——
// 这一步是必要的信任门槛：模型偶尔会"编得很像"，看过才敢批量跑。
func (s *AISubtitleService) AdminPreview(c *gin.Context) {
	var req previewRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ResourceID == 0 {
		util.BadRequest(c, "请选择要预览的文件")
		return
	}
	var res model.Resource
	if err := s.db.First(&res, req.ResourceID).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	plan, err := s.LoadPlan()
	if err != nil {
		util.ServerError(c, "读取计划失败")
		return
	}

	// 预览可能跑一次完整模型调用 + 一次音频探测，给足超时。
	ctx, cancel := context.WithTimeout(c.Request.Context(), 240*time.Second)
	defer cancel()

	gen, err := s.generate(ctx, &res, plan)
	if err != nil {
		var skip skipError
		if errors.As(err, &skip) {
			util.OK(c, gin.H{
				"ok":      false,
				"skipped": true,
				"message": skip.reason,
			})
			return
		}
		util.ServerError(c, err.Error())
		return
	}

	// 只回前 40 条：一眼能判断内容对不对，也不会把响应撑到几 MB。
	limit := 40
	cues := gen.cues
	truncated := len(cues) > limit
	if truncated {
		cues = cues[:limit]
	}
	list := make([]gin.H, 0, len(cues))
	for _, c := range cues {
		list = append(list, gin.H{
			"start": c.Start,
			"end":   c.End,
			"text":  c.Text,
			"trans": c.Trans,
		})
	}
	util.OK(c, gin.H{
		"ok":        true,
		"lang":      gen.lang,
		"model":     gen.model,
		"align":     gen.align,
		"alignText": alignText(gen.align),
		"duration":  gen.dur,
		"cues":      len(gen.cues),
		"lines":     list,
		"truncated": truncated,
	})
}

// AdminFile GET /api/admin/ai/subtitles/file/:id
//
// 直接看某个资源生成出来的那份 VTT（排查"字幕生成了但不对"时用）。
func (s *AISubtitleService) AdminFile(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	if s.store == nil {
		util.ServerError(c, "字幕存储不可用")
		return
	}
	row, err := s.store.Get(id)
	if err != nil || row == nil {
		util.NotFound(c, "这条资源还没有字幕")
		return
	}
	c.Header("Content-Type", "text/vtt; charset=utf-8")
	c.String(200, "%s", row.VTT)
}
