package tianyi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// Tianyi 天翼网盘适配器
//
// 天翼云盘（cloud.189.cn）的网页版接口，需要 Cookie 中的
// COOKIE_LOGIN_USER / SESSION_KEY 等字段。
type Tianyi struct {
	http *adapter.HTTPClient
}

func init() {
	adapter.Register(New())
}

func New() *Tianyi {
	return &Tianyi{http: adapter.NewHTTPClient(30 * time.Second)}
}

func (t *Tianyi) Type() adapter.DiskType { return adapter.DiskTianyi }
func (t *Tianyi) Name() string           { return "天翼网盘" }

const (
	webURL  = "https://cloud.189.cn"
	apiBase = "https://cloud.189.cn/api"
	tyUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
)

func (t *Tianyi) headers(cookie string) map[string]string {
	return map[string]string{
		"User-Agent": tyUA,
		"Cookie":     cookie,
		"Referer":    "https://cloud.189.cn/web/main/",
		"Accept":     "application/json;charset=UTF-8",
	}
}

// ============ 认证 ============

// ============ 扫码登录 ============
//
// ⚠️ 天翼**不提供扫码登录**，SupportsQR 返回 false。
//
// 实测（在服务器上直连验证）：cloud.189.cn/api/logbox/* 这一组接口
// 在未持有有效会话时一律返回
//
//	{"errorCode":"InvalidSessionKey",
//	 "errorMsg":"cookieUserSession is null or invalid"}
//
// 而天翼网页版的登录入口会 302 到
// open.e.189.cn/api/logbox/oauth2/separate/auth/unifyAccountLogin.do
// —— 也就是统一账号平台，扫码要在那套 OAuth 体系里做，
// 拿 cloud.189.cn 的接口引导不出来。
//
// 参考包（影视播放器）对天翼用的也是**账号密码 + captchaToken**，
// 不是扫码。所以这里如实声明"不支持"，让前端引导用户去粘 Cookie。
//
// 下面的 GetQRCode / PollQRCode 保留实现，是为了将来天翼放开接口时
// 能直接接上，不代表当前可用。
func (t *Tianyi) SupportsQR() bool { return false }

// GetQRCode 获取扫码登录二维码（当前不可用，见上方说明）
func (t *Tianyi) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var resp struct {
		ResCode    int    `json:"res_code"`
		UUID       string `json:"uuid"`
		QrCodeURL  string `json:"qrCodeUrl"`
		QrLoginURL string `json:"qrLoginUrl"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/logbox/getLoginQrCode",
		Headers: map[string]string{"User-Agent": tyUA, "Referer": webURL + "/web/login.html"},
		Query:   map[string]string{"noCache": fmt.Sprint(time.Now().UnixMilli())},
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("获取天翼二维码失败: %w", err)
	}
	if resp.UUID == "" {
		return nil, fmt.Errorf("天翼未返回 uuid")
	}

	// 二维码内容优先取 qrLoginUrl；qrCodeUrl 多数时候只是第三方生成的
	// 图片地址，拿它当"内容"再编码一次会得到"二维码的二维码"。
	content := resp.QrLoginURL
	if content == "" {
		content = resp.QrCodeURL
	}
	return &adapter.QRCode{
		SessionID: "tianyi_" + resp.UUID,
		Content:   content,
		Image:     resp.QrCodeURL,
		ExpiresIn: 180,
	}, nil
}

// PollQRCode 轮询扫码状态，确认后用它给的 redirectUrl 换 cookie。
func (t *Tianyi) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	uuid := strings.TrimPrefix(sessionID, "tianyi_")
	if uuid == "" {
		return nil, fmt.Errorf("扫码会话无效")
	}
	var resp struct {
		ResCode     int    `json:"res_code"`
		ResMsg      string `json:"res_msg"`
		RedirectURL string `json:"redirectUrl"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/logbox/getLoginQrCodeInfo",
		Headers: map[string]string{"User-Agent": tyUA, "Referer": webURL + "/web/login.html"},
		Query:   map[string]string{"uuid": uuid, "noCache": fmt.Sprint(time.Now().UnixMilli())},
	}, &resp)
	if err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}

	// res_code=0 且带 redirectUrl ⇒ 用户已在手机上确认。
	// 只有 redirectUrl 才说明可以换票；拿不到它就还停在"等待/已扫码"。
	if resp.ResCode == 0 && resp.RedirectURL != "" {
		cookie, err := adapter.FetchCookies(ctx, resp.RedirectURL, map[string]string{
			"User-Agent": tyUA,
			"Referer":    webURL + "/web/login.html",
		})
		if err != nil || cookie == "" {
			msg := "未取到登录 Cookie"
			if err != nil {
				msg += ": " + err.Error()
			}
			return &adapter.LoginResult{Status: "failed", Message: msg}, nil
		}
		return &adapter.LoginResult{Status: "confirmed", Cookie: cookie, Message: "登录成功"}, nil
	}
	if resp.ResCode == 0 {
		return &adapter.LoginResult{Status: "scanned", Message: "已扫码，请在手机上确认"}, nil
	}
	return &adapter.LoginResult{Status: "waiting", Message: orDefault(resp.ResMsg, "等待扫码")}, nil
}

