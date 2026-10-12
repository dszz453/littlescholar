package baidu

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
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
}

func init() {
	adapter.Register(New())
}

func New() *Baidu {
	return &Baidu{http: adapter.NewHTTPClient(30 * time.Second)}
}

func (b *Baidu) Type() adapter.DiskType { return adapter.DiskBaidu }
func (b *Baidu) Name() string           { return "百度网盘" }

const (
	panURL     = "https://pan.baidu.com"
	basePanAPI = "https://pan.baidu.com/api"
)

const ua = "netdisk;P2SP;2.2.60.26"

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

func (b *Baidu) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	// 百度网盘扫码登录：先取 sign，再生成二维码
	var devResp struct {
		Errno int    `json:"errno"`
		Sign  string `json:"sign"`
		Img   string `json:"img"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     "https://passport.baidu.com/v2/api/getqrcode",
		Headers: map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"},
		Query: map[string]string{
			"lp":   "pc",
			"qrloginfrom": "pc",
			"gid":  "A1B2C3D4",
			"apiver": "v3",
		},
	}, &devResp)
	if err != nil {
		return nil, fmt.Errorf("获取百度二维码失败: %w", err)
	}
	if devResp.Sign == "" {
		return nil, fmt.Errorf("百度未返回二维码 sign")
	}
	sessionID := fmt.Sprintf("baidu_%d", time.Now().UnixNano())
	qrContent := "https://passport.baidu.com/v2/api/qrcode?sign=" + url.QueryEscape(devResp.Sign)
	return &adapter.QRCode{
		SessionID: sessionID,
		Content:   qrContent,
		Image:     devResp.Img,
		ExpiresIn: 180,
	}, nil
}

func (b *Baidu) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	// 百度扫码轮询接口
	var resp struct {
		Errno int    `json:"errno"`
		ChannelID string `json:"channel_id"`
		Bdstoken  string `json:"bdstoken"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "GET",
		URL:     "https://passport.baidu.com/channel/unicast",
		Headers: map[string]string{"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"},
		Query:   map[string]string{"channel_id": sessionID, "tpl": "netdisk", "apiver": "v3"},
	}, &resp)
	if err != nil {
		return &adapter.LoginResult{Status: "waiting", Message: "轮询中"}, nil
	}
	if resp.Errno == 1 {
		return &adapter.LoginResult{Status: "waiting", Message: "等待扫码"}, nil
	}
	if resp.Errno != 0 {
		return &adapter.LoginResult{Status: "failed", Message: fmt.Sprintf("扫码失败: errno=%d", resp.Errno)}, nil
	}
	return &adapter.LoginResult{
		Status:  "scanned",
		Message: "已扫码，请在手机上确认（确认后请在后台手动粘贴 BDUSS 完成绑定）",
	}, nil
}

