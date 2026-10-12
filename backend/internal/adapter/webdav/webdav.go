// Package webdav 把通用 WebDAV 服务接入成系统里的一种「存储源」。
//
// 设计取舍说明：
//
// WebDAV 不是网盘，但它和网盘在本系统里的用法高度一致 ——
// 都要能列目录、能拿到可播放的地址、能新建目录。因此复用 adapter.DiskAdapter
// 抽象比另起一套链路划算得多：转码（service.TranscodeService）、
// 播放（handler.PlayHandler）、目录浏览（handler.DiskHandler）全部零改动复用。
//
// 凭证约定（复用 Credential 结构，不新增字段，避免改动公共类型）：
//
//	Credential.Cookie       = "用户名:密码"（Basic Auth 原文，落库前会被加密）
//	Credential.Extra["url"] = WebDAV 根地址，如 https://nas.example.com:5006/dav
//
// dirID / fileID 统一是**相对根地址的未编码路径**，如 "/学习/数学"。
// 根目录用 "" 或 "/" 表示。编码交给 net/url 处理，避免双重转义。
package webdav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

func init() {
	adapter.Register(New())
}

// WebDAV 适配器
type WebDAV struct {
	http *adapter.HTTPClient
}

func New() *WebDAV {
	// 超时给到 60s：NAS / Alist 上的大目录做 PROPFIND 常常超过 30s，
	// 用默认的 30s 会在真实场景里频繁超时。
	return &WebDAV{http: adapter.NewHTTPClient(60 * time.Second)}
}

func (w *WebDAV) Type() adapter.DiskType { return adapter.DiskWebdav }
func (w *WebDAV) Name() string           { return "WebDAV" }

// SupportsQR 实现 adapter.QRCapable。
//
// 声明为 false 之后，管理后台的「扫码添加」会对 WebDAV 变灰，
// 用户只能走「手动添加」——这才符合协议事实，否则点了扫码只会
// 收到一句"不支持"，体验上像是功能坏了。
func (w *WebDAV) SupportsQR() bool { return false }

// ============ 凭证解析 ============

func baseURL(cred *adapter.Credential) (string, error) {
	if cred == nil || cred.Extra == nil {
		return "", fmt.Errorf("WebDAV 缺少配置（需要有 url）")
	}
	raw := strings.TrimSpace(cred.Extra["url"])
	if raw == "" {
		// 兼容用户直接把地址填在 Cookie 里的情况？不做兼容 ——
		// 静默猜测只会让配错时更难排查，这里明确报错。
		return "", fmt.Errorf("WebDAV 未配置服务器地址")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("WebDAV 地址不合法: %w", err)
	}
	return strings.TrimRight(raw, "/"), nil
}

// authHeader 由 "用户名:密码" 生成 Basic Auth 头值。
func authHeader(cred *adapter.Credential) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred.Cookie))
}

// joinURL 把相对路径拼到根地址上，由 net/url 负责百分号编码。
func joinURL(base, p string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(p, "/")
	u.RawPath = "" // 让 url 重新计算 RawPath
	return u.String(), nil
}

// relPath 把服务器返回的 href 归一成"相对根地址的未编码路径"。
//
// 归一很关键：不同实现返回的 href 五花八门 ——
// 有带完整 URL 的、有带根地址前缀的、有结尾带不带斜杠的、
// 还有已经百分号编码过的。统一处理掉，上层才不用到处做兼容。
func relPath(base, href string) string {
	if u, err := url.Parse(href); err == nil && u.Path != "" {
		href = u.Path
	}
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	bp := ""
	if u, err := url.Parse(base); err == nil {
		bp = strings.TrimRight(u.Path, "/")
	}
	if bp != "" && strings.HasPrefix(href, bp) {
		href = href[len(bp):]
	}
	href = "/" + strings.TrimLeft(href, "/")
	if len(href) > 1 {
		href = strings.TrimRight(href, "/")
	}
	return href
}

// ============ WebDAV 协议 ============

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:displayname/>
    <D:resourcetype/>
    <D:getcontentlength/>
    <D:getlastmodified/>
  </D:prop>
