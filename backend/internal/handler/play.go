package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/adapter"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// PlayHandler 播放
//
// transcode 允许为 nil：转码未启用（配置关闭或未找到 ffmpeg）时
// 播放接口退化为原有 direct / external 两种模式，不影响基础播放能力。
type PlayHandler struct {
	db        *gorm.DB
	perm      *service.PermissionService
	encryptor *util.Encryptor
	transcode *service.TranscodeService
	// secret 用于签发播放票据（见 stream.go）。播放 URL 要进 <video src>，
	// 带不了 Authorization 头，所以不能靠常规 JWT 中间件鉴权。
	secret string
	// ebook 为 nil 时电子书退化为"给原始直链"，不影响其它类型。
	ebook *EbookHandler
	// audioFmt 为 nil 时音频退化为"给原始直链"。
	audioFmt *AudioFormatHandler
	// imageFmt 为 nil 时 HEIC/TIFF 退化为"给原始直链"（多半是空白）。
	imageFmt *ImageFormatHandler
}

func NewPlayHandler(db *gorm.DB, perm *service.PermissionService, encryptor *util.Encryptor, transcode *service.TranscodeService, secret string) *PlayHandler {
	return &PlayHandler{db: db, perm: perm, encryptor: encryptor, transcode: transcode, secret: secret}
}

// WithEbook 注入电子书转换能力（链式，避免改动既有构造签名）。
func (h *PlayHandler) WithEbook(e *EbookHandler) *PlayHandler {
	h.ebook = e
	return h
}

// WithAudioFormat 注入音频格式转换能力。
func (h *PlayHandler) WithAudioFormat(a *AudioFormatHandler) *PlayHandler {
	h.audioFmt = a
	return h
}

// WithImageFormat 注入图片格式转换能力（HEIC / TIFF → JPEG）。
func (h *PlayHandler) WithImageFormat(m *ImageFormatHandler) *PlayHandler {
	h.imageFmt = m
	return h
}

// canProxy 判断资源是否具备"由后端代取流"的条件：
// 必须已登记到某个接入账号，并拿到了源文件 ID/路径。
func canProxy(res *model.Resource) bool {
	return res.DiskAccountID != nil && *res.DiskAccountID > 0 && res.FileID != ""
}

// 播放画质。
//
// 为什么要让孩子/家长来选，而不是后端自动决定：
// 转码这件事的两端差别极大 ——
//   - mp4/m4v 这类源，转码纯粹是白等（几分钟起，画质还可能被降档）；
//   - rmvb/mkv 这类源，不转码浏览器根本放不出来（MediaError(4)）。
// 一个默认值不可能同时照顾好这两种情况，所以默认走"不等待"的原画，
// 需要兼容性的场景由用户显式切到流畅。
const (
	QualityOriginal = "original"
	QualitySmooth   = "smooth"
)

// parsePlayQuality 解析画质参数，无法识别时一律回落到 original。
//
// 剥离成纯函数是为了可单测：Query 的取值分散在 HTTP 层，
// 而"默认必须是不转码"这条约定值得被测试锁住 —— 一旦回归成默认转码，
// 用户看到的就是刚修好的"一直提示在转码"重新出现。
func parsePlayQuality(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case QualitySmooth, "fluent", "low":
		return QualitySmooth
	default:
		return QualityOriginal
	}
}

// playQuality 取请求里的画质参数。
func playQuality(c *gin.Context) string {
	return parsePlayQuality(c.Query("quality"))
}

// needsProxy 判断该资源的播放地址是否**必须**由后端代理转发。
//
// 结论：只要具备代理条件就一律代理。
//
// 理由（也是踩过的坑）：夸克/阿里等网盘的直链大多要求带 Referer/UA，
// 浏览器 <video src> 无法附加请求头，直接把直链丢过去经常 403；
// 而走同源的 /api/stream 代理，既能把头补上，又顺带解决 CORS。
// 本地硬盘的 file:// 与 WebDAV 的 Basic Auth 更是只能代理。
// 只有"拿不到源文件"（纯分享链接）时才回退到直链/网盘页面。
func needsProxy(res *model.Resource) bool { return canProxy(res) }

