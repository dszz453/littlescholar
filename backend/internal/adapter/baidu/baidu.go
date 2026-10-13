package baidu

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

// Baidu 百度网盘适配器
//
// 说明：百度网盘的分享解析与转存需要 BDUSS + STOKEN 等凭证，
// 且转存接口调用链较长（verify -> transfer -> task 轮询）。
// 本适配器实现了完整的目录浏览、直链获取（含分块信息）与转存骨架，
// 转存的细节参数建议结合实际账号在阶段二联调时校准。
type Baidu struct {
	http *adapter.HTTPClient

	// bdstoken 缓存：写操作全都要它，但每个请求都去换一遍太浪费。
	tokenMu sync.Mutex
	tokens  map[string]tokenEntry
}

type tokenEntry struct {
	token string
	at    time.Time
}

func init() {
	adapter.Register(New())
}

func New() *Baidu {
	return &Baidu{
		http:   adapter.NewHTTPClient(30 * time.Second),
		tokens: make(map[string]tokenEntry),
	}
}

func (b *Baidu) Type() adapter.DiskType { return adapter.DiskBaidu }
func (b *Baidu) Name() string           { return "百度网盘" }

const (
	panURL     = "https://pan.baidu.com"
	basePanAPI = "https://pan.baidu.com/api"
	passport   = "https://passport.baidu.com"
)

const ua = "netdisk;P2SP;2.2.60.26"

// webUA passport 域下的登录/扫码接口认浏览器 UA，
// 拿网盘客户端 UA（netdisk;P2SP;…）去调会直接被拒。
const webUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// gid 百度用的设备标识，任意 32 位十六进制串即可，但要每次新生成。
func gid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "A1B2C3D4E5F60718293A4B5C6D7E8F90"
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

func (b *Baidu) headers(cred *adapter.Credential) map[string]string {
	h := map[string]string{
		"User-Agent": ua,
		"Cookie":     cred.Cookie,
		"Referer":    "https://pan.baidu.com/disk/home",
	}
	if cred.Extra != nil {
		if stoken := cred.Extra["STOKEN"]; stoken != "" {
			h["Cookie"] = strings.TrimRight(cred.Cookie, "; ") + "; STOKEN=" + stoken
		}
	}
	return h
}

// ============ 认证 ============

// ============ 扫码登录 ============
//
// 百度的扫码是三段式，之前只做了第一段，所以永远停在"已扫码待确认"：
//
//	1. getqrcode  取 sign + 图片；sign 的**前 32 位**就是 channel_id
//	2. unicast    拿 channel_id 换 channel_v（URL 编码的 JSON）
//	3. channel_v  里 status=2 时 v 字段就是 BDUSS，再用它换 STOKEN
//
// 之前第 2 步把**自己生成的 sessionID** 当成 channel_id 传了过去，
// 服务端根本查不到这个通道；第 3 步则完全没有，用户扫完还得手抄 BDUSS。

