package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// StreamHandler 媒体流代理
//
// 为什么必须有这一层：WebDAV 一类的存储源靠 HTTP Basic Auth 鉴权，
// 而浏览器的 <video src="..."> **无法携带 Authorization 头**，
// 现代浏览器也禁止把 user:pass 写进 URL。所以只能由后端取流再转发。
//
// 顺带好处：网盘直链需要 Referer/UA 的场景也能统一在这里补，
// 前端不必再关心各家网盘的差异。
type StreamHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
	secret    string
	client    *http.Client

	// 系统配置的短缓存。
	//
	// 读配置要查库，而这里是每个媒体分片请求都会走的热路径，
	// 所以整表缓存 10 秒 —— 管理员改完最多 10 秒生效，且不牺牲吞吐。
	cfgMu sync.Mutex
	cfg   map[string]string
	cfgAt time.Time
}

func NewStreamHandler(db *gorm.DB, encryptor *util.Encryptor, secret string) *StreamHandler {
	return &StreamHandler{
		db:        db,
		encryptor: encryptor,
		secret:    secret,
		// 刻意不设 Timeout：那是"整个请求的生命周期"上限，
		// 设了会在长视频播到一半时把连接掐断。
		// 连接/首字节的超时交给 Transport 的参数控制。
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:          32,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				DisableCompression:    true, // 媒体流不做 gzip，避免破坏 Range 语义
			},
		},
	}
}

// ============ 播放票据 ============
//
// 播放 URL 会被塞进 <video src>，因此不能带 Authorization 头，
// 也就用不了常规的 JWT 中间件。这里用自签票据替代：
// 票据绑定"某一个资源 + 过期时间"，由 JWT secret 做 HMAC，
// 即使泄露也只能播放那一个资源，且很快失效。
//
// 不引入数据库存储（不落库、不查表）——票据本身自带全部信息。
// 这样流媒体请求链路上完全无 DB 查询。

// StreamTicketTTL 票据有效期。够看完一部电影，又不至于长期有效。
const StreamTicketTTL = 4 * time.Hour

// SignStreamTicket 生成播放票据，格式 "<过期时间戳>.<签名>"。
func SignStreamTicket(secret string, resourceID uint64, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d|%d", resourceID, exp)
	return fmt.Sprintf("%d.%s", exp, hex.EncodeToString(mac.Sum(nil)))
}

