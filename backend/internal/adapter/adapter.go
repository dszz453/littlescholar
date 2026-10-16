package adapter

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
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

	// SharePath 分享内的相对路径（仅百度用）。
	//
	// 夸克/UC 下钻子目录靠**目录 fid**，而百度的 /share/list 靠**路径**
	// （dir=/xxx），transfer 又要 fs_id —— 同一层目录在百度那边需要
	// "ID 用 fs_id、下钻用路径"两个值，所以单独放一个字段。
	// 同样只在服务端流转，不下发前端。
	SharePath string `json:"-"`
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

// ShareEntry 分享内某一层目录的内容（支撑「搜全网 → 一层文件夹一层打开」）。
//
// ⚠️ Files 里每个 FileItem 的 Token 是**转存准入令牌**，只能留在服务端。
// handler 回给前端之前必须剥掉，否则等于把转存凭据发给了客户端。
type ShareEntry struct {
	// Fid 本层目录的 ID（"0" 表示分享根目录），前端用作面包屑与下钻参数。
	Fid string `json:"fid"`
	// Title 分享标题（根目录层才有意义）。
	Title string     `json:"title"`
	Files []FileItem `json:"files"`

	// 下面三个是**转存所需的分享级凭据**，同样只留在服务端。
	//
	// 为什么 ListShareDir 要返回它们：用户"只转存点中的那一项"时，
	// handler 会用它构造一个只含该项的 ShareInfo 交给 SaveToDir，
	// 而 SaveToDir 除了逐项的 Token 之外还依赖分享级的凭据
	// （夸克/UC 用 stoken，百度用 shareid + 分享者 uk + randsk）。
	RootID  string `json:"-"`
	OwnerID string `json:"-"`
	SeKey   string `json:"-"`
}

// ShareBrowser 是 DiskAdapter 的**可选**能力：浏览分享内部的目录层级。
//
// 为什么不直接塞进 DiskAdapter：只有夸克 / UC / 百度这类"能把分享当目录看"
// 的网盘才支持它，WebDAV、本地硬盘、天翼、阿里云盘根本不适用，
// 不该被逼着实现一个只会 return error 的方法。所以单列一个接口，
// 由 handler 用类型断言按需取用。
type ShareBrowser interface {
	// ListShareDir 列出分享内指定目录的内容。
	// pdirFid 为空或 "0" 表示分享根目录。
	ListShareDir(ctx context.Context, cred *Credential, shareURL, passcode, pdirFid string) (*ShareEntry, error)
}

// ============ 创建分享（反方向）============

// ShareExpirePermanent 表示"永久有效"。
//
// 语义放在适配器层统一，是因为各家网盘的"永久"取值完全不一样
// （夸克是 expired_type=1，百度是 period=0 …）。上层的
// ShareCreateOptions.ExpireDays 用**天数**这一个口径表达意图，
// 由适配器翻译成自家参数 —— 否则每个调用方都需要知道夸克的 1~7
// 分别代表多少天，迟早会有人写错。
const ShareExpirePermanent = -1

// ShareResult 创建分享的结果。
//
// Passcode 一定是**真实可用**的那一个 —— 适配器在返回前会用它与
// ShareURL 做一次解锁验证。这不是洁癖：提取码由服务端生成、
// 或服务端忽略了调用方提交的值，这两种情况都真实存在，
// 而症状是"后台显示已共享、孩子端一到转存就说提取码错误"，
// 排查时完全看不出问题出在创建环节。
type ShareResult struct {
	ShareURL string `json:"shareUrl"`
	Passcode string `json:"passcode"`
	ShareID  string `json:"shareId"`
	// ExpireAt 过期时间戳（秒）。0 = 永久有效。
	ExpireAt int64 `json:"expireAt"`
}

// ShareCreateOptions 创建分享的可选项。
type ShareCreateOptions struct {
	// Title 分享标题（在网盘上显示的名字），空则用适配器默认值。
	Title string
	// Passcode 期望的提取码。为空 = 由适配器生成（推荐，
	// 因为多数网盘对提取码字符集有要求，自己拼一串容易撞上非法字符）。
	Passcode string
	// ExpireDays 有效期天数：>0 = 具体天数；0 = 用适配器默认值；
	// ShareExpirePermanent = 永久。
	//
	// 为什么用天数而不是直接暴露各家参数：夸克是 expired_type 的
	// 1~7（1=永久, 2=1天, 3=7天, 4=30天 …），百度是 period 的
	// 0/1/7/30。让 UI 记住两套编号只会制造 bug。
	ExpireDays int
}