// GetQRCode 获取扫码登录二维码
func (b *Baidu) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	var devResp struct {
		Errno  int    `json:"errno"`
		Sign   string `json:"sign"`
		ImgURL string `json:"imgurl"`
		Img    string `json:"img"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     passport + "/v2/api/getqrcode",
		Headers: map[string]string{"User-Agent": webUA},
		Query: map[string]string{
			"lp":          "pc",
			"qrloginfrom": "pc",
			"gid":         gid(),
			"apiver":      "v3",
		},
	}, &devResp)
	if err != nil {
		return nil, fmt.Errorf("获取百度二维码失败: %w", err)
	}
	if devResp.Errno != 0 {
		return nil, fmt.Errorf("百度返回 errno=%d", devResp.Errno)
	}
	if devResp.Sign == "" {
		return nil, fmt.Errorf("百度未返回二维码 sign")
	}

	// sign 前 32 位即 channel_id —— 轮询必须用它，不能用自造的 ID。
	channelID := devResp.Sign
	if len(channelID) > 32 {
		channelID = channelID[:32]
	}

	// 官方二维码内容就是它给的 imgurl。
	// 注意字段名是 imgurl 而不是 img，且返回值**不带协议头**
	// （形如 passport.baidu.com/v2/api/qrcode?sign=…），要补 https://。
	qrContent := devResp.ImgURL
	if qrContent == "" {
		qrContent = passport + "/v2/api/qrcode?sign=" + url.QueryEscape(devResp.Sign) +
			"&lp=pc&qrloginfrom=pc"
	} else if !strings.HasPrefix(qrContent, "http") {
		qrContent = "https://" + qrContent
	}

	// 百度没有内联 base64 图，imgurl 本身就是一个"返回 PNG"的地址，
	// 直接交给前端当 <img src> 用即可。
	image := devResp.Img
	if image == "" {
		image = qrContent
	}

	return &adapter.QRCode{
		SessionID: "baidu_" + channelID,
		Content:   qrContent,
		Image:     image,
		ExpiresIn: 180,
	}, nil
}

// PollQRCode 轮询扫码状态
func (b *Baidu) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	channelID := strings.TrimPrefix(sessionID, "baidu_")
	if channelID == "" {
		return nil, fmt.Errorf("扫码会话无效")
	}

	var resp struct {
		Errno    int    `json:"errno"`
		ChannelV string `json:"channel_v"`
		Msg      string `json:"msg"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     passport + "/channel/unicast",
		Headers: map[string]string{"User-Agent": webUA},
		Query:   map[string]string{"channel_id": channelID, "tpl": "netdisk", "apiver": "v3"},
	}, &resp)
	if err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}
	if resp.Errno != 0 {
		// errno=1 是"尚未产生结果"，属正常等待；其余才算失败。
		if resp.Errno == 1 {
			return &adapter.LoginResult{Status: "waiting", Message: "等待扫码"}, nil
		}
		return &adapter.LoginResult{
			Status:  "failed",
			Message: fmt.Sprintf("百度扫码失败: errno=%d %s", resp.Errno, resp.Msg),
		}, nil
	}

	// channel_v 是 URL 编码的 JSON：{"status":2,"v":"<BDUSS>"}
	raw, err := url.QueryUnescape(resp.ChannelV)
	if err != nil {
		raw = resp.ChannelV
	}
	var cv struct {
		Status int    `json:"status"`
		V      string `json:"v"`
	}
	if err := json.Unmarshal([]byte(raw), &cv); err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "等待扫码"}, nil
	}

	switch cv.Status {
	case 0:
		return &adapter.LoginResult{Status: "waiting", Message: "等待扫码"}, nil
	case 1:
		return &adapter.LoginResult{Status: "scanned", Message: "已扫码，请在手机上确认"}, nil
	case 2:
		if cv.V == "" {
			return &adapter.LoginResult{Status: "failed", Message: "未取到 BDUSS"}, nil
		}
		cookie, err := b.cookieFromBDUSS(ctx, cv.V)
		if err != nil {
			// 拿不到 STOKEN 也算"能用一半"：BDUSS 本身足够读，
			// 只是转存会失败。把 BDUSS 存进去并如实说明。
			return &adapter.LoginResult{
				Status:  "confirmed",
				Cookie:  "BDUSS=" + cv.V,
				Message: "已登录，但 " + err.Error(),
			}, nil
		}
		return &adapter.LoginResult{Status: "confirmed", Cookie: cookie, Message: "登录成功"}, nil
	default:
		return &adapter.LoginResult{Status: "waiting", Message: "等待扫码"}, nil
	}
}