// verifyStreamTicket 校验票据。
func verifyStreamTicket(secret string, resourceID uint64, ticket string) bool {
	expStr, sig, ok := strings.Cut(ticket, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d|%d", resourceID, exp)
	expect := hex.EncodeToString(mac.Sum(nil))
	// 定长比较，避免时序侧信道
	return hmac.Equal([]byte(sig), []byte(expect))
}

// ============ 容器格式自愈 ============
//
// 背景（一条真实的踩坑记录）：
//
// 网盘资源的 FileID 是一串短 hash（既不是路径、也不含后缀），而 Title 在
// 入库时被刻意剥掉了扩展名。对"上线之前就已经入库"的老资源来说，我们手里
// **一点格式信息都没有** —— 于是 NeedsTranscode 判不出 rmvb，直链被塞进
// <video>，前端只报「播放地址可能已过期」，运维方向完全跑偏。
//
// 但上游 CDN 其实把答案明明白白写在响应头里：
// 夸克那批动画返回的是 `application/vnd.rn-realmedia-vbr`（RMVB）。
//
// 所以这里做一件事：**第一次取流时顺手把 Content-Type 记回资源**。
// 之后的播放判定就都正确了。代价只有"某个资源第一次播时多失败一次"，
// 且仅对老数据发生 —— 新导入的资源在 ImportService / instant 里
// 已经直接把扩展名写进 Ext 了。

// contentTypeExt 把上游的 Content-Type 归一成扩展名（不含点），认不出返回空。
func contentTypeExt(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "video/mp4":
		return "mp4"
	case "video/x-matroska":
		return "mkv"
	case "video/x-msvideo", "video/avi", "video/msvideo":
		return "avi"
	case "video/x-ms-wmv", "video/x-ms-asf":
		return "wmv"
	case "video/quicktime":
		return "mov"
	case "video/x-flv", "video/flv":
		return "flv"
	case "video/mp2t", "video/vnd.dlna.mpeg-tts":
		return "ts"
	case "video/mpeg":
		return "mpeg"
	case "video/3gpp":
		return "3gp"
	case "video/webm":
		return "webm"
	case "video/x-ms-vob":
		return "vob"
	case "video/x-m4v":
		return "m4v"
	// RealMedia 家族：夸克/百度上大量国内老动画都是这个格式，
	// 也正是这次"播不了"的主角。
	case "application/vnd.rn-realmedia", "application/vnd.rn-realmedia-vbr":
		return "rmvb"
	case "application/vnd.rn-realmedia-real", "application/x-pn-realmedia":
		return "rm"
	case "audio/mpeg":
		return "mp3"
	case "audio/mp4", "audio/x-m4a":
		return "m4a"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/aac", "audio/x-aac":
		return "aac"
	case "audio/ogg", "audio/vorbis", "application/ogg":
		return "ogg"
	case "audio/opus":
		return "opus"
	case "audio/x-ms-wma", "audio/wma":
		return "wma"
	case "audio/x-ape":
		return "ape"
	case "audio/amr", "audio/x-amr":
		return "amr"
	case "audio/ac3":
		return "ac3"
	case "audio/vnd.dts", "audio/x-dts":
		return "dts"
	case "audio/x-matroska":
		return "mka"
	case "audio/aiff", "audio/x-aiff":
		return "aiff"
	case "audio/basic", "audio/x-au":
		return "au"
	case "application/pdf":
		return "pdf"
	// 电子书。Kindle 的 azw3 上游普遍报 application/vnd.amazon.ebook，
	// 认不出 azw3 与 azw 的区别，统一按 azw3 处理（转换逻辑一样）。
	case "application/epub+zip":
		return "epub"
	case "application/x-mobipocket-ebook":
		return "mobi"
	case "application/vnd.amazon.ebook":
		return "azw3"
	case "application/vnd.comicbook+zip":
		return "cbz"
	case "application/vnd.comicbook-rar":
		return "cbr"
	case "image/heic", "image/heif":
		return "heic"
	case "image/tiff":
		return "tiff"
	case "image/avif":
		return "avif"
	}
	return ""
}

// learnContainerType 把上游声明的容器格式记回资源。
//
// 三条护栏：
//   - 只认得出扩展名时才写，认不出就什么都不做；
//   - 只在**原本没有任何格式信息**时写 —— WebDAV 的 FileID 带后缀，
//     比 CDN 的 MIME 更权威，不能被覆盖掉；
//   - 写失败只记日志，绝不因为"记格式"这种副业影响正在进行的播放。
func (h *StreamHandler) learnContainerType(res *model.Resource, contentType string) {
	if res == nil || res.Ext != "" || contentType == "" {
		return
	}
	ext := contentTypeExt(contentType)
	if ext == "" {
		return
	}
	err := h.db.Model(&model.Resource{}).
		Where("id = ? AND (ext IS NULL OR ext = '')", res.ID).
		UpdateColumn("ext", ext).Error
	if err != nil {
		log.Printf("[stream] 记录容器格式失败 resource=%d: %v", res.ID, err)
		return
	}
	res.Ext = ext
}

// ============ 代理 ============

// serveHeaders 需要原样透传给浏览器的响应头。
//
// Content-Range / Accept-Ranges 是拖动进度的关键，漏一个就会导致
// "进度条能拖但一拖就卡住"这种玄学问题。
var serveHeaders = []string{
	"Content-Type",
	"Content-Length",
	"Content-Range",
	"Accept-Ranges",
	"Last-Modified",
	"ETag",
}

