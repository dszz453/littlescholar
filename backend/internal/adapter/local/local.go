// Package local 把「服务器本地硬盘上的一个目录」接入成系统里的一种存储源。
//
// 为什么需要它：很多用户的内容本来就躺在部署机（NAS / 小主机 / 树莓派）的
// 硬盘上，既不想再套一个 WebDAV 服务，也不想重复上传到网盘。把这台机器上的
// 目录直接当成一种「接入源」，就能复用整套资源 / 映射 / 播放 / 转码链路。
//
// 为什么复用 adapter.DiskAdapter：
// 和 WebDAV 的取舍完全一致 —— 列目录、新建目录、取可播放地址这些能力
// 与网盘、WebDAV 是同构的，抽象成同一种接口后，资源映射
// （service.ImportService）、播放（handler.PlayHandler）、转码
// （service.TranscodeService）都能零改动复用，不必为「本地文件」
// 再写一条平行的支线。
//
// 凭证约定（复用 Credential，不新增字段）：
//
//	Credential.Extra["rootPath"] = 服务器上的根目录绝对路径，如 /mnt/docker/ls-data/media
//	Credential.Cookie            = 不使用（本地文件没有凭据），留空即可
//
// dirID / fileID 统一是**相对 rootPath 的斜杠路径**，如 "/动画/汪汪队"。
// 根目录用 "" 或 "/" 表示。对外一律用正斜杠，跨平台由 filepath.FromSlash 转换。
//
// ⚠️ 安全：所有对外路径都必须经 resolve() 校验，确保解析后的绝对路径仍落在
// rootPath 之内。少了这一步，一个 fileID = "/../../etc/passwd" 就能读取整台机器。
package local

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/littlescholar/core/internal/adapter"
)

func init() {
	adapter.Register(New())
}

// Local 本地硬盘适配器
type Local struct{}

func New() *Local { return &Local{} }

func (l *Local) Type() adapter.DiskType { return adapter.DiskLocal }
func (l *Local) Name() string           { return "本地硬盘" }

// SupportsQR 实现 adapter.QRCapable。
// 本地目录没有"登录"概念，声明 false 让后台的扫码入口对它置灰。
func (l *Local) SupportsQR() bool { return false }

// RootPathKey 是 Extra 里存放根目录的键名，导出给前端 / 文档共用，
// 避免各处硬编码字符串写错。
const RootPathKey = "rootPath"

// ============ 凭证解析 ============

func rootOf(cred *adapter.Credential) (string, error) {
	if cred == nil || cred.Extra == nil {
		return "", fmt.Errorf("本地硬盘缺少配置（需要有 rootPath）")
	}
	raw := strings.TrimSpace(cred.Extra[RootPathKey])
	if raw == "" {
		// 容忍用户把路径直接填在 Cookie 字段里（前端老表单只有 Cookie 一格）。
		raw = strings.TrimSpace(cred.Cookie)
	}
	if raw == "" {
		return "", fmt.Errorf("本地硬盘未配置根目录路径")
	}
	abs, err := filepath.Abs(filepath.FromSlash(raw))
	if err != nil {
		return "", fmt.Errorf("根目录路径不合法: %w", err)
	}
	return filepath.Clean(abs), nil
}

// resolve 把对外路径（相对 rootPath 的斜杠路径）解析成绝对路径，并做越界校验。
func (l *Local) resolve(cred *adapter.Credential, p string) (string, error) {
	root, err := rootOf(cred)
	if err != nil {
		return "", err
	}
	// 先用 path.Clean 归一掉 ".."，再拼到 root 上。
	// 注意不能只靠 path.Clean —— "a/../../b" 归一后是 "../b"，
	// 仍然会跳出 root，所以后面还要用 filepath.Rel 复核。
	clean := path.Clean("/" + strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "/"))
	full := filepath.Join(root, filepath.FromSlash(clean))

	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("路径解析失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法路径：越出根目录")
	}
	return full, nil
}

// toRel 把绝对路径转回对外的斜杠相对路径（以 / 开头）。
func toRel(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "/"
	}
	slash := filepath.ToSlash(rel)
	if slash == "." || slash == "" {
		return "/"
	}
	return "/" + strings.TrimPrefix(slash, "/")
}

// ============ 认证 ============

// GetQRCode 本地目录不支持扫码。
func (l *Local) GetQRCode(ctx context.Context) (*adapter.QRCode, error) {
	return nil, fmt.Errorf("本地硬盘不需要扫码，请在「手动添加」里填写服务器上的目录路径")
}

func (l *Local) PollQRCode(ctx context.Context, sessionID string) (*adapter.LoginResult, error) {
	return nil, fmt.Errorf("本地硬盘不支持扫码")
}