// cookieFromBDUSS 用 BDUSS 补齐 STOKEN，拼出可用的完整 cookie。
//
// 百度网盘除 BDUSS 外，转存类接口还要 STOKEN；STOKEN 不能凭空造，
// 必须拿 BDUSS 去问 gettemplatevariable。少了它，账号"校验通过"，
// 一列目录就报 -6（登录失效）。
func (b *Baidu) cookieFromBDUSS(ctx context.Context, bduss string) (string, error) {
	var resp struct {
		Errno  int                 `json:"errno"`
		Stoken string              `json:"stoken"`
		Result []map[string]string `json:"result"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    basePanAPI + "/gettemplatevariable",
		Headers: map[string]string{
			"User-Agent": ua,
			"Cookie":     "BDUSS=" + bduss,
			"Referer":    panURL + "/disk/home",
		},
		Query: map[string]string{
			"clienttype": "0", "app_id": "250528", "web": "1",
			"fields": `["stoken"]`,
		},
	}, &resp)
	if err != nil {
		return "", fmt.Errorf("获取 STOKEN 失败: %w", err)
	}
	stoken := resp.Stoken
	if stoken == "" && len(resp.Result) > 0 {
		stoken = resp.Result[0]["stoken"]
	}
	if stoken == "" {
		return "", fmt.Errorf("未取到 STOKEN")
	}
	return "BDUSS=" + bduss + "; STOKEN=" + stoken, nil
}

func (b *Baidu) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		Errno    int    `json:"errno"`
		Bdstoken string `json:"bdstoken"`
		Quota    struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"quota"`
		Username    string `json:"username"`
		NetdiskName string `json:"netdisk_name"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     basePanAPI + "/quota",
		Headers: b.headers(cred),
		Query:   map[string]string{"checkexpire": "1", "checkfree": "1", "clienttype": "0", "app_id": "250528", "web": "1"},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Errno == -6 {
		return nil, adapter.ErrCookieInvalid
	}
	if resp.Errno != 0 {
		return nil, fmt.Errorf("百度账号校验失败: errno=%d", resp.Errno)
	}

	// 补一道 STOKEN 可用性探测（否则会出现"界面显示有效、一转存就失败"）。
	//
	// uinfo 与 /quota 只认 BDUSS，STOKEN 过期时它们照样返回 errno=0，
	// 于是界面显示"有效"。但真正要用的时候全挂：实测 STOKEN 失效时
	// gettemplatevariable 返回 errno=-6、share/verify 对所有分享一律
	// 返回 errno=-12（8/8 条不同分享全部如此，与分享本身无关）。
	// bdstoken 只有 STOKEN 有效才拿得到，所以拿它作为"这个 cookie 还能写"
	// 的判据：拿不到就如实报失效，提示用户重新接入，而不是让用户
	// 在转存时面对一个语焉不详的错误。
	var tpl struct {
		Errno  int `json:"errno"`
		Result struct {
			Bdstoken string `json:"bdstoken"`
		} `json:"result"`
	}
	if _, e := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     basePanAPI + "/gettemplatevariable",
		Headers: b.headers(cred),
		Query: map[string]string{
			"fields": `["bdstoken"]`, "clienttype": "0", "app_id": "250528", "web": "1",
		},
	}, &tpl); e == nil && (tpl.Errno != 0 || tpl.Result.Bdstoken == "") {
		return nil, adapter.ErrCookieInvalid
	}

	return &adapter.AccountInfo{
		Nickname:      orDefault(resp.NetdiskName, resp.Username, "百度网盘"),
		CapacityUsed:  resp.Quota.Used,
		CapacityTotal: resp.Quota.Total,
	}, nil
}

// ============ 文件操作 ============

func (b *Baidu) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "/"
	}
	var resp struct {
		Errno int `json:"errno"`
		List  []struct {
			FsID        int64  `json:"fs_id"`
			Path        string `json:"path"`
			ServerName  string `json:"server_filename"`
			IsDir       int    `json:"isdir"`
			Size        int64  `json:"size"`
			ServerMtime int64  `json:"server_mtime"`
		} `json:"list"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     basePanAPI + "/list",
		Headers: b.headers(cred),
		Query: map[string]string{
			"dir": dirID, "order": "time", "desc": "1",
			"web": "1", "app_id": "250528", "clienttype": "0",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Errno != 0 {
		return nil, fmt.Errorf("列出百度目录失败: errno=%d", resp.Errno)
	}
	items := make([]adapter.FileItem, 0, len(resp.List))
	for _, f := range resp.List {
		// 目录的 ID 用**路径**而不是 fs_id。
		//
		// 百度 /api/list 的 dir 参数、转存/新建/删除的 path 参数，
		// 要的都是绝对路径；只有取直链（filemetas）要 fs_id。
		// 之前目录也返回 fs_id，导致"点进子目录"必定报错、
		// 转存也拿不到合法的目标路径。文件保持 fs_id 不变。
		id := fmt.Sprint(f.FsID)
		if f.IsDir == 1 && f.Path != "" {
			id = f.Path
		}
		items = append(items, adapter.FileItem{
			ID:        id,
			Name:      f.ServerName,
			IsDir:     f.IsDir == 1,
			Size:      f.Size,
			UpdatedAt: time.Unix(f.ServerMtime, 0).Format(time.RFC3339),
			Ext:       extOf(f.ServerName),
		})
	}
	return items, nil
}

func (b *Baidu) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	path := strings.TrimRight(parentID, "/") + "/" + name
	token, err := b.bdstoken(ctx, cred)
	if err != nil {
		return "", err
	}
	var resp struct {
		Errno int   `json:"errno"`
		FsID  int64 `json:"fs_id"`
	}
	_, err = b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/create",
		Headers: b.headers(cred),
		Query:   map[string]string{"a": "commit", "bdstoken": token, "web": "1", "app_id": "250528", "clienttype": "0"},
		Body:    map[string]interface{}{"path": path, "isdir": 1, "block_list": []string{}},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Errno != 0 {
		return "", fmt.Errorf("百度新建目录失败: errno=%d", resp.Errno)
	}
	return path, nil
}

func (b *Baidu) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	token, err := b.bdstoken(ctx, cred)
	if err != nil {
		return err
	}
	// 百度 filemanager 的 filelist 收的是"路径"数组的 JSON 串。
	var filelist []string
	for _, id := range fileIDs {
		filelist = append(filelist, `"`+id+`"`)
	}
	body := strings.NewReader(`{"filelist":[` + strings.Join(filelist, ",") + `]}`)
	var resp struct {
		Errno int `json:"errno"`
	}
	_, err = b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/filemanager",
		Headers: b.headers(cred),
		Query:   map[string]string{"opera": "delete", "bdstoken": token, "web": "1", "app_id": "250528", "clienttype": "0"},
		RawBody: mustRead(body),
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Errno != 0 {
		return fmt.Errorf("百度删除失败: errno=%d", resp.Errno)
	}
	return nil
}