// streamURL 生成带票据的代理播放地址。
func (h *PlayHandler) streamURL(id uint64) string {
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/stream/%d?t=%s", id, SignStreamTicket(h.secret, id, exp))
}

// directURL 返回"可以直接交给浏览器播放"的地址。
//
// 具备代理条件的资源统一换成后端代理地址（带票据）；
// 其余（只有分享链接、没有源文件）走原有的直链解析。
//
// 取不到时返回空串，由调用方决定退路。
func (h *PlayHandler) directURL(res *model.Resource) string {
	if needsProxy(res) {
		return h.streamURL(res.ID)
	}
	if link, err := h.resolveDirectLink(res); err == nil && link != "" {
		return link
	}
	return ""
}

// setDirectPlay 决定"不需要转码时"的播放方式。
func (h *PlayHandler) setDirectPlay(res *model.Resource, playMode, playURL *string) {
	if needsProxy(res) {
		// 不能走 fallbackDirect —— 那会把需要鉴权头/本地路径的直链
		// 塞给 <video>，浏览器必然 403 或直接失败。
		// 代理层会自己重新取直链并带上必要的头。
		*playMode = "proxy"
		*playURL = h.streamURL(res.ID)
		return
	}
	h.fallbackDirect(res, playMode, playURL)
}

// setupEbook 决定电子书的取文件方式，并返回前端该用的渲染器名。
//
// 两类分开：
//   - epub / txt / cbz：浏览器那边有纯前端解析器，直接代理原文件（支持 Range）；
//   - mobi / azw3 / cbr / fb2：WebView 解不了，交给 EbookHandler 转成 EPUB。
func (h *PlayHandler) setupEbook(res *model.Resource, playMode, playURL *string) string {
	ext := service.ResourceExt(res)
	if h.ebook != nil && service.EbookNeedsConvertExt(ext) {
		*playMode = "ebook"
		*playURL = h.ebook.URLFor(res.ID)
		return "epub"
	}
	h.setDirectPlay(res, playMode, playURL)
	return ext
}

