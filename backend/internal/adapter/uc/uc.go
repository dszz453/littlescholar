package uc

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// UC UC网盘适配器
//
// UC网盘与夸克同属阿里体系，接口协议高度相似（UC 是夸克的姊妹产品），
// 主要差异在 baseURL、client_id 与部分字段命名。
type UC struct {
	http *adapter.HTTPClient
}

func init() {
	adapter.Register(New())
}

func New() *UC {
	return &UC{http: adapter.NewHTTPClient(30 * time.Second)}
}

func (u *UC) Type() adapter.DiskType { return adapter.DiskUC }
func (u *UC) Name() string           { return "UC网盘" }

const (
	apiBase = "https://pc-api.uc.cn/1/clouddrive"
	ucPan   = "https://drive.uc.cn"
	// UC 的 client_id
	clientID = "5"
	ucUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) uc-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
)

func (u *UC) headers(cookie string) map[string]string {
	return map[string]string{
		"User-Agent": ucUA,
		"Cookie":     cookie,
		"Referer":    "https://drive.uc.cn/",
		"Origin":     "https://drive.uc.cn",
		"Accept":     "application/json, text/plain, */*",
	}
}

// ============ 认证 ============

func (u *UC) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var resp struct {
		Status int `json:"status"`
		Data   struct {
			Members struct {
				Token     string `json:"token"`
				QRCodeURL string `json:"qrcode_url"`
			} `json:"members"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    "https://uop.uc.cn/cas/ajax/getTokenForQrcodeLogin",
		Query:  map[string]string{"client_id": clientID, "v": "1.2", "request_id": fmt.Sprint(time.Now().UnixMilli())},
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("获取 UC 二维码失败: %w", err)
	}
	if resp.Data.Members.Token == "" {
		return nil, fmt.Errorf("UC 返回的二维码 token 为空")
	}
	return &adapter.QRCode{
		SessionID: fmt.Sprintf("uc_%d", time.Now().UnixNano()),
		Content:   resp.Data.Members.QRCodeURL,
		ExpiresIn: 180,
	}, nil
}

func (u *UC) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	// UC 与夸克同源，这里返回等待状态，实际扫码确认后由 adapter 层填入 cookie
	// 生产环境应保存 token 并轮询，参见夸克实现
	return &adapter.LoginResult{
		Status:  "waiting",
		Message: "请在 UC App 中扫码确认（建议使用手动 Cookie 方式绑定以获得更稳定的会话）",
	}, nil
}

func (u *UC) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			Nickname string `json:"nickname"`
			Avatar   string `json:"avatar_url"`
			Vip      bool   `json:"vip"`
			Capacity struct {
				Used  int64 `json:"used"`
				Total int64 `json:"total"`
			} `json:"capacity"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/account/info",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
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

func (u *UC) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "0"
	}
	var resp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			List []struct {
				FID         string `json:"fid"`
				FileName    string `json:"file_name"`
				Dir         bool   `json:"dir"`
				Size        int64  `json:"size"`
				UpdatedAt   int64  `json:"updated_at"`
			} `json:"list"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/file/sort",
		Headers: u.headers(cred.Cookie),
		Query: map[string]string{
			"pr": "UCBrowser", "fr": "pc", "pdir_fid": dirID,
			"_page": "1", "_size": "200", "_sort": "file_type:asc,updated_at:desc",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("列出 UC 目录失败: code=%d", resp.Code)
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

func (u *UC) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "0"
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			FID string `json:"fid"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/file",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body: map[string]interface{}{
			"pdir_fid": parentID, "file_name": name,
			"dir_path": "", "dir_init_lock": false,
		},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("UC 新建目录失败: code=%d", resp.Code)
	}
	return resp.Data.FID, nil
}

func (u *UC) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/file/delete",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body:    map[string]interface{}{"action_type": 2, "filelist": fileIDs, "exclude_fids": []string{}},
	}, nil)
	return err
}

func (u *UC) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/file/rename",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body:    map[string]interface{}{"fid": fileID, "file_name": newName},
	}, nil)
	return err
}

