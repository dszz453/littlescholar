package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ============ PanSou 搜索服务 ============

// PansouClient PanSou 网盘搜索 API 客户端
//
// baseURL/token 支持运行时热更新（见 Configure）。
// 用读写锁而不是裸字段：搜索是并发调用的，而管理后台改配置是低频写。
type PansouClient struct {
	mu      sync.RWMutex
	baseURL string
	token   string
	http    *HTTPDoer
}

func NewPansouClient(baseURL, token string) *PansouClient {
	return &PansouClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    NewHTTPDoer(60 * time.Second),
	}
}

// Configure 运行时更新服务地址与令牌，改完立即生效、无需重启。
//
// 起因是一个真实的坑：用户在管理后台把 pansou_url 从默认的
// http://pansou:8888 改成 http://192.168.31.1:8001 后搜索仍然失败，
// 因为 baseURL 是启动时固化的。界面显示"配置已保存"，实际根本没被用上 ——
// 这种"看起来成功了"的失败最难排查。
func (c *PansouClient) Configure(baseURL, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(baseURL, "/")
	c.token = token
}

// endpoint 取当前基址与令牌的快照，避免请求中途配置被改。
func (c *PansouClient) endpoint(path string) (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL + path, c.token
}

// SearchRequest 搜索请求
type SearchRequest struct {
	Keyword   string   `json:"kw"`
	Res       string   `json:"res,omitempty"`         // merge / all
	Src       string   `json:"src,omitempty"`         // tg / plugin / all
	DiskTypes []string `json:"cloud_types,omitempty"` // baidu/quark/uc/tianyi...
	Filter    *Filter  `json:"filter,omitempty"`
}

// Filter 过滤条件
type Filter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// SearchItem 搜索结果项
type SearchItem struct {
	URL      string   `json:"url"`
	Password string   `json:"password"`
	Note     string   `json:"note"`
	DiskType string   `json:"diskType"`
	Datetime string   `json:"datetime"`
	Source   string   `json:"source"`
	Images   []string `json:"images"`
	// 客户端补充字段
	Alive        string `json:"alive,omitempty"`
	AliveSummary string `json:"aliveSummary,omitempty"`
}

// SearchResponse 搜索响应
type SearchResponse struct {
	Total  int
	CostMs int64
	List   []SearchItem
}

// pansouRawResponse pansou 原始响应结构
type pansouRawResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Total        int `json:"total"`
		MergedByType map[string][]struct {
			URL      string   `json:"url"`
			Password string   `json:"password"`
			Note     string   `json:"note"`
			Datetime string   `json:"datetime"`
			Source   string   `json:"source"`
			Images   []string `json:"images"`
		} `json:"merged_by_type"`
	} `json:"data"`
}

// Search 执行搜索
func (c *PansouClient) Search(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	start := time.Now()

	body := map[string]interface{}{
		"kw": req.Keyword,
	}
	if req.Res != "" {
		body["res"] = req.Res
	} else {
		body["res"] = "merge"
	}
	if req.Src != "" {
		body["src"] = req.Src
	}
	if len(req.DiskTypes) > 0 {
		body["cloud_types"] = req.DiskTypes
	}
	if req.Filter != nil {
		body["filter"] = req.Filter
	}

	apiURL, token := c.endpoint("/api/search")
	headers := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}

	var raw pansouRawResponse
	if err := c.http.PostJSON(ctx, apiURL, body, headers, &raw); err != nil {
		return nil, fmt.Errorf("PanSou 搜索失败: %w", err)
	}
	if raw.Code != 0 {
		return nil, fmt.Errorf("PanSou 返回错误: %s", raw.Message)
	}

	resp := &SearchResponse{
		Total:  raw.Data.Total,
		CostMs: time.Since(start).Milliseconds(),
	}
	for diskType, items := range raw.Data.MergedByType {
		for _, it := range items {
			resp.List = append(resp.List, SearchItem{
				URL:      it.URL,
				Password: it.Password,
				Note:     it.Note,
				DiskType: diskType,
				Datetime: it.Datetime,
				Source:   it.Source,
				Images:   it.Images,
			})
		}
	}
	return resp, nil
}

// ============ 存活检测 ============

// CheckLinkItem 待检测链接
type CheckLinkItem struct {
	DiskType string `json:"disk_type"`
	URL      string `json:"url"`
	Password string `json:"password,omitempty"`
}

