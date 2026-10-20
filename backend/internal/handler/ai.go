package handler

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/service"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AIHandler AI 配置管理
type AIHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
	// 目录可见性。main 与 router 两处都会注入；为 nil 时按"不限"处理，
	// 这样单测里 new 出来的实例仍然可用（见 ai_tutor.go 的 visibleCategoryIDs）。
	perm *service.PermissionService
}

func NewAIHandler(db *gorm.DB, encryptor *util.Encryptor) *AIHandler {
	return &AIHandler{db: db, encryptor: encryptor}
}

// WithPerm 注入权限服务。链式调用，与 DigestHandler.WithLogger 的用法一致 ——
// 不直接改 NewAIHandler 签名，免得动到一堆测试。
func (h *AIHandler) WithPerm(p *service.PermissionService) *AIHandler {
	h.perm = p
	return h
}

// ListConfigs 配置列表
func (h *AIHandler) ListConfigs(c *gin.Context) {
	var configs []model.AIConfig
	if err := h.db.Order("is_default desc, id asc").Find(&configs).Error; err != nil {
		util.ServerError(c, "查询失败")
		return
	}
	util.OK(c, configs)
}

type saveAIConfigRequest struct {
	Name        string  `json:"name" binding:"required"`
	Provider    string  `json:"provider"`
	BaseURL     string  `json:"baseUrl" binding:"required"`
	APIKey      string  `json:"apiKey"`
	Model       string  `json:"model" binding:"required"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"maxTokens"`
	TimeoutSec  int     `json:"timeoutSec"`
	IsDefault   bool    `json:"isDefault"`
	Remark      string  `json:"remark"`
}

// CreateConfig 新增 AI 配置
func (h *AIHandler) CreateConfig(c *gin.Context) {
	var req saveAIConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "名称、接口地址、模型不能为空")
		return
	}
	encrypted, err := h.encryptor.Encrypt(req.APIKey)
	if err != nil {
		util.ServerError(c, "密钥加密失败")
		return
	}
	cfg := &model.AIConfig{
		Name:            req.Name,
		Provider:        orDefaultStr(req.Provider, "custom"),
		BaseURL:         req.BaseURL,
		APIKeyEncrypted: encrypted,
		APIKeyMasked:    util.MaskSecret(req.APIKey),
		Model:           req.Model,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		TimeoutSec:      req.TimeoutSec,
		IsDefault:       req.IsDefault,
		Remark:          req.Remark,
		Status:          "unknown",
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault {
			tx.Model(&model.AIConfig{}).Update("is_default", false)
		}
		return h.db.Create(cfg).Error
	})
	if err != nil {
		util.ServerError(c, "创建失败")
		return
	}
	util.OK(c, gin.H{"id": cfg.ID})
}

// UpdateConfig 更新 AI 配置
func (h *AIHandler) UpdateConfig(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的配置 ID")
		return
	}
	var cfg model.AIConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		util.NotFound(c, "配置不存在")
		return
	}
	var req saveAIConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "参数错误")
		return
	}

	updates := map[string]interface{}{
		"name":        req.Name,
		"provider":    orDefaultStr(req.Provider, cfg.Provider),
		"base_url":    req.BaseURL,
		"model":       req.Model,
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"timeout_sec": req.TimeoutSec,
		"is_default":  req.IsDefault,
		"remark":      req.Remark,
	}
	// 只有传了新 key 才更新
	if req.APIKey != "" {
		encrypted, err := h.encryptor.Encrypt(req.APIKey)
		if err != nil {
			util.ServerError(c, "密钥加密失败")
			return
		}
		updates["api_key_encrypted"] = encrypted
		updates["api_key_masked"] = util.MaskSecret(req.APIKey)
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if req.IsDefault {
			tx.Model(&model.AIConfig{}).Where("id != ?", id).Update("is_default", false)
		}
		return tx.Model(&cfg).Updates(updates).Error
	})
	if err != nil {
		util.ServerError(c, "更新失败")
		return
	}
	util.OKWithMessage(c, "更新成功", nil)
}

// DeleteConfig 删除配置
func (h *AIHandler) DeleteConfig(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的配置 ID")
		return
	}
	if err := h.db.Delete(&model.AIConfig{}, id).Error; err != nil {
		util.ServerError(c, "删除失败")
		return
	}
	util.OKWithMessage(c, "删除成功", nil)
}

