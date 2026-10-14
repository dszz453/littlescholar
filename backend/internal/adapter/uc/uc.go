package uc

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/littlescholar/core/internal/adapter"
)

// UC UC网盘适配器
//
// UC网盘与夸克同属阿里体系，文件接口协议高度相似（UC 是夸克的姊妹产品），
// 主要差异在 baseURL、client_id 与部分字段命名。
//
// 但**扫码登录这一块两者差得很远**，不能照抄（见下方扫码段落）。
type UC struct {
	http *adapter.HTTPClient

	// 扫码会话暂存：sessionID -> token。
	// token 只对签发它的那个进程有效，所以必须自己存着 —— 轮询时要用。
	mu       sync.RWMutex
	sessions map[string]*qrSession

	// probeBase 仅用于登录校验那一次请求，留作测试接缝
	// （单测里指向 httptest，其余业务请求仍走 apiBase）。
	probeBase string
}

type qrSession struct {
	Token     string
	CreatedAt time.Time
}

func init() {
	adapter.Register(New())
}

func New() *UC {
	return &UC{
		http:     adapter.NewHTTPClient(30 * time.Second),
		sessions: make(map[string]*qrSession),
	}
}

func (u *UC) Type() adapter.DiskType { return adapter.DiskUC }
func (u *UC) Name() string           { return "UC网盘" }

const (
	apiBase = "https://pc-api.uc.cn/1/clouddrive"
	ucPan   = "https://drive.uc.cn"
	// clientID UC 扫码所用的客户端标识，必须与二维码短链里的 client_id 一致。
	//
	// 要用 381，不是 5。5 是"UC 浏览器内嵌网页"那一套的 id，拿它去 CAS
	// 建 token 虽然也返回 200，但生成的二维码在 UC App 里不被认作登录凭证。
	// 取值对齐参考实现（xiaoya-alist 的 uc_cookie.py）。
	clientID = "381"
	ucUA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) uc-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"

	// statusNotLogin UC 明确表示"未登录"的业务状态码。
	//
	// 只有它（而不是任何错误码）才用来判定 cookie 失效。见 checkLoginSession。
	statusNotLogin = 31001
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

// ============ 扫码登录 ============
//
// 逐条对齐参考实现（xiaoya-alist 的 glue_python/uc_cookie/uc_cookie.py）。
// UC 与夸克虽然同属阿里、共用一套 CAS 语义，但**扫码这一步差异很大**：
//
//	              夸克                         UC
//	client_id     532                          381
//	建 token      GET  + query                 POST + form + __dt/__t
//	二维码短码     su.quark.cn/4_eMHBJ          su.uc.cn/1_n0ZCv
//	换票域名       pan.quark.cn/account/info    drive.uc.cn/account/info
//	补 __puus      /1/clouddrive/config         /1/clouddrive/file/sort
//
// 之前这几项全部按夸克抄，于是出现两个症状：
//  1. 选了 UC 却扫出夸克二维码 —— su.uc.cn 与 su.quark.cn 共用同一套短链
//     路由，拿夸克的短码去 su.uc.cn，会一路跳到 b.quark.cn 的端内确认页；
//  2. 轮询永远走不到最后 —— 原 PollQRCode 是个桩，直接返回"尚未实现"。

// UC 扫码相关端点。建 token / 轮询都走 api.open.uc.cn 的 CAS。
const (
	qrCreateURL = "https://api.open.uc.cn/cas/ajax/getTokenForQrcodeLogin"
	qrPollURL   = "https://api.open.uc.cn/cas/ajax/getServiceTicketByQrcodeToken"
	// qrTicketURL 用 service_ticket 换 cookie 的入口
	qrTicketURL = "https://drive.uc.cn/account/info"

	// qrTTL 扫码会话本地保留时长，纯粹是防无限轮询的兜底；
	// 真正的"过期"以服务端返回的 50004002 为准。
	qrTTL = 180 * time.Second

	// ucParamStr UC 客户端的埋点串。二维码 URL 里必须带上，
	// 缺了它 UC App 打开短链后不会进入登录流程。
	ucParamStr = "dsdnfrpfbivesscpgimibtbmnijblauputogpintnwktprchmt"
)

