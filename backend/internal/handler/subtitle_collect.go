package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/subtitle"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// SubtitleCollector 后台字幕采集器。
//
// 用户的原话是「字幕后台自动通过 AI 去搜索和匹配、存储」。这一版把它拆成
// 三步，每步都可能"没有结果"，而且**没有结果不会写垃圾数据**：
//
//	① 找候选：列出视频所在目录，挑出同目录的字幕文件（.srt/.ass/.ssa/.vtt）。
//	   这一步不花钱，也最准 —— 国内资源站打包的动画/课程基本都自带字幕。
//	② AI 匹配：候选分不开时（比如 xxx.chs.srt 与 xxx.eng.srt 并存，
//	   或者名字只差几个字），让大模型按片名+集数+语言选一个，并给置信度。
//	③ 存储：转成 WebVTT 写进 resource_subtitles 表（永久），
//	   下次播放直接命中，不再打网盘 API、不再联网搜。
//
// ⚠️ 三条纪律：
//   - **一轮只扫 limit 条**：每条都要列目录 + 可能取直链，扫太多会把网盘 API
//     打到限流，还会和正在看的播放抢带宽。
//   - **置信度不够就不存**：错字幕比没字幕更伤（孩子会跟着错的学）。
//   - **不覆盖人工指定的那条**：见 subtitle.Store.Save。
type SubtitleCollector struct {
	db    *gorm.DB
	enc   *util.Encryptor
	store *subtitle.Store
	// chat 惰性获取：没配 AI 时返回 nil，采集照样能跑（只用本地匹配）
	chat func() subtitle.ChatFunc

	maxPerRound int
	client      *http.Client

	mu       sync.Mutex
	lastRun  time.Time
	lastStat CollectStat
}

// CollectStat 一轮采集的结果（给管理页看）。
type CollectStat struct {
	At       time.Time `json:"at"`
	Scanned  int       `json:"scanned"`
	Stored   int       `json:"stored"`
	Skipped  int       `json:"skipped"`
	Failed   int       `json:"failed"`
	LastNote string    `json:"lastNote"`
}

// NewSubtitleCollector 构造。
func NewSubtitleCollector(db *gorm.DB, enc *util.Encryptor, store *subtitle.Store,
	chat func() subtitle.ChatFunc) *SubtitleCollector {
	return &SubtitleCollector{
		db:          db,
		enc:         enc,
		store:       store,
		chat:        chat,
		maxPerRound: 30,
		// 下载字幕给 30 秒：字幕文件通常几十 KB，慢的是网盘签直链那一步
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetMaxPerRound 调整单轮上限（配置/测试用）。
func (c *SubtitleCollector) SetMaxPerRound(n int) {
	if n > 0 && n <= 200 {
		c.maxPerRound = n
	}
}

// LastStat 上一轮的结果。
func (c *SubtitleCollector) LastStat() CollectStat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastStat
}

// Start 起后台循环。
//
// ⚠️ 先跑一次再进 ticker：只挂 ticker 的话第一次要等一整个周期，
// 部署完当天看不到任何效果，会让人以为没做。
func (c *SubtitleCollector) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		// 部署后先等一会儿：启动瞬间要抢数据库迁移和缓存预热，别和它们挤
		time.Sleep(3 * time.Minute)
		c.RunOnce(context.Background())
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			c.RunOnce(context.Background())
		}
	}()
}

// RunOnce 扫一轮。可以被管理接口手动触发（`POST /api/admin/subtitle/collect`）。
func (c *SubtitleCollector) RunOnce(ctx context.Context) CollectStat {
	if c == nil || c.db == nil || c.store == nil {
		return CollectStat{At: time.Now(), LastNote: "采集器未初始化"}
	}
	// 同一时刻只允许一轮：手动触发和定时任务撞上会重复打网盘 API
	if !c.mu.TryLock() {
		return c.lastStat
	}
	defer c.mu.Unlock()

	stat := CollectStat{At: time.Now()}
	rows, err := c.store.Missing(c.maxPerRound)
	if err != nil {
		stat.LastNote = "查待配资源失败：" + err.Error()
		c.lastStat = stat
		return stat
	}
	if len(rows) == 0 {
		stat.LastNote = "没有待配字幕的资源"
		c.lastStat = stat
		return stat
	}

	chat := subtitle.ChatFunc(nil)
	if c.chat != nil {
		chat = c.chat()
	}

	for i := range rows {
		if ctx.Err() != nil {
			stat.LastNote = "已取消"
			break
		}
		res := &rows[i]
		stat.Scanned++
		got, err := c.collectOne(ctx, res, chat)
		switch {
		case err != nil:
			stat.Failed++
			stat.LastNote = fmt.Sprintf("资源 %d：%v", res.ID, err)
		case got:
			stat.Stored++
		default:
			stat.Skipped++
		}
		// 每条之间喘一口：网盘 API 对"连续快速列举"很敏感，
		// 而且这一刻可能正有人在看视频，别把带宽和配额抢光。
		select {
		case <-ctx.Done():
		case <-time.After(1200 * time.Millisecond):
		}
	}
	if stat.LastNote == "" {
		stat.LastNote = fmt.Sprintf("扫描 %d，入库 %d，跳过 %d", stat.Scanned, stat.Stored, stat.Skipped)
	}
	c.lastRun = time.Now()
	c.lastStat = stat
	log.Printf("[subtitle] 采集一轮：%s", stat.LastNote)
	return stat
}

