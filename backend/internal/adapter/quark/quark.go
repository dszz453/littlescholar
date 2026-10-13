package quark

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

	// qrTTL 扫码会话在本地保留的时长，纯粹是防无限轮询的兜底。
	//
	// 真正的"过期"以服务端返回的 50004002 为准 —— 那才是权威信号。
	// 之前这里按夸克"120 秒过期"的传闻写死 120 秒，比服务端更早地
	// 判死二维码，用户拿起手机稍慢一点就被告知过期，这就是
	// 「夸克扫码总是提示过期」的直接原因。
	qrTTL = 180 * time.Second
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

// qrContentURL 由 token 拼出二维码内容。
//
// 这里必须自己拼，不能指望接口给：夸克早期版本在 getTokenForQrcodeLogin
// 的响应里带 qrcode_url，现在**只返回 token**。
//
// 参数与参考实现（xiaoya-alist 的 glue_python/quark_cookie/quark_cookie.py）
// 逐字对齐，一个都不能少。末尾那两个 uc_param_str / uc_biz_str 看着像噪音，
// 但实测缺了它们时，夸克 App 扫码后不会把 token 交给登录模块 —— 手机上
// 表现为「登录请求已过期，请重新扫码」，而服务端轮询始终停在"等待扫码"。
// 这正是此前"整个网盘扫码都不行"里夸克那一半的根因。
func qrContentURL(token string) string {
	return "https://su.quark.cn/4_eMHBJ?token=" + token +
		"&client_id=532&ssb=weblogin&uc_param_str=" +
		"&uc_biz_str=" + url.QueryEscape("S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0")
}

// newRequestID 生成 CAS 接口要求的 request_id。
//
// 必须用随机 UUID，不能用毫秒时间戳：夸克 CAS 会把 request_id 拿去
// 做去重/时序校验，同一毫秒内重复、或与 token 签发时刻对不上的取值
// 会被直接判为无效请求，返回 50004002（二维码已过期）。
// 参考实现里用的就是 uuid4。
func newRequestID() string { return uuid.NewString() }

