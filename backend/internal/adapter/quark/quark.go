package quark

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// Quark 夸克网盘适配器
// 基于夸克网盘网页版公开接口实现
type Quark struct {
	http *adapter.HTTPClient

	// 扫码会话暂存
	mu       sync.RWMutex
	sessions map[string]*qrSession
}

type qrSession struct {
	Token     string
	CreatedAt time.Time
}

const (
	baseURL    = "https://drive-pc.quark.cn/1/clouddrive"
	accountURL = "https://pan.quark.cn"

	// 扫码相关
	qrCreateURL = "https://uop.quark.cn/cas/ajax/getTokenForQrcodeLogin"
	qrPollURL   = "https://uop.quark.cn/cas/ajax/getServiceTicketByQrcodeToken"
)

func init() {
	adapter.Register(New())
}

// New 创建夸克适配器
func New() *Quark {
	return &Quark{
		http:     adapter.NewHTTPClient(30 * time.Second),
		sessions: make(map[string]*qrSession),
	}
}

func (q *Quark) Type() adapter.DiskType { return adapter.DiskQuark }
func (q *Quark) Name() string           { return "夸克网盘" }

// userAgent 夸克 PC 客户端 UA
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"

// headers 构造通用请求头
func (q *Quark) headers(cookie string) map[string]string {
	return map[string]string{
		"User-Agent": userAgent,
		"Cookie":     cookie,
		"Referer":    "https://pan.quark.cn/",
		"Origin":     "https://pan.quark.cn",
		"Accept":     "application/json, text/plain, */*",
	}
}

// ============ 认证 ============

// GetQRCode 获取扫码登录二维码
func (q *Quark) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var resp struct {
		Status int    `json:"status"`
		Data   struct {
			Members struct {
				Token    string `json:"token"`
				QRCodeURL string `json:"qrcode_url"`
			} `json:"members"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    qrCreateURL,
		Query:  map[string]string{"client_id": "532", "v": "1.2", "request_id": fmt.Sprint(time.Now().UnixMilli())},
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("获取二维码失败: %w", err)
	}
	if resp.Data.Members.Token == "" {
		return nil, fmt.Errorf("夸克返回的二维码 token 为空")
	}

	sessionID := fmt.Sprintf("quark_%d", time.Now().UnixNano())
	q.mu.Lock()
	q.sessions[sessionID] = &qrSession{Token: resp.Data.Members.Token, CreatedAt: time.Now()}
	q.mu.Unlock()

	return &adapter.QRCode{
		SessionID: sessionID,
		Content:   resp.Data.Members.QRCodeURL,
		ExpiresIn: 180,
	}, nil
}

// PollQRCode 轮询扫码状态
func (q *Quark) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	q.mu.RLock()
	sess, ok := q.sessions[sessionID]
	q.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("扫码会话不存在或已过期")
	}
	if time.Since(sess.CreatedAt) > 5*time.Minute {
		delete(q.sessions, sessionID)
		return &adapter.LoginResult{Status: "expired", Message: "二维码已过期"}, nil
	}

	var resp struct {
		Status int    `json:"status"`
		Data   struct {
			Members struct {
				ServiceTicket string `json:"service_ticket"`
				Nickname      string `json:"nickname"`
				Avatar        string `json:"avatar"`
			} `json:"members"`
		} `json:"data"`
		Message string `json:"message"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    qrPollURL,
		Query: map[string]string{
			"client_id": "532",
			"v":         "1.2",
			"token":     sess.Token,
			"request_id": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &resp)
	if err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}

	switch resp.Status {
	case 2000000:
		// 已扫码，待确认
		return &adapter.LoginResult{Status: "scanned", Message: "已扫码，请在手机上确认"}, nil
	case 2000001:
		// 确认成功，用 service_ticket 换 cookie
		ticket := resp.Data.Members.ServiceTicket
		if ticket == "" {
			return &adapter.LoginResult{Status: "failed", Message: "未获取到 service ticket"}, nil
		}
		cookie, err := q.exchangeTicket(ctx, ticket)
		if err != nil {
			return &adapter.LoginResult{Status: "failed", Message: err.Error()}, nil
		}
		delete(q.sessions, sessionID)
		return &adapter.LoginResult{
			Status:   "confirmed",
			Cookie:   cookie,
			Nickname: resp.Data.Members.Nickname,
			Avatar:   resp.Data.Members.Avatar,
		}, nil
	default:
		return &adapter.LoginResult{Status: "waiting", Message: resp.Message}, nil
	}
}