// ucStart 进程启动时刻，用来算 CAS 要求的 __dt（"页面已停留多久"）。
// 官方脚本在模块加载时取一次，此处等价。
var ucStart = time.Now().UnixMilli()

// qrContentURL 由 token 拼出二维码内容。
//
// 短码取 1_n0ZCv，**不能沿用夸克的 4_eMHBJ** —— 那正是"选 UC 扫出夸克码"
// 的根因（详见上方对照表）。
func qrContentURL(token string) string {
	return "https://su.uc.cn/1_n0ZCv?uc_param_str=" + ucParamStr +
		"&token=" + token +
		"&client_id=" + clientID +
		"&uc_biz_str=" + url.QueryEscape("S:custom|C:titlebar_fix")
}

// casPost 以 form 表单 POST 一个 CAS 接口。
//
// UC 的这两个端点都只认 application/x-www-form-urlencoded，且要求 URL 上
// 带 __dt / __t 两个埋点参数：
//
//	__t  —— 当前毫秒时间戳
//	__dt —— 页面打开至今的毫秒数
//
// 之前用 GET + query 调用，接口会返回 200 但 data 为空，token 自然拿不到。
func (u *UC) casPost(ctx context.Context, apiURL, token string, out interface{}) error {
	now := time.Now().UnixMilli()

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("v", "1.2")
	form.Set("request_id", fmt.Sprint(now))
	if token != "" {
		form.Set("token", token)
	}

	full := fmt.Sprintf("%s?__dt=%d&__t=%d", apiURL, now-ucStart, now)
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method: "POST",
		URL:    full,
		Headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
			"User-Agent":   ucUA,
			"Referer":      ucPan + "/",
			"Origin":       ucPan,
		},
		RawBody: []byte(form.Encode()),
	}, out)
	return err
}