// Info 获取播放信息
func (h *PlayHandler) Info(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}

	var res model.Resource
	if err := h.db.Preload("Category").First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}

	// 权限校验
	user := currentUser(c)
	if user == nil {
		// 后台管理员通过 AuthMiddleware 但可能未注入 user，重新查
		var u model.User
		if err := h.db.First(&u, util.CurrentUserID(c)).Error; err == nil {
			user = &u
		}
	}
	if res.CategoryID > 0 && user != nil {
		allowed, err := h.perm.CanAccessCategory(user, res.CategoryID)
		if err != nil || !allowed {
			util.Forbidden(c, "无该资源的访问权限")
			return
		}
	}

	mediaType := res.MediaType
	// media_type 是后来补的列（见 MEMORY「AutoMigrate 补列不回填默认值」），
	// 存量行可能是空；而这些资源的 Title 在入库时就被剥掉了扩展名，
	// 按标题猜只会得到空 ext → 默认 video。真实格式还留在 Ext / FileID 里，
	// 先按它反推才对 —— 否则一本 EPUB 会被当成视频塞进 <video>。
	if mediaType == "" {
		if e := service.ResourceExt(&res); e != "" {
			mediaType = service.MediaTypeByExt(e)
		} else {
			mediaType = guessMediaType(res.Title)
		}
	} else if mediaType == model.MediaTypeVideo {
		// 历史误判（早年导入一律写 video）：Ext 明确不是视频时以 Ext 为准
		if e := service.ResourceExt(&res); e != "" {
			if t := service.MediaTypeByExt(e); t != model.MediaTypeVideo {
				mediaType = t
			}
		}
	}

	// 播放地址生成策略
	//
	// 优先级：
	//  1. hls          —— 已转码就绪，直接播放切片（最可靠，支持拖动、跨端兼容）
	//  2. transcoding  —— 正在转码中，返回进度让前端轮询等待
	//  3. proxy        —— WebDAV 等需要带鉴权头的源，走后端代理转发
	//  4. direct       —— 拿到网盘直链且无需转码，直接播放
	//  5. external     —— 以上都不可用，回退到网盘原始页面
	playMode := "external"
	playURL := res.ShareURL
	var qualities []gin.H
	var transcodeTaskID uint64
	var transcodeProgress int
	// smoothUnavailable：用户点了「流畅」但转码引擎不可用（未装 ffmpeg /
	// 被配置关掉），已按原画降级。如实告诉前端，否则用户只会看到
	// 画质按钮点了没反应，以为是坏的。
	var smoothUnavailable bool

	// 先补容器格式，再决定播放方式。
	//
	// 顺序很重要：NeedsTranscode 依赖 ResourceExt，而存量网盘资源这一项是空的。
	// 不先探一次，rmvb/mkv 会被当成"浏览器能播"直接代理出去，第一次必然失败。
	if mediaType == model.MediaTypeVideo && service.ResourceExt(&res) == "" {
		h.probeContainer(c.Request.Context(), &res)
	}

	// 画质选择：由孩子在播放页决定走不走转码。
	//
	//   original（默认）—— 不转码，直接把源文件代理出去（秒开）
	//   smooth          —— 转成 HLS（360p/480p/720p），更省流量、弱网更稳
	//
	// 这段替换掉了原先"按容器自动决定要不要转码"的逻辑。老逻辑的问题是
	// 用户完全没有选择权：只要容器被判为需要转码，每次点开都是"正在转码"，
	// 而且转码产物一旦存在就再也播不回源文件。现在默认不等待，
	// 只有用户明确选了流畅才付出等待成本。
	quality := playQuality(c)
	wantSmooth := quality == QualitySmooth
	transcodeReady := h.transcode != nil && h.transcode.Enabled()

	// 电子书没有"码率/转码"这回事，它要回答的是另外两个问题：
	// 前端该用哪个渲染器、文件从哪拿。所以单开一个分支，
	// 不参与下面的画质选择。
	ebookFormat := ""
	switch {
	case mediaType == model.MediaTypeEbook:
		ebookFormat = h.setupEbook(&res, &playMode, &playURL)
	case mediaType == model.MediaTypeAudio && h.audioFmt != nil && h.audioFmt.NeedsConvert(&res):
		// wma / ape / amr / ac3 …：WebView 的 <audio> 直接放不出来。
		// 这里给一个"服务端已经转成 AAC"的地址，前端不用做任何判断。
		playMode = "audioformat"
		playURL = h.audioFmt.URLFor(res.ID)
	case mediaType == model.MediaTypeImage && h.imageFmt != nil && h.imageFmt.NeedsConvert(&res):
		// HEIC / TIFF：<img> 在 WebView 里就是一块空白，转成 JPEG 再给
		playMode = "imageformat"
		playURL = h.imageFmt.URLFor(res.ID)
	case wantSmooth && transcodeReady:
		// EnsureHLS 会：已就绪→直接返回；未开始→入队并返回；进行中→返回进度
		masterURL, ready, taskID, err := h.transcode.EnsureHLS(c.Request.Context(), &res)
		switch {
		case err == nil && ready && masterURL != "":
			playMode = "hls"
			playURL = masterURL
			// ready 时 taskID 是该产物的任务 ID（非 0），下游靠它取清晰度档位
			transcodeTaskID = taskID
			// 刷新产物访问时间：GC 按它做 LRU 淘汰，
			// 正在被反复播放的产物因此不会被清理
			h.transcode.Touch(taskID)
		case err == nil && taskID > 0:
			playMode = "transcoding"
			transcodeTaskID = taskID
			transcodeProgress = h.transcodeProgress(taskID)
			// 转码期间先给出可播地址，前端可降级先播着，转好后再切 HLS。
			//
			// 但**只对浏览器放得出来的容器**这么做。给一个 rmvb/mkv 的
			// 直链不是"先播着"，而是必然抛 MediaError(4) —— 画面全黑，
			// 还会把"正在转码中"的进度提示挤掉，用户看到的是莫名其妙的
			// 播放失败。这种时候宁可让前端展示一个干净的"转码中"。
			if !service.ContainerUnsupported(&res) {
				if link := h.directURL(&res); link != "" {
					playURL = link
				} else {
					playURL = res.ShareURL
				}
			} else {
				playURL = ""
			}
		default:
			// 转码不可用（引擎忙、ffmpeg 缺失等）→ 退回原画，不阻塞播放
			smoothUnavailable = true
			h.setDirectPlay(&res, &playMode, &playURL)
		}
	case wantSmooth && !transcodeReady:
		smoothUnavailable = true
		h.setDirectPlay(&res, &playMode, &playURL)
	default:
		// 原画：不转码，直接用源文件。
		//
		// 注意这里特意**不看**是否已有 HLS 产物：用户选了原画就是要原始画质，
		// 拿转码产物糊弄过去属于表里不一。
		h.setDirectPlay(&res, &playMode, &playURL)
	}

	// 档位信息：HLS 模式下告知前端可选清晰度
	if playMode == "hls" && transcodeTaskID > 0 {
		qualities = h.qualitiesOf(transcodeTaskID)
	}

	// 断点续播
	var lastPosition int
	if user != nil {
		var rec model.PlayRecord
		if err := h.db.Where("user_id = ? AND resource_id = ?", user.ID, res.ID).
			First(&rec).Error; err == nil {
			lastPosition = rec.Position
		}
	}

	// 播放计数
	h.db.Model(&model.Resource{}).Where("id = ?", res.ID).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))

	util.OK(c, gin.H{
		"resourceId":        res.ID,
		"title":             res.Title,
		"mediaType":         mediaType,
		"ext":               service.ResourceExt(&res),
		"playMode":          playMode,
		"url":               playURL,
		"poster":            res.Cover,
		"duration":          res.Duration,
		"lastPosition":      lastPosition,
		"qualities":         qualities,
		"categoryName":      categoryNameOf(res.Category),
		"transcodeTaskId":   transcodeTaskID,
		"transcodeProgress": transcodeProgress,
		// 本次实际生效的画质（原样回给前端，便于前端纠正 UI 状态）
		"quality": quality,
		// 转码引擎是否可用：不可用时前端把「流畅」按钮置灰，
		// 而不是让孩子点了以后干等一个永远不会来的产物。
		"transcodeAvailable": transcodeReady,
		// 用户点了流畅但转码没接上，已按原画降级
		"smoothUnavailable": smoothUnavailable,
		// 浏览器放不出来的容器：前端据此给出「格式需要转码」而不是
		// 含糊的「地址可能已过期」。转码成功进入 hls 模式后即为 false。
		// 已经交给转码/转换链路（hls / audioformat / ebook）时不算"放不出来"，
		// 否则前端会在音频刚转好的那一刻弹一句"格式不支持"。
		"containerUnsupported": service.ContainerUnsupported(&res) &&
			playMode != "hls" && playMode != "transcoding" &&
			playMode != "audioformat" && playMode != "ebook" && playMode != "imageformat",
		// 电子书用哪个渲染器：epub / txt / cbz / pdf（转换过的统一是 epub）。
		// 前端据此决定用 epub.js、纯文本、图片序列还是 pdf.js。
		"ebookFormat": ebookFormat,
		// 字幕地址（服务端签好票据）。
		//
		// 为什么不让前端自己拼 `/api/subtitle/<id>?t=`：PRO 会把 url 换成
		// 自己的加速流（`/edge/stream/...`），前端手里就没有小书童的票据了，
		// 字幕请求必然 401。由服务端签好带过来，两条链路都能直接用。
		// 非音视频返回空串，前端据此不显示字幕按钮。
		"subtitleUrl": subtitleURLFor(h.secret, res.ID, mediaType),
	})
}

