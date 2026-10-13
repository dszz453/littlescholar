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
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "application/pdf":
		return "pdf"
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
	link, err := a.GetDirectLink(c.Request.Context(), &adapter.Credential{
		DiskType: adapter.DiskType(res.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, res.FileID)
	if err != nil {
		util.ServerError(c, "获取播放地址失败: "+err.Error())
		return
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

	type result struct {
		idx  int
		data []byte
		err  error
	}

	tasks := make(chan adapter.Chunk, n)
	results := make(chan result, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ck := range tasks {
				data, _, err := h.fetchRange(ctx, link, ck)
				select {
				case results <- result{idx: ck.Index, data: data, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	// 收尾：先关任务队列让 worker 退出，再等它们结束。
	// 顺序反了会 panic（向已关闭 channel 发送）。
	defer func() {
		close(tasks)
		wg.Wait()
	}()

	inFlight := 0
	push := func(i int) bool {
		if i >= len(chunks) {
			return false
		}
		select {
		case tasks <- chunks[i]:
			inFlight++
			return true
		case <-ctx.Done():
			return false
		}
	}
	// 第 0 块已写出，从 1 开始预填窗口
	sent := 1
	for i := 1; i < len(chunks) && inFlight < n; i++ {
		if !push(i) {
			break
		}
		sent = i + 1
	}

	pending := map[int]result{}
	for next := 1; next < len(chunks); {
		res, ok := pending[next]
		if ok {
			delete(pending, next)
			inFlight--
		} else {
			select {
			case r := <-results:
				if r.idx != next {
					// 还没轮到的块先攒着；它仍占着在飞名额。
					pending[r.idx] = r
					continue
				}
				inFlight--
				res = r
			case <-ctx.Done():
				return true
			}
		}

		if res.err != nil {
			// 已经写出一部分，没法再改状态码，只能断开让播放器重试。
			return true
		}
		if _, err := c.Writer.Write(res.data); err != nil {
			return true
		}
		c.Writer.Flush()

		next++
		if sent < len(chunks) {
			push(sent)
			sent++
		}
	}
	return true
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