// ============ 分享与转存 ============

var ucShareRe = regexp.MustCompile(`/s/([0-9a-zA-Z]+)`)

func (u *UC) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	m := ucShareRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法提取 UC 分享 ID: %s", shareURL)
	}
	pwdID := m[1]

	// 1. 获取 stoken
	var tokenResp struct {
		Code int `json:"code"`
		Data struct {
			Stoken string `json:"stoken"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/share/sharepage/token",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body:    map[string]interface{}{"pwd_id": pwdID, "passcode": passcode},
	}, &tokenResp)
	if err != nil {
		return nil, err
	}
	if tokenResp.Code == 41013 || tokenResp.Code == 41012 {
		return nil, adapter.ErrNeedPassword
	}
	if tokenResp.Code != 0 {
		return nil, adapter.ErrShareExpired
	}

	// 2. 列出分享内容
	var listResp struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				FID      string `json:"fid"`
				FileName string `json:"file_name"`
				Dir      bool   `json:"dir"`
				Size     int64  `json:"size"`
			} `json:"list"`
			ShareToken string `json:"share_token"`
		} `json:"data"`
	}
	_, err = u.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    apiBase + "/share/sharepage/detail",
		Headers: u.headers(cred.Cookie),
		Query: map[string]string{
			"pr": "UCBrowser", "fr": "pc", "pwd_id": pwdID,
			"stoken": tokenResp.Data.Stoken, "pdir_fid": "0",
			"force": "0", "_page": "1", "_size": "200",
		},
	}, &listResp)
	if err != nil {
		return nil, err
	}
	if listResp.Code != 0 {
		return nil, fmt.Errorf("UC 分享列出失败: code=%d", listResp.Code)
	}

	info := &adapter.ShareInfo{
		ShareURL: shareURL,
		Passcode: passcode,
		RootID:   tokenResp.Data.Stoken,
		Title:    fmt.Sprintf("UC分享_%s", pwdID),
	}
	for _, f := range listResp.Data.List {
		info.Files = append(info.Files, adapter.FileItem{
			ID: f.FID, Name: f.FileName, IsDir: f.Dir, Size: f.Size, Ext: extOf(f.FileName),
		})
		info.TotalSize += f.Size
	}
	if len(info.Files) == 1 {
		info.Title = info.Files[0].Name
	}
	return info, nil
}

func (u *UC) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	if targetDirID == "" {
		targetDirID = "0"
	}
	var fids []string
	for _, f := range share.Files {
		fids = append(fids, f.ID)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/share/sharepage/save",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body: map[string]interface{}{
			"fid_list": fids, "fid_token_list": fids,
			"to_pdir_fid": targetDirID,
			"pwd_id":      extractPwdID(share.ShareURL),
			"stoken":      share.RootID,
			"pdir_fid":    "0", "scene": "link",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("UC 转存失败: code=%d", resp.Code)
	}
	return &adapter.SaveResult{
		FileIDs: fids, Count: len(fids),
		Message: fmt.Sprintf("成功转存 %d 个文件", len(fids)),
	}, nil
}

func (u *UC) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := u.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return u.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

func (u *UC) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	var resp struct {
		Code int `json:"code"`
		Data []struct {
			DownloadURL string `json:"download_url"`
			Size        int64  `json:"size"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/file/download",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body:    map[string]interface{}{"fids": []string{fileID}},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 || resp.Data[0].DownloadURL == "" {
		return nil, fmt.Errorf("UC 获取直链失败: code=%d", resp.Code)
	}
	return &adapter.DirectLink{
		URL:          resp.Data[0].DownloadURL,
		Size:         resp.Data[0].Size,
		ExpiresAt:    time.Now().Add(30 * time.Minute).Unix(),
		Headers:      map[string]string{"User-Agent": ucUA, "Referer": "https://drive.uc.cn/"},
		AcceptRanges: true,
	}, nil
}

func (u *UC) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return u.GetDirectLink(ctx, cred, fileID)
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
	m := ucShareRe.FindStringSubmatch(shareURL)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}