func (t *Tianyi) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		ResCode    int    `json:"res_code"`
		ResMessage string `json:"res_message"`
		UserInfo   struct {
			LoginName string `json:"loginName"`
			Nickname  string `json:"nickname"`
			IconURL   string `json:"iconURL"`
		} `json:"userInfo"`
		CloudCapacityInfo struct {
			UsedSize  int64 `json:"usedSize"`
			TotalSize int64 `json:"totalSize"`
		} `json:"cloudCapacityInfo"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/getUserSizeInfo.action",
		Headers: t.headers(cred.Cookie),
		Query:   map[string]string{"noCache": fmt.Sprint(time.Now().UnixMilli())},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ResCode != 0 {
		return nil, adapter.ErrCookieInvalid
	}
	return &adapter.AccountInfo{
		Nickname:      orDefault(resp.UserInfo.Nickname, resp.UserInfo.LoginName, "天翼网盘"),
		Avatar:        resp.UserInfo.IconURL,
		CapacityUsed:  resp.CloudCapacityInfo.UsedSize,
		CapacityTotal: resp.CloudCapacityInfo.TotalSize,
	}, nil
}

// ============ 文件操作 ============

func (t *Tianyi) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "-11" // 天翼根目录 ID
	}
	var resp struct {
		ResCode    int `json:"res_code"`
		FileListAO struct {
			FileList []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Size       int64  `json:"size"`
				LastOpTime string `json:"lastOpTime"`
				IsFolder   bool   `json:"isFolder"`
			} `json:"fileList"`
			FolderList []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"folderList"`
		} `json:"fileListAO"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/listFiles.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"folderId": dirID, "mediaType": "0", "iconOption": "5",
			"orderBy": "lastOpTime", "descending": "true",
			"pageNum": "1", "pageSize": "200",
			"noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ResCode != 0 {
		return nil, fmt.Errorf("列出天翼目录失败: res_code=%d", resp.ResCode)
	}

	var items []adapter.FileItem
	for _, f := range resp.FileListAO.FolderList {
		items = append(items, adapter.FileItem{
			ID: fmt.Sprint(f.ID), Name: f.Name, IsDir: true,
		})
	}
	for _, f := range resp.FileListAO.FileList {
		items = append(items, adapter.FileItem{
			ID: fmt.Sprint(f.ID), Name: f.Name, Size: f.Size,
			UpdatedAt: f.LastOpTime, Ext: extOf(f.Name),
		})
	}
	return items, nil
}

func (t *Tianyi) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "-11"
	}
	var resp struct {
		ResCode int   `json:"res_code"`
		ID      int64 `json:"id"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/createFolder.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"folderName": name, "parentFolderId": parentID,
			"noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.ResCode != 0 {
		return "", fmt.Errorf("天翼新建目录失败: res_code=%d", resp.ResCode)
	}
	return fmt.Sprint(resp.ID), nil
}

func (t *Tianyi) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/batchTask/create.action",
		Headers: t.headers(cred.Cookie),
		Query:   map[string]string{"type": "DELETE", "taskInfos": strings.Join(fileIDs, ","), "noCache": fmt.Sprint(time.Now().UnixMilli())},
	}, nil)
	return err
}

func (t *Tianyi) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/renameFile.action",
		Headers: t.headers(cred.Cookie),
		Query:   map[string]string{"fileId": fileID, "destFileName": newName, "noCache": fmt.Sprint(time.Now().UnixMilli())},
	}, nil)
	return err
}

// ============ 分享与转存 ============