func (b *Baidu) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	var resp struct {
		Errno    int    `json:"errno"`
		Bdstoken string `json:"bdstoken"`
		Quota    struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"quota"`
		Username string `json:"username"`
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
			FsID      int64  `json:"fs_id"`
			Path      string `json:"path"`
			ServerName string `json:"server_filename"`
			IsDir     int    `json:"isdir"`
			Size      int64  `json:"size"`
			ServerMtime int64 `json:"server_mtime"`
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
		items = append(items, adapter.FileItem{
			ID:        fmt.Sprint(f.FsID),
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
	var resp struct {
		Errno int   `json:"errno"`
		FsID  int64 `json:"fs_id"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/create",
		Headers: b.headers(cred),
		Query:   map[string]string{"a": "commit", "bdstoken": "", "web": "1", "app_id": "250528", "clienttype": "0"},
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
	var filelist []string
	for _, id := range fileIDs {
		filelist = append(filelist, `"`+id+`"`)
	}
	body := strings.NewReader(`{"filelist":[` + strings.Join(filelist, ",") + `]}`)
	var resp struct {
		Errno int `json:"errno"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/filemanager",
		Headers: b.headers(cred),
		Query:   map[string]string{"opera": "delete", "bdstoken": "", "web": "1", "app_id": "250528", "clienttype": "0"},
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
	var resp struct {
		Errno int `json:"errno"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     basePanAPI + "/filemanager",
		Headers: b.headers(cred),
		Query:   map[string]string{"opera": "rename", "bdstoken": "", "web": "1", "app_id": "250528", "clienttype": "0"},
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
	var verifyResp struct {
		Errno int    `json:"errno"`
		RandSkey string `json:"randsk"`
		ShareID  int64  `json:"shareid"`
		UK       int64  `json:"uk"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     panURL + "/share/verify",
		Headers: b.headers(cred),
		Query:   map[string]string{"surl": shareID, "bdstoken": "", "t": fmt.Sprint(time.Now().UnixMilli()), "channel": "chunlei", "web": "1", "app_id": "250528", "clienttype": "0"},
		Body:    map[string]interface{}{"pwd": passcode, "vcode": "", "vcode_str": ""},
	}, &verifyResp)
	if err != nil {
		return nil, err
	}
	if verifyResp.Errno == -9 {
		return nil, adapter.ErrNeedPassword
	}
	if verifyResp.Errno != 0 {
		return nil, adapter.ErrShareExpired
	}

	// 2. 列出分享内容
	var listResp struct {
		Errno int `json:"errno"`
		List  []struct {
			FsID      int64  `json:"fs_id"`
			Path      string `json:"path"`
			ServerName string `json:"server_filename"`
			IsDir     int    `json:"isdir"`
			Size      int64  `json:"size"`
		} `json:"list"`
	}
	_, err = b.http.DoJSON(ctx, &adapter.Request{
		Method: "GET",
		URL:    panURL + "/share/list",
		Headers: b.headers(cred),
		Query: map[string]string{
			"shorturl": shareID, "root": "1", "web": "1", "app_id": "250528",
			"channel": "chunlei", "clienttype": "0",
		},
	}, &listResp)
	if err != nil {
		return nil, err
	}
	if listResp.Errno != 0 {
		return nil, fmt.Errorf("百度分享列出失败: errno=%d", listResp.Errno)
	}

	info := &adapter.ShareInfo{
		ShareURL: shareURL,
		Passcode: passcode,
		RootID:   fmt.Sprintf("%d", verifyResp.ShareID),
		Title:    fmt.Sprintf("百度分享_%s", shareID),
	}
	for _, f := range listResp.List {
		info.Files = append(info.Files, adapter.FileItem{
			ID:    fmt.Sprint(f.FsID),
			Name:  f.ServerName,
			IsDir: f.IsDir == 1,
			Size:  f.Size,
		})
		info.TotalSize += f.Size
	}
	if len(info.Files) == 1 {
		info.Title = info.Files[0].Name
	}
	return info, nil
}

func (b *Baidu) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	// 百度转存：share/transfer
	var fsidList []string
	for _, f := range share.Files {
		fsidList = append(fsidList, f.ID)
	}
	var resp struct {
		Errno   int   `json:"errno"`
		TaskID  int64 `json:"task_id"`
		ErrMsg  string `json:"err_msg"`
	}
	_, err := b.http.DoJSON(ctx, &adapter.Request{
		Method:  "POST",
		URL:     panURL + "/share/transfer",
		Headers: b.headers(cred),
		Query:   map[string]string{"shareid": share.RootID, "from": "", "bdstoken": "", "channel": "chunlei", "web": "1", "app_id": "250528", "clienttype": "0"},
		Body: map[string]interface{}{
			"fsidlist": fsidList,
			"path":     targetDirID,
		},
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Errno != 0 {
		return nil, fmt.Errorf("百度转存失败: errno=%d msg=%s", resp.Errno, resp.ErrMsg)
	}
	return &adapter.SaveResult{
		FileIDs: fsidList,
		Count:   len(fsidList),
		Message: fmt.Sprintf("成功转存 %d 个文件", len(fsidList)),
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
		ExpiresAt:    time.Now().Add(8 * time.Hour).Unix(),
		Headers:      map[string]string{"User-Agent": ua, "Referer": "https://pan.baidu.com/"},
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