// GetQRCode 获取 UC 扫码登录二维码
func (u *UC) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var resp struct {
		Status int `json:"status"`
		Data   struct {
			Members struct {
				Token string `json:"token"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := u.casPost(ctx, qrCreateURL, "", &resp); err != nil {
		return nil, fmt.Errorf("获取 UC 二维码失败: %w", err)
	}
	if resp.Data.Members.Token == "" {
		return nil, fmt.Errorf("UC 返回的二维码 token 为空")
	}

	sessionID := "uc_" + uuid.NewString()
	u.mu.Lock()
	u.sessions[sessionID] = &qrSession{Token: resp.Data.Members.Token, CreatedAt: time.Now()}
	u.mu.Unlock()

	return &adapter.QRCode{
		SessionID: sessionID,
		Content:   qrContentURL(resp.Data.Members.Token),
		ExpiresIn: int(qrTTL / time.Second),
	}, nil
}

// PollQRCode 轮询扫码状态，成功后换票落 cookie
func (u *UC) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	u.mu.RLock()
	sess, ok := u.sessions[sessionID]
	u.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("扫码会话不存在或已过期")
	}
	if time.Since(sess.CreatedAt) > qrTTL {
		u.forget(sessionID)
		return &adapter.LoginResult{Status: "expired", Message: "二维码已过期，请点击「重新获取二维码」"}, nil
	}

	var resp struct {
		Status int `json:"status"`
		Data   struct {
			Members struct {
				ServiceTicket string `json:"service_ticket"`
			} `json:"members"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := u.casPost(ctx, qrPollURL, sess.Token, &resp); err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}

	// UC 的 status 语义与夸克同源：
	//   2000000  —— 扫码成功，members.service_ticket 就绪
	//   50004001 —— 尚未扫码 / 查询无结果，继续等
	//   50004002 —— 二维码无效或已过期
	switch resp.Status {
	case 2000000:
		ticket := resp.Data.Members.ServiceTicket
		if ticket == "" {
			return &adapter.LoginResult{Status: "failed", Message: "未获取到 service ticket"}, nil
		}
		cookie, err := u.exchangeTicket(ctx, ticket)
		if err != nil {
			return &adapter.LoginResult{Status: "failed", Message: err.Error()}, nil
		}
		u.forget(sessionID)
		return &adapter.LoginResult{Status: "confirmed", Cookie: cookie, Message: "登录成功"}, nil
	case 50004002:
		u.forget(sessionID)
		return &adapter.LoginResult{Status: "expired", Message: "二维码已过期，请点击「重新获取二维码」"}, nil
	default:
		msg := resp.Message
		if msg == "" {
			msg = "等待扫码"
		}
		return &adapter.LoginResult{Status: "waiting", Message: msg}, nil
	}
}

// forget 丢弃一个扫码会话。
func (u *UC) forget(sessionID string) {
	u.mu.Lock()
	delete(u.sessions, sessionID)
	u.mu.Unlock()
}

// exchangeTicket 用 service_ticket 换 cookie，并补上 __puus。
//
// 同样是两步，一步都不能少：
//  1. GET drive.uc.cn/account/info?st=<ticket>，从 Set-Cookie 拿主 cookie；
//  2. 再用该 cookie 打一次 file/sort，把 **__puus** 捞回来 —— UC 的 __puus
//     不在换票响应里下发，缺了它后续所有接口都会鉴权失败，表现就是
//     "扫码成功了但一用就失效"。
func (u *UC) exchangeTicket(ctx context.Context, ticket string) (string, error) {
	cookie, err := adapter.FetchCookies(ctx, qrTicketURL+"?st="+url.QueryEscape(ticket), map[string]string{
		"User-Agent": ucUA,
		"Referer":    ucPan + "/",
	})
	if err != nil {
		return "", err
	}
	if cookie == "" {
		return "", fmt.Errorf("扫码确认后未拿到有效 cookie（可能已过期，请重新扫码）")
	}

	puus, err := adapter.FetchCookies(ctx,
		apiBase+"/file/sort?pr=UCBrowser&fr=pc&pdir_fid=0&_page=1&_size=50"+
			"&_fetch_total=1&_fetch_sub_dirs=0&_sort=file_type:asc,updated_at:desc",
		map[string]string{
			"User-Agent": ucUA,
			"Referer":    ucPan + "/",
			"Cookie":     cookie,
		})
	if err != nil {
		return "", fmt.Errorf("获取 __puus 失败: %w", err)
	}
	return adapter.MergeCookie(cookie, puus), nil
}

// ValidateCookie 校验 cookie 并取回账号信息。
//
// ⚠️ 端点必须用 drive.uc.cn/account/info，不能用 pc-api 那套：
// pc-api.uc.cn/1/clouddrive/account/info 实测恒返回 404，
// 于是 status=404 ≠ 200，判定恒为"cookie 已失效"（与夸克同款 bug）。
//
// 正确端点响应体为：
//
//	{"success":true,"code":"OK","data":{"nickname":…,"vip":…,"capacity":{…}}}
//
// 注意 code 是字符串 "OK" 而非数字。
func (u *UC) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
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
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     ucPan + "/account/info",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, adapter.ErrCookieInvalid
	}
	// 账号资料接口过了还不够 —— 见 checkLoginSession 的注释。
	if err := u.checkLoginSession(ctx, cred); err != nil {
		return nil, err
	}
	return &adapter.AccountInfo{
		Nickname:      resp.Data.Nickname,
		Avatar:        resp.Data.Avatar,
		VIP:           resp.Data.Vip,
		CapacityUsed:  resp.Data.Capacity.Used,
		CapacityTotal: resp.Data.Capacity.Total,
	}, nil
}

