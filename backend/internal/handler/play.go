package handler

import (
	"context"
	"fmt"
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
}

func NewPlayHandler(db *gorm.DB, perm *service.PermissionService, encryptor *util.Encryptor, transcode *service.TranscodeService) *PlayHandler {
	return &PlayHandler{db: db, perm: perm, encryptor: encryptor, transcode: transcode}
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
	//  3. direct       —— 拿到网盘直链且无需转码，直接播放
	//  4. external     —— 以上都不可用，回退到网盘原始页面
	playMode := "external"
	playURL := res.ShareURL
	var qualities []gin.H
	var transcodeTaskID uint64
	var transcodeProgress int

	if h.transcode != nil && h.transcode.Enabled() && service.NeedsTranscode(&res) {
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
			// 转码期间先给出直链，前端可降级先播着，转好后再切 HLS
			if link, lerr := h.resolveDirectLink(&res); lerr == nil && link != "" {
				playURL = link
			} else {
				playURL = res.ShareURL
			}
		default:
			// 转码不可用（引擎忙、ffmpeg 缺失等）→ 回退直链
			h.fallbackDirect(&res, &playMode, &playURL)
		}
	} else {
		h.fallbackDirect(&res, &playMode, &playURL)
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
		"playMode":          playMode,
		"url":               playURL,
		"poster":            res.Cover,
		"duration":          res.Duration,
		"lastPosition":      lastPosition,
		"qualities":         qualities,
		"categoryName":      categoryNameOf(res.Category),
		"transcodeTaskId":   transcodeTaskID,
		"transcodeProgress": transcodeProgress,
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
	if res.DiskAccountID == nil || res.FileID == "" {
		return "", fmt.Errorf("缺少转存信息")
	}
	var acc model.DiskAccount
	if err := h.db.First(&acc, *res.DiskAccountID).Error; err != nil {
		return "", fmt.Errorf("账号不存在")
	}
	cookie, err := h.encryptor.Decrypt(acc.CookieEncrypted)
	if err != nil {
		return "", fmt.Errorf("凭据解密失败")
	}
	extra := map[string]string{}
	for k, v := range acc.Extra {
		if s, ok := v.(string); ok {
			extra[k] = s
		}
	}
	a, err := adapter.Get(adapter.DiskType(res.DiskType))
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	link, err := a.GetDirectLink(ctx, &adapter.Credential{
		DiskType: adapter.DiskType(res.DiskType),
		Cookie:   cookie,
		Extra:    extra,
	}, res.FileID)
	if err != nil {
		return "", err
	}
	return link.URL, nil
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

func guessMediaType(title string) string {
	ext := ""
	for i := len(title) - 1; i >= 0; i-- {
		if title[i] == '.' {
			ext = title[i+1:]
			break
		}
		if title[i] == '/' || title[i] == '\\' {
			break
		}
	}
	switch ext {
	case "mp3", "wav", "flac", "aac", "m4a", "ogg", "wma":
		return model.MediaTypeAudio
	case "pdf":
		return model.MediaTypePDF
	case "jpg", "jpeg", "png", "gif", "webp", "bmp":
		return model.MediaTypeImage
	case "mp4", "mkv", "avi", "mov", "wmv", "flv", "rmvb", "ts", "m3u8", "webm":
		return model.MediaTypeVideo
	default:
		return model.MediaTypeVideo
	}
}
