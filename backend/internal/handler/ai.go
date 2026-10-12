package handler

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
	"gorm.io/gorm"
)

// AIHandler AI 配置管理
type AIHandler struct {
	db        *gorm.DB
	encryptor *util.Encryptor
}

func NewAIHandler(db *gorm.DB, encryptor *util.Encryptor) *AIHandler {
	return &AIHandler{db: db, encryptor: encryptor}
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

	// 取用户可访问的资源目录，作为推荐依据
	var cats []model.Category
	h.db.Where("level <= 2").Order("sort_order asc").Find(&cats)
	var catNames []string
	for _, ct := range cats {
		catNames = append(catNames, ct.Name)
	}

	messages := ai.BuildTutorPrompt(orDefaultStr(req.Grade, ""), catNames)

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

	// 从回复中提取可能的资源推荐
	recommends := h.matchResources(reply)

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

// matchResources 从 AI 回复中匹配系统内资源
func (h *AIHandler) matchResources(reply string) []gin.H {
	var resources []model.Resource
	h.db.Order("view_count desc").Limit(100).Find(&resources)

	var out []gin.H
	seen := map[uint64]bool{}
	for _, r := range resources {
		if len(out) >= 5 {
			break
		}
		if seen[r.ID] {
			continue
		}
		// 标题中任意 2 字以上片段出现在回复里即视为命中
		if containsAnyFragment(reply, r.Title) {
			var cat model.Category
			h.db.First(&cat, r.CategoryID)
			out = append(out, gin.H{
				"id": r.ID, "title": r.Title,
				"categoryId": r.CategoryID, "categoryName": cat.Name,
				"cover": r.Cover, "mediaType": r.MediaType,
			})
			seen[r.ID] = true
		}
	}
	return out
}

func containsAnyFragment(text, title string) bool {
	if len(title) < 4 {
		return len(title) > 0 && contains(text, title)
	}
	// 取标题的连续片段做匹配
	for n := 4; n <= len(title) && n <= 12; n += 2 {
		for i := 0; i+ n <= len(title); i += n {
			frag := title[i : i+n]
			if contains(text, frag) {
				return true
			}
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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
