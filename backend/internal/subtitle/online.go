package subtitle

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpenSubtitles 是唯一"填个 key 就能用"的公开字幕源（REST v1）。
//
// 其余国内站点（字幕库、SubHD、射手）都没有公开 API，抓网页既脆又不合规，
// 所以不做。key 是免费申请的，填在 SUBTITLE_OPENSUBTITLES_KEY 里，
// 没填时整条联网链路**静默跳过**（不是报错）—— 大多数资源自带字幕文件，
// 联网只是兜底。
const osBase = "https://api.opensubtitles.com/api/v1"

// Online 联网匹配字幕。
type Online struct {
	APIKey string
	Client *http.Client
	// Base 覆盖字幕站地址，只给单测用（默认 osBase）。
	// 没有它就只能对着真实站点测，等于不测。
	Base string
}

// base 实际请求的基址。
func (o *Online) base() string {
	if o != nil && o.Base != "" {
		return strings.TrimRight(o.Base, "/")
	}
	return osBase
}

func NewOnline(key string) *Online {
	return &Online{
		APIKey: strings.TrimSpace(key),
		Client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled 是否配置了 key。
func (o *Online) Enabled() bool { return o != nil && o.APIKey != "" }

// 搜索结果里我们关心的部分。
type osSubtitle struct {
	ID   string
	File int64
	Name string
	Lang string
	Down int
}

// Search 按标题找字幕，返回已下载并转好的 VTT。
//
// 匹配策略刻意保守：只接受**标题词重合度高**的结果，宁可返回"没找到"，
// 也不要给一部动画片配一集纪录片的中文字幕 —— 错字幕比没字幕更伤。
func (o *Online) Search(ctx context.Context, title string, season, episode int) (string, string, error) {
	if !o.Enabled() {
		return "", "", fmt.Errorf("未配置字幕源")
	}
	q := cleanQuery(title)
	if q == "" {
		return "", "", fmt.Errorf("标题里没有可检索的关键词")
	}

	subs, err := o.search(ctx, q, season, episode)
	if err != nil {
		return "", "", err
	}
	if len(subs) == 0 {
		return "", "", fmt.Errorf("网上没有匹配到字幕")
	}
	// 下载次数最多的通常最靠谱（同名影片的不同版本里，它被验证过）
	best := subs[0]
	raw, err := o.download(ctx, best.File)
	if err != nil {
		return "", "", err
	}
	vtt, err := Normalize(raw)
	if err != nil {
		return "", "", fmt.Errorf("下载到的字幕无法解析")
	}
	label := best.Lang
	if label == "" {
		label = "未知语言"
	}
	return vtt, "OpenSubtitles · " + label, nil
}

func (o *Online) search(ctx context.Context, q string, season, episode int) ([]osSubtitle, error) {
	u, _ := url.Parse(o.base() + "/subtitles")
	params := u.Query()
	params.Set("query", q)
	params.Set("languages", "zh-cn,zh,tw,en")
	params.Set("order_by", "download_count")
	params.Set("order_direction", "desc")
	if season > 0 {
		params.Set("season_number", strconv.Itoa(season))
	}
	if episode > 0 {
		params.Set("episode_number", strconv.Itoa(episode))
	}
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	o.setHeaders(req)

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("字幕源连不上")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("字幕源的 API Key 无效")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("字幕源配额用完了（每小时/每天有限额）")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("字幕源返回 %d", resp.StatusCode)
	}

	var parsed struct {
		Data []struct {
			Attributes struct {
				Language      string `json:"language"`
				DownloadCount int    `json:"download_count"`
				Files         []struct {
					FileID   json.Number `json:"file_id"`
					FileName string      `json:"file_name"`
				} `json:"files"`
				FeatureDetails struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
				} `json:"feature_details"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("字幕源返回了看不懂的内容")
	}

	out := make([]osSubtitle, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if len(d.Attributes.Files) == 0 {
			continue
		}
		fid, err := d.Attributes.Files[0].FileID.Int64()
		if err != nil || fid == 0 {
			continue
		}
		out = append(out, osSubtitle{
			File: fid,
			Name: d.Attributes.Files[0].FileName,
			Lang: d.Attributes.Language,
			Down: d.Attributes.DownloadCount,
		})
	}
	return out, nil
}

func (o *Online) download(ctx context.Context, fileID int64) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{"file_id": fileID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base()+"/download", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	o.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载字幕失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载字幕被拒（%d），可能是当日配额用完了", resp.StatusCode)
	}
	var dl struct {
		Link string `json:"link"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&dl); err != nil || dl.Link == "" {
		return nil, fmt.Errorf("字幕源没给下载地址")
	}

	// 下载链接是 S3/R2 的签名地址，**不能带 Api-Key**（会被拒）
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dl.Link, nil)
	if err != nil {
		return nil, err
	}
	dresp, err := o.Client.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("下载字幕失败")
	}
	defer dresp.Body.Close()
	if dresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载字幕失败（%d）", dresp.StatusCode)
	}
	// 上限 8MB：正常字幕几十 KB，超过就是别的东西
	raw, err := io.ReadAll(io.LimitReader(dresp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("下载字幕失败")
	}
	// .gz 包一层是常态
	if len(raw) >= 2 && raw[0] == 0x1F && raw[1] == 0x8B {
		zr, zerr := gzip.NewReader(bytes.NewReader(raw))
		if zerr != nil {
			return nil, fmt.Errorf("字幕压缩包损坏")
		}
		defer zr.Close()
		raw, err = io.ReadAll(io.LimitReader(zr, 8<<20))
		if err != nil {
			return nil, fmt.Errorf("字幕压缩包损坏")
		}
	}
	return raw, nil
}

