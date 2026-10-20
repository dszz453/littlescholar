package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ChatFunc 一次一问一答的最小接口。
//
// 刻意不直接依赖 `internal/ai`：字幕包的匹配逻辑要能单测，
// 而单测里最不该出现的就是"真的去调一次大模型"。
// 生产侧传一个把 ai.Client 包一层的闭包即可（见 handler/subtitle_collect.go）。
type ChatFunc func(ctx context.Context, system, user string) (string, error)

// ParsedTitle 从资源标题里解析出来的检索要素。
type ParsedTitle struct {
	// Name 片名/书名，去掉集数、画质、压制组这些噪音
	Name string `json:"name"`
	// Season / Episode 第几季第几集，0 表示没有这回事
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	// Lang 期望语言：zh / en / ""（不限）
	Lang string `json:"lang"`
	// IsSeries 是不是连续剧/连载动画（决定要不要带集数去搜）
	IsSeries bool `json:"isSeries"`
}

// aiTitleSystem 系统提示。
//
// 要求"只输出 JSON"是刻意的：模型爱写解释，而解释会把 JSON 解析搅黄。
const aiTitleSystem = `你是资源文件名解析器。用户给一个中文学习资源/动画/影视的文件名，你要提取检索要素。
规则：
1. 去掉画质/编码/字幕组/年份后缀（1080p、HEVC、x264、[某某字幕组]、WEB-DL 等）。
2. 集数可能写成 第03集 / EP03 / E03 / 03 / 03话 / Lesson 3，统一成 episode 数字（不带前导零）。
3. 只有明确是连续剧/连载动画才 isSeries=true；单集电影、有声书、儿歌合集给 false。
4. Lang：文件名里出现"国语/中文/中字/chs/cht/简/繁"给 "zh"，出现"英文/eng/双语"给 "en"，否则 ""。
5. 只输出一行 JSON，不要解释、不要代码块。
输出格式：{"name":"片名","season":0,"episode":0,"lang":"","isSeries":false}`

// ParseTitleAI 用大模型解析标题；失败时退回本地正则 [ParseTitleLocal]。
//
// ⚠️ 失败必须能退回：AI 额度用完/网关超时都是常态，
// 这条链路上"没解析出来"只该意味着"搜得差一点"，不该意味着"整条字幕没了"。
func ParseTitleAI(ctx context.Context, chat ChatFunc, raw string) ParsedTitle {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ParsedTitle{}
	}
	if chat != nil {
		out, err := chat(ctx, aiTitleSystem, raw)
		if err == nil {
			if p, ok := parseTitleJSON(out); ok {
				if p.Name == "" {
					p.Name = ParseTitleLocal(raw).Name
				}
				return p
			}
		}
	}
	return ParseTitleLocal(raw)
}

// parseTitleJSON 从模型输出里抠出 JSON（容忍代码块、前后废话）。
func parseTitleJSON(s string) (ParsedTitle, bool) {
	var p ParsedTitle
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return ParsedTitle{}, false
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Lang = strings.ToLower(strings.TrimSpace(p.Lang))
	return p, p.Name != ""
}