// exchangeTicket 用 service ticket 换取 cookie
func (q *Quark) exchangeTicket(ctx context.Context, ticket string) (string, error) {
	// 夸克登录后 cookie 中关键字段为 __puus / __uid 等
	// 实际通过访问带 ticket 的登录回调地址，从 set-cookie 获取
	loginURL := fmt.Sprintf("%s/uc/api/account/login?ticket=%s", accountURL, url.QueryEscape(ticket))

	req, err := http.NewRequestWithContext(ctx, "GET", loginURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	hc := &httpClientWrapper{}
	return hc.getCookies(req)
}

// ValidateCookie 校验 cookie
func (q *Quark) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Nickname string `json:"nickname"`
			Avatar   string `json:"avatar_url"`
			Vip      bool   `json:"vip"`
			Capacity struct {
				Used  int64 `json:"used"`
				Total int64 `json:"total"`
			} `json:"capacity"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     baseURL + "/account/info",
		Query:   map[string]string{"pr": "ucpro", "fr": "pc", "uc_param_str": ""},
		Headers: q.headers(cred.Cookie),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 || resp.Code != 0 {
		return nil, adapter.ErrCookieInvalid
	}
	return &adapter.AccountInfo{
		Nickname:      resp.Data.Nickname,
		Avatar:        resp.Data.Avatar,
		VIP:           resp.Data.Vip,
		CapacityUsed:  resp.Data.Capacity.Used,
		CapacityTotal: resp.Data.Capacity.Total,
	}, nil
}

// ============ 文件操作 ============

// ListDir 列出目录
func (q *Quark) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "0"
	}
	var resp struct {
		Status int    `json:"status"`
		Code   int    `json:"code"`
		Data   struct {
			List []struct {
				FID       string `json:"fid"`
				FileName  string `json:"file_name"`
				Dir       bool   `json:"dir"`
				Size      int64  `json:"size"`
				UpdatedAt int64  `json:"updated_at"`
				FileType  int    `json:"file_type"`
				ObjCategory string `json:"obj_category"`
			} `json:"list"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     baseURL + "/file/sort",
		Headers: q.headers(cred.Cookie),
		Query: map[string]string{
			"pr":       "ucpro",
			"fr":       "pc",
			"pdir_fid": dirID,
			"_page":    "1",
			"_size":    "200",
			"_sort":    "file_type:asc,updated_at:desc",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 || resp.Code != 0 {
		return nil, fmt.Errorf("列出目录失败: code=%d", resp.Code)
	}

	items := make([]adapter.FileItem, 0, len(resp.Data.List))
	for _, f := range resp.Data.List {
		items = append(items, adapter.FileItem{
			ID:        f.FID,
			Name:      f.FileName,
			IsDir:     f.Dir,
			Size:      f.Size,
			UpdatedAt: time.Unix(f.UpdatedAt, 0).Format(time.RFC3339),
			Ext:       extOf(f.FileName),
		})
	}
	return items, nil
}

// MakeDir 新建目录
func (q *Quark) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "0"
	}
	var resp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			FID string `json:"fid"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/file",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body: map[string]interface{}{
			"pdir_fid":     parentID,
			"file_name":    name,
			"dir_path":     "",
			"dir_init_lock": false,
		},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("新建目录失败: code=%d", resp.Code)
	}
	return resp.Data.FID, nil
}

// DeleteFile 删除
func (q *Quark) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/file/delete",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body:    map[string]interface{}{"action_type": 2, "filelist": fileIDs, "exclude_fids": []string{}},
	}, nil)
	return err
}

// Rename 重命名
func (q *Quark) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/file/rename",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body:    map[string]interface{}{"fid": fileID, "file_name": newName},
	}, nil)
	return err
}

// ============ 分享与转存 ============

// shareURLRe 提取分享 ID
var shareURLRe = regexp.MustCompile(`/s/([0-9a-zA-Z]+)`)

// ParseShare 解析分享链接
func (q *Quark) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	m := shareURLRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法从链接中提取分享 ID: %s", shareURL)
	}
	pwdID := m[1]

	// 1. 获取分享 token（stoken）
	stoken, err := q.getShareToken(ctx, cred, pwdID, passcode)
	if err != nil {
		return nil, err
	}

	// 2. 用 stoken 列出分享内容
	info := &adapter.ShareInfo{ShareURL: shareURL, Passcode: passcode}
	var listResp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			List []struct {
				FID      string `json:"fid"`
				FileName string `json:"file_name"`
				Dir      bool   `json:"dir"`
				Size     int64  `json:"size"`
			} `json:"list"`
			Share struct {
				Title    string `json:"title"`
				ShareURL string `json:"share_url"`
			} `json:"share"`
			ShareToken string `json:"share_token"`
		} `json:"data"`
	}
	_, err = q.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    baseURL + "/share/sharepage/detail",
		Headers: q.headers(cred.Cookie),
		Query: map[string]string{
			"pr":          "ucpro",
			"fr":          "pc",
			"pwd_id":      pwdID,
			"stoken":      stoken,
			"pdir_fid":    "0",
			"force":       "0",
			"_page":       "1",
			"_size":       "200",
			"_fetch_banner": "0",
			"_fetch_share":  "1",
			"_fetch_total":  "1",
			"_sort":         "file_type:asc,updated_at:desc",
		},
	}, &listResp)
	if err != nil {
		return nil, err
	}
	if listResp.Code != 0 {
		return nil, adapter.ErrShareExpired
	}

	info.Title = listResp.Data.Share.Title
	info.RootID = listResp.Data.ShareToken
	for _, f := range listResp.Data.List {
		info.Files = append(info.Files, adapter.FileItem{
			ID:    f.FID,
			Name:  f.FileName,
			IsDir: f.Dir,
			Size:  f.Size,
			Ext:   extOf(f.FileName),
		})
		info.TotalSize += f.Size
	}
	return info, nil
}

// getShareToken 获取分享 stoken
func (q *Quark) getShareToken(ctx context.Context, cred *adapter.Credential, pwdID, passcode string) (string, error) {
	query := map[string]string{
		"pr":     "ucpro",
		"fr":     "pc",
		"pwd_id": pwdID,
	}
	if passcode != "" {
		query["passcode"] = passcode
	}

	var resp struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Stoken string `json:"stoken"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/share/sharepage/token",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body:    map[string]interface{}{"pwd_id": pwdID, "passcode": passcode},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Code == 41013 || resp.Code == 41012 {
		return "", adapter.ErrNeedPassword
	}
	if resp.Code != 0 || resp.Data.Stoken == "" {
		return "", fmt.Errorf("获取分享 token 失败: code=%d msg=%s", resp.Code, resp.Message)
	}
	_ = query
	return resp.Data.Stoken, nil
}