// subtitleURLFor 签发字幕地址。只有音视频才有字幕。
func subtitleURLFor(secret string, id uint64, mediaType string) string {
	if mediaType != model.MediaTypeVideo && mediaType != model.MediaTypeAudio {
		return ""
	}
	exp := time.Now().Add(StreamTicketTTL).Unix()
	return fmt.Sprintf("/api/subtitle/%d?t=%s", id, SignStreamTicket(secret, id, exp))
}

// fallbackDirect 回退到网盘直链；取不到直链则退到网盘页面。
func (h *PlayHandler) fallbackDirect(res *model.Resource, playMode, playURL *string) {
	if res.DiskType == "" || res.ShareURL == "" {
		*playMode = "external"
		*playURL = res.ShareURL
		return
	}
	link, err := h.resolveDirectLink(res)
	if err == nil && link != "" {
		*playMode = "direct"
		*playURL = link
		return
	}
	*playMode = "external"
	*playURL = res.ShareURL
}

// transcodeProgress 读取转码任务当前进度（0~100）。
func (h *PlayHandler) transcodeProgress(taskID uint64) int {
	var t model.TranscodeTask
	if err := h.db.First(&t, taskID).Error; err != nil {
		return 0
	}
	return t.Progress
}

// qualitiesOf 读取某转码任务产出的清晰度档位。
func (h *PlayHandler) qualitiesOf(taskID uint64) []gin.H {
	if h.transcode == nil {
		return nil
	}
	list := h.transcode.VariantsOf(taskID)
	if len(list) == 0 {
		return nil
	}
	out := make([]gin.H, 0, len(list))
	for _, v := range list {
		out = append(out, gin.H{
			"name":      v.Name,
			"height":    v.Height,
			"playlist":  v.Playlist,
			"bandwidth": v.Bandwidth,
		})
	}
	return out
}

