package adapter

import (
	"context"
	"errors"
	"fmt"
)

// DiskType 网盘类型
type DiskType string

const (
	DiskQuark  DiskType = "quark"
	DiskBaidu  DiskType = "baidu"
	DiskUC     DiskType = "uc"
	DiskTianyi DiskType = "tianyi"
	DiskAliyun DiskType = "aliyun"
	// DiskWebdav 通用 WebDAV（NAS / Alist / 群晖 / rclone 等）。
	// 它不是"网盘"，但复用同一套账号/目录/播放抽象后，
	// 转码链路（service.TranscodeService）无需任何改动即可复用。
	DiskWebdav DiskType = "webdav"
	// DiskLocal 服务器本地硬盘上的目录。
	// 同样不是网盘，但"列目录 / 新建目录 / 取播放地址"与 WebDAV 同构，
	// 复用 DiskAdapter 后资源映射、播放代理、转码全部零改动复用。
	// 注意：它属于**存储源**而非**网盘**，UI 上要与网盘分开归类。
	DiskLocal DiskType = "local"
)

// AllDiskTypes 全部支持的存储类型
var AllDiskTypes = []DiskType{DiskQuark, DiskBaidu, DiskUC, DiskTianyi, DiskAliyun, DiskWebdav, DiskLocal}

// StorageTypes 属于"存储源"的类型：不是网盘，无需扫码，靠手动填参数接入。
// 前端据此把它归到「WebDAV / 本地硬盘」分组，而不是「网盘接入」。
var StorageTypes = []DiskType{DiskWebdav, DiskLocal}

// IsStorageType 判断是否为存储源（WebDAV / 本地硬盘）。
func IsStorageType(t DiskType) bool {
	for _, s := range StorageTypes {
		if s == t {
			return true
		}
	}
	return false
}

// DiskTypeNames 中文名
var DiskTypeNames = map[DiskType]string{
	DiskQuark:  "夸克网盘",
	DiskBaidu:  "百度网盘",
	DiskUC:     "UC网盘",
	DiskTianyi: "天翼网盘",
	DiskAliyun: "阿里云盘",
	DiskWebdav: "WebDAV",
	DiskLocal:  "本地硬盘",
}

// 常见错误
var (
	ErrNotImplemented = errors.New("该网盘此功能尚未实现")
	ErrCookieInvalid  = errors.New("cookie 已失效")
	ErrShareExpired   = errors.New("分享链接已失效")
	ErrNeedPassword   = errors.New("需要提取码或提取码错误")
	ErrRateLimited    = errors.New("请求过于频繁，已被网盘限流")
)

// Credential 网盘凭证
type Credential struct {
	DiskType DiskType
	Cookie   string            // 主 cookie
	Extra    map[string]string // 额外凭证，如 BDUSS
}

// QRCode 扫码二维码
type QRCode struct {
	SessionID string // 会话ID，用于轮询
	Content   string // 二维码内容
	Image     string // 二维码图片 (data URI)，可能为空
	ExpiresIn int    // 有效期秒数
}

// LoginResult 扫码登录结果
type LoginResult struct {
	Status   string // waiting/scanned/confirmed/expired/failed
	Nickname string
	Avatar   string
	Cookie   string
	Extra    map[string]string
	Message  string
}

// FileItem 网盘文件项
type FileItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updatedAt"`
	Ext       string `json:"ext"`

	// Token 分享内的一次性访问令牌（夸克/UC 的 share_fid_token）。
	//
	// 转存时必须成对提交 (fid, share_fid_token)，两者是一一对应的：
	// fid 只说明"哪个文件"，token 才说明"凭什么叫你转存"。
	// 之前把 fid 原样当成 token 传，服务端一律拒绝 —— 这就是
	// 「夸克/UC 转存失败」的直接原因。
	Token string `json:"-"`
}

// ShareInfo 分享链接解析结果
type ShareInfo struct {
	ShareURL string     `json:"shareUrl"`
	Title    string     `json:"title"`
	Passcode string     `json:"passcode"`
	Files    []FileItem `json:"files"`
	// 分享根目录的 ID（转存时需要）。
	//
	// 语义各家不同，别混用：
	//   夸克/UC —— sharepage/token 返回的 stoken
	//   百度    —— share/verify 返回的 shareid（数字）
	RootID string `json:"rootId"`

	// OwnerID 分享者 ID。
	//
	// 百度转存的 from 参数必须填**分享者的 uk**（不是自己的），
	// 填错或不填会直接 errno=2（参数错误）。
	OwnerID string `json:"ownerId"`

	// SeKey 分享的附加密钥。
	//
	// 百度 share/verify 返回 randsk，转存时要作为 sekey 原样回传。
	SeKey string `json:"seKey,omitempty"`

	// 分享者昵称
	OwnerName string `json:"ownerName"`
	TotalSize int64  `json:"totalSize"`
}

// SaveResult 转存结果
type SaveResult struct {
	FileIDs []string `json:"fileIds"`
	Count   int      `json:"count"`
	Message string   `json:"message"`
}

// DirectLink 直链信息
type DirectLink struct {
	URL       string            `json:"url"`
	ExpiresAt int64             `json:"expiresAt"` // 过期时间戳（秒）
	Headers   map[string]string `json:"headers"`   // 访问直链需要的额外请求头
	Size      int64             `json:"size"`
	// 是否支持 Range 请求（决定能否分块加速）
	AcceptRanges bool `json:"acceptRanges"`
}