// audioContentType 按资源扩展名给出**准确的音频 MIME**。
//
// ============ 为什么非补这一个头不可 ============
//
// 网盘（尤其夸克）对媒体文件普遍只回 `application/octet-stream`，
// 而我们的播放地址是 `/api/stream/8391?t=…` —— **既没有 MIME，
// 也没有扩展名**。libVLC 挑 demuxer 就靠这两条线索，两条都没有时
// 它把数据当未知流处理：**整份文件都下载完了，却一个音频帧都出不来**，
// 而且只抛一个 EncounteredError、不带任何原因（2026-10-26 柯林斯大猫
// 的 wma 实录 —— 日志里 VLC 明明把 3.2MB 全拉走，就是没声音）。
//
// 只补音频：视频的容器判定更依赖内容嗅探，乱标 MIME 反而可能让
// 浏览器拒播（例如把 mkv 标成 video/mp4）。音频这几个格式 MIME 与
// 容器一一对应，标错的风险为零。
func audioContentType(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "wma":
		return "audio/x-ms-wma"
	case "ape":
		return "audio/x-ape"
	case "amr":
		return "audio/amr"
	case "ac3":
		return "audio/ac3"
	case "dts":
		return "audio/vnd.dts"
	case "m4a":
		return "audio/mp4"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "aac":
		return "audio/aac"
	case "ogg":
		return "audio/ogg"
	case "opus":
		return "audio/opus"
	case "mka":
		return "audio/x-matroska"
	case "aiff":
		return "audio/aiff"
	case "au":
		return "audio/basic"
	}
	return ""
}