// resolveDirectLink 解析资源的直链
func (h *PlayHandler) resolveDirectLink(res *model.Resource) (string, error) {
	link, err := h.resolveDirectLinkObj(res)
	if err != nil {
		return "", err
	}
	return link.URL, nil
}

// resolveDirectLinkObj 与 resolveDirectLink 相同，但保留 Headers。
//
// 需要 headers 的场景（如 probeContainer 要向上游发一个 Range 试探请求）
// 不能只用 URL —— 夸克/UC 的 CDN 缺了 Cookie 会直接 412。
func (h *PlayHandler) resolveDirectLinkObj(res *model.Resource) (*adapter.DirectLink, error) {
	return resolveDirectLinkFor(h.db, h.encryptor, res)
}

// resolveDirectLinkFor 包级版本：播放、容器探测、电子书转换都走这一条。
//
// 抽出来的原因很实际 —— 电子书转换要先把源文件下载到本地，
// 而复刻一份"读账号 → 解密 Cookie → 调适配器 → 写缓存"的逻辑，
// 迟早会和这边漂移（漏了缓存就会每次多打一次网盘 API）。
func resolveDirectLinkFor(db *gorm.DB, encryptor *util.Encryptor, res *model.Resource) (*adapter.DirectLink, error) {
	if res.DiskAccountID == nil || res.FileID == "" {
		return nil, fmt.Errorf("缺少转存信息")
	}
	var acc model.DiskAccount
	if err := db.First(&acc, *res.DiskAccountID).Error; err != nil {
		return nil, fmt.Errorf("账号不存在")
	}
	cookie, err := encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return nil, fmt.Errorf("凭据解密失败")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	a, err := adapter.Get(adapter.DiskType(res.DiskType))
	if err != nil {
		return nil, err
	}
	// 与取流侧共用同一份直链缓存：探测容器格式、取直链这些"前置动作"
	// 没必要每次都真打一次网盘 API（见 linkcache.go 的说明）。
	key := linkCacheKey(*res.DiskAccountID, res.FileID)
	if link, ok := getCachedLink(key); ok {
		return link, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	link, err := a.GetDirectLink(ctx, &adapter.Credential{
		DiskType: adapter.DiskType(res.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, res.FileID)
	if err != nil {
		return nil, err
	}
	putCachedLink(key, link)
	return link, nil
}

// probeTimeout / probeClient 供 probeExtFromLink 向上游做一次极小试探。
//
// 超时给得短是刻意的：这是播放链路上的**前置**调用，它慢一秒，
// 用户点开播放页就多等一秒。宁可探不出来（退回原有行为），也不能拖住播放。
const probeTimeout = 6 * time.Second

var probeClient = &http.Client{Timeout: probeTimeout}

// probeExtFromLink 向上游要 64 字节，从 Content-Type 推断容器格式。
//
// 认不出来、上游报错、网络超时，一律返回空串 —— 调用方据此"什么都不做"。
func probeExtFromLink(ctx context.Context, link *adapter.DirectLink) string {
	if link == nil || link.URL == "" {
		return ""
	}
	if strings.HasPrefix(link.URL, "file://") {
		return "" // 本地硬盘的文件名本身就是路径，不需要探
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return ""
	}
	// 必须带上适配器给的请求头：夸克/UC 的 CDN 缺了 Cookie 直接 412，
	// 那样探到的就是一个错误页的 Content-Type。
	for k, v := range link.Headers {
		req.Header.Set(k, v)
	}
	// 只要头部 64 字节：够 CDN 声明 Content-Type，也不会真把整个文件拉下来
	req.Header.Set("Range", "bytes=0-63")

	resp, err := probeClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	return contentTypeExt(resp.Header.Get("Content-Type"))
}

// probeContainer 补全资源的容器格式（只在 Ext 为空时生效）。
//
// 解决的是一个很具体的坑：**存量网盘资源的 Ext 是空的**。
//   - 入库时标题被剥掉了扩展名（diskimport.Register / instant.go 都这么做）；
//   - 网盘的 FileID 是短 hash（332041b0494f466bab164b06a27261dd），没有后缀。
// 于是 service.NeedsTranscode 判不出 rmvb/mkv，直接按"浏览器能播"代理出去，
// 浏览器一取流就 MediaError(4)，用户看到的是**"播放地址可能已过期"** ——
// 一个和真实原因毫无关系的提示。
//
// 上游 CDN 的 Content-Type 其实会明说（application/vnd.rn-realmedia-vbr 即 RMVB）。
// 原先只靠 StreamHandler 在取流时"顺手学"，代价是**第一次播放必然失败一次**
// （黑屏 → 前端自动重试 → 才开始转码）。这里把学习提前到"决定播放方式之前"，
// 于是第一次就能判对、直接进转码等待。
//
// 成本：每个资源只探一次（认出后 Ext 落库，service.ResourceExt 就不再为空）。
// 全程 best-effort —— 任何一步失败都只是"仍然不知道格式"，退回原有行为。
func (h *PlayHandler) probeContainer(ctx context.Context, res *model.Resource) {
	if res == nil || !canProxy(res) {
		return
	}
	// 探不出来就别每次都探：那是一次真实的网络往返，白等 1~3 秒，
	// 而且每次点开都等（见 probeMissRecently 的注释）。
	if probeMissRecently(res.ID) {
		return
	}
	link, err := h.resolveDirectLinkObj(res)
	if err != nil || link == nil {
		return
	}
	ext := probeExtFromLink(ctx, link)
	if ext == "" {
		markProbeMiss(res.ID)
		return
	}
	clearProbeMiss(res.ID)
	// 条件更新：只在仍然没有格式信息时写，避免覆盖更权威的值
	// （WebDAV 的 FileID 带后缀，比 CDN 的 MIME 更准）。
	if err := h.db.Model(&model.Resource{}).
		Where("id = ? AND (ext IS NULL OR ext = '')", res.ID).
		UpdateColumn("ext", ext).Error; err != nil {
		return
	}
	res.Ext = ext
}

// ReportProgress 上报播放进度
func (h *PlayHandler) ReportProgress(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的资源 ID")
		return
	}
	var req struct {
		Position      int  `json:"position"`
		Duration      int  `json:"duration"`
		PlayedSeconds int  `json:"playedSeconds"`
		Finished      bool `json:"finished"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	userID := util.CurrentUserID(c)
	if userID == 0 {
		util.Unauthorized(c, "")
		return
	}

	var res model.Resource
	if err := h.db.First(&res, id).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}

	// 计算完成度
	percentage := 0.0
	if req.Duration > 0 {
		percentage = float64(req.Position) / float64(req.Duration) * 100
		if percentage > 100 {
			percentage = 100
		}
	}
	if percentage >= 95 && !req.Finished {
		req.Finished = true
	}

	now := time.Now()
	var rec model.PlayRecord
	err := h.db.Where("user_id = ? AND resource_id = ?", userID, id).First(&rec).Error
	if err == gorm.ErrRecordNotFound {
		rec = model.PlayRecord{
			UserID: userID, ResourceID: id,
			Position: req.Position, Duration: req.Duration,
			Percentage: percentage, Finished: req.Finished, PlayedAt: now,
		}
		h.db.Create(&rec)
	} else {
		h.db.Model(&rec).Updates(map[string]interface{}{
			"position":   req.Position,
			"duration":   req.Duration,
			"percentage": percentage,
			"finished":   req.Finished,
			"played_at":  now,
		})
	}

	// 记录学习行为
	categoryName := ""
	if res.CategoryID > 0 {
		var cat model.Category
		if h.db.First(&cat, res.CategoryID).Error == nil {
			categoryName = cat.Name
		}
	}
	action := "play_progress"
	if req.Finished {
		action = "play_finish"
	}
	h.db.Create(&model.StudyRecord{
		UserID:        userID,
		ResourceID:    id,
		CategoryID:    res.CategoryID,
		CategoryName:  categoryName,
		ResourceTitle: res.Title,
		Action:        action,
		Duration:      req.PlayedSeconds,
		Position:      req.Position,
		MediaType:     res.MediaType,
		HappenedAt:    now,
		StudyDate:     now.Format("2006-01-02"),
		StudyHour:     now.Hour(),
	})

	util.OKWithMessage(c, "已记录", nil)
}

// History 播放历史
func (h *PlayHandler) History(c *gin.Context) {
	page, pageSize := util.GetPage(c)
	userID := util.CurrentUserID(c)

	q := h.db.Model(&model.PlayRecord{}).Where("user_id = ?", userID)
	var total int64
	q.Count(&total)

	var records []model.PlayRecord
	q.Order("played_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records)

	type out struct {
		model.PlayRecord
		Title        string `json:"title"`
		Cover        string `json:"cover"`
		CategoryName string `json:"categoryName"`
	}
	list := make([]out, 0, len(records))
	for _, r := range records {
		var res model.Resource
		h.db.Preload("Category").First(&res, r.ResourceID)
		cn := ""
		if res.Category != nil {
			cn = res.Category.Name
		}
		list = append(list, out{
			PlayRecord: r, Title: res.Title, Cover: res.Cover, CategoryName: cn,
		})
	}
	util.Page(c, total, page, pageSize, list)
}

// playlistLimit 单目录播放列表最多返回多少条。
//
// 从 50 提到 500：一个目录 52 集是常态（牛津树），卡在 50 会让
// "顺序播放"莫名其妙断在倒数第三集，而孩子根本不知道为什么。
// 500 条只是几个字段的轻量查询，不构成负担。
const playlistLimit = 500

// Playlist 某目录下的播放列表
//
// 兼容两种取参方式：
//  1. 路径参数 /play/:id/playlist（孩子端使用）
//  2. query 参数 ?resourceId=123（后台使用）
func (h *PlayHandler) Playlist(c *gin.Context) {
	resourceID, _ := util.GetUintParam(c, "id")
	if resourceID == 0 {
		resourceID = util.GetUintQuery(c, "resourceId", 0)
	}
	if resourceID == 0 {
		util.BadRequest(c, "缺少资源 ID")
		return
	}
	var res model.Resource
	if err := h.db.First(&res, resourceID).Error; err != nil {
		util.NotFound(c, "资源不存在")
		return
	}
	// 同目录下的资源整体构成播放列表，按标题「数字自然序」升序。
	//
	// 两处和旧实现不同，都是为了让「顺序播放」真的能顺着往下播：
	//
	//  1. **不再把当前资源强制排到第一位**。原来第二条就是列表头，
	//     于是"当前在哪一集"和"下一集是哪一集"永远差一位，顺序播放
	//     播到第二集就开始绕圈。现在它待在自己该在的位置上。
	//  2. **不再按 sort_order / id 排**，改按「音视频优先 + 标题自然序」
	//     —— 与孩子在目录列表里看到的顺序、以及"这一集后面该是哪一集"
	//     的直觉一致。纯字典序会把 1-10 排到 1-2 前面（见 util.NaturalCompare）。
	//
	//     ⚠️ 排序比较器与目录列表**共用** service.SortItem.Less。
	//     两处各写一份的话，同一个目录会出现"列表里第一个是 A、
	//     点播放下一集却是 B"——列表和播放列表说的是同一件事，必须同口径。
	var items []model.Resource
	// 带上 id asc：截断发生在哪一条必须可复现。不带排序时"取哪 500 条"
	// 由数据库自己决定，同一个目录两次请求可能给出不同的列表。
	h.db.Where("category_id = ?", res.CategoryID).
		Order("id asc").Limit(playlistLimit).Find(&items)

	// 当前资源可能因 limit 被截断，但它必须在列表里：前端要据此标出
	// "正在播放"，顺序播放也要先找得到它才能决定下一集。
	inList := false
	for i := range items {
		if items[i].ID == res.ID {
			inList = true
			break
		}
	}
	if !inList {
		items = append(items, res)
	}

	// 借 service.SortItem 排一遍顺序，再按排好的 id 重排 items ——
	// 这样"怎么排"只有一处实现（列表接口用的是同一份）。
	keys := make([]service.SortItem, 0, len(items))
	for _, it := range items {
		keys = append(keys, service.SortItem{
			ID: it.ID, Title: it.Title, MediaType: it.MediaType, Ext: it.Ext,
		})
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	byID := make(map[uint64]model.Resource, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	ordered := make([]model.Resource, 0, len(keys))
	for _, k := range keys {
		if it, ok := byID[k.ID]; ok {
			ordered = append(ordered, it)
		}
	}
	items = ordered

	list := make([]gin.H, 0, len(items))
	for _, it := range items {
		list = append(list, gin.H{
			"id": it.ID, "title": it.Title,
			"playUrl": fmt.Sprintf("/api/play/%d", it.ID),
			"duration": it.Duration,
		})
	}
	util.OK(c, list)
}

func categoryNameOf(cat *model.Category) string {
	if cat == nil {
		return ""
	}
	return cat.Name
}

// guessMediaType 按标题里的扩展名猜媒体类型。
//
// 实现已下沉到 service（导入链路也要用同一套判断），
// 这里保留薄封装，是为了不改动既有调用处的语义。
func guessMediaType(title string) string {
	return service.GuessMediaType(title)
}
