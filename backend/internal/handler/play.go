package handler

import (
	"context"
	"fmt"
	"net/http"
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
}

func NewPlayHandler(db *gorm.DB, perm *service.PermissionService, encryptor *util.Encryptor, transcode *service.TranscodeService, secret string) *PlayHandler {
	return &PlayHandler{db: db, perm: perm, encryptor: encryptor, transcode: transcode, secret: secret}
}

// canProxy 判断资源是否具备"由后端代取流"的条件：
// 必须已登记到某个接入账号，并拿到了源文件 ID/路径。
func canProxy(res *model.Resource) bool {
	return res.DiskAccountID != nil && *res.DiskAccountID > 0 && res.FileID != ""
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
	if mediaType == "" {
		mediaType = guessMediaType(res.Title)
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

	// 先补容器格式，再决定播放方式。
	//
	// 顺序很重要：NeedsTranscode 依赖 ResourceExt，而存量网盘资源这一项是空的。
	// 不先探一次，rmvb/mkv 会被当成"浏览器能播"直接代理出去，第一次必然失败。
	if mediaType == model.MediaTypeVideo && service.ResourceExt(&res) == "" {
		h.probeContainer(c.Request.Context(), &res)
	}

	// DirectPlay（搜全网的即时转存）默认跳过转码，保证"点了就播"。
	//
	// 但仅限于**浏览器原生放得出来**的容器。如果是一个 rmvb/mkv，
	// 跳过转码换来的不是"秒播"，而是 <video> 抛 MediaError 加一句
	// 让人摸不着头脑的"播放地址可能已过期"。这时候宁可贵几分钟。
	skipTranscode := res.DirectPlay && !service.ContainerUnsupported(&res)

	if h.transcode != nil && h.transcode.Enabled() && !skipTranscode && service.NeedsTranscode(&res) {
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
			// 转码不可用（引擎忙、ffmpeg 缺失等）→ 回退可播地址
			h.setDirectPlay(&res, &playMode, &playURL)
		}
	} else {
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
		// 浏览器放不出来的容器：前端据此给出「格式需要转码」而不是
		// 含糊的「地址可能已过期」。转码成功进入 hls 模式后即为 false。
		"containerUnsupported": service.ContainerUnsupported(&res) &&
			playMode != "hls" && playMode != "transcoding",
	})
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
	if res.DiskAccountID == nil || res.FileID == "" {
		return nil, fmt.Errorf("缺少转存信息")
	}
	var acc model.DiskAccount
	if err := h.db.First(&acc, *res.DiskAccountID).Error; err != nil {
		return nil, fmt.Errorf("账号不存在")
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
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
	link, err := h.resolveDirectLinkObj(res)
	if err != nil || link == nil {
		return
	}
	ext := probeExtFromLink(ctx, link)
	if ext == "" {
		return
	}
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
	// 同目录下的其他资源作为播放列表
	var siblings []model.Resource
	h.db.Where("category_id = ? AND id != ?", res.CategoryID, res.ID).
		Order("sort_order asc, id asc").Limit(50).Find(&siblings)

	list := []gin.H{{
		"id": res.ID, "title": res.Title,
		"playUrl": fmt.Sprintf("/api/play/%d", res.ID),
		"duration": res.Duration,
	}}
	for _, s := range siblings {
		list = append(list, gin.H{
			"id": s.ID, "title": s.Title,
			"playUrl": fmt.Sprintf("/api/play/%d", s.ID),
			"duration": s.Duration,
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