// SaveToDir 转存到指定目录
func (q *Quark) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	if targetDirID == "" {
		targetDirID = "0"
	}
	var fids, tokenFids []string
	for _, f := range share.Files {
		fids = append(fids, f.ID)
		tokenFids = append(tokenFids, f.ID)
	}

	var resp struct {
		Status  int    `json:"status"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/share/sharepage/save",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body: map[string]interface{}{
			"fid_list":       fids,
			"fid_token_list": tokenFids,
			"to_pdir_fid":    targetDirID,
			"pwd_id":         extractPwdID(share.ShareURL),
			"stoken":         share.RootID,
			"pdir_fid":       "0",
			"scene":          "link",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("转存失败: code=%d msg=%s", resp.Code, resp.Message)
	}

	// 转存是异步任务，需要轮询任务状态
	if resp.Data.TaskID != "" {
		if err := q.waitTask(ctx, cred, resp.Data.TaskID); err != nil {
			return nil, err
		}
	}

	return &adapter.SaveResult{
		FileIDs: fids,
		Count:   len(fids),
		Message: fmt.Sprintf("成功转存 %d 个文件", len(fids)),
	}, nil
}

// waitTask 轮询转存任务完成
func (q *Quark) waitTask(ctx context.Context, cred *adapter.Credential, taskID string) error {
	deadline := time.Now().Add(60 * time.Second)
	var lastMsg string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}

		var resp struct {
			Status int    `json:"status"`
			Code   int    `json:"code"`
			Data   struct {
				Status   int    `json:"status"`
				TaskTitle string `json:"task_title"`
				Message  string `json:"message"`
			} `json:"data"`
		}
		_, err := q.http.DoJSON(ctx, &adapter.Request{
			Method:  "GET",
			URL:     baseURL + "/task/" + taskID,
			Headers: q.headers(cred.Cookie),
			Query:   map[string]string{"pr": "ucpro", "fr": "pc", "retry_index": "1"},
		}, &resp)
		if err != nil {
			continue
		}
		// status 2 = 成功, 3 = 失败
		switch resp.Data.Status {
		case 2:
			return nil
		case 3:
			return fmt.Errorf("转存任务失败: %s", resp.Data.Message)
		default:
			lastMsg = resp.Data.Message
		}
	}
	if lastMsg == "" {
		lastMsg = "任务超时"
	}
	return fmt.Errorf("转存任务未在预期时间内完成: %s", lastMsg)
}

// Transfer 一步完成解析 + 转存
func (q *Quark) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := q.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return q.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

// GetDirectLink 获取直链
func (q *Quark) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	var resp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   []struct {
			DownloadURL string `json:"download_url"`
			Size        int64  `json:"size"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     baseURL + "/file/download",
		Headers: q.headers(cred.Cookie),
		Query:   map[string]string{"pr": "ucpro", "fr": "pc"},
		Body:    map[string]interface{}{"fids": []string{fileID}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || resp.Data[0].DownloadURL == "" {
		return nil, fmt.Errorf("获取下载直链失败: code=%d", resp.Code)
	}
	return &adapter.DirectLink{
		URL:          resp.Data[0].DownloadURL,
		Size:         resp.Data[0].Size,
		ExpiresAt:    time.Now().Add(30 * time.Minute).Unix(),
		Headers:      map[string]string{"User-Agent": userAgent, "Referer": "https://pan.quark.cn/"},
		AcceptRanges: true, // 夸克直链支持 Range
	}, nil
}

// GetDownloadInfo 获取下载信息
func (q *Quark) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return q.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

func extractPwdID(shareURL string) string {
	m := shareURLRe.FindStringSubmatch(shareURL)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// httpClientWrapper 用于获取 set-cookie
type httpClientWrapper struct{}

func (h *httpClientWrapper) getCookies(req *http.Request) (string, error) {
	client := &httpClient{}
	return client.do(req)
}