// CheckResult 检测结果
type CheckResult struct {
	DiskType      string `json:"disk_type"`
	URL           string `json:"url"`
	NormalizedURL string `json:"normalized_url"`
	State         string `json:"state"` // ok/bad/locked/unsupported/uncertain
	Summary       string `json:"summary"`
	CacheHit      bool   `json:"cache_hit"`
	CheckedAt     int64  `json:"checked_at"`
	ExpiresAt     int64  `json:"expires_at"`
}

// CheckLinks 调用 PanSou 的检测接口
func (c *PansouClient) CheckLinks(ctx context.Context, items []CheckLinkItem) ([]CheckResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	var resp struct {
		Results []CheckResult `json:"results"`
	}
	apiURL, token := c.endpoint("/api/check/links")
	headers := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	body := map[string]interface{}{"items": items}
	if err := c.http.PostJSON(ctx, apiURL, body, headers, &resp); err != nil {
		return nil, fmt.Errorf("存活性检测失败: %w", err)
	}
	return resp.Results, nil
}

// Health 健康检查
func (c *PansouClient) Health(ctx context.Context) (map[string]interface{}, error) {
	var resp map[string]interface{}
	apiURL, _ := c.endpoint("/api/health")
	if err := c.http.GetJSON(ctx, apiURL, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ============ PanCheck 客户端 ============
//
// PanCheck（"网盘链接检查工具"）的真实接口与旧实现假设的完全不同，
// 这也是"接进去但一直不出结果"的原因。实测（http://192.168.31.1:6080）：
//
//	POST /api/v1/auth/login        {"password":"…"}   登录（可选，见下）
//	POST /api/v1/links/check       {"links":[…]}      提交检测，立刻返回
//	GET  /api/v1/links/submissions/{id}               查结果
//	GET  /api/v1/health                              健康检查
//
// 两个关键点：
//
//  1. 鉴权走请求头 **X-Admin-Password**，不是 Bearer，也不是登录换 token。
//     登录接口只是给网页端发会话 cookie，接口调用直接带密码头即可。
//  2. /links/check 是**异步**的：它先返回一个 submission（此时结果可能
//     全在 pending_links 里），要再查 /links/submissions/{id} 才有
//     valid/invalid/locked。旧实现拿 POST 的响应当最终结果，
//     于是永远只能看到"pending"。
type PancheckClient struct {
	mu       sync.RWMutex
	baseURL  string
	password string
	http     *HTTPDoer
}

func NewPancheckClient(baseURL, password string) *PancheckClient {
	return &PancheckClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		// 提交检测本身很快（真正耗时的是它内部的后台任务），
		// 30 秒足够；结果靠轮询拿，不会卡在一个长请求上。
		http: NewHTTPDoer(30 * time.Second),
	}
}

// Configure 运行时更新地址与密码（见 PansouClient.Configure 的说明）。
func (p *PancheckClient) Configure(baseURL, password string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.baseURL = strings.TrimRight(baseURL, "/")
	p.password = password
}

func (p *PancheckClient) endpoint(path string) (string, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.baseURL + path, p.password
}

// Configured 是否已配置地址（未配置时不该发起调用）。
func (p *PancheckClient) Configured() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.baseURL != ""
}

// AliveSummary 把状态码翻译成给用户看的中文说明。
func AliveSummary(state string) string {
	switch state {
	case AliveOK:
		return "有效"
	case AliveBad:
		return "已失效"
	case AliveLocked:
		return "需要提取码"
	case AliveUncertain:
		return "未检出"
	default:
		return ""
	}
}

// pancheckSubmission 一次检测提交的结果快照。
type pancheckSubmission struct {
	ID            int64    `json:"id"`
	Status        string   `json:"status"` // processing / checked
	OriginalLinks []string `json:"original_links"`
	ValidLinks    []string `json:"valid_links"`
	InvalidLinks  []string `json:"invalid_links"`
	LockedLinks   []string `json:"locked_links"`
	PendingLinks  []string `json:"pending_links"`
}

// 存活性状态（与 pansou 的 state 取值保持一致，便于前端统一渲染）
const (
	AliveOK        = "ok"        // 有效
	AliveBad       = "bad"       // 失效
	AliveLocked    = "locked"    // 需要提取码 / 被锁
	AliveUncertain = "uncertain" // 未检出（超时或平台不支持）
)