// collectOne 处理一条资源。返回 (是否入库, 错误)。
func (c *SubtitleCollector) collectOne(ctx context.Context, res *model.Resource,
	chat subtitle.ChatFunc) (bool, error) {

	a, cred, err := adapterFor(c.db, c.enc, res)
	if err != nil {
		return false, err
	}
	dirID := res.OwnerDirID
	if dirID == "" {
		return false, fmt.Errorf("不知道它在哪个目录")
	}

	listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	entries, err := a.ListDir(listCtx, cred, dirID)
	if err != nil {
		return false, fmt.Errorf("列目录失败：%w", err)
	}

	// 转成与 adapter 解耦的结构，顺带把视频自己的名字记下来
	sibs := make([]subtitle.SiblingEntry, 0, len(entries))
	for _, e := range entries {
		sibs = append(sibs, subtitle.SiblingEntry{ID: e.ID, Name: e.Name, IsDir: e.IsDir, Size: e.Size})
	}
	// ⚠️ Title 在入库时被刻意去掉了扩展名（列表更干净），
	// 而磁盘上的字幕是按"带扩展名的原文件名"命名的 —— 比对时把 ext 补回去，
	// 否则 `第01集.mp4` 会被拿去和 `第01集.srt` 比"第01集" vs "第01集"，
	// 看着也能对上，但一旦碰上"同名不同容器"的目录就会选错。
	videoName := res.Title
	if res.Ext != "" && !strings.HasSuffix(strings.ToLower(videoName), "."+strings.ToLower(res.Ext)) {
		videoName = res.Title + "." + res.Ext
	}
	cands := subtitle.RankSiblings(videoName, sibs)
	if len(cands) == 0 {
		return false, nil
	}

	idx, conf := subtitle.PickCandidateAI(ctx, chat, videoName, cands)
	if idx < 0 || idx >= len(cands) {
		return false, nil
	}
	if conf < subtitle.MinConfidence {
		return false, nil
	}
	picked := cands[idx]

	raw, err := c.download(ctx, a, cred, picked.FileID)
	if err != nil {
		return false, fmt.Errorf("下载字幕失败：%w", err)
	}
	vtt, err := subtitle.Normalize(raw)
	if err != nil {
		return false, fmt.Errorf("字幕解析失败：%w", err)
	}
	if strings.TrimSpace(vtt) == "" {
		return false, fmt.Errorf("字幕内容为空")
	}

	row := &model.ResourceSubtitle{
		ResourceID: res.ID,
		Lang:       guessLang(picked.Name),
		Source:     "同目录字幕 · " + picked.Name,
		Origin:     "sibling",
		Confidence: conf,
		Cues:       subtitle.CountCues(vtt),
		Size:       len(vtt),
		VTT:        vtt,
	}
	if err := c.store.Save(row); err != nil {
		return false, err
	}
	log.Printf("[subtitle] 资源 %d 已配字幕：%s（置信度 %d）", res.ID, picked.Name, conf)
	return true, nil
}

// download 走直链把字幕文件拉下来。
func (c *SubtitleCollector) download(ctx context.Context, a adapter.DiskAdapter,
	cred *adapter.Credential, fileID string) ([]byte, error) {

	link, err := a.GetDirectLink(ctx, cred, fileID)
	if err != nil {
		return nil, err
	}
	if link == nil || link.URL == "" {
		return nil, fmt.Errorf("拿不到直链")
	}
	// ⚠️ 本地硬盘（DiskLocal）给的是 `file://` 地址，http 客户端不认。
	// 不单独处理的话，接入本地硬盘的部署会**永远配不上字幕**，
	// 而日志里只会写一句 "unsupported protocol scheme file"，
	// 看着像网络问题，其实是这一条分支没写。
	if strings.HasPrefix(link.URL, "file://") {
		return readLocalFile(link.URL)
	}
	// 字幕不该有几百 KB 以上；超过就说明拿错文件了（比如拿成了视频）
	const maxSize = 4 << 20

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("文件超过 4MB，不像是字幕")
	}
	return b, nil
}