// Serve 代理取流。路由：GET /api/stream/:id?t=<ticket>
func (h *StreamHandler) Serve(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	if !verifyStreamTicket(h.secret, id, c.Query("t")) {
		util.Forbidden(c, "播放凭证无效或已过期，请重新进入播放页")
		return
	}

	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	if res.DiskAccountID == nil || *res.DiskAccountID == 0 || res.FileID == "" {
		util.BadRequest(c, "该资源没有可播放的源文件")
		return
	}

	var acc model.DiskAccount
	if err := h.db.First(&acc, *res.DiskAccountID).Error; err != nil {
		util.NotFound(c, "网盘账号不存在")
		return
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		util.ServerError(c, "凭据解密失败")
		return
	}
	extra := make(map[string]string, len(acc.Extra))
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}

	a, err := adapter.Get(adapter.DiskType(res.DiskType))
	if err != nil {
		util.BadRequest(c, err.Error())
		return
	}

	// 用独立的 context：c.Request.Context() 在客户端断开时会取消，
	// 那正是我们要停止拉流的时机，所以沿用它是正确的。
	//
	// ⚠️ 直链**先查进程内缓存**。播放一个视频会发十几个 Range 请求，
	// 每个都真去打一次网盘 API 的话，点开要等、拖进度要等、
	// 还容易撞上网盘限流（见 linkcache.go 的说明）。
	linkKey := linkCacheKey(*res.DiskAccountID, res.FileID)
	link, cached := getCachedLink(linkKey)
	if !cached {
		link, err = a.GetDirectLink(c.Request.Context(), &adapter.Credential{
			DiskType: adapter.DiskType(res.DiskType),
			Cookie:   cookie,
			Extra:    extra,
		}, res.FileID)
		if err != nil {
			util.ServerError(c, "获取播放地址失败: "+err.Error())
			return
		}
		putCachedLink(linkKey, link)
	}
	if link == nil || link.URL == "" {
		util.ServerError(c, "获取到的播放地址为空")
		return
	}

	// 本地硬盘接入源返回的是 file:// 地址（浏览器读不了本地文件），
	// 这里直接读盘并用 http.ServeContent 转发 —— 它自带 Range /
	// If-Range / If-Modified-Since 处理，等于免费拿到"可拖动进度"。
	if path, ok := strings.CutPrefix(link.URL, "file://"); ok {
		h.serveLocalFile(c, path)
		return
	}

	// ============ 非会员加速 ============
	//
	// 优先级：配了外部代理就交给它（它专门做多连接）；没配就用自研的
	// 分块并发。两者不叠加 —— 叠了等于把同一件事做两遍，只会互相拖慢。
	//
	// 外部代理支持两种配置写法：
	//   1. 含 {url} 占位符 → 按模板替换（可对接任意网关，如 /proxy?url={url}）
	//   2. 不含占位符      → 直接把直链拼在其后（适配 http://host:port/https://… 形式）
	accel := h.accelProxy()
	if accel != "" {
		link.URL = applyAccel(accel, link.URL)
	} else if n := h.multiConn(); n > 1 && link.AcceptRanges && link.Size > 0 &&
		isWholeFileRequest(c.GetHeader("Range")) {
		if h.serveMultiConn(c, &res, link, n) {
			return
		}
		// 返回 false 表示一个字节都没写出去，可以安全回退到下面的单流。
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, link.URL, nil)
	if err != nil {
		util.ServerError(c, "构造请求失败")
		return
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	// 透传 Range —— 没有它，<video> 既不能拖进度也不能边下边播
	if r := c.GetHeader("Range"); r != "" {
		req.Header.Set("Range", r)
	}
	if ir := c.GetHeader("If-Range"); ir != "" {
		req.Header.Set("If-Range", ir)
	}
	if r := c.GetHeader("If-None-Match"); r != "" {
		req.Header.Set("If-None-Match", r)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		util.ServerError(c, "拉取媒体流失败: "+err.Error())
		return
	}
	// 缓存里的直链失效了（典型：夸克 `__puus` 过期 —— 列目录/取链仍回 200，
	// 只有 CDN 真取字节才 401/403/412）。这时**丢掉缓存重取一次**再试，
	// 而不是把一个"看不了"丢给孩子。
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 412 {
		_, _ = io.CopyN(io.Discard, resp.Body, 1<<10)
		resp.Body.Close()
		dropCachedLink(linkKey)

		if nl, derr := a.GetDirectLink(c.Request.Context(), &adapter.Credential{
			DiskType: adapter.DiskType(res.DiskType),
			Cookie:   cookie,
			Extra:    extra,
		}, res.FileID); derr == nil && nl != nil && nl.URL != "" {
			putCachedLink(linkKey, nl)
			link = nl
			if req2, rerr := http.NewRequestWithContext(c.Request.Context(),
				http.MethodGet, link.URL, nil); rerr == nil {
				for k, v := range link.Headers {
					req2.Header.Set(k, v)
				}
				if r := c.GetHeader("Range"); r != "" {
					req2.Header.Set("Range", r)
				}
				if ir := c.GetHeader("If-Range"); ir != "" {
					req2.Header.Set("If-Range", ir)
				}
				if r := c.GetHeader("If-None-Match"); r != "" {
					req2.Header.Set("If-None-Match", r)
				}
				resp, err = h.client.Do(req2)
				if err != nil {
					util.ServerError(c, "拉取媒体流失败: "+err.Error())
					return
				}
			}
		} else {
			util.ServerError(c, "播放地址已失效，请重新进入播放页")
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		util.ServerError(c, fmt.Sprintf("源站返回 %d", resp.StatusCode))
		return
	}

	// 上游告诉了我们真实容器格式就记下来（只对老数据补一次，
	// 详见 learnContainerType 的注释）。
	h.learnContainerType(&res, resp.Header.Get("Content-Type"))

	for _, name := range serveHeaders {
		if v := resp.Header.Get(name); v != "" {
			c.Header(name, v)
		}
	}
	// 上游只给 octet-stream（或干脆没给）时，按资源自己的扩展名补一个
	// 准确类型 —— 见 audioContentType 顶部那段：没有它 libVLC 会
	// "文件下完了但不出声"。放在透传之后，是为了尊重上游给出的真实类型。
	if ct := c.Writer.Header().Get("Content-Type"); ct == "" ||
		strings.HasPrefix(strings.ToLower(ct), "application/octet-stream") {
		if mt := audioContentType(service.ResourceExt(&res)); mt != "" {
			c.Header("Content-Type", mt)
		}
	}
	// 票据过期后同一 URL 不应被缓存复用
	c.Header("Cache-Control", "no-store")

	c.Status(resp.StatusCode)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		// 客户端主动断开（拖进度、关页面）会走到这里，属正常情况，
		// 不值得记成错误 —— 只中断拷贝即可。
		return
	}
}

// ============ 非会员加速 ============
//
// 网盘对非会员普遍做**单连接限速**：一条连接再快也就几百 KB/s，而拆成
// 多条连接各拉一段，总吞吐常常能翻好几倍。参考包（影视播放器的本地
// 资源 jar）用的正是这个思路 —— 它把这件事交给一个外部代理二进制
// （aliproxy），并用 thread_limit 系列参数控制并发数。
//
// 这里把两种做法都保留，按优先级自动选：
//
//	1. 配了「外部加速代理」→ 单流改写直链过去（外挂，交给它做并发）
//	2. 没配代理但 multi_conn > 1 → **自己分块并发拉**（默认路径）
//	3. 都不满足 → 老老实实单流
//
// 之所以自研而不是照搬外挂：外挂代理是给"盒子性能不够"的场景用的，
// 我们的服务端 CPU/内存都富余，多一层外部进程只是多一个故障点。