var (
	// 第03集 / 第3话 / 第三集 之类
	reCnEpisode = regexp.MustCompile(`第\s*(\d{1,4})\s*[集话話期课課]`)
	// EP03 / E03 / Lesson 3
	reEnEpisode = regexp.MustCompile(`(?i)\b(?:ep|e|lesson|l)\s*[._-]?\s*(\d{1,4})\b`)
	// S01E02
	reSeasonEp = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*[._-]?\s*E(\d{1,4})\b`)
	// 集数之前的"剧名"分界：空格、横线、点、方括号
	reSplit = regexp.MustCompile(`[\s\[\]()（）【】_\-.]+`)
	// 画质/编码/来源这些噪音词，出现就整段丢掉
	noiseWords = []string{
		"1080p", "720p", "2160p", "4k", "8k", "hevc", "h264", "h265", "x264", "x265",
		"web-dl", "webdl", "webrip", "bluray", "bdrip", "hdr", "sdr", "aac", "flac",
		"国语", "粤语", "中字", "双语", "字幕", "高清", "超清", "蓝光", "完整版",
	}
)

// ParseTitleLocal 纯本地的标题解析（不花 AI 额度）。
//
// 顺序有讲究：先认 S01E02 这种"季集都给全"的，再认"第N集/EPn"。
// 反过来会把 `S01E02` 里的 `01` 当成集数。
func ParseTitleLocal(raw string) ParsedTitle {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ParsedTitle{}
	}
	p := ParsedTitle{}

	if m := reSeasonEp.FindStringSubmatch(s); m != nil {
		p.Season = atoiSafe(m[1])
		p.Episode = atoiSafe(m[2])
		p.IsSeries = true
		s = s[:strings.Index(s, m[0])]
	} else if m := reCnEpisode.FindStringSubmatch(s); m != nil {
		p.Episode = atoiSafe(m[1])
		p.IsSeries = true
		s = s[:strings.Index(s, m[0])]
	} else if m := reEnEpisode.FindStringSubmatch(s); m != nil {
		p.Episode = atoiSafe(m[1])
		p.IsSeries = true
		s = s[:strings.Index(s, m[0])]
	}

	parts := reSplit.Split(s, -1)
	keep := make([]string, 0, len(parts))
	for _, part := range parts {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		skip := false
		for _, n := range noiseWords {
			if strings.Contains(low, n) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		keep = append(keep, t)
	}
	p.Name = strings.Join(keep, " ")
	if p.Name == "" {
		p.Name = strings.TrimSpace(raw)
	}

	switch {
	case strings.Contains(raw, "国语") || strings.Contains(raw, "中文") ||
		strings.Contains(raw, "中字") || strings.Contains(raw, "简") ||
		strings.Contains(strings.ToLower(raw), "chs"):
		p.Lang = "zh"
	case strings.Contains(raw, "英文") || strings.Contains(strings.ToLower(raw), "eng"):
		p.Lang = "en"
	}
	return p
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// aiPickSystem 让模型在几个候选里挑一个。
//
// 允许回答 -1：宁可说"都不对"，也不要硬凑一个 ——
// 给孩子配错字幕（别的动画片的中文字幕）比没有字幕更糟。
const aiPickSystem = `你在为一部视频挑选匹配的字幕文件。用户会给出视频文件名和若干候选字幕文件名。
判定标准（按重要性从高到低）：
1. 片名必须一致（中英文名互相认可，如「小猪佩奇」与「Peppa Pig」算一致）；
2. 集数必须一致；某一方没有集数时视为不冲突；
3. 语言优先中文；
4. 拿不准就回答"都不对"，不要为了给出答案而硬凑。
只输出一行 JSON：{"index": <候选序号，从0开始；都不对给 -1>, "confidence": <0-100 的整数>, "reason": "<不超过20字>"}`

// PickCandidateAI 让 AI 在候选里选一个。
//
// 返回 (index, confidence)。AI 不可用/超时/乱答时退回"取相似度最高的那个，
// 置信度按相似度折算" —— 有 AI 更好，没 AI 不能没有结果。
func PickCandidateAI(ctx context.Context, chat ChatFunc, videoName string, cands []SiblingCandidate) (int, int) {
	if len(cands) == 0 {
		return -1, 0
	}
	// 只有一个候选，且相似度已经很高：不用问 AI，省一次调用
	if len(cands) == 1 {
		if cands[0].Score >= 90 {
			return 0, cands[0].Score
		}
		return 0, cands[0].Score
	}
	// 头名明显领先（第二名低 15 分以上）：本地判断就够，不必花额度
	if cands[0].Score-cands[1].Score >= 15 {
		return 0, cands[0].Score
	}
	if chat == nil {
		return 0, cands[0].Score
	}

	var b strings.Builder
	fmt.Fprintf(&b, "视频文件名：%s\n候选字幕：\n", videoName)
	for i, c := range cands {
		fmt.Fprintf(&b, "%d. %s（相似度 %d）\n", i, c.Name, c.Score)
	}

	out, err := chat(ctx, aiPickSystem, b.String())
	if err != nil {
		return 0, cands[0].Score
	}
	var pick struct {
		Index      int `json:"index"`
		Confidence int `json:"confidence"`
	}
	body := strings.TrimSpace(out)
	if i := strings.Index(body, "{"); i >= 0 {
		if j := strings.LastIndex(body, "}"); j > i {
			body = body[i : j+1]
		}
	}
	if err := json.Unmarshal([]byte(body), &pick); err != nil {
		return 0, cands[0].Score
	}
	if pick.Index < 0 || pick.Index >= len(cands) {
		return -1, 0
	}
	conf := pick.Confidence
	if conf <= 0 {
		conf = cands[pick.Index].Score
	}
	if conf > 100 {
		conf = 100
	}
	return pick.Index, conf
}

// MinConfidence 自动入库的门槛。
//
// 60 分的含义：字幕名与视频名至少是"带了语言后缀"这一级（见 MatchScore）。
// 低于这个就不存 —— 让它在播放时走实时解析，或者干脆没有字幕。
const MinConfidence = 60