var tyShareRe = regexp.MustCompile(`(?:t\.cn/|cloud\.189\.cn/(?:web/share|t)/)([0-9a-zA-Z]+)`)
var tyAccessRe = regexp.MustCompile(`[（(]([0-9a-zA-Z]{4})[)）]`)

func (t *Tianyi) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	m := tyShareRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法提取天翼分享 ID: %s", shareURL)
	}
	shareCode := m[1]
	if passcode == "" {
		if am := tyAccessRe.FindStringSubmatch(shareURL); len(am) >= 2 {
			passcode = am[1]
		}
	}

	// 1. 获取分享访问码会话
	var checkResp struct {
		ResCode int   `json:"res_code"`
		ShareID int64 `json:"shareId"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/share/getShareInfoByCodeV2.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"shareCode": shareCode, "noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &checkResp)
	if err != nil {
		return nil, err
	}
	if checkResp.ResCode != 0 {
		return nil, adapter.ErrShareExpired
	}

	// 2. 列出分享文件
	var listResp struct {
		ResCode    int `json:"res_code"`
		FileListAO struct {
			FileList []struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Size     int64  `json:"size"`
				IsFolder bool   `json:"isFolder"`
			} `json:"fileList"`
			FolderList []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"folderList"`
		} `json:"fileListAO"`
	}
	_, err = t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/share/listShareDir.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"shareId": fmt.Sprint(checkResp.ShareID), "accessCode": passcode,
			"pageNum": "1", "pageSize": "200", "noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &listResp)
	if err != nil {
		return nil, err
	}

	info := &adapter.ShareInfo{ShareURL: shareURL, Passcode: passcode, RootID: fmt.Sprint(checkResp.ShareID)}
	for _, f := range listResp.FileListAO.FolderList {
		info.Files = append(info.Files, adapter.FileItem{ID: fmt.Sprint(f.ID), Name: f.Name, IsDir: true})
	}
	for _, f := range listResp.FileListAO.FileList {
		info.Files = append(info.Files, adapter.FileItem{ID: fmt.Sprint(f.ID), Name: f.Name, Size: f.Size})
		info.TotalSize += f.Size
	}
	info.Title = fmt.Sprintf("天翼分享_%s", shareCode)
	if len(info.Files) == 1 {
		info.Title = info.Files[0].Name
	}
	return info, nil
}

func (t *Tianyi) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	if targetDirID == "" {
		targetDirID = "-11"
	}
	var ids []string
	for _, f := range share.Files {
		ids = append(ids, f.ID)
	}
	var resp struct {
		ResCode int   `json:"res_code"`
		TaskID  int64 `json:"taskId"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/share/saveShareFile.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"shareId": share.RootID, "fileId": strings.Join(ids, ","),
			"targetFolderId": targetDirID, "noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ResCode != 0 {
		return nil, fmt.Errorf("天翼转存失败: res_code=%d", resp.ResCode)
	}
	return &adapter.SaveResult{
		FileIDs: ids, Count: len(ids),
		Message: fmt.Sprintf("成功转存 %d 个文件", len(ids)),
	}, nil
}

func (t *Tianyi) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := t.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return t.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

func (t *Tianyi) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	var resp struct {
		ResCode  int `json:"res_code"`
		FileInfo struct {
			DownloadURL string `json:"downloadUrl"`
			Size        int64  `json:"size"`
			FileMd5     string `json:"fileMd5"`
		} `json:"fileInfo"`
	}
	_, err := t.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/getFileDownloadUrl.action",
		Headers: t.headers(cred.Cookie),
		Query: map[string]string{
			"fileId": fileID, "dt": "1", "noCache": fmt.Sprint(time.Now().UnixMilli()),
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ResCode != 0 || resp.FileInfo.DownloadURL == "" {
		return nil, fmt.Errorf("天翼获取直链失败: res_code=%d", resp.ResCode)
	}
	return &adapter.DirectLink{
		URL:          resp.FileInfo.DownloadURL,
		Size:         resp.FileInfo.Size,
		ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
		// 天翼云盘的下载域名同样会校验登录态，带上 Cookie 保持一致。
		Headers:      adapter.MediaHeaders(tyUA, "https://cloud.189.cn/", cred.Cookie),
		AcceptRanges: true,
	}, nil
}

func (t *Tianyi) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return t.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

func orDefault(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