// Check 批量检测链接，返回 url → 状态。
//
// 会先把结果按 URL 归一化后建索引：PanCheck 返回的是原始链接字符串，
// 而搜索结果里的链接常带参数/大小写差异，直接当 key 会匹配不上。
func (p *PancheckClient) Check(ctx context.Context, links []string) (map[string]string, error) {
	out := make(map[string]string, len(links))
	if len(links) == 0 {
		return out, nil
	}

	sub, err := p.submit(ctx, links)
	if err != nil {
		return out, err
	}

	// 异步任务：pending 还有内容就轮询，最多等 45 秒。
	// 到点还没出结果的一律标 uncertain —— 宁可显示"未检出"，
	// 也不能把没查完的链接说成"有效"。
	deadline := time.Now().Add(45 * time.Second)
	for sub != nil && len(sub.PendingLinks) > 0 && sub.Status != "checked" {
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			sub = nil
		case <-time.After(1200 * time.Millisecond):
		}
		if sub == nil {
			break
		}
		next, err := p.submission(ctx, sub.ID)
		if err != nil {
			break
		}
		sub = next
	}

	for _, u := range links {
		out[NormalizeShareURL(u)] = AliveUncertain
	}
	if sub != nil {
		for _, u := range sub.ValidLinks {
			out[NormalizeShareURL(u)] = AliveOK
		}
		for _, u := range sub.InvalidLinks {
			out[NormalizeShareURL(u)] = AliveBad
		}
		for _, u := range sub.LockedLinks {
			out[NormalizeShareURL(u)] = AliveLocked
		}
		// pending 的保持 uncertain，不覆盖
	}
	return out, nil
}

// submit 提交检测任务。
func (p *PancheckClient) submit(ctx context.Context, links []string) (*pancheckSubmission, error) {
	apiURL, password := p.endpoint("/api/v1/links/check")
	headers := map[string]string{"Content-Type": "application/json"}
	if password != "" {
		headers["X-Admin-Password"] = password
	}
	var resp struct {
		SubmissionID int64    `json:"submission_id"`
		ValidLinks   []string `json:"valid_links"`
		InvalidLinks []string `json:"invalid_links"`
		LockedLinks  []string `json:"locked_links"`
		PendingLinks []string `json:"pending_links"`
	}
	if err := p.http.PostJSON(ctx, apiURL, map[string]interface{}{"links": links}, headers, &resp); err != nil {
		return nil, fmt.Errorf("存活性检测失败: %w", err)
	}
	return &pancheckSubmission{
		ID:           resp.SubmissionID,
		ValidLinks:   resp.ValidLinks,
		InvalidLinks: resp.InvalidLinks,
		LockedLinks:  resp.LockedLinks,
		PendingLinks: resp.PendingLinks,
	}, nil
}

// submission 查询检测结果。
func (p *PancheckClient) submission(ctx context.Context, id int64) (*pancheckSubmission, error) {
	if id <= 0 {
		return nil, fmt.Errorf("无效的检测任务 ID")
	}
	apiURL, password := p.endpoint(fmt.Sprintf("/api/v1/links/submissions/%d", id))
	headers := map[string]string{"Content-Type": "application/json"}
	if password != "" {
		headers["X-Admin-Password"] = password
	}
	var sub pancheckSubmission
	if err := p.http.GetJSON(ctx, apiURL, headers, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// CheckOne 检测单个链接（存活巡检逐条调用时用）。
func (p *PancheckClient) CheckOne(ctx context.Context, diskType, link string) (string, error) {
	res, err := p.Check(ctx, []string{link})
	if err != nil {
		return AliveUncertain, err
	}
	if s, ok := res[NormalizeShareURL(link)]; ok {
		return s, nil
	}
	return AliveUncertain, nil
}

// Health 健康检查
func (p *PancheckClient) Health(ctx context.Context) error {
	var resp map[string]interface{}
	apiURL, _ := p.endpoint("/api/v1/health")
	return p.http.GetJSON(ctx, apiURL, nil, &resp)
}

// 确保 json/url 包被使用（供后续扩展）
var (
	_ = json.Marshal
	_ = url.Parse
	_ = context.Background
)