// adapterFor 由资源反查「适配器 + 凭据」。
//
// 与 resolveDirectLinkFor 共用同一套解密流程 —— 别各写一份：
// 凭据解密错一次的表现是"所有网盘操作一起失败"，而两边写法一旦漂开，
// 排查时会怀疑到完全无关的地方去。
func adapterFor(db *gorm.DB, enc *util.Encryptor, res *model.Resource) (adapter.DiskAdapter, *adapter.Credential, error) {
	if res.DiskAccountID == nil || res.FileID == "" {
		return nil, nil, fmt.Errorf("缺少网盘信息")
	}
	var acc model.DiskAccount
	if err := db.First(&acc, *res.DiskAccountID).Error; err != nil {
		return nil, nil, fmt.Errorf("账号不存在")
	}
	cookie, err := enc.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return nil, nil, fmt.Errorf("凭据解密失败")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	a, err := adapter.Get(adapter.DiskType(res.DiskType))
	if err != nil {
		return nil, nil, err
	}
	return a, &adapter.Credential{
		DiskType: adapter.DiskType(res.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, nil
}

// guessLang 从字幕文件名猜语言（只用于展示/统计）。
func guessLang(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "cht") || strings.Contains(l, "繁"):
		return "zh-Hant"
	case strings.Contains(l, "chs") || strings.Contains(l, "简") ||
		strings.Contains(l, "chi") || strings.Contains(l, "zh") || strings.Contains(l, "中文"):
		return "zh-Hans"
	case strings.Contains(l, "eng") || strings.Contains(l, ".en."):
		return "en"
	}
	return ""
}

// ============================================================
//  管理接口
// ============================================================

// AdminStats GET /api/admin/subtitle/stats
//
// 只报"存了多少、还有多少没配、上一轮什么时候跑的"。
// 不把字幕正文吐出来：那是几十 KB 的文本，管理页也用不上。
func (c *SubtitleCollector) AdminStats(ctx *gin.Context) {
	st := c.store.Stats()
	last := c.LastStat()
	ctx.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "success",
		"data": gin.H{
			"stored":  st.Stored,
			"total":   st.Video,
			"pending": maxInt64(st.Video-st.Stored, 0),
			"last":    last,
		},
	})
}

// AdminCollect POST /api/admin/subtitle/collect
//
// 手动触发一轮。**异步**执行：一轮要列几十次目录，同步等会让管理页转圈几分钟，
// 而前端还会以为超时了。触发后立刻返回，进度看 stats 的 last。
func (c *SubtitleCollector) AdminCollect(ctx *gin.Context) {
	go c.RunOnce(context.Background())
	ctx.JSON(http.StatusOK, gin.H{"code": 0, "message": "已开始采集，稍后看统计"})
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// readLocalFile 读 `file://` 地址。
//
// 跨平台这里有两个坑，都必须处理：
//   - **路径**：Linux 是 `file:///a/b`（去掉前缀正好是 `/a/b`），
//     而 Windows 适配器给的是 `file://C/Users/...`（少了冒号）；
//   - **编码**：中文文件名是百分号编码的（`%E7%AC%AC01%E9%9B%86.srt`），
//     不 unescape 就会拿一个带 `%` 的路径去 open，必然 No such file。
func readLocalFile(rawURL string) ([]byte, error) {
	p := strings.TrimPrefix(rawURL, "file://")
	if unescaped, err := url.PathUnescape(p); err == nil {
		p = unescaped
	}
	// Windows 盘符：`C/Users/...` → `C:/Users/...`；`/C:/Users/...` → `C:/Users/...`
	if len(p) >= 2 && p[0] == '/' && p[1] != '/' && len(p) > 2 && p[2] == ':' {
		p = p[1:]
	} else if len(p) >= 2 && p[1] == '/' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) {
		p = string(p[0]) + ":" + p[1:]
	}

	const maxSize = 4 << 20
	f, err := os.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("文件超过 4MB，不像是字幕")
	}
	return b, nil
}
