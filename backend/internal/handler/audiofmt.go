package handler

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/task"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// 转码输出参数。
//
// 2026-10-29 从 `128k / 立体声` 降到这里，起因是用户报「标准版打开大猫
// 里的音频，挂着"正在准备音频"要等十几秒」。
//
// 这十几秒由三段串起来：解析直链（网盘 API）、从网盘拉源文件（网络）、
// ffmpeg 编码（纯 CPU）。前两段只能靠预热绕开，第三段能直接砍：
// AAC 编码耗时近似正比于「码率 × 声道数」，64k 单声道只有原来 1/4 的
// 编码量 —— 对 J1900 这种没有 AVX 的老 CPU 差别很实在。
//
// 敢降到单声道，是因为这批"要转码的音频"全是**朗读**（大猫、分级读物、
// 故事），人声单声道听感没有损失；同样时长下产物体积也减半，
// 孩子在 4G 上拉第一个字节更快。真要放音乐类的，改这两个常量即可。
const (
	audioOutBitrate  = "64k"
	audioOutChannels = "1"
)

// 预热参数：一轮最多转多少条、产物目录超过多少 GB 就停手。
//
// 上限存在的意义是"别把硬盘吃满"——缓存写满是**静默**恶化，
// 出事的时候通常已经是几天后了。绘本音频一条也就一两兆，6GB 够装几千条。
const (
	audioWarmBatch = 30
	audioWarmMaxGB = 6.0
)

// AudioFormatHandler 把浏览器放不出来的音频（wma / ape / amr / ac3 / dts …）
// 转成单文件 AAC（.m4a）再交给 <audio>。
//
// 为什么不复用视频那套 HLS 转码：
//   - HLS 的价值是"边转边播 + 多档码率"，音频只有几十 MB，转完再播更简单；
//   - 部分 Android WebView 的 <audio> 不认 m3u8，认 AAC 是 100%。
//
// 产物按资源 ID 落盘缓存，第二次打开直接读文件。
type AudioFormatHandler struct {
	db     *gorm.DB
	enc    *util.Encryptor
	acc    *task.Accelerator
	ffmpeg string
	outDir string
	secret string
	locks  sync.Map
	// sem 是"用户正在等"的那两个槽位。预热用另一条 warmSem，
	// 既能并行，又能在有播放请求时立刻让路（见 WarmOnce）。
	sem     chan struct{}
	warmSem chan struct{}

	// 预热状态：只允许一轮在跑，失败两次的资源本轮之后不再重试
	// （坏资源重试只会白打一遍网盘 API，还占着 CPU）。
	warmMu   sync.Mutex
	warmFail map[uint64]int
	// warmFailAt 失败计数是何时开始攒的。
	// 攒满 6 小时就整体清零：这个计数的用途只是"别每 15 分钟对着同一条
	// 坏资源重试"，不是"永久拉黑" —— 网盘偶发限流/抖动造成的那两次失败
	// 如果记一辈子，那条音频就再也不会被预热了，孩子每次都还得等。
	warmFailAt time.Time
	statMu     sync.RWMutex
	lastWarm   WarmStat
}

// WarmStat 预热进度快照（日志 + 排障用）。
type WarmStat struct {
	At        time.Time
	Todo      int     // 扫到的"该转但还没缓存"的条数
	Cached    int     // 已经有产物的条数
	Converted int     // 本轮真转成功的
	Failed    int     // 本轮转失败的（网盘/ffmpeg 出错，要盯）
	Skipped   int     // 本轮跳过的（没有转存信息，取不到直链，转也白转）
	CacheGB   float64 // 产物目录当前占用
	Note      string
}

func (s WarmStat) String() string {
	return fmt.Sprintf("待转 %d 条、已有缓存 %d 条、本轮转好 %d 条、失败 %d 条、无转存跳过 %d 条、缓存占用 %.2fGB",
		s.Todo, s.Cached, s.Converted, s.Failed, s.Skipped, s.CacheGB)
}

func NewAudioFormatHandler(db *gorm.DB, enc *util.Encryptor, acc *task.Accelerator,
	ffmpegPath, outDir, jwtSecret string) *AudioFormatHandler {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(os.TempDir(), "ls-audiofmt")
	}
	_ = os.MkdirAll(outDir, 0o755)
	if acc == nil {
		acc = task.NewAccelerator(8)
	}
	return &AudioFormatHandler{
		db: db, enc: enc, acc: acc,
		ffmpeg: ffmpegPath, outDir: outDir, secret: jwtSecret,
		sem:        make(chan struct{}, 2),
		warmSem:    make(chan struct{}, 1),
		warmFail:   make(map[uint64]int),
		warmFailAt: time.Now(),
	}
}

