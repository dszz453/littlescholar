package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