// GetQRCode 获取扫码登录二维码
func (q *Quark) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var resp struct {
		Status int `json:"status"`
		Data   struct {
			Members struct {
				Token string `json:"token"`
			} `json:"members"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    qrCreateURL,
		Query:  map[string]string{"client_id": "532", "v": "1.2", "request_id": newRequestID()},
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
		Content:   qrContentURL(resp.Data.Members.Token),
		ExpiresIn: int(qrTTL / time.Second),
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
	// 本地计时只用于兜底，真正的"过期"以服务端 50004002 为准。
	if time.Since(sess.CreatedAt) > qrTTL {
		delete(q.sessions, sessionID)
		return &adapter.LoginResult{Status: "expired", Message: "二维码已过期，请点击「重新获取二维码」"}, nil
	}

	var resp struct {
		Status int `json:"status"`
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
			// 每轮换一个全新 request_id：复用同一个值会被 CAS 当成重放。
			"request_id": newRequestID(),
		},
	}, &resp)
	if err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}

	// 夸克 CAS 的 status 语义（对照参考实现与实测）：
	//   2000000  —— 登录成功，data.members.service_ticket 已就绪，可直接换 cookie
	//   50004001 —— 尚未扫码 / 查询无结果（"Query result is empty"），继续等
	//   50004002 —— 二维码无效或已过期，必须让用户重新获取
	// 没有独立的「已扫码待确认」中间态（2000001 并不存在）。
	switch resp.Status {
	case 2000000:
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
	case 50004002:
		delete(q.sessions, sessionID)
		return &adapter.LoginResult{Status: "expired", Message: "二维码已过期，请点击「重新获取二维码」"}, nil
	default:
		msg := resp.Message
		if msg == "" {
			msg = "等待扫码"
		}
		return &adapter.LoginResult{Status: "waiting", Message: msg}, nil
	}
}

// exchangeTicket 用 service ticket 换取 cookie。
//
// 分两步，一步都不能少：
//
//  1. 换票：pan.quark.cn/account/info，参数名是 st（不是 ticket），
//     且要带 lw=scan。之前写的是 /uc/api/account/login?ticket=...，
//     那是不存在的回调，扫码确认后拿不到 __puus / __uid 等关键 cookie。
//
//  2. 补刀：夸克最关键的 __puus **不在换票响应里下发**，要等下一次
//     业务请求（/1/clouddrive/config）才随 Set-Cookie 补上。少了它，
//     后续列目录、取直链一律 401 —— 表现为"扫码成功但一用就失效"。
//
// 参考实现：xiaoya-alist 的 glue_python/quark_cookie/quark_cookie.py。
func (q *Quark) exchangeTicket(ctx context.Context, ticket string) (string, error) {
	loginURL := fmt.Sprintf("%s/account/info?st=%s&lw=scan", accountURL, url.QueryEscape(ticket))

	cookie, err := adapter.FetchCookies(ctx, loginURL, map[string]string{
		"User-Agent": userAgent,
		"Referer":    "https://pan.quark.cn/",
	})
	if err != nil {
		return "", err
	}
	if cookie == "" {
		return "", fmt.Errorf("扫码确认后未拿到有效 cookie（可能已过期，请重新扫码）")
	}

	puus, err := adapter.FetchCookies(ctx, baseURL+"/config?pr=ucpro&fr=pc&uc_param_str=", map[string]string{
		"User-Agent": userAgent,
		"Referer":    "https://pan.quark.cn/",
		"Cookie":     cookie,
	})
	if err != nil {
		return "", fmt.Errorf("获取 __puus 失败: %w", err)
	}
	return adapter.MergeCookie(cookie, puus), nil
}

// ValidateCookie 校验 cookie 并取回账号信息。
//
// ⚠️ 端点必须用 pan.quark.cn/account/info，**不是** drive-pc 那套。
// 之前写成 baseURL + "/account/info"（即 drive-pc.quark.cn/1/clouddrive/account/info），
// 实测该路径恒返回：
//
//	{"timestamp":…,"status":404,"error":"Not Found","path":"/1/clouddrive/account/info"}
//
// 于是 status=404 ≠ 200，判定恒为"cookie 已失效"——哪怕 cookie 完全正常。
// 而列目录走的是 drive-pc 的 /file/sort（存在），所以出现
// 「提示失效、实际能点开」这种自相矛盾的现象。
//
// 正确端点的响应体也不是 {status,code} 那套：
//
//	{"success":true,"code":"OK","data":{"nickname":…,"avatar_url":…,"vip":…,"capacity":{…}}}
//
// code 是**字符串** "OK"，不是数字。旧结构体用 int 接，还会额外触发
// 反序列化错误。
func (q *Quark) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
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
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     accountURL + "/account/info",
		Query:   map[string]string{"fr": "pc", "platform": "pc"},
		Headers: q.headers(cred.Cookie),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
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
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			List []struct {
				FID         string `json:"fid"`
				FileName    string `json:"file_name"`
				Dir         bool   `json:"dir"`
				Size        int64  `json:"size"`
				UpdatedAt   int64  `json:"updated_at"`
				FileType    int    `json:"file_type"`
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
			"pdir_fid":      parentID,
			"file_name":     name,
			"dir_path":      "",
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

	files, title, err := q.listShareFiles(ctx, cred, pwdID, stoken, "0")
	if err != nil {
		return nil, err
	}

	info.Title = title
	// RootID 必须用 **token 接口返回的 stoken**。
	//
	// 详情响应里的 data.share_token 实测**恒为空串**（实测 data 顶层键
	// 只有 is_owner / list / share），旧代码却把它当成 stoken 用，
	// 于是转存必然报 code=14001 Bad Parameter: [token内容错误]。
	// 这一点已用真实账号在服务器上直连接口验证过：
	//   stoken 用 token 接口的值 → code=0 且返回 task_id
	//   stoken 用详情的 share_token（空）→ code=14001
	info.RootID = stoken
	info.Files = files
	for _, f := range files {
		info.TotalSize += f.Size
	}
	return info, nil
}

// listShareFiles 列出分享内指定目录（pdirFid 为 "0" 表示根目录）。
//
// 抽出来是因为「一层文件夹一层打开」和「解析整个分享」用的是同一个
// 接口，只有 pdir_fid 不同 —— 复制一份迟早会两边改漏。
//
// 返回的每个 FileItem 都带着 share_fid_token（转存的准入令牌），
// 转存时逐项回传；**这是每个文件一个的令牌，不能整份分享共用一个 stoken**。
func (q *Quark) listShareFiles(ctx context.Context, cred *adapter.Credential, pwdID, stoken, pdirFid string) ([]adapter.FileItem, string, error) {
	if pdirFid == "" {
		pdirFid = "0"
	}
	var listResp struct {
		Status int `json:"status"`
		Code   int `json:"code"`
		Data   struct {
			List []struct {
				FID      string `json:"fid"`
				FileName string `json:"file_name"`
				Dir      bool   `json:"dir"`
				Size     int64  `json:"size"`
				// share_fid_token 是转存的"准入令牌"，必须逐项带回。
				// 之前漏读这个字段，SaveToDir 只能拿 fid 冒充 token，
				// 转存必然失败。
				ShareFidToken string `json:"share_fid_token"`
			} `json:"list"`
			Share struct {
				Title    string `json:"title"`
				ShareURL string `json:"share_url"`
			} `json:"share"`
			ShareToken string `json:"share_token"`
		} `json:"data"`
	}
	_, err := q.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     baseURL + "/share/sharepage/detail",
		Headers: q.headers(cred.Cookie),
		Query: map[string]string{
			"pr":            "ucpro",
			"fr":            "pc",
			"pwd_id":        pwdID,
			"stoken":        stoken,
			"pdir_fid":      pdirFid,
			"force":         "0",
			"_page":         "1",
			"_size":         "200",
			"_fetch_banner": "0",
			"_fetch_share":  "1",
			"_fetch_total":  "1",
			"_sort":         "file_type:asc,updated_at:desc",
		},
	}, &listResp)
	if err != nil {
		return nil, "", err
	}
	if listResp.Code != 0 {
		return nil, "", adapter.ErrShareExpired
	}

	files := make([]adapter.FileItem, 0, len(listResp.Data.List))
	for _, f := range listResp.Data.List {
		files = append(files, adapter.FileItem{
			ID:    f.FID,
			Name:  f.FileName,
			IsDir: f.Dir,
			Size:  f.Size,
			Ext:   extOf(f.FileName),
			Token: f.ShareFidToken,
		})
	}
	return files, listResp.Data.Share.Title, nil
}

// ListShareDir 列出分享内某一层目录 —— 供前端"一层文件夹一层打开"。
//
// 实现为 adapter.ShareBrowser（可选能力），不进 DiskAdapter 主接口。
func (q *Quark) ListShareDir(ctx context.Context, cred *adapter.Credential, shareURL, passcode, pdirFid string) (*adapter.ShareEntry, error) {
	m := shareURLRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法从链接中提取分享 ID: %s", shareURL)
	}
	pwdID := m[1]

	stoken, err := q.getShareToken(ctx, cred, pwdID, passcode)
	if err != nil {
		return nil, err
	}
	files, title, err := q.listShareFiles(ctx, cred, pwdID, stoken, pdirFid)
	if err != nil {
		return nil, err
	}
	if pdirFid == "" {
		pdirFid = "0"
	}
	return &adapter.ShareEntry{Fid: pdirFid, Title: title, Files: files, RootID: stoken}, nil
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
//
// 关键点（错一个就整单失败）：
//
//	fid_list       —— 文件 ID，来自分享详情
//	fid_token_list —— 逐项对应的 share_fid_token，**不是 fid 本身**
//	to_pdir_fid    —— 目标目录 fid（"0" 表示根目录）
//	pwd_id/stoken  —— 分享 ID 与分享 stoken，缺一会报"分享已失效"
//	pdir_fid       —— 分享内要转存的父目录，顶层固定 "0"
//
// 之前 fid_token_list 直接复制了 fid_list，服务端一律拒绝，
// 表现就是"夸克转存失败"（百度同理，见 baidu.go）。
func (q *Quark) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
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
	// 令牌缺失就不必发了 —— 直接说清楚原因，比让服务端回一个
	// 语焉不详的 code 更容易定位。
	for i, t := range tokenFids {
		if t == "" {
			return nil, fmt.Errorf("分享条目 %q 缺少转存令牌，请重新解析分享", share.Files[i].Name)
		}
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
	if resp.Status != 200 || resp.Code != 0 {
		return nil, fmt.Errorf("转存失败: status=%d code=%d msg=%s", resp.Status, resp.Code, resp.Message)
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
//
// 两个必须点（都踩过）：
//
//  1. 端点形式是 GET /task?task_id=<id>（task_id 走 query），
//     写成 /task/<id> 会**恒返回 404** —— 于是每次轮询都解析不出状态，
//     一直循环到 deadline，最后误报"任务超时"。
//  2. 失败原因在**顶层** code/message，不在 data 里。只认 data.status
//     会把 code=32003 "capacity limit"（空间不足）这种真实原因吞掉。
func (q *Quark) waitTask(ctx context.Context, cred *adapter.Credential, taskID string) error {
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
				Status    int    `json:"status"`
				TaskTitle string `json:"task_title"`
				Message   string `json:"message"`
				SaveAs    struct {
					RemainCapacity int64 `json:"remain_capacity"`
				} `json:"save_as"`
			} `json:"data"`
			Metadata struct {
				MissingCapacity int64 `json:"missing_capacity"`
			} `json:"metadata"`
		}
		_, err := q.http.DoJSON(ctx, &adapter.Request{
			Method:  "GET",
			URL:     baseURL + "/task",
			Headers: q.headers(cred.Cookie),
			Query: map[string]string{
				"pr": "ucpro", "fr": "pc",
				"task_id": taskID, "retry_index": "0",
			},
		}, &resp)
		if err != nil {
			continue
		}
		// data.status：2=成功，3=失败
		if resp.Data.Status == 2 {
			return nil
		}
		// 顶层 code 非 0 也是终态失败（任务在跑时 code=0）。
		if resp.Data.Status == 3 || resp.Code != 0 {
			msg := resp.Data.Message
			if msg == "" {
				msg = resp.Message
			}
			return fmt.Errorf("%s", adapter.TaskErrorText("夸克网盘", resp.Code, msg,
				resp.Metadata.MissingCapacity, resp.Data.SaveAs.RemainCapacity))
		}
		lastMsg = resp.Data.Message
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
		ExpiresAt: time.Now().Add(30 * time.Minute).Unix(),
		// Cookie 是必需的：夸克 CDN 靠它做准入，只带 UA/Referer 会被
		// 判 412（实测，见 adapter.MediaHeaders 的注释）。
		Headers:      adapter.MediaHeaders(userAgent, "https://pan.quark.cn/", cred.Cookie),
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