// NeedsConvert 这条音频资源是不是"浏览器放不出来、必须转"。
//
// 判据只看**扩展名**，不看 media_type：
//
//   - media_type 是后补列，存量行可能还是导入时统一写死的 "video"，
//     而早年这条 `res.MediaType != audio` 的判断会让它们**永远**走不到转码，
//     直接拿直链塞进 <video> —— 浏览器解不了 wma，孩子看到的就是
//     "这个音频放不了"（2026-10-20 线上「大猫」目录下的 wma 就是这个形状）；
//   - 扩展名才是文件格式的权威来源，且 AudioNeedsTranscodeExt 里全是
//     音频专用格式，不存在"把视频误判成音频"的风险。
func (h *AudioFormatHandler) NeedsConvert(res *model.Resource) bool {
	if h == nil || res == nil {
		return false
	}
	switch res.MediaType {
	case model.MediaTypeEbook, model.MediaTypeImage, model.MediaTypePDF:
		// 电子书/图片有各自的转换链，别抢
		return false
	}
	return service.AudioNeedsTranscodeExt(service.ResourceExt(res))
}

// URLFor 带票据的地址（与 stream / ebook 同一套签名）。
func (h *AudioFormatHandler) URLFor(id uint64) string {
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/audio/%d?t=%s", id, SignStreamTicket(h.secret, id, exp))
}

// pathFor 这条资源的产物路径（平铺在 outDir 下，`{id}.m4a`）。
func (h *AudioFormatHandler) pathFor(id uint64) string {
	return filepath.Join(h.outDir, fmt.Sprintf("%d.m4a", id))
}

// Cached 产物是否已经在盘上（之前转过 / 预热过）。
//
// 页面用它决定**先走哪条路**（2026-10-29）：
//   · 有产物 → 直接 `<audio src=/api/audio/…>`，命中缓存是 0.5 秒，
//     而且完全不占 CPU（只是把一个早就转好的文件发出去）。
//   · 没有产物 → 才交给壳里的 libVLC 直连原文件，避免现场转码。
//
// 为什么要这么排：libVLC 这条路本身没问题，但它要从公网拉原文件，
// 而且失败时是**静默**的（不报错、不出声、连 TCP 都未必建），
// 页面只能靠看门狗兜底 —— 那是 12 秒的干等。既然盘上已经有现成的，
// 就没有理由让孩子去赌那条路。
func (h *AudioFormatHandler) Cached(id uint64) bool {
	if h == nil {
		return false
	}
	return fileExists(h.pathFor(id))
}

// Serve GET /api/audio/:id?t=<ticket>
func (h *AudioFormatHandler) Serve(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	if !verifyStreamTicket(h.secret, id, c.Query("t")) {
		util.Unauthorized(c, "播放链接已过期，请重新打开")
		return
	}
	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	// 明确请求转换的，只要不是"确定不需要转"就放行：
	// 扩展名未知的资源只能靠转换兜底，硬拒等于让它彻底播不了。
	if !h.NeedsConvert(&res) && service.ResourceExt(&res) != "" {
		util.BadRequest(c, "这个音频不需要转换")
		return
	}

	dst := h.pathFor(id)
	if !fileExists(dst) {
		if err := h.convert(c.Request.Context(), &res, dst); err != nil {
			log.Printf("[audiofmt] 转换失败 resource=%d: %v", id, err)
			util.ServerError(c, "这个音频暂时转不了，请稍后再试")
			return
		}
	}
	c.Header("Content-Type", "audio/mp4")
	c.Header("Cache-Control", "private, max-age=600")
	c.File(dst)
}

func (h *AudioFormatHandler) convert(ctx context.Context, res *model.Resource, dst string) error {
	return h.convertWith(ctx, res, dst, h.sem)
}

