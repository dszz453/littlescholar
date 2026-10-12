package adapter

import (
	"context"
	"errors"
)

// DiskType 网盘类型
type DiskType string

const (
	DiskQuark  DiskType = "quark"
	DiskBaidu  DiskType = "baidu"
	DiskUC     DiskType = "uc"
	DiskTianyi DiskType = "tianyi"
	DiskAliyun DiskType = "aliyun"
)

// AllDiskTypes 全部支持的网盘
var AllDiskTypes = []DiskType{DiskQuark, DiskBaidu, DiskUC, DiskTianyi, DiskAliyun}

// DiskTypeNames 中文名
var DiskTypeNames = map[DiskType]string{
	DiskQuark:  "夸克网盘",
	DiskBaidu:  "百度网盘",
	DiskUC:     "UC网盘",
	DiskTianyi: "天翼网盘",
	DiskAliyun: "阿里云盘",
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
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	UpdatedAt string `json:"updatedAt"`
	Ext      string `json:"ext"`
}

// ShareInfo 分享链接解析结果
type ShareInfo struct {
	ShareURL string     `json:"shareUrl"`
	Title    string     `json:"title"`
	Passcode string     `json:"passcode"`
	Files    []FileItem `json:"files"`
	// 分享根目录的 ID（转存时需要）
	RootID string `json:"rootId"`
	// 分享者
	OwnerName string `json:"ownerName"`
	TotalSize int64    `json:"totalSize"`
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