func (b *Baidu) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	token, err := b.bdstoken(ctx, cred)
	if err != nil {
		return err
	}
	var resp struct {
		Errno int `json:"errno"`
	}
	_, err = b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/filemanager",
		Headers: b.headers(cred),
		Query:   map[string]string{"opera": "rename", "bdstoken": token, "web": "1", "app_id": "250528", "clienttype": "0"},
		Body:    map[string]interface{}{"filelist": []map[string]string{{"path": fileID, "newname": newName}}},
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Errno != 0 {
		return fmt.Errorf("百度重命名失败: errno=%d", resp.Errno)
	}
	return nil
}

// ============ 分享与转存 ============

var baiduShareRe = regexp.MustCompile(`/s/([0-9a-zA-Z_-]+)`)

func (b *Baidu) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	m := baiduShareRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法提取百度分享 ID: %s", shareURL)
	}
	shareID := m[1]

	// 1. 验证提取码并获取 shareid / uk
	realShareID, uk, randsk, err := b.shareVerify(ctx, cred, shareID, passcode)
	if err != nil {
		return nil, err
	}

	// 2. 列出分享内容
	files, err := b.listShareDir(ctx, cred, shareID, "")
	if err != nil {
		return nil, err
	}

	info := &adapter.ShareInfo{
		ShareURL: shareURL,
		Passcode: passcode,
		RootID:   fmt.Sprintf("%d", realShareID),
		// from 参数要的是**分享者的 uk**，不是自己的 uid。
		// 旧代码把它丢掉了，转存只能传 from=""，服务端直接判参数错误。
		OwnerID: fmt.Sprintf("%d", uk),
		// randsk 转存时要作为 sekey 原样回传。
		SeKey: randsk,
		Title: fmt.Sprintf("百度分享_%s", shareID),
		Files: files,
	}
	for _, f := range files {
		info.TotalSize += f.Size
	}
	if len(info.Files) == 1 {
		info.Title = info.Files[0].Name
	}
	return info, nil
}