// TestConfig 测试连通性
func (h *AIHandler) TestConfig(c *gin.Context) {
	id, ok := util.GetUintParam(c, "id")
	if !ok {
		util.BadRequest(c, "无效的配置 ID")
		return
	}
	client, err := h.buildClient(id)
	if err != nil {
		util.NotFound(c, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	latency, reply, err := client.Ping(ctx)
	if err != nil {
		h.db.Model(&model.AIConfig{}).Where("id = ?", id).Update("status", "invalid")
		util.OK(c, gin.H{"ok": false, "latencyMs": latency, "message": err.Error()})
		return
	}
	h.db.Model(&model.AIConfig{}).Where("id = ?", id).Update("status", "valid")
	util.OK(c, gin.H{"ok": true, "latencyMs": latency, "reply": reply})
}

// buildClient 根据配置 ID 构造 AI 客户端
func (h *AIHandler) buildClient(id uint64) (*ai.Client, error) {
	var cfg model.AIConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		return nil, errNotFound
	}
	key, err := h.encryptor.Decrypt(cfg.APIKeyEncrypted)
	if err != nil {
		return nil, errDecrypt
	}
	return ai.NewClient(ai.Config{
		BaseURL:     cfg.BaseURL,
		APIKey:      key,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		TimeoutSec:  cfg.TimeoutSec,
	}), nil
}

// DefaultClient 取默认 AI 客户端（供学情分析使用）
func (h *AIHandler) DefaultClient() (*ai.Client, uint64, error) {
	var cfg model.AIConfig
	if err := h.db.Where("is_default = ?", true).First(&cfg).Error; err != nil {
		if err := h.db.Order("id asc").First(&cfg).Error; err != nil {
			return nil, 0, err
		}
	}
	key, err := h.encryptor.Decrypt(cfg.APIKeyEncrypted)
	if err != nil {
		return nil, 0, err
	}
	return ai.NewClient(ai.Config{
		BaseURL:     cfg.BaseURL,
		APIKey:      key,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		TimeoutSec:  cfg.TimeoutSec,
	}), cfg.ID, nil
}

var (
	errNotFound = gorm.ErrRecordNotFound
	errDecrypt  = errDecryptErr{}
)

type errDecryptErr struct{}

func (errDecryptErr) Error() string { return "API 密钥解密失败" }

// ============ 智能体问答 ============

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Message   string        `json:"message"`   // 兼容旧版：单条消息
	Messages  []chatMessage `json:"messages"`  // 新版：多轮消息数组（前端 Tutor 使用）
	SessionID string        `json:"sessionId"`
	Grade     string        `json:"grade"`
}