// convertWith sem 由调用方给：用户请求走 h.sem（2 槽），
// 后台预热走 h.warmSem（1 槽），这样预热既不会把播放请求堵死，
// 也不会和视频转码一起把 J1900 的 4 个核吃干净。
func (h *AudioFormatHandler) convertWith(ctx context.Context, res *model.Resource, dst string, sem chan struct{}) error {
	mu, _ := h.locks.LoadOrStore(res.ID, &sync.Mutex{})
	mtx := mu.(*sync.Mutex)
	mtx.Lock()
	defer mtx.Unlock()
	if fileExists(dst) {
		return nil
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("服务器正忙")
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	ext := service.ResourceExt(res)
	src := filepath.Join(h.outDir, fmt.Sprintf("%d.src.%s", res.ID, ext))
	defer os.Remove(src)

	// 两段分别计时：这段等待里"取源"和"编码"到底谁是瓶颈，日志说了算。
	// 之前只有总耗时，"等十几秒"就只能靠猜 —— 而这两段的优化手段完全不同
	// （取源靠预热/直读网盘，编码靠降码率），猜错等于白改。
	tFetch := time.Now()
	if err := h.fetchSource(ctx, res, src); err != nil {
		return err
	}
	fetchDur := time.Since(tFetch)

	tmp := dst + ".part"
	defer os.Remove(tmp)
	args := []string{
		"-y", "-nostdin", "-i", src,
		// 视频流必须丢掉：有些 .mka/.wma 其实封了封面图（mjpeg），
		// 不 -vn 的话 ffmpeg 会尝试把它当视频编码，白等一场。
		"-vn",
		"-c:a", "aac",
		"-b:a", audioOutBitrate,
		"-ac", audioOutChannels,
		// faststart：把 moov 放到文件头，边下边播不用等整首下完
		"-movflags", "+faststart",
		"-f", "mp4",
		"-loglevel", "error",
		tmp,
	}
	tEnc := time.Now()
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, truncateTail(string(out), 400))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	var size int64
	if st, e := os.Stat(dst); e == nil {
		size = st.Size()
	}
	log.Printf("[audiofmt] 资源 %d 转好：取源 %.1fs + 编码 %.1fs = %.1fs，产物 %.1fMB（%s/%s）",
		res.ID, fetchDur.Seconds(), time.Since(tEnc).Seconds(),
		fetchDur.Seconds()+time.Since(tEnc).Seconds(),
		float64(size)/1024/1024, audioOutBitrate, audioOutChannels)
	return nil
}

func (h *AudioFormatHandler) fetchSource(ctx context.Context, res *model.Resource, dst string) error {
	return downloadResourceSource(ctx, h.db, h.enc, h.acc, res, dst)
}

// ---------------------------------------------------------------------------
// 预热
//
// 为什么需要它：`/api/audio/:id` 是**同步**的——解析直链、从网盘拉源文件、
// ffmpeg 转码，全都在这一次请求里做完，孩子点下去就干等。
// 缓存命中之后是毫秒级，所以"第一次"永远慢，而"第一次"恰恰是唯一的体验。
//
// 所以把这件事挪到前面：启动后空闲时扫一遍库里所有"该转但没转"的音频，
// 一条一条转好落盘。孩子点开时就是缓存命中。
// ---------------------------------------------------------------------------

// StartWarmup 起后台预热循环。
//
// ⚠️ 先跑一次再进 ticker（和字幕采集器同一个坑）：只挂 ticker 的话第一次
// 要等一整个周期，部署完当天看不到任何效果，会让人以为没做。
func (h *AudioFormatHandler) StartWarmup(interval time.Duration) {
	if h == nil || h.db == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	go func() {
		// 启动瞬间要抢数据库迁移、缓存预热，别和它们挤
		time.Sleep(5 * time.Minute)
		h.WarmOnce(context.Background())
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			h.WarmOnce(context.Background())
		}
	}()
}

// LastWarm 上一轮预热的结果。**不阻塞**：跑着的时候返回中间进度。
func (h *AudioFormatHandler) LastWarm() WarmStat {
	if h == nil {
		return WarmStat{}
	}
	h.statMu.RLock()
	defer h.statMu.RUnlock()
	return h.lastWarm
}

func (h *AudioFormatHandler) setWarm(s WarmStat) {
	h.statMu.Lock()
	h.lastWarm = s
	h.statMu.Unlock()
}