// configValue 读取系统配置，带 10 秒整表缓存。
func (h *StreamHandler) configValue(key string) string {
	h.cfgMu.Lock()
	defer h.cfgMu.Unlock()

	if !h.cfgAt.IsZero() && time.Since(h.cfgAt) < 10*time.Second {
		return h.cfg[key]
	}

	h.cfgAt = time.Now()
	h.cfg = map[string]string{}
	var rows []model.SystemConfig
	if err := h.db.Find(&rows).Error; err == nil {
		for _, r := range rows {
			h.cfg[r.Key] = strings.TrimSpace(r.Value)
		}
	}
	return h.cfg[key]
}

// accelProxy 外部加速代理地址，可能为空。
func (h *StreamHandler) accelProxy() string { return h.configValue("accel_proxy") }

// multiConn 分块并发的连接数。
//
// 默认 4：足以拉开非会员的单连接限速，又不至于把账号的并发数顶爆
// —— 参考包里反复强调"遇到账号被限制并发数，请把数值改小"，
// 说明并发开太大是有代价的（甚至封号）。
// 配 0 或 1 表示关闭，退回单流。
func (h *StreamHandler) multiConn() int {
	v := h.configValue("multi_conn")
	if v == "" {
		return defaultMultiConn
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultMultiConn
	}
	if n > maxMultiConn {
		n = maxMultiConn
	}
	return n
}

const (
	defaultMultiConn = 4
	maxMultiConn     = 32

	// 首块刻意切小：播放器要的是"尽快出画面"，让第一块只覆盖
	// 起播所需的头部数据，首字节延迟就不会被块体积拖长。
	firstChunkSize = 256 << 10 // 256 KiB
	// 后续块 4 MiB：块太小会让请求数暴涨，容易被网盘判成滥用。
	chunkSize = 4 << 20
)

// planChunks 切分分块计划（首块小、后续块大）。
func planChunks(size int64) []adapter.Chunk {
	if size <= 0 {
		return nil
	}
	if size <= firstChunkSize {
		return []adapter.Chunk{{Index: 0, Start: 0, End: size - 1}}
	}
	chunks := []adapter.Chunk{{Index: 0, Start: 0, End: firstChunkSize - 1}}
	idx := 1
	for start := int64(firstChunkSize); start < size; start += chunkSize {
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		chunks = append(chunks, adapter.Chunk{Index: idx, Start: start, End: end})
		idx++
	}
	return chunks
}

// isWholeFileRequest 判断客户端是不是在要"整个文件"。
//
// 只在"从头播整份"时启用多线程。客户端带 Range 时通常是在拖进度，
// 或浏览器先探一小段看能不能播 —— 那种小范围请求单流更快，
// 强行分块只是徒增调度开销和对源站的并发压力。
func isWholeFileRequest(rng string) bool {
	rng = strings.TrimSpace(rng)
	if rng == "" {
		return true
	}
	v, ok := strings.CutPrefix(rng, "bytes=")
	if !ok || strings.Contains(v, ",") {
		return false
	}
	return strings.TrimSpace(v) == "0-"
}