// checkLoginSession 用一次轻量的真实业务请求确认会话"真的能干活"。
//
// 为什么不能只靠 ValidateCookie 里那个 account/info：
// 那是**网页引导接口，游客会话同样返回 success**。线上因此出现过
// 账号状态显示"有效"、但列目录/转存/播放全部失败的情况 ——
// 上游真实回应是 HTTP 401 + {"code":31001,"message":"require login [guest]"}。
// 用户看到"有效"却什么都做不了，比直接标红更难排查。
//
// 判据刻意收得很紧：**只有上游明确说"未登录"才判失效**。
// 限流、超时、网络抖动一律不改判定 —— 用户反馈过反方向的问题
// （提示 cookie 失效、实际能正常打开），宁可漏报也不能误杀好账号。
func (u *UC) checkLoginSession(ctx context.Context, cred *adapter.Credential) error {
	base := apiBase
	if u.probeBase != "" {
		base = u.probeBase
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	// 只取 1 条，尽量轻。
	//
	// 这里刻意吞掉 err：DoJSON 对非 2xx 也会解析 body，所以 401 的
	// code/message 照样拿得到；而网络层失败并不是"未登录"，按上面的
	// 原则不该改判定。
	_, _ = u.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     base + "/file/sort",
		Headers: u.headers(cred.Cookie),
		Query: map[string]string{
			"pr": "UCBrowser", "fr": "pc", "pdir_fid": "0",
			"_page": "1", "_size": "1", "_sort": "file_type:asc,updated_at:desc",
		},
	}, &resp)

	if resp.Code == statusNotLogin ||
		strings.Contains(strings.ToLower(resp.Message), "require login") {
		return adapter.ErrCookieInvalid
	}
	return nil
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
				FID       string `json:"fid"`
				FileName  string `json:"file_name"`
				Dir       bool   `json:"dir"`
				Size      int64  `json:"size"`
				UpdatedAt int64  `json:"updated_at"`
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
	stoken, pwdID, err := u.shareToken(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}

	// 2. 列出分享内容
	files, err := u.listShareFiles(ctx, cred, pwdID, stoken, "0")
	if err != nil {
		return nil, err
	}

	info := &adapter.ShareInfo{
		ShareURL: shareURL,
		Passcode: passcode,
		RootID:   stoken,
		Title:    fmt.Sprintf("UC分享_%s", pwdID),
		Files:    files,
	}
	for _, f := range files {
		info.TotalSize += f.Size
	}
	if len(info.Files) == 1 {
		info.Title = info.Files[0].Name
	}
	return info, nil
}

// shareToken 取分享 stoken（UC 与夸克同协议）。
func (u *UC) shareToken(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (string, string, error) {
	m := ucShareRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return "", "", fmt.Errorf("无法提取 UC 分享 ID: %s", shareURL)
	}
	pwdID := m[1]

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
		return "", "", err
	}
	if tokenResp.Code == 41013 || tokenResp.Code == 41012 {
		return "", "", adapter.ErrNeedPassword
	}
	if tokenResp.Code != 0 {
		return "", "", adapter.ErrShareExpired
	}
	if tokenResp.Data.Stoken == "" {
		return "", "", adapter.ErrShareExpired
	}
	return tokenResp.Data.Stoken, pwdID, nil
}