// WarmOnce 扫一轮并把本轮额度内的音频转好。
func (h *AudioFormatHandler) WarmOnce(ctx context.Context) WarmStat {
	stat := WarmStat{At: time.Now()}
	if h == nil || h.db == nil {
		stat.Note = "预热器未初始化"
		return stat
	}
	// 同一时刻只允许一轮：手动触发和定时任务撞上会重复打网盘 API
	if !h.warmMu.TryLock() {
		return h.LastWarm()
	}
	defer h.warmMu.Unlock()
	// 兜底：结构体字面量造出来的 handler（单测、或将来别处直接用）
	// 没有这个 map，写进去就是 nil map panic。
	if h.warmFail == nil {
		h.warmFail = make(map[uint64]int)
		h.warmFailAt = time.Now()
	}
	if time.Since(h.warmFailAt) > 6*time.Hour {
		h.warmFail = make(map[uint64]int)
		h.warmFailAt = time.Now()
	}

	stat.CacheGB = dirSizeGB(h.outDir)
	if stat.CacheGB >= audioWarmMaxGB {
		stat.Note = fmt.Sprintf("产物已占 %.1fGB，达到上限 %.0fGB，本轮跳过", stat.CacheGB, audioWarmMaxGB)
		h.setWarm(stat)
		log.Printf("[audiofmt] 预热：%s", stat.Note)
		return stat
	}

	// 一轮给个总时限：候选里有失效资源时，每条都要等一次"取直链"失败
	// （最坏 20 秒），不封顶的话一轮能拖着跑几个小时，而下一轮还在排队。
	// 到点就停，剩下的下一轮接着来 —— 反正失败计数会让坏资源尽快退场。
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	// 起手先喊一声。不然一轮要跑七八分钟（30 条 × 十来秒），
	// 这段时间日志里只有转码成功行、没有任何"我在干活"的标记，
	// 排查时会以为预热压根没起来（2026-10-29 就这么怀疑过一次）。
	log.Printf("[audiofmt] 预热开始：产物占用 %.2fGB，开始扫描待转音频", stat.CacheGB)

	// 一次把候选捞出来：ext 命中转码表，或者"ext 空但 media_type 是音频"
	// （存量行 ext 可能是空，格式只能从标题/FileID 反推 → 交给 NeedsConvert 再筛一遍）。
	//
	// ⚠️ 括号不能省：这里只有一个 Where，但 SQL 里同时有 AND 和 OR，
	// 拆成两条 Where 时 gorm 是**直接 AND 拼**、不加括号，
	// 那个 OR 分支就会把"跳过 dead 资源"绕过去（去转一堆已失效的）。
	//
	// ⚠️⚠️ **不要在这里加 `Select(...)` 只查几列**。第一版就是这么写的，
	// 少选了 `disk_account_id`，于是 `DiskAccountID` 是 nil，
	// `resolveDirectLinkFor` 当场判"缺少转存信息" —— 整轮 93 条全部"失败"，
	// 而库里那条 SQL 明明写着 125 条都有账号。教训：这条链下游要的是**整个资源**，
	// 少一列就是全零值，且报错信息会指向完全错误的方向。
	var rows []model.Resource
	err := h.db.
		Where("COALESCE(status, '') <> ? AND (lower(COALESCE(ext, '')) IN ? OR (COALESCE(ext, '') = '' AND media_type = ?))",
			model.ResourceStatusDead, service.AudioTranscodeExts(), model.MediaTypeAudio).
		Order("id").
		Find(&rows).Error
	if err != nil {
		stat.Note = "查询失败：" + err.Error()
		h.setWarm(stat)
		log.Printf("[audiofmt] 预热：%s", stat.Note)
		return stat
	}

	for i := range rows {
		if ctx.Err() != nil {
			break
		}
		res := rows[i]
		if !h.NeedsConvert(&res) {
			continue
		}
		dst := h.pathFor(res.ID)
		if fileExists(dst) {
			stat.Cached++
			continue
		}
		stat.Todo++
		if stat.Converted >= audioWarmBatch {
			// 额度用完：后面的只统计、不再转，下一轮接着来
			continue
		}
		// 压根没有转存信息的资源（早年从搜索结果导入、从没转存过）根本取不到直链，
		// 这种不算"失败"：它连播放都走 external 兜底，预热对它没有意义。
		// 单独计数，免得每轮几十条"失败"把真正的故障淹掉。
		if res.DiskAccountID == nil || res.FileID == "" {
			stat.Skipped++
			continue
		}
		if h.warmFail[res.ID] >= 2 {
			continue
		}
		// 让路：h.sem 里有东西 = 有孩子正在等这条音频/这个视频，
		// 后台任务不该跟他抢那几个核。
		if len(h.sem) > 0 {
			stat.Note = "有播放请求在转码，本轮提前收工"
			break
		}
		if err := h.convertWith(ctx, &res, dst, h.warmSem); err != nil {
			stat.Failed++
			h.warmFail[res.ID]++
			log.Printf("[audiofmt] 预热失败 resource=%d: %v", res.ID, err)
			continue
		}
		stat.Converted++
		// 连续转码之间喘一口：J1900 长时间满载会热降频，
		// 也顺手把对网盘的请求摊开（一轮 30 条别撞成一串）。
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}

	h.setWarm(stat)
	log.Printf("[audiofmt] 预热：%s %s", stat, strings.TrimSpace(stat.Note))
	return stat
}

// dirSizeGB 产物目录当前占用（GB）。
//
// 只扫一层：产物是平铺的 `{id}.m4a`，没有子目录。
func dirSizeGB(dir string) float64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return float64(total) / 1024 / 1024 / 1024
}

func truncateTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