// ShareCreator 是 DiskAdapter 的**可选**能力：把**自己网盘里的**
// 文件/目录变成一条分享链接。
//
// 方向与 ShareBrowser / SaveToDir 相反 —— 那两个是"把别人的分享
// 搬进我的盘"，这个是"把我盘里的东西分享出去"。本项目的用法：
// 资源是映射进后台网盘账号的，孩子用的是**另一个**账号扫码接入，
// 两者之间唯一的合法通道就是一条分享链接。
//
// 同样做成可选接口而不是塞进 DiskAdapter：WebDAV / 本地硬盘
// 根本没有"分享"这个概念，不该被逼着实现一个恒返回 error 的方法。
type ShareCreator interface {
	// CreateShare 为 fileIDs 创建**一条**分享（多个 fid 会在分享根
	// 目录里并列出现）。
	//
	// 为什么是一次多文件而不是逐个文件一条分享：夸克这类网盘的
	// /share 接口本来就收 fid_list，一次分享一个目录下的几十个文件
	// 只需一次调用；逐个建分享会瞬间打出几十个请求，直接撞上限流。
	// 而且上层按"一个映射目录一条分享"回写，语义也更自然。
	CreateShare(ctx context.Context, cred *Credential, fileIDs []string, opt ShareCreateOptions) (*ShareResult, error)
}

// ShareCreatorOf 取某个网盘类型的"创建分享"能力。
//
// 不支持时返回的是一句能直接显示给用户的话，而不是原生的
// 类型断言失败 —— 后台的按钮点下去要有个说法。
func ShareCreatorOf(t DiskType) (ShareCreator, error) {
	a, err := Get(t)
	if err != nil {
		return nil, err
	}
	sc, ok := a.(ShareCreator)
	if !ok {
		name := DiskTypeNames[t]
		if name == "" {
			name = string(t)
		}
		return nil, fmt.Errorf("%s暂不支持在后台创建分享", name)
	}
	return sc, nil
}

// sharePasscodeAlphabet 提取码字符集。
//
// 刻意剔掉容易看错、听错的字符（0/O、1/l/I），因为提取码的常态是
// 家长在手机上看一眼、再到另一台设备上手动敲进去。少这几个字符
// 对熵的影响可以忽略，对"一次就敲对"的影响很大。
const sharePasscodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// RandomPasscode 生成 n 位提取码。
//
// 用 crypto/rand 而不是 math/rand：提取码是访问凭据，虽然不是
// 高价值密钥，但没必要在一个有真随机源的地方留一个可预测的口子。
//
// 用取模拒绝而不是直接 `% len(alphabet)`：直接取模会让靠前的字符
// 出现概率偏高（256 不是字符集大小的整数倍），这种偏斜对 4 位码来说
// 影响不大，但修掉它只要三行，没有留着的理由。
func RandomPasscode(n int) string {
	if n <= 0 {
		n = 4
	}
	const (
		limit = 256 - (256 % len(sharePasscodeAlphabet)) // 大于等于它的一律丢弃
	)
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand 失败等于系统级故障。退化成固定串而不是 panic：
			// 后台至少还能把分享建出来，用户看得到链接，也就知道去哪查。
			return strings.Repeat("a", n)
		}
		if int(buf[0]) >= limit {
			continue
		}
		out = append(out, sharePasscodeAlphabet[int(buf[0])%len(sharePasscodeAlphabet)])
	}
	return string(out)
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

// MediaHeaders 构造"向网盘 CDN 取流"时要带的请求头。
//
// 为什么统一在这里构造：这几个头不是可选的装饰，而是 **CDN 的准入凭证**，
// 少一个就返回 4xx —— 而且是"接口能调通、资源能列出、一播就失败"的
// 隐蔽故障。此前五个适配器各写各的 map，结果**全都漏了 Cookie**：
// 夸克 CDN 直接回 412 Precondition Failed，用户看到的前端提示是
// 「播放地址可能已过期」，与真实原因（缺鉴权头）完全不沾边。
//
// 实测证据（同一份签名直链、同样带 Range: bytes=0-1023）：
//
//	UA + Referer          -> 412 Precondition Failed
//	UA                    -> 412
//	不带任何头             -> 412
//	UA + Cookie           -> 206 Content-Range: bytes 0-1023/111361619  video/mp4
//	UA + Referer + Cookie -> 206 video/mp4
//
// 结论：Cookie 是必需项，UA/Referer 是兜底项，三个都带上最稳。
//
// 注意 cookie 为空时**不要写入该键**：部分 CDN 见到空 Cookie 头会
// 判成非法请求，不如干脆不带。
func MediaHeaders(userAgent, referer, cookie string) map[string]string {
	h := make(map[string]string, 3)
	if userAgent != "" {
		h["User-Agent"] = userAgent
	}
	if referer != "" {
		h["Referer"] = referer
	}
	if cookie != "" {
		h["Cookie"] = cookie
	}
	return h
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