// shareVerify 验证百度分享的提取码，返回 shareid / 分享者 uk / randsk。
//
// 抽出来给「解析整个分享」和「一层层打开文件夹」共用 —— 两者都要先过
// 这一关，复制一份迟早会两边改漏。
func (b *Baidu) shareVerify(ctx context.Context, cred *adapter.Credential, surl, passcode string) (int64, int64, string, error) {
	var verifyResp struct {
		Errno    int    `json:"errno"`
		RandSkey string `json:"randsk"`
		ShareID  int64  `json:"shareid"`
		UK       int64  `json:"uk"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     panURL + "/share/verify",
		Headers: b.headers(cred),
		Query:   map[string]string{"surl": surl, "bdstoken": "", "t": fmt.Sprint(time.Now().UnixMilli()), "channel": "chunlei", "web": "1", "app_id": "250528", "clienttype": "0"},
		Body:    map[string]interface{}{"pwd": passcode, "vcode": "", "vcode_str": ""},
	}, &verifyResp)
	if err != nil {
		return 0, 0, "", err
	}
	if verifyResp.Errno == -9 {
		return 0, 0, "", adapter.ErrNeedPassword
	}
	if verifyResp.Errno != 0 {
		return 0, 0, "", adapter.ErrShareExpired
	}
	return verifyResp.ShareID, verifyResp.UK, verifyResp.RandSkey, nil
}

// listShareDir 列出百度分享内某一层目录。
//
// 百度的 /share/list 用两种模式：
//
//	root=1            列分享根目录
//	root=0&dir=<路径> 列分享内的子目录（dir 是分享内的**相对路径**）
//
// 目录项同时带 fs_id 和 path：转存要前者、下钻要后者，
// 所以目录项把 path 一起放进 SharePath。
//
// ⚠️ 本函数与 ListShareDir 未经线上验证：线上百度账号的 STOKEN 已失效
// （share/verify 对所有分享一律 errno=-12），拿不到有效数据。
// 实现与 ParseShare 走同一套调用，账号恢复后即可生效。
func (b *Baidu) listShareDir(ctx context.Context, cred *adapter.Credential, surl, pdir string) ([]adapter.FileItem, error) {
	q := map[string]string{
		"shorturl": surl, "web": "1", "app_id": "250528",
		"channel": "chunlei", "clienttype": "0",
	}
	if pdir == "" || pdir == "/" {
		q["root"] = "1"
	} else {
		q["root"] = "0"
		q["dir"] = pdir
	}

	var listResp struct {
		Errno int `json:"errno"`
		List  []struct {
			FsID       int64  `json:"fs_id"`
			Path       string `json:"path"`
			ServerName string `json:"server_filename"`
			IsDir      int    `json:"isdir"`
			Size       int64  `json:"size"`
		} `json:"list"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     panURL + "/share/list",
		Headers: b.headers(cred),
		Query:   q,
	}, &listResp)
	if err != nil {
		return nil, err
	}
	if listResp.Errno != 0 {
		return nil, fmt.Errorf("百度分享列出失败: errno=%d", listResp.Errno)
	}

	files := make([]adapter.FileItem, 0, len(listResp.List))
	for _, f := range listResp.List {
		files = append(files, adapter.FileItem{
			ID:    fmt.Sprint(f.FsID),
			Name:  f.ServerName,
			IsDir: f.IsDir == 1,
			Size:  f.Size,
			Ext:   extOf(f.ServerName),
			// 下钻用：目录的相对路径。
			SharePath: f.Path,
		})
	}
	return files, nil
}