// fetchRange 拉取一个字节区间。
//
// 只接受 206：源站回 200 说明它不认 Range，会把**整份文件**当成这一段
// 塞回来 —— 那既撑爆内存，又会让后续每一块都重复整份文件。必须拒绝。
func (h *StreamHandler) fetchRange(ctx context.Context, link *adapter.DirectLink, ck adapter.Chunk) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", ck.Start, ck.End))

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return nil, "", fmt.Errorf("源站不支持分块请求（HTTP %d）", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// ============ 为什么是"边下边发"而不是"整块读完再发" ============
//
// 2026-10-22 之前这里（以及 PRO 的 accelerate 包）是这样的：
// 每个 4MiB 分块用 io.ReadAll 读满，再按序号写出去。
//
// 后果在真机上是**肉眼可见的**：
//   · 首块 256KiB 写完（约 2 秒画面）之后，客户端要等下一整块 4MiB ——
//     而单条连接往往被网盘限到 300~600KB/s，也就是 **7~13 秒一个字节都没有**；
//   · 播放器把缓冲吃干 → 转圈等；等 4 块几乎同时到达，又突然灌进 16MiB。
//   · 用户看到的就是"打开视频慢"+"播放不流畅"，日志里却一切正常
//     （每个请求都 200，没有报错）。
//
// 所以现在改成：每个分块**一条连接、一次请求**（请求数不变，不会被网盘
// 判成异常流量），但响应体按 64KiB 读一块就往有序槽位里塞一块，
// 写协程按序立刻 flush。数据从第一个 64KiB 起就是连续流动的。

// streamBlock 流式转发的粒度。
//
// 64KiB 是"够小所以不会憋"与"够大所以不会把 syscall/flush 打爆"的折中：
// 4K 会让 CPU 花在调度上，1MiB 又退回到"憋一秒"。
const streamBlock = 64 << 10

// slotBuffer 每个分块槽位的缓冲块数。
//
// 内存上限 ≈ 并发数 × slotBuffer × 64KiB = 4 × 8 × 64KiB = 2MiB。
// 跑得快的连接会被自己的槽位挡住 —— 这就是背压，不需要额外的信号量。
const slotBuffer = 8

// serveMultiConn 用 n 个并发连接分块拉流。
//
// 返回 true 表示已经接管响应（可能已写出数据）；返回 false 表示
// 一个字节都还没写，调用方可以安全回退到单流。
func (h *StreamHandler) serveMultiConn(c *gin.Context, res *model.Resource, link *adapter.DirectLink, n int) bool {
	chunks := planChunks(link.Size)
	if len(chunks) == 0 {
		return false
	}
	ctx := c.Request.Context()

	// 先自己取第一块：既验证源站是否真支持 Range（必须 206），
	// 也顺带把首字节拿到手。这一步失败就干净地交回单流。
	first, contentType, err := h.fetchRange(ctx, link, chunks[0])
	if err != nil {
		return false
	}
	want := chunks[0].End - chunks[0].Start + 1
	if int64(len(first)) != want {
		return false
	}

	// 分块路径同样能拿到 Content-Type，顺手补一次格式信息
	h.learnContainerType(res, contentType)

	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Length", strconv.FormatInt(link.Size, 10))
	// 票据过期后同一 URL 不应被缓存复用
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)

	if _, err := c.Writer.Write(first); err != nil {
		return true
	}
	c.Writer.Flush()

	if len(chunks) == 1 {
		return true
	}

	// ============ 流式转发 ============
	//
	// 槽位数组：slots[i] 是第 i 块的数据管道（64KiB 一块）。
	// worker 只管往里写，写满就阻塞（背压）；写协程严格按 1,2,3… 的顺序取。
	// 关键点：**worker 不能"提前太多"** —— 任务队列容量是 n，
	// 也就是最多 n 块在飞，不会有 175 个槽位同时占着内存。
	streamCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	// ⚠️ 必须"先取消再等"：写协程提前返回时，worker 可能正卡在
	// `out <- piece` 上；不取消，wg.Wait() 会一直等下去（连接挂死）。
	defer func() {
		cancel()
		wg.Wait()
	}()

	slots := make([]chan []byte, len(chunks))
	for i := 1; i < len(slots); i++ {
		slots[i] = make(chan []byte, slotBuffer)
	}

	var (
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancel() // 任何一路失败都立刻把其他连接停掉，别再拉没人要的字节
	}
	getErr := func() error {
		errMu.Lock()
		defer errMu.Unlock()
		return firstErr
	}

	tasks := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range tasks {
				if err := h.pumpChunk(streamCtx, link, chunks[idx], slots[idx]); err != nil {
					setErr(err)
				}
			}
		}()
	}
	go func() {
		for i := 1; i < len(chunks); i++ {
			select {
			case tasks <- i:
			case <-streamCtx.Done():
				close(tasks)
				return
			}
		}
		close(tasks)
	}()

	for next := 1; next < len(chunks); next++ {
		for piece := range slots[next] {
			if _, err := c.Writer.Write(piece); err != nil {
				// 客户端断开（拖进度、关页面）—— 正常情况，安静收工
				cancel()
				return true
			}
			c.Writer.Flush()
		}
		if err := getErr(); err != nil {
			// 已经写出一部分字节，改不了状态码了，只能断开让播放器重试。
			// 这也是"播到一半卡住不动"的真实原因：Content-Length 还在，
			// 但后面永远不会再有数据 —— 断开比让播放器干等更好。
			log.Printf("[stream] 分块中断 resource=%d idx=%d: %v", res.ID, next, err)
			return true
		}
	}
	return true
}

