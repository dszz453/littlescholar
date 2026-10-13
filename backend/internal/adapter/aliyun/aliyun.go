package aliyun

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// Aliyun 阿里云盘适配器（使用 refresh_token 方式认证）
type Aliyun struct {
	http *adapter.HTTPClient
}

func init() {
	adapter.Register(New())
}

func New() *Aliyun {
	return &Aliyun{http: adapter.NewHTTPClient(30 * time.Second)}
}

func (a *Aliyun) Type() adapter.DiskType { return adapter.DiskAliyun }
func (a *Aliyun) Name() string           { return "阿里云盘" }

const (
	apiBase = "https://api.aliyundrive.com"
	authURL = "https://auth.aliyundrive.com/v2/account/token"
	ua      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
)

// refreshToken 换取 accessToken
func (a *Aliyun) refreshToken(ctx context.Context, cred *adapter.Credential) (string, string, error) {
	rt := ""
	if cred.Extra != nil {
		rt = cred.Extra["refresh_token"]
	}
	if rt == "" {
		// 也支持直接传 refresh_token 到 cookie 字段
		rt = cred.Cookie
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Message      string `json:"message"`
	}
	_, err := a.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     authURL,
		Headers: map[string]string{"User-Agent": ua},
		Body:    map[string]interface{}{"grant_type": "refresh_token", "refresh_token": rt},
	}, &resp)
	if err != nil {
		return "", "", err
	}
	if resp.AccessToken == "" {
		return "", "", adapter.ErrCookieInvalid
	}
	return resp.AccessToken, resp.RefreshToken, nil
}

// ============ 认证 ============

// SupportsQR 阿里云盘不提供扫码登录。
//
// 官方扫码走的是 PKCE + OAuth，需要自行申请 client_id / client_secret，
// 没有公开可用的固定凭证；社区通行做法是让用户粘贴 refresh_token。
// 明确声明 false，前端就会直接收掉"扫码登录"页签、改引导手动填写，
// 而不是给用户一个扫了也没用的二维码。
func (a *Aliyun) SupportsQR() bool { return false }

func (a *Aliyun) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	// 阿里云盘扫码流程较复杂（需 PKCE + OAuth），
	// 推荐使用 refresh_token 方式，在后台「手动添加」中粘贴 refresh_token
	return nil, fmt.Errorf("阿里云盘推荐使用 refresh_token 绑定：请在阿里云盘网页版获取 refresh_token 后通过「手动添加」录入")
}

func (a *Aliyun) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	return nil, fmt.Errorf("阿里云盘暂不支持扫码绑定，请使用 refresh_token")
}

func (a *Aliyun) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	at, _, err := a.refreshToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	var resp struct {
		NickName string `json:"nick_name"`
		Avatar   string `json:"avatar"`
		Phone    string `json:"phone"`
	}
	_, err = a.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/v2/user/get",
		Headers: map[string]string{"Authorization": "Bearer " + at, "User-Agent": ua},
		Body:    map[string]interface{}{},
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &adapter.AccountInfo{
		Nickname: orDefault(resp.NickName, resp.Phone, "阿里云盘"),
		Avatar:   resp.Avatar,
	}, nil
}

// ============ 文件操作 ============

func (a *Aliyun) doAuth(ctx context.Context, cred *adapter.Credential, method, path string, body interface{}, out interface{}) error {
	at, _, err := a.refreshToken(ctx, cred)
	if err != nil {
		return err
	}
	_, err = a.http.DoJSON(ctx, &adapter.Request{
		Method:  method,
		URL:     apiBase + path,
		Headers: map[string]string{"Authorization": "Bearer " + at, "User-Agent": ua},
		Body:    body,
	}, out)
	return err
}

func (a *Aliyun) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "root"
	}
	var resp struct {
		Items []struct {
			FileID        string `json:"file_id"`
			Name          string `json:"name"`
			Type          string `json:"type"`
			Size          int64  `json:"size"`
			UpdatedAt     string `json:"updated_at"`
			FileExtension string `json:"file_extension"`
		} `json:"items"`
		Message string `json:"message"`
	}
	if err := a.doAuth(ctx, cred, "POST", "/v2/file/list", map[string]interface{}{
		"drive_id": "", "parent_file_id": dirID, "limit": 200,
		"order_by": "updated_at", "order_direction": "DESC",
	}, &resp); err != nil {
		return nil, err
	}
	if resp.Message != "" {
		return nil, fmt.Errorf("阿里云盘列出目录失败: %s", resp.Message)
	}
	items := make([]adapter.FileItem, 0, len(resp.Items))
	for _, f := range resp.Items {
		items = append(items, adapter.FileItem{
			ID:        f.FileID,
			Name:      f.Name,
			IsDir:     f.Type == "folder",
			Size:      f.Size,
			UpdatedAt: f.UpdatedAt,
			Ext:       f.FileExtension,
		})
	}
	return items, nil
}