// ListShareDir 列出百度分享内某一层目录 —— 供前端"一层文件夹一层打开"。
func (b *Baidu) ListShareDir(ctx context.Context, cred *adapter.Credential, shareURL, passcode, pdirFid string) (*adapter.ShareEntry, error) {
	m := baiduShareRe.FindStringSubmatch(shareURL)
	if len(m) < 2 {
		return nil, fmt.Errorf("无法提取百度分享 ID: %s", shareURL)
	}
	surl := m[1]
	realShareID, uk, randsk, err := b.shareVerify(ctx, cred, surl, passcode)
	if err != nil {
		return nil, err
	}
	files, err := b.listShareDir(ctx, cred, surl, pdirFid)
	if err != nil {
		return nil, err
	}
	if pdirFid == "" {
		pdirFid = "/"
	}
	return &adapter.ShareEntry{
		Fid:   pdirFid,
		Title: fmt.Sprintf("百度分享_%s", surl),
		Files: files,
		// 百度转存要 shareid + 分享者 uk + randsk 三件套，缺一转存必失败。
		RootID:  fmt.Sprintf("%d", realShareID),
		OwnerID: fmt.Sprintf("%d", uk),
		SeKey:   randsk,
	}, nil
}

// bdstoken 取百度网盘的操作令牌。
//
// 百度的写操作（转存/新建/删除/重命名）全部要在 query 里带 bdstoken，
// 并且它**不在 cookie 里**，得单独问服务端要。旧代码一律传空串，
// 于是所有写操作都失败 —— 这正是"转存到百度也是失败的"的根因之一。
//
// 令牌会随会话变化，所以缓存 30 分钟即可，不必每次请求都换。
func (b *Baidu) bdstoken(ctx context.Context, cred *adapter.Credential) (string, error) {
	key := hashCookie(cred)
	b.tokenMu.Lock()
	if t, ok := b.tokens[key]; ok && time.Since(t.at) < 30*time.Minute {
		token := t.token
		b.tokenMu.Unlock()
		return token, nil
	}
	b.tokenMu.Unlock()

	var resp struct {
		Errno  int `json:"errno"`
		Result struct {
			Bdstoken string `json:"bdstoken"`
		} `json:"result"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     basePanAPI + "/gettemplatevariable",
		Headers: b.headers(cred),
		Query: map[string]string{
			"fields": `["bdstoken"]`, "clienttype": "0", "app_id": "250528", "web": "1",
		},
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Errno != 0 || resp.Result.Bdstoken == "" {
		return "", fmt.Errorf("获取 bdstoken 失败: errno=%d（Cookie 可能已失效）", resp.Errno)
	}

	b.tokenMu.Lock()
	b.tokens[key] = tokenEntry{token: resp.Result.Bdstoken, at: time.Now()}
	b.tokenMu.Unlock()
	return resp.Result.Bdstoken, nil
}

// hashCookie 用 cookie 做缓存键（不落盘，仅进程内）。
func hashCookie(cred *adapter.Credential) string {
	h := sha1.Sum([]byte(cred.Cookie))
	return hex.EncodeToString(h[:])
}

// SaveToDir 把分享转存到自己的网盘目录。
//
// 百度的转存接口对参数极其挑剔，旧实现几乎每一项都是错的：
//
//	✗ from=""            必须填**分享者 uk**（ShareInfo.OwnerID）
//	✗ bdstoken=""        必须填真实令牌
//	✗ 缺 sekey           必须回传 verify 得到的 randsk
//	✗ body 走 JSON       必须 form 编码（fsidlist=[..]&path=/x）
//	✗ path 用目录 ID     必须是自己网盘里的**绝对路径**
//
// 另外注意 path 的默认值：空路径对百度没有"根目录"的含义，
// 必须落到 / 或具体目录，否则 errno=2（参数错误）。
func (b *Baidu) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	var fsidList []string
	for _, f := range share.Files {
		fsidList = append(fsidList, f.ID)
	}
	if len(fsidList) == 0 {
		return nil, fmt.Errorf("分享里没有可转存的文件")
	}

	token, err := b.bdstoken(ctx, cred)
	if err != nil {
		return nil, err
	}

	// targetDirID 在本适配器里就是"目标路径"。空 / "0" 一律归到根目录。
	targetPath := strings.TrimSpace(targetDirID)
	if targetPath == "" || targetPath == "0" {
		targetPath = "/"
	}
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	fsidJSON, _ := json.Marshal(fsidList)
	form := url.Values{}
	form.Set("fsidlist", string(fsidJSON))
	form.Set("path", targetPath)

	q := url.Values{}
	q.Set("shareid", share.RootID)
	q.Set("from", share.OwnerID)
	q.Set("bdstoken", token)
	q.Set("sekey", share.SeKey)
	q.Set("channel", "chunlei")
	q.Set("web", "1")
	q.Set("app_id", "250528")
	q.Set("clienttype", "0")

	var resp struct {
		Errno int `json:"errno"`
		Info  []struct {
			Errno int    `json:"errno"`
			Path  string `json:"path"`
		} `json:"info"`
		ShowMsg string `json:"show_msg"`
		ErrMsg  string `json:"err_msg"`
	}
	_, err = b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     panURL + "/share/transfer?" + q.Encode(),
		Headers: b.headers(cred),
		RawBody: []byte(form.Encode()),
	}, &resp)
	if err != nil {
		return nil, err
	}

	// errno 语义（实测 + 公开实现）：
	//   0          成功
	//   12         同名文件已存在 —— 视作成功，用户要的就是"有这份文件"
	//   -6         cookie 失效
	//   2 / 其它   参数错误、分享被取消等
	switch resp.Errno {
	case 0, 12:
	default:
		if resp.Errno == -6 {
			return nil, adapter.ErrCookieInvalid
		}
		msg := orDefault(resp.ShowMsg, resp.ErrMsg, fmt.Sprintf("errno=%d", resp.Errno))
		return nil, fmt.Errorf("百度转存失败: %s", msg)
	}

	// 逐项错误码里只要有一个非 0/12 就说明这份文件没进去。
	for _, it := range resp.Info {
		if it.Errno != 0 && it.Errno != 12 {
			return nil, fmt.Errorf("百度转存失败(%s): errno=%d", it.Path, it.Errno)
		}
	}

	return &adapter.SaveResult{
		FileIDs: fsidList,
		Count:   len(fsidList),
		Message: fmt.Sprintf("成功转存 %d 个文件到 %s", len(fsidList), targetPath),
	}, nil
}

func (b *Baidu) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	share, err := b.ParseShare(ctx, cred, shareURL, passcode)
	if err != nil {
		return nil, err
	}
	return b.SaveToDir(ctx, cred, share, targetDirID)
}

// ============ 播放 / 下载 ============

func (b *Baidu) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	var resp struct {
		Errno int `json:"errno"`
		List  []struct {
			Dlink struct {
				Dlink string `json:"dlink"`
			} `json:"dlink"`
			Size int64 `json:"size"`
		} `json:"list"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     basePanAPI + "/filemetas",
		Headers: b.headers(cred),
		Query: map[string]string{
			"fsids": "[" + fileID + "]", "dlink": "1", "thumb": "1",
			"web": "1", "app_id": "250528", "clienttype": "0",
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if len(resp.List) == 0 || resp.List[0].Dlink.Dlink == "" {
		return nil, fmt.Errorf("百度获取直链失败: errno=%d", resp.Errno)
	}
	// 百度直链需要追加 access_token（如果使用 OAuth），这里直接用 dlink + cookie 访问
	link := resp.List[0].Dlink.Dlink
	if strings.Contains(link, "?") {
		link += "&access_token=" + extractBDUSS(cred.Cookie)
	} else {
		link += "?access_token=" + extractBDUSS(cred.Cookie)
	}
	return &adapter.DirectLink{
		URL:          link,
		Size:         resp.List[0].Size,
		ExpiresAt: time.Now().Add(8 * time.Hour).Unix(),
		// 百度 PCS 的下载域名（d.pcs.baidu.com）对部分文件会校验 BDUSS，
		// 与其它网盘保持一致带上 Cookie，避免"换个文件就 403"。
		Headers:      adapter.MediaHeaders(ua, "https://pan.baidu.com/", cred.Cookie),
		AcceptRanges: true,
	}, nil
}

func (b *Baidu) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return b.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func extractBDUSS(cookie string) string {
	jar := adapter.ParseCookie(cookie)
	if v := jar.Get("BDUSS"); v != "" {
		return v
	}
	return jar.Get("bds_token")
}

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

func mustRead(r *strings.Reader) []byte {
	buf := make([]byte, r.Len())
	_, _ = r.Read(buf)
	return buf
}
