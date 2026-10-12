package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ============ PanSou 搜索服务 ============

// PansouClient PanSou 网盘搜索 API 客户端
type PansouClient struct {
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

// SearchRequest 搜索请求
type SearchRequest struct {
	Keyword    string   `json:"kw"`
	Res        string   `json:"res,omitempty"`          // merge / all
	Src        string   `json:"src,omitempty"`          // tg / plugin / all
	DiskTypes  []string `json:"cloud_types,omitempty"`  // baidu/quark/uc/tianyi...
	Filter     *Filter  `json:"filter,omitempty"`
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
		Total int `json:"total"`
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

	headers := map[string]string{"Content-Type": "application/json"}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}

	var raw pansouRawResponse
	if err := c.http.PostJSON(ctx, c.baseURL+"/api/search", body, headers, &raw); err != nil {
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
	headers := map[string]string{"Content-Type": "application/json"}
	if c.token != "" {
		headers["Authorization"] = "Bearer " + c.token
	}
	body := map[string]interface{}{"items": items}
	if err := c.http.PostJSON(ctx, c.baseURL+"/api/check/links", body, headers, &resp); err != nil {
		return nil, fmt.Errorf("存活性检测失败: %w", err)
	}
	return resp.Results, nil
}

// Health 健康检查
func (c *PansouClient) Health(ctx context.Context) (map[string]interface{}, error) {
	var resp map[string]interface{}
	if err := c.http.GetJSON(ctx, c.baseURL+"/api/health", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ============ PanCheck 客户端 ============

// PancheckClient PanCheck 存活检测客户端（补充方案）
type PancheckClient struct {
	baseURL  string
	password string
	http     *HTTPDoer
}

func NewPancheckClient(baseURL, password string) *PancheckClient {
	return &PancheckClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		http:     NewHTTPDoer(30 * time.Second),
	}
}

// CheckOne 检测单个链接
func (p *PancheckClient) CheckOne(ctx context.Context, diskType, link string) (string, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	if p.password != "" {
		headers["X-Admin-Password"] = p.password
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Valid bool   `json:"valid"`
			Status string `json:"status"`
		} `json:"data"`
	}
	body := map[string]interface{}{"url": link, "platform": diskType}
	if err := p.http.PostJSON(ctx, p.baseURL+"/api/v1/check", body, headers, &resp); err != nil {
		return "uncertain", err
	}
	if resp.Data.Valid {
		return "ok", nil
	}
	return "bad", nil
}

// Health 健康检查
func (p *PancheckClient) Health(ctx context.Context) error {
	var resp map[string]interface{}
	return p.http.GetJSON(ctx, p.baseURL+"/api/v1/health", nil, &resp)
}

// 确保 json/url 包被使用（供后续扩展）
var (
	_ = json.Marshal
	_ = url.Parse
	_ = context.Background
)