// Chat 智能体问答（结合系统资源推荐）
func (h *AIHandler) Chat(c *gin.Context) {
	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		util.BadRequest(c, "请输入问题")
		return
	}

	// 兼容两种调用形态：
	//  - 旧版：{ message: "..." }，单条提问
	//  - 新版：{ messages: [{role, content}, ...] }，孩子端 Tutor 的多轮上下文
	// 新版取最后一条 user 消息作为本轮问题；旧版直接用 message。
	userText := strings.TrimSpace(req.Message)
	if userText == "" && len(req.Messages) > 0 {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				userText = strings.TrimSpace(req.Messages[i].Content)
				break
			}
		}
	}
	if userText == "" {
		util.BadRequest(c, "请输入问题")
		return
	}

	client, _, err := h.DefaultClient()
	if err != nil {
		util.ServerError(c, "未配置 AI 接口，请先在后台配置")
		return
	}

	userID := util.CurrentUserID(c)
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = util.NewSessionID()
		h.db.Create(&model.AIChatSession{
			SessionID: sessionID, UserID: userID,
			Title: truncate(userText, 30),
		})
	}

	// 先弄清楚"这个孩子能看到什么"，再让 AI 开口。
	//
	// 次序很关键：可见范围既决定提示词里的目录概况，也决定检索的边界，
	// 两处必须是同一个 scope —— 否则会出现"推荐的目录孩子点不进去"。
	// 改之前这里写的是 `level <= 2`，既没按权限过滤，也漏掉了三级以下。
	scopeIDs, unrestricted, err := h.visibleCategoryIDs(c)
	if err != nil {
		util.ServerError(c, "读取目录权限失败")
		return
	}
	briefs := h.tutorBriefs(scopeIDs, unrestricted)
	// 检索一遍再问 AI：它得先知道"库里到底有什么能对上这个问题"，
	// 才谈得上推荐具体内容，而不是每次都回"去乐学英语看看吧"。
	hits := h.tutorHits(userText, scopeIDs, unrestricted, 12)

	messages := ai.BuildTutorPrompt(
		orDefaultStr(req.Grade, ""),
		formatBriefs(briefs),
		toHints(hits),
	)

	// 优先用请求里携带的多轮上下文（前端已把最近若干轮带上），
	// 避免重复查库历史导致上下文不一致；否则回退到按 session 查库。
	if len(req.Messages) > 0 {
		for _, m := range req.Messages {
			content := strings.TrimSpace(m.Content)
			if content == "" {
				continue
			}
			role := m.Role
			if role != "user" && role != "assistant" {
				role = "user"
			}
			messages = append(messages, ai.Message{Role: role, Content: content})
		}
	} else {
		var history []model.AIChatMessage
		h.db.Where("session_id = ?", sessionID).Order("id asc").Limit(10).Find(&history)
		for _, m := range history {
			messages = append(messages, ai.Message{Role: m.Role, Content: m.Content})
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()

	reply, err := client.Chat(ctx, messages)
	if err != nil {
		util.ServerError(c, "AI 调用失败: "+err.Error())
		return
	}

	// 先从"这次检索出来的候选"里挑 AI 提到的 —— 卡片基本都来自这里，
	// 因为上面已经把这些标题原样告诉了 AI，它提到时是照抄的。
	recommends := h.pickFromHits(reply, hits)
	if len(recommends) == 0 {
		// 兜底：AI 可能提到了一个真实存在、但我们这轮没检索出来的资源
		recommends = h.matchResources(reply, scopeIDs, unrestricted)
	}

	// 保存对话（只记本轮问答，避免与前端上下文重复）
	h.db.Create(&model.AIChatMessage{SessionID: sessionID, Role: "user", Content: userText})
	h.db.Create(&model.AIChatMessage{SessionID: sessionID, Role: "assistant", Content: reply})

	// 记录学习行为
	h.recordStudy(userID, "ai_chat", "", "")

	util.OK(c, gin.H{
		// 前端 Tutor 读取的字段
		"content":    reply,
		"resources":  recommends,
		"sessionId":  sessionID,
		// 保留旧字段，避免破坏潜在的其他调用方
		"reply":              reply,
		"recommendResources": recommends,
	})
}

// matchResources 兜底匹配：从「这个孩子看得到的」热门资源里找 AI 提到的。
//
// 注意权限：改之前这里直接查全站 view_count 前 100，没有任何过滤 ——
// 孩子可能拿到一张"自己无权访问"的资源卡片，点进去是 404。
func (h *AIHandler) matchResources(reply string, scopeIDs []uint64, unrestricted bool) []gin.H {
	q := h.db.Model(&model.Resource{}).Preload("Category")
	if !unrestricted {
		if len(scopeIDs) == 0 {
			return nil
		}
		q = q.Where("category_id IN ?", scopeIDs)
	}
	var resources []model.Resource
	if err := q.Order("view_count desc").Limit(200).Find(&resources).Error; err != nil {
		return nil
	}

	out := make([]gin.H, 0, 5)
	for _, r := range resources {
		if len(out) >= 5 {
			break
		}
		// 兜底扫的是全库热门，片段要求放长（6 字），否则很容易把不相关的热门
		// 资源当成"AI 推荐的"塞给孩子 —— 这比不给卡片更糟。
		if containsAnyFragment(reply, r.Title, 6) {
			out = append(out, resourceCard(r))
		}
	}
	return out
}

// containsAnyFragment 判断标题的某个连续片段是否出现在文本里。
//
// ⚠️ 两个要点，都是踩过才知道的：
//
//  1. **必须按 rune 切，不能按字节**。中文一个字 3 字节，`title[i:i+n]`
//     用字节索引切出来会是"一个半字"这种碎片 —— 它不可能出现在任何文本里，
//     于是整套匹配对中文等于完全失效（只有 n 恰好是 3 的倍数、且 i 也从 0
//     起步时才蒙对几次）。实测就是这样：孩子问恐龙，卡片给的却是
//     "the big snowball"。
//  2. **片段下限要跟着用途走**。候选集内的匹配可以用 4 个字；
//     兜底匹配扫的是全库热门，片段太短会把"随便谁都能撞上"的标题也捞进来，
//     所以要求 6 个字。
//
// 片段上限取 minFrag+2：再长就要求 AI 一字不差地引用整句，命中率反而下降。
func containsAnyFragment(text, title string, minFrag int) bool {
	tr := []rune(title)
	if len(tr) < minFrag {
		return len(tr) > 0 && strings.Contains(text, title)
	}
	maxN := minFrag + 2
	for n := minFrag; n <= len(tr) && n <= maxN; n++ {
		for i := 0; i+n <= len(tr); i++ {
			if strings.Contains(text, string(tr[i:i+n])) {
				return true
			}
		}
	}
	return false
}

func (h *AIHandler) recordStudy(userID uint64, action, resourceTitle, categoryName string) {
	now := time.Now()
	h.db.Create(&model.StudyRecord{
		UserID:        userID,
		Action:        action,
		ResourceTitle: resourceTitle,
		CategoryName:  categoryName,
		HappenedAt:    now,
		StudyDate:     now.Format("2006-01-02"),
		StudyHour:     now.Hour(),
	})
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