// Chunk 分块信息（非会员加速）
type Chunk struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// AccountInfo 账号信息
type AccountInfo struct {
	Nickname      string `json:"nickname"`
	Avatar        string `json:"avatar"`
	CapacityUsed  int64  `json:"capacityUsed"`
	CapacityTotal int64  `json:"capacityTotal"`
	VIP           bool   `json:"vip"`
}

// DiskAdapter 网盘适配器统一接口
// 所有网盘实现此接口，上层 Service 无需关心具体网盘差异
type DiskAdapter interface {
	// Type 返回网盘类型
	Type() DiskType
	// Name 返回中文名
	Name() string

	// === 认证 ===

	// GetQRCode 获取扫码登录二维码
	GetQRCode(ctx context.Context) (*QRCode, error)
	// PollQRCode 轮询扫码状态
	PollQRCode(ctx context.Context, sessionID string) (*LoginResult, error)
	// ValidateCookie 校验 cookie 是否有效，并返回账号信息
	ValidateCookie(ctx context.Context, cred *Credential) (*AccountInfo, error)

	// === 文件操作 ===

	// ListDir 列出目录内容
	ListDir(ctx context.Context, cred *Credential, dirID string) ([]FileItem, error)
	// MakeDir 新建目录，返回目录ID
	MakeDir(ctx context.Context, cred *Credential, parentID, name string) (string, error)
	// DeleteFile 删除文件/目录
	DeleteFile(ctx context.Context, cred *Credential, fileIDs []string) error
	// Rename 重命名
	Rename(ctx context.Context, cred *Credential, fileID, newName string) error

	// === 分享与转存（核心）===

	// ParseShare 解析分享链接，拿到文件列表
	ParseShare(ctx context.Context, cred *Credential, shareURL, passcode string) (*ShareInfo, error)
	// SaveToDir 把解析好的分享转存到指定目录
	SaveToDir(ctx context.Context, cred *Credential, share *ShareInfo, targetDirID string) (*SaveResult, error)
	// Transfer 一步完成：解析 + 转存
	Transfer(ctx context.Context, cred *Credential, shareURL, passcode, targetDirID string) (*SaveResult, error)

	// === 播放 / 下载 ===

	// GetDirectLink 获取文件直链
	GetDirectLink(ctx context.Context, cred *Credential, fileID string) (*DirectLink, error)
	// GetDownloadURL 获取可用于分块下载的地址（通常是直链 + 必要 header）
	GetDownloadInfo(ctx context.Context, cred *Credential, fileID string) (*DirectLink, error)
}

// QRCapable 可选接口：适配器可声明自己是否支持扫码登录。
//
// 做成可选接口而不是往 DiskAdapter 上加方法，是为了不改动已有的
// 5 个网盘适配器 —— 它们都支持扫码，不该被迫多写一个恒返回 true 的方法。
// 未实现该接口的适配器沿用默认值（支持扫码）。
type QRCapable interface {
	SupportsQR() bool
}

// SplitChunks 按块大小切分文件（非会员加速用）
func SplitChunks(size, chunkSize int64) []Chunk {
	if size <= 0 || chunkSize <= 0 {
		return nil
	}
	var chunks []Chunk
	idx := 0
	for start := int64(0); start < size; start += chunkSize {
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		chunks = append(chunks, Chunk{Index: idx, Start: start, End: end})
		idx++
	}
	return chunks
}

// TaskErrorText 把网盘异步任务的失败码翻译成用户能看懂的一句话。
//
// 为什么需要它：夸克/UC 的转存是异步任务，提交接口一律返回 code=0，
// 真正的结果只能靠轮询任务接口拿。任务失败时失败原因放在**顶层**
// code/message（例如 code=32003 message="capacity limit"），而不是
// data 里 —— 如果只解析 data.status，就会把这个信息丢掉，
// 最终给用户报一个语焉不详的"任务超时"。
//
// 参数：
//
//	prefix         网盘名（"夸克网盘" / "UC 网盘"）
//	code, msg      任务接口返回的顶层 code 与 message
//	missing, remain 容量类错误附带的"还缺多少 / 还剩多少"（字节，可能为 0）
func TaskErrorText(prefix string, code int, msg string, missing, remain int64) string {
	switch code {
	case 32003, 32004: // capacity limit —— 空间不足
		if missing > 0 {
			return fmt.Sprintf("%s空间不足：当前剩余约 %s，本资源还需 %s，请清理空间或开通会员后重试",
				prefix, humanSize(remain), humanSize(missing))
		}
		return prefix + "空间不足，请清理空间或开通会员后重试"
	case 41013, 41012: // 需要提取码
		return prefix + "分享需要提取码，请填写提取码后重试"
	case -6, 31001: // 登录态失效
		return prefix + "登录已失效，请重新接入该账号"
	}
	if msg != "" {
		return fmt.Sprintf("%s转存失败: code=%d %s", prefix, code, msg)
	}
	return fmt.Sprintf("%s转存失败: code=%d", prefix, code)
}

// humanSize 把字节数格式化成人类可读的大小（按 1024 进制）。
func humanSize(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}