// pumpChunk 把一个分块从上游流进槽位。
//
// 与原来的 fetchRange（整块 ReadAll）的区别只有一点：**不攒**。
// 读到 64KiB 就交出去，下游写协程立刻能 flush。
//
// 断点续传：body 中途断了（网盘 CDN 常有）就从已经发出去的偏移继续发，
// 而不是把整块丢掉 —— 丢掉意味着播放器要重开一个请求，用户看到一次卡顿。
func (h *StreamHandler) pumpChunk(
	ctx context.Context,
	link *adapter.DirectLink,
	ck adapter.Chunk,
	out chan<- []byte,
) error {
	defer close(out)

	pos := ck.Start
	attempt := 0
	for pos <= ck.End {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		body, _, err := h.openRange(ctx, link, pos, ck.End)
		if err != nil {
			attempt++
			if attempt > 3 || ctx.Err() != nil {
				return err
			}
			select {
			case <-time.After(time.Duration(150*(1<<attempt)) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}

		readErr := func() error {
			defer body.Close()
			buf := make([]byte, streamBlock)
			for pos <= ck.End {
				n := int64(len(buf))
				if rem := ck.End - pos + 1; rem < n {
					n = rem
				}
				if _, e := io.ReadFull(body, buf[:n]); e != nil {
					return e
				}
				// 必须拷贝：buf 下一轮会被覆盖，而槽位里的数据是异步消费的
				piece := make([]byte, n)
				copy(piece, buf[:n])
				select {
				case out <- piece:
				case <-ctx.Done():
					return ctx.Err()
				}
				pos += n
			}
			return nil
		}()

		if readErr == nil {
			return nil
		}
		// 中断：pos 已经推进到读失败的位置，下一轮从这里续
		attempt++
		if attempt > 3 || ctx.Err() != nil {
			return readErr
		}
	}
	return nil
}

// openRange 发起一次 Range 请求并返回响应体。
//
// 只接受 206：源站回 200 说明它不认 Range，会把整份文件当成这一段塞回来 ——
// 那既撑爆内存，又会让后面每一个字节的偏移全部错位。
func (h *StreamHandler) openRange(
	ctx context.Context,
	link *adapter.DirectLink,
	start, end int64,
) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusPartialContent {
		_, _ = io.CopyN(io.Discard, resp.Body, 1<<10)
		resp.Body.Close()
		return nil, "", fmt.Errorf("源站不支持分块请求（HTTP %d）", resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// applyAccel 把上游直链改写到外部加速代理。
func applyAccel(proxy, target string) string {
	if strings.Contains(proxy, "{url}") {
		return strings.ReplaceAll(proxy, "{url}", url.QueryEscape(target))
	}
	return strings.TrimRight(proxy, "/") + "/" + target
}

// serveLocalFile 把本地硬盘上的文件以支持 Range 的方式转发给客户端。
//
// 用 http.ServeContent 而不是手工处理 Range：
// 它已经把单段/多段 Range、If-Range、If-Modified-Since、Content-Type
// 推断全部处理好了，手写一遍只会引入等价性 bug。
func (h *StreamHandler) serveLocalFile(c *gin.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			util.NotFound(c, "本地文件不存在")
			return
		}
		util.ServerError(c, "打开本地文件失败: "+err.Error())
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		util.ServerError(c, "读取文件信息失败")
		return
	}
	if st.IsDir() {
		util.BadRequest(c, "该路径是目录，不能播放")
		return
	}
	c.Header("Cache-Control", "no-store")
	http.ServeContent(c.Writer, c.Request, st.Name(), st.ModTime(), f)
}