// listShareFiles 列出分享内指定目录（pdirFid "0" = 根目录）。
//
// 与夸克同协议；抽出来给「解析整个分享」和「一层层打开文件夹」共用。
func (u *UC) listShareFiles(ctx context.Context, cred *adapter.Credential, pwdID, stoken, pdirFid string) ([]adapter.FileItem, error) {
	if pdirFid == "" {
		pdirFid = "0"
	}
	var listResp struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				FID      string `json:"fid"`
				FileName string `json:"file_name"`
				Dir      bool   `json:"dir"`
				Size     int64  `json:"size"`
				// 与夸克同协议：转存必须逐项回传 share_fid_token。
				ShareFidToken string `json:"share_fid_token"`
			} `json:"list"`
			ShareToken string `json:"share_token"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     apiBase + "/share/sharepage/detail",
		Headers: u.headers(cred.Cookie),
		Query: map[string]string{
			"pr": "UCBrowser", "fr": "pc", "pwd_id": pwdID,
			"stoken": stoken, "pdir_fid": pdirFid,
			"force": "0", "_page": "1", "_size": "200",
		},
	}, &listResp)
	if err != nil {
		return nil, err
	}
	if listResp.Code != 0 {
		return nil, fmt.Errorf("UC 分享列出失败: code=%d", listResp.Code)
	}
	files := make([]adapter.FileItem, 0, len(listResp.Data.List))
	for _, f := range listResp.Data.List {
		files = append(files, adapter.FileItem{
			ID: f.FID, Name: f.FileName, IsDir: f.Dir, Size: f.Size,
			Ext: extOf(f.FileName), Token: f.ShareFidToken,
		})
	}
	return files, nil
}

// ListShareDir 列出分享内某一层目录 —— 供前端"一层文件夹一层打开"。
func (u *UC) ListShareDir(ctx context.Context, cred *adapter.Credential, shareURL, passcode, pdirFid string) (*adapter.ShareEntry, error) {
	stoken, pwdID, err := u.shareToken(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	files, err := u.listShareFiles(ctx, cred, pwdID, stoken, pdirFid)
	if err != nil {
		return nil, err
	}
	if pdirFid == "" {
		pdirFid = "0"
	}
	return &adapter.ShareEntry{Fid: pdirFid, Title: fmt.Sprintf("UC分享_%s", pwdID), Files: files, RootID: stoken}, nil
}

func (u *UC) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	if targetDirID == "" {
		targetDirID = "0"
	}
	var fids, tokenFids []string
	for _, f := range share.Files {
		fids = append(fids, f.ID)
		tokenFids = append(tokenFids, f.Token)
	}
	if len(fids) == 0 {
		return nil, fmt.Errorf("分享里没有可转存的文件")
	}
	for i, t := range tokenFids {
		if t == "" {
			return nil, fmt.Errorf("分享条目 %q 缺少转存令牌，请重新解析分享", share.Files[i].Name)
		}
	}
	var resp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	_, err := u.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     apiBase + "/share/sharepage/save",
		Headers: u.headers(cred.Cookie),
		Query:   map[string]string{"pr": "UCBrowser", "fr": "pc"},
		Body: map[string]interface{}{
			"fid_list": fids, "fid_token_list": tokenFids,
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
	if resp.Data.TaskID != "" {
		if err := u.waitTask(ctx, cred, resp.Data.TaskID); err != nil {
			return nil, err
		}
	}
	return &adapter.SaveResult{
		FileIDs: fids, Count: len(fids),
		Message: fmt.Sprintf("成功转存 %d 个文件", len(fids)),
	}, nil
}

// waitTask 轮询转存任务。
//
// UC 与夸克同协议：data.status 2=成功、3=失败、其余表示仍在跑。
// 不轮询就等于"发完请求就说成功"——转存实际失败也看不出来。
//
// 与夸克同样的两个坑：端点必须是 GET /task?task_id=<id>（写成
// /task/<id> 恒 404，会一路轮询到 deadline 误报"超时"）；失败原因在
// **顶层** code/message，必须读，否则会吞掉"空间不足"这类真实原因。
func (u *UC) waitTask(ctx context.Context, cred *adapter.Credential, taskID string) error {
	deadline := time.Now().Add(90 * time.Second)
	var lastMsg string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
		var resp struct {
			Status  int    `json:"status"`
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
				SaveAs  struct {
					RemainCapacity int64 `json:"remain_capacity"`
				} `json:"save_as"`
			} `json:"data"`
			Metadata struct {
				MissingCapacity int64 `json:"missing_capacity"`
			} `json:"metadata"`
		}
		_, err := u.http.DoJSON(ctx, &adapter.Request{
			Method:  "GET",
			URL:     apiBase + "/task",
			Headers: u.headers(cred.Cookie),
			Query: map[string]string{
				"pr": "UCBrowser", "fr": "pc",
				"task_id": taskID, "retry_index": "0",
			},
		}, &resp)
		if err != nil {
			continue
		}
		if resp.Data.Status == 2 {
			return nil
		}
		if resp.Data.Status == 3 || resp.Code != 0 {
			msg := resp.Data.Message
			if msg == "" {
				msg = resp.Message
			}
			return fmt.Errorf("%s", adapter.TaskErrorText("UC 网盘", resp.Code, msg,
				resp.Metadata.MissingCapacity, resp.Data.SaveAs.RemainCapacity))
		}
		lastMsg = resp.Data.Message
	}
	if lastMsg == "" {
		lastMsg = "任务超时"
	}
	return fmt.Errorf("UC 转存任务未在预期时间内完成: %s", lastMsg)
}

func (u *UC) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := u.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return u.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

// ucCookieRefreshTTL 同夸克：刷新后的 cookie 在内存里保留 5 分钟，
// 覆盖"播放信息 + 取流"两次相邻调用，又不至于拖到 __puus 失效。
const ucCookieRefreshTTL = 5 * time.Minute

type ucCachedCookie struct {
	cookie string
	exp    time.Time
}

var (
	ucRefreshMu    sync.Mutex
	ucRefreshCache = map[string]ucCachedCookie{} // key = 库里存的原始 cookie
)

// refreshCookie 用一次 file/sort 把 __puus 换成新鲜的。
//
// ⚠️ 与夸克同源的问题：UC 与夸克同属阿里系，CDN 准入同样看 __puus，而
// 库里存的 cookie 是扫码那一刻的快照。__puus 旧了之后，列目录/取直链的
// API 照样 200，但真去下载时 CDN 回 412 —— 孩子端就是「不能播放，可能
// 地址过期或格式不支持」。修法见 quark.refreshCookie 的详细注释。
//
// UC 的 __puus 不是 /config 下发，而是要打一次 file/sort（与 exchangeTicket
// 第二步一致）。
func (u *UC) refreshCookie(ctx context.Context, cookie string, force bool) string {
	if !force {
		ucRefreshMu.Lock()
		c, ok := ucRefreshCache[cookie]
		ucRefreshMu.Unlock()
		if ok && time.Now().Before(c.exp) {
			return c.cookie
		}
	}

	fresh, err := adapter.FetchCookies(ctx,
		apiBase+"/file/sort?pr=UCBrowser&fr=pc&pdir_fid=0&_page=1&_size=50"+
			"&_fetch_total=1&_fetch_sub_dirs=0&_sort=file_type:asc,updated_at:desc",
		map[string]string{
			"User-Agent": ucUA,
			"Referer":    ucPan + "/",
			"Cookie":     cookie,
		})
	if err != nil || strings.TrimSpace(fresh) == "" {
		return cookie
	}
	merged := adapter.MergeCookie(cookie, fresh)

	ucRefreshMu.Lock()
	ucRefreshCache[cookie] = ucCachedCookie{cookie: merged, exp: time.Now().Add(ucCookieRefreshTTL)}
	ucRefreshMu.Unlock()
	return merged
}

// GetDirectLink 获取直链。先刷新 __puus 再取链，失败强制再刷一次重试。
func (u *UC) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	cookie := u.refreshCookie(ctx, cred.Cookie, false)
	link, err := u.fetchLink(ctx, cookie, fileID)
	if err != nil {
		link, err = u.fetchLink(ctx, u.refreshCookie(ctx, cred.Cookie, true), fileID)
		if err != nil {
			return nil, err
		}
	}
	// UC 与夸克同属阿里系 CDN，准入同样看 Cookie —— 这里必须用刷新过的那份。
	link.Headers = adapter.MediaHeaders(ucUA, "https://drive.uc.cn/", cookie)
	return link, nil
}

func (u *UC) fetchLink(ctx context.Context, cookie, fileID string) (*adapter.DirectLink, error) {
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
		Headers: u.headers(cookie),
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
