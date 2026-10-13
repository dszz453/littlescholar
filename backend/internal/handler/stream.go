package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

	// 外部加速代理配置的短缓存。
	//
	// 读系统配置要查库，而这里是每个媒体分片请求都会走的热路径，
	// 所以缓存 10 秒 —— 管理员改完配置最多 10 秒生效，且不牺牲吞吐。
	accelMu  sync.Mutex
	accelURL string
	accelAt  time.Time
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
	// 网盘对非会员普遍做单连接限速，"多线程/分块并发"由外部加速代理
	// （参考包里的 aliproxy 就是这样一个二进制）来承担。
	// 这里不做重复实现，而是把上游直链改写到用户配置的代理上：
	// 代理负责多连接拉流，我们仍负责补鉴权头、透传 Range、转发给浏览器。
	//
	// 支持两种配置写法：
	//   1. 含 {url} 占位符 → 按模板替换（可对接任意网关，如 /proxy?url={url}）
	//   2. 不含占位符      → 直接把直链拼在其后（适配 http://host:port/https://… 形式）
	if accel := h.accelProxy(); accel != "" {
		link.URL = applyAccel(accel, link.URL)
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

// ============ 非会员加速：外部代理 ============

// accelProxy 读取「外部加速代理」配置，带 10 秒缓存。
func (h *StreamHandler) accelProxy() string {
	h.accelMu.Lock()
	defer h.accelMu.Unlock()
	if !h.accelAt.IsZero() && time.Since(h.accelAt) < 10*time.Second {
		return h.accelURL
	}
	h.accelAt = time.Now()
	h.accelURL = ""
	var row model.SystemConfig
	if err := h.db.Where("key = ?", "accel_proxy").First(&row).Error; err == nil {
		h.accelURL = strings.TrimSpace(row.Value)
	}
	return h.accelURL
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