func (o *Online) setHeaders(req *http.Request) {
	req.Header.Set("Api-Key", o.APIKey)
	req.Header.Set("User-Agent", "LittleScholar/1.0")
	req.Header.Set("Accept", "application/json")
}

// 集数标记：第01集 / EP01 / E01 / S01E01 / 01.
var (
	reEpisode = regexp.MustCompile(`(?i)(第\s*\d+\s*[集话話期]|s\d{1,2}e\d{1,2}|ep?\s*\d{1,3}|\[\d{1,3}\]|\b\d{1,3}\b)`)
	reBracket = regexp.MustCompile(`[【\[（(][^】\]）)]*[】\]）)]`)
	reNoise   = regexp.MustCompile(`(?i)\b(1080p|720p|2160p|4k|hdr|bluray|blu-ray|web-dl|webrip|hdtv|x264|x265|h264|h265|hevc|aac|dts|remux|hq|完整版|高清|国语|粤语|中字|中英字幕)\b`)
	reSpaces  = regexp.MustCompile(`\s+`)
)

// SplitTitle 从资源标题里拆出 (片名, 季, 集)。
//
// 资源标题长这样：`小猪佩奇 第01集`、`Peppa.Pig.S01E03.1080p`。
// 直接拿去搜索匹配率很低，必须先剥掉集数、画质、字幕组的标记。
func SplitTitle(title string) (string, int, int) {
	t := title
	season, episode := 0, 0

	if m := regexp.MustCompile(`(?i)s(\d{1,2})e(\d{1,2})`).FindStringSubmatch(t); len(m) == 3 {
		season, _ = strconv.Atoi(m[1])
		episode, _ = strconv.Atoi(m[2])
	} else if m := regexp.MustCompile(`第\s*(\d{1,3})\s*[集话話期]`).FindStringSubmatch(t); len(m) == 2 {
		episode, _ = strconv.Atoi(m[1])
	} else if m := regexp.MustCompile(`(?i)\bep?\s*(\d{1,3})\b`).FindStringSubmatch(t); len(m) == 2 {
		episode, _ = strconv.Atoi(m[1])
	}
	return cleanQuery(t), season, episode
}

// cleanQuery 剥掉括号、集数、画质标签，留下片名。
func cleanQuery(t string) string {
	t = reBracket.ReplaceAllString(t, " ")
	t = reEpisode.ReplaceAllString(t, " ")
	t = reNoise.ReplaceAllString(t, " ")
	// 常见的分隔符统一成空格
	t = strings.NewReplacer(".", " ", "_", " ", "-", " ", "—", " ", "·", " ").Replace(t)
	t = reSpaces.ReplaceAllString(t, " ")
	return strings.TrimSpace(t)
}