</D:propfind>`

type davMultistatus struct {
	Responses []davResponse `xml:"response"`
}

type davResponse struct {
	Href     string        `xml:"href"`
	Propstat []davPropstat `xml:"propstat"`
}

type davPropstat struct {
	Status string  `xml:"status"`
	Prop   davProp `xml:"prop"`
}

type davProp struct {
	DisplayName   string          `xml:"displayname"`
	ContentLength string          `xml:"getcontentlength"`
	LastModified  string          `xml:"getlastmodified"`
	ResourceType  davResourceType `xml:"resourcetype"`
}

type davResourceType struct {
	// 指针非 nil 即表示是目录（collection 元素存在）。
	// 用 *struct{} 而不是 bool，因为要区分"元素不存在"与"元素为 false"。
	Collection *struct{} `xml:"collection"`
}

// propfind 发一次 PROPFIND，depth 用 "0" 或 "1"。
func (w *WebDAV) propfind(ctx context.Context, cred *adapter.Credential, path, depth string) (*davMultistatus, error) {
	base, err := baseURL(cred)
	if err != nil {
		return nil, err
	}
	full, err := joinURL(base, path)
	if err != nil {
		return nil, err
	}

	resp, err := w.http.Do(ctx, &adapter.Request{
		Method: "PROPFIND",
		URL:    full,
		Headers: map[string]string{
			"Authorization": authHeader(cred),
			"Depth":         depth,
			"Content-Type":  "application/xml; charset=utf-8",
		},
		RawBody: []byte(propfindBody),
	})
	if err != nil {
		return nil, fmt.Errorf("WebDAV 请求失败: %w", err)
	}
	switch resp.StatusCode {
	case 207:
		// 正常：Multi-Status
	case 200:
		// 少数实现（个别简化版）会对 PROPFIND 直接回 200 + 单个 response
	case 401, 403:
		return nil, adapter.ErrCookieInvalid
	case 404:
		return nil, fmt.Errorf("路径不存在: %s", path)
	default:
		return nil, fmt.Errorf("WebDAV 返回异常状态 %d: %s", resp.StatusCode, truncate(string(resp.Body), 200))
	}

	var ms davMultistatus
	if err := xml.Unmarshal(resp.Body, &ms); err != nil {
		return nil, fmt.Errorf("解析 WebDAV 响应失败: %w (原始内容: %s)", err, truncate(string(resp.Body), 200))
	}
	return &ms, nil
}

// ============ 认证 ============

// GetQRCode WebDAV 不支持扫码。
//
// 不是"没实现"，而是协议本身没有这个概念：WebDAV 用 HTTP Basic/Digest
// 认证，没有二维码登录流程。返回明确提示引导用户走「手动添加」。
func (w *WebDAV) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	return nil, fmt.Errorf("WebDAV 不支持扫码，请在「手动添加」里填写地址、用户名和密码")
}

func (w *WebDAV) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	return nil, fmt.Errorf("WebDAV 不支持扫码")
}

// ValidateCookie 用 PROPFIND 验证地址与凭据是否可用。
func (w *WebDAV) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	base, err := baseURL(cred)
	if err != nil {
		return nil, err
	}
	if _, err := w.propfind(ctx, cred, "/", "0"); err != nil {
		return nil, err
	}
	u, _ := url.Parse(base)
	return &adapter.AccountInfo{
		// WebDAV 没有昵称，用 host 当展示名，用户还能手动覆盖
		Nickname: u.Host,
	}, nil
}

// ============ 文件操作 ============

func (w *WebDAV) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	if dirID == "" {
		dirID = "/"
	}
	base, err := baseURL(cred)
	if err != nil {
		return nil, err
	}
	ms, err := w.propfind(ctx, cred, dirID, "1")
	if err != nil {
		return nil, err
	}

	selfPath := relPath(base, dirID)
	items := make([]adapter.FileItem, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		p := relPath(base, r.Href)
		// depth=1 会把被查询的目录本身也返回，跳过它，
		// 否则界面上会看到自己套自己。
		if p == selfPath {
			continue
		}
		prop := pickProp(r)
		name := prop.DisplayName
		if name == "" {
			name = pathBase(p)
		}
		var size int64
		if prop.ContentLength != "" {
			size, _ = strconv.ParseInt(prop.ContentLength, 10, 64)
		}
		items = append(items, adapter.FileItem{
			ID:        p,
			Name:      name,
			IsDir:     prop.ResourceType.Collection != nil,
			Size:      size,
			UpdatedAt: parseHTTPDate(prop.LastModified),
			Ext:       extOf(name),
		})
	}
	return items, nil
}

// pickProp 取第一个成功 propstat 的 prop。
//
// 为什么不能直接取 Responses[i].Propstat[0]：PROPFIND 常返回多个 propstat，
// 分别对应 200 和 404（比如某些属性不被支持）。取错就会拿到空属性，
// 表现为"文件列表全是无名空条目"。
func pickProp(r davResponse) davProp {
	for _, ps := range r.Propstat {
		if ps.Status == "" || strings.Contains(ps.Status, " 200 ") {
			return ps.Prop
		}
	}
	if len(r.Propstat) > 0 {
		return r.Propstat[0].Prop
	}
	return davProp{}
}

func (w *WebDAV) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if parentID == "" {
		parentID = "/"
	}
	base, err := baseURL(cred)
	if err != nil {
		return "", err
	}
	target := strings.TrimRight(parentID, "/") + "/" + name
	full, err := joinURL(base, target)
	if err != nil {
		return "", err
	}
	resp, err := w.http.Do(ctx, &adapter.Request{
		Method:  "MKCOL",
		URL:     full,
		Headers: map[string]string{"Authorization": authHeader(cred)},
	})
	if err != nil {
		return "", err
	}
	// 201 Created 是成功；405 表示已存在，对使用者来说结果一样，也放行
	if resp.StatusCode != 201 && resp.StatusCode != 405 {
		return "", fmt.Errorf("新建目录失败: HTTP %d", resp.StatusCode)
	}
	return target, nil
}

func (w *WebDAV) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	base, err := baseURL(cred)
	if err != nil {
		return err
	}
	for _, id := range fileIDs {
		full, err := joinURL(base, id)
		if err != nil {
			return err
		}
		resp, err := w.http.Do(ctx, &adapter.Request{
			Method:  "DELETE",
			URL:     full,
			Headers: map[string]string{"Authorization": authHeader(cred)},
		})
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			return fmt.Errorf("删除失败 %s: HTTP %d", id, resp.StatusCode)
		}
	}
	return nil
}

func (w *WebDAV) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	base, err := baseURL(cred)
	if err != nil {
		return err
	}
	src, err := joinURL(base, fileID)
	if err != nil {
		return err
	}
	dstPath := strings.TrimRight(pathDir(fileID), "/") + "/" + newName
	dst, err := joinURL(base, dstPath)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(ctx, &adapter.Request{
		Method: "MOVE",
		URL:    src,
		Headers: map[string]string{
			"Authorization": authHeader(cred),
			"Destination":   dst,
			"Overwrite":     "F",
		},
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != 201 && resp.StatusCode != 204 {
		return fmt.Errorf("重命名失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ============ 分享与转存：WebDAV 语义上不适用 ============

func (w *WebDAV) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	return nil, adapter.ErrNotImplemented
}

func (w *WebDAV) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}

func (w *WebDAV) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}

// ============ 播放 / 下载 ============

// GetDirectLink 返回文件的 WebDAV 地址与 Basic Auth 头。
//
// ⚠️ 这个 URL **不能直接给浏览器播放**：`<video src>` 无法携带
// Authorization 头，现代浏览器也禁止在 URL 里写 user:pass。
// 所以前端播 WebDAV 必须走后端代理（handler.StreamHandler），
// 由后端带上这里的 Headers 去取流并转发 Range。
//
// 而 ffmpeg 转码不受影响 —— 它支持 -headers，能直接吃这份 Headers，
// 这也是"WebDAV 接入后转码链路零改动复用"的原因。
func (w *WebDAV) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	base, err := baseURL(cred)
	if err != nil {
		return nil, err
	}
	full, err := joinURL(base, fileID)
	if err != nil {
		return nil, err
	}
	return &adapter.DirectLink{
		URL:          full,
		Headers:      map[string]string{"Authorization": authHeader(cred)},
		AcceptRanges: true, // WebDAV 规范要求支持 Range，播放大文件依赖它
	}, nil
}

func (w *WebDAV) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return w.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func pathDir(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// parseHTTPDate 解析 RFC1123 格式的 getlastmodified，失败返回空串
// （宁可留空，也不要用 time.Now() 伪造一个"刚刚修改"，那会误导用户）。
func parseHTTPDate(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