func (a *Aliyun) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "root"
	}
	var resp struct {
		FileID  string `json:"file_id"`
		Message string `json:"message"`
	}
	if err := a.doAuth(ctx, cred, "POST", "/adrive/v1.0/openFile/create", map[string]interface{}{
		"drive_id": "", "parent_file_id": parentID, "name": name,
		"type": "folder", "check_name_mode": "refuse",
	}, &resp); err != nil {
		return "", err
	}
	if resp.FileID == "" {
		return "", fmt.Errorf("阿里云盘新建目录失败: %s", resp.Message)
	}
	return resp.FileID, nil
}

func (a *Aliyun) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	return a.doAuth(ctx, cred, "POST", "/v2/recyclebin/trash", map[string]interface{}{
		"drive_id": "", "file_id": fileIDs[0],
	}, nil)
}

func (a *Aliyun) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	return a.doAuth(ctx, cred, "POST", "/v2/file/update", map[string]interface{}{
		"drive_id": "", "file_id": fileID, "name": newName,
	}, nil)
}

// ============ 分享与转存 ============

func (a *Aliyun) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	shareID := extractShareID(shareURL)
	if shareID == "" {
		return nil, fmt.Errorf("无法提取阿里云盘分享 ID: %s", shareURL)
	}
	at, _, err := a.refreshToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ShareName string `json:"share_name"`
		CreatorID string `json:"creator_id"`
		ShareID   string `json:"share_id"`
		Files     []struct {
			FileID string `json:"file_id"`
			Name   string `json:"name"`
			Type   string `json:"type"`
			Size   int64  `json:"size"`
		} `json:"files"`
		Message string `json:"message"`
	}
	_, err = a.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/adrive/v3/share_link/get_share_by_anonymous",
		Headers: map[string]string{"Authorization": "Bearer " + at, "User-Agent": ua},
		Body:    map[string]interface{}{"share_id": shareID, "share_pwd": passcode},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Message != "" {
		return nil, adapter.ErrShareExpired
	}

	info := &adapter.ShareInfo{
		ShareURL: shareURL, Passcode: passcode,
		RootID: resp.ShareID, Title: resp.ShareName,
	}
	for _, f := range resp.Files {
		info.Files = append(info.Files, adapter.FileItem{
			ID: f.FileID, Name: f.Name, IsDir: f.Type == "folder", Size: f.Size,
		})
		info.TotalSize += f.Size
	}
	return info, nil
}

func (a *Aliyun) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	var fileIDs []string
	for _, f := range share.Files {
		fileIDs = append(fileIDs, f.ID)
	}
	var resp struct {
		Message string `json:"message"`
	}
	if err := a.doAuth(ctx, cred, "POST", "/adrive/v1.0/openFile/copy", map[string]interface{}{
		"drive_id": "", "file_id": strings.Join(fileIDs, ","),
		"to_parent_file_id": targetDirID, "share_id": share.RootID,
	}, &resp); err != nil {
		return nil, err
	}
	return &adapter.SaveResult{FileIDs: fileIDs, Count: len(fileIDs), Message: "转存完成"}, nil
}

func (a *Aliyun) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := a.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return a.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

func (a *Aliyun) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	var resp struct {
		URL        string `json:"url"`
		Size       int64  `json:"size"`
		Expiration string `json:"expiration"`
		Message    string `json:"message"`
	}
	if err := a.doAuth(ctx, cred, "POST", "/v2/file/get_download_url", map[string]interface{}{
		"drive_id": "", "file_id": fileID,
	}, &resp); err != nil {
		return nil, err
	}
	if resp.URL == "" {
		return nil, fmt.Errorf("阿里云盘获取直链失败: %s", resp.Message)
	}
	return &adapter.DirectLink{
		URL:          resp.URL,
		Size:         resp.Size,
		ExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
		// 阿里云盘直链本身是签名 URL，但带上 Cookie 无副作用，
		// 且能覆盖"签名校验之外还有登录态校验"的边缘节点。
		Headers:      adapter.MediaHeaders(ua, "https://www.aliyundrive.com/", cred.Cookie),
		AcceptRanges: true,
	}, nil
}

func (a *Aliyun) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return a.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func extractShareID(u string) string {
	// https://www.aliyundrive.com/s/xxxxx  或  alipan.com/s/xxxxx
	parts := strings.Split(u, "/s/")
	if len(parts) < 2 {
		return ""
	}
	id := parts[1]
	if i := strings.IndexAny(id, "?&#/"); i >= 0 {
		id = id[:i]
	}
	return id
}

func orDefault(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
