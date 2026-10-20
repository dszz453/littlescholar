package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/service"
)

// Client AI 客户端（兼容 OpenAI Chat Completions 协议）
// 适配 OpenAI / DeepSeek / 通义千问(兼容模式) / Ollama / 任意兼容服务
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	http        *service.HTTPDoer
}

// Config AI 配置
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	MaxTokens   int
	TimeoutSec  int
}

// NewClient 创建 AI 客户端
func NewClient(cfg Config) *Client {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	temp := cfg.Temperature
	if temp == 0 {
		temp = 0.7
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &Client{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		temperature: temp,
		maxTokens:   maxTokens,
		http:        service.NewHTTPDoer(timeout),
	}
}

// Message 对话消息
type Message struct {
	Role    string `json:"role"` // system / user / assistant
	Content string `json:"content"`
}

// chatRequest OpenAI 格式请求
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

// chatResponse OpenAI 格式响应
type chatResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Index   int     `json:"index"`
		Message Message `json:"message"`
		Finish  string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Model 返回当前使用的模型名（写进产物/任务记录，方便回看是谁生成的）。
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Chat 发起对话
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
		Stream:      false,
	}

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + c.apiKey,
	}

	var resp chatResponse
	endpoint := c.baseURL + "/chat/completions"
	if err := c.http.PostJSON(ctx, endpoint, reqBody, headers, &resp); err != nil {
		return "", fmt.Errorf("AI 请求失败: %w", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("AI 返回错误: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("AI 未返回任何内容")
	}
	return resp.Choices[0].Message.Content, nil
}

// ChatJSON 要求 AI 返回 JSON 并解析
func (c *Client) ChatJSON(ctx context.Context, messages []Message, out interface{}) error {
	content, err := c.Chat(ctx, messages)
	if err != nil {
		return err
	}
	content = extractJSON(content)
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return fmt.Errorf("AI 返回内容无法解析为 JSON: %w (原始: %s)", err, truncateStr(content, 500))
	}
	return nil
}

// Ping 测试连通性
func (c *Client) Ping(ctx context.Context) (int64, string, error) {
	start := time.Now()
	reply, err := c.Chat(ctx, []Message{
		{Role: "user", Content: "请回复两个字：正常"},
	})
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, "", err
	}
	return latency, reply, nil
}

// extractJSON 从 AI 回复中提取 JSON（处理 ```json 包裹的情况）
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// 去掉 markdown 代码块
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	// 找到第一个 { 或 [
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return s
	}
	// 从后往前找最后一个 } 或 ]
	end := strings.LastIndexAny(s, "}]")
	if end < 0 || end < start {
		return s[start:]
	}
	return s[start : end+1]
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ============ Prompt 模板 ============

// BuildStudyAnalysisPrompt 构造学情分析提示词
func BuildStudyAnalysisPrompt(childName, grade string, stats interface{}, resources []string) []Message {
	systemPrompt := `你是一位经验丰富的儿童教育顾问和学习规划师。
你需要根据孩子的学习行为数据，生成一份专业、温暖、可执行的学习分析报告。

要求：
1. 用鼓励性的语气，先肯定孩子的努力
2. 客观分析学习时长分布、科目偏好、学习规律
3. 指出可能的薄弱环节（不要批评，用建议的方式表达）
4. 给出具体可执行的改进建议，最好结合系统内已有的资源
5. 建议要适合孩子的年级水平

请严格按照以下 JSON 格式返回（不要有多余文字）：
{
  "summary": "总体评价，150字左右",
  "highlights": ["亮点1", "亮点2", "亮点3"],
  "weaknesses": ["待改进点1", "待改进点2"],
  "suggestions": ["具体建议1", "具体建议2", "具体建议3"]
}`

	userPrompt := fmt.Sprintf(`孩子姓名：%s
年级：%s

学习数据统计：
%s

孩子可访问的系统资源目录：
%s

请生成学习分析报告。`, childName, grade, toJSONPretty(stats), strings.Join(resources, "、"))

	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
}

// TutorResourceHint 一条候选资源（标题 + 它所在的目录名）。
type TutorResourceHint struct {
	Title    string
	Category string
}

// BuildTutorPrompt 构造「小书童」的系统提示词。
//
// availableResources 是目录概况（带缩进与资源数），hits 是本次提问检索出来的
// 具体资源。以前只给一张目录名清单，结果 AI 永远只能泛泛而谈 —— 它没法推荐
// 自己都不知道存在的东西。
func BuildTutorPrompt(grade string, availableResources []string, hits []TutorResourceHint) []Message {
	var hitBlock string
	if len(hits) == 0 {
		hitBlock = "（这次没有从资源库里检索到直接相关的内容。别硬凑：可以推荐下面的目录方向，或者干脆聊学习方法。）"
	} else {
		var b strings.Builder
		for i, hit := range hits {
			fmt.Fprintf(&b, "%d. %s", i+1, hit.Title)
			if hit.Category != "" {
				fmt.Fprintf(&b, "（%s）", hit.Category)
			}
			b.WriteString("\n")
		}
		hitBlock = strings.TrimRight(b.String(), "\n")
	}

	systemPrompt := fmt.Sprintf(`你是「小书童」，一位亲切耐心的儿童学习助手，陪着 %s 的孩子一起学习。

【这个孩子能看到的内容】
%s

【这次提问，系统刚从资源库里检出的相关资源】
%s

怎么回答：
- 语言亲切、简洁。孩子自己看得懂，家长看也不觉得幼稚。
- **优先推荐上面「检出的相关资源」**。要提到它们时，请把标题**一字不改地完整写出来**
  —— 系统靠标题把这些资源变成可点的卡片，写错一个字卡片就出不来了。
- 检出的资源不合适，就推荐「能看到的内容」里的方向，或者直接讲学习方法，别硬凑。
- 一次给 2-4 个具体内容，不要只说一句"去看看 XX"。
- 库里没有的东西不要编。不确定就说"乐园里我暂时没找到"。
- 孩子如果只是闲聊、问天气、说心情，就正常聊，不用往学习上扯。`,
		orDefault(grade, "小学"),
		strings.Join(availableResources, "\n"),
		hitBlock)

	return []Message{{Role: "system", Content: systemPrompt}}
}

func toJSONPretty(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