// ValidateCookie 校验根目录是否存在、可读、是目录。
func (l *Local) ValidateCookie(ctx context.Context, cred *adapter.Credential) (*adapter.AccountInfo, error) {
	root, err := rootOf(cred)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("目录不存在: %s", root)
		}
		return nil, fmt.Errorf("无法访问目录: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("路径不是目录: %s", root)
	}
	// 真正探一次读，Stat 通过不代表有读权限（比如 root 建的目录）
	f, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("目录不可读（权限不足）: %w", err)
	}
	f.Close()

	return &adapter.AccountInfo{
		Nickname: filepath.Base(root),
	}, nil
}

// ============ 文件操作 ============

func (l *Local) ListDir(ctx context.Context, cred *adapter.Credential, dirID string) ([]adapter.FileItem, error) {
	root, err := rootOf(cred)
	if err != nil {
		return nil, err
	}
	full, err := l.resolve(cred, dirID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("目录不存在: %s", dirID)
		}
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}

	items := make([]adapter.FileItem, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// 跳过隐藏文件（.DS_Store / @eaDir 这类目录噪音多，孩子端不需要看）
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		var size int64
		updated := ""
		if !e.IsDir() {
			size = info.Size()
		}
		if !info.ModTime().IsZero() {
			updated = info.ModTime().Format(time.RFC3339)
		}
		p := strings.TrimRight(full, string(filepath.Separator)) + string(filepath.Separator) + name
		items = append(items, adapter.FileItem{
			ID:        toRel(root, p),
			Name:      name,
			IsDir:     e.IsDir(),
			Size:      size,
			UpdatedAt: updated,
			Ext:       extOf(name),
		})
	}
	return items, nil
}

func (l *Local) MakeDir(ctx context.Context, cred *adapter.Credential, parentID, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("目录名不能为空")
	}
	// 目录名里不允许出现分隔符，否则可以借"新建目录"跳到 root 之外
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("目录名不能包含路径分隔符")
	}
	root, err := rootOf(cred)
	if err != nil {
		return "", err
	}
	parentFull, err := l.resolve(cred, parentID)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parentFull, name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", fmt.Errorf("新建目录失败: %w", err)
	}
	return toRel(root, target), nil
}

func (l *Local) DeleteFile(ctx context.Context, cred *adapter.Credential, fileIDs []string) error {
	for _, id := range fileIDs {
		// 严禁删根目录本身 —— 那是整个接入源的根，误删等于清空用户数据
		if strings.Trim(strings.ReplaceAll(id, "\\", "/"), "/") == "" {
			return fmt.Errorf("不能删除接入源的根目录")
		}
		full, err := l.resolve(cred, id)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("删除失败 %s: %w", id, err)
		}
	}
	return nil
}

func (l *Local) Rename(ctx context.Context, cred *adapter.Credential, fileID, newName string) error {
	if strings.TrimSpace(newName) == "" {
		return fmt.Errorf("新名称不能为空")
	}
	if strings.ContainsAny(newName, `/\`) {
		return fmt.Errorf("名称不能包含路径分隔符")
	}
	src, err := l.resolve(cred, fileID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("文件不存在: %s", fileID)
	}
	dst := filepath.Join(filepath.Dir(src), newName)
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("重命名失败: %w", err)
	}
	return nil
}

// ============ 分享与转存：本地目录语义上不适用 ============

func (l *Local) ParseShare(ctx context.Context, cred *adapter.Credential, shareURL, passcode string) (*adapter.ShareInfo, error) {
	return nil, adapter.ErrNotImplemented
}

func (l *Local) SaveToDir(ctx context.Context, cred *adapter.Credential, share *adapter.ShareInfo, targetDirID string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}

func (l *Local) Transfer(ctx context.Context, cred *adapter.Credential, shareURL, passcode, targetDirID string) (*adapter.SaveResult, error) {
	return nil, adapter.ErrNotImplemented
}

// ============ 播放 / 下载 ============

// GetDirectLink 返回一个 file:// 地址。
//
// ⚠️ 它不能直接交给浏览器 —— 浏览器不允许 <video src> 读本地文件。
// 但 handler.StreamHandler 会识别 file:// 前缀，改为直接读本地文件并转发
// （含 Range 支持），所以播放链路照旧；
// ffmpeg 也能直接吃 file:// 这个输入，转码同样无需特殊处理。
func (l *Local) GetDirectLink(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	full, err := l.resolve(cred, fileID)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("该路径是目录，不能播放")
	}
	return &adapter.DirectLink{
		URL:          "file://" + filepath.ToSlash(full),
		AcceptRanges: true, // 由 StreamHandler 用 http.ServeContent 实现 Range
		Size:         st.Size(),
	}, nil
}

func (l *Local) GetDownloadInfo(ctx context.Context, cred *adapter.Credential, fileID string) (*adapter.DirectLink, error) {
	return l.GetDirectLink(ctx, cred, fileID)
}

// ============ 辅助 ============

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}
