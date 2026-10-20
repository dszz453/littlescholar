// Package subtitle 把各种字幕格式统一成 WebVTT，并负责"去哪找字幕"。
//
// 为什么一定要统一成 VTT：孩子端是 WebView 里的 <video>/<audio>，
// 唯一被所有内核认的字幕格式就是 WebVTT（<track src="x.vtt">）。
// SRT 只是"大部分能凑合"，ASS/SSA 直接不认，GBK 编码还会整段乱码 ——
// 所以转换必须在服务端做完，前端只吃 VTT。
package subtitle

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Exts 我们认得的字幕扩展名。
var Exts = map[string]bool{"srt": true, "ass": true, "ssa": true, "vtt": true, "sub": true}

// IsSubtitle 判断文件名是不是字幕。
func IsSubtitle(name string) bool {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return false
	}
	return Exts[strings.ToLower(name[i+1:])]
}

// BaseName 去掉扩展名并小写，用于"视频名 ↔ 字幕名"配对。
//
// 影视资源常见的配对写法有三种，都要能对上：
//
//	第01集.mp4        + 第01集.srt
//	第01集.mp4        + 第01集.chs.srt      （语言后缀）
//	Movie.2020.1080p.mkv + Movie.2020.1080p.zh-CN.ass
//
// 所以配对规则是"**前缀匹配**"而不是相等 —— 但只允许"字幕名前缀 == 视频名"，
// 反过来不行，否则 `第1集.mp4` 会被 `第10集.srt` 认领（第1是第10的前缀）。
func BaseName(name string) string {
	i := strings.LastIndex(name, ".")
	if i > 0 {
		name = name[:i]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// MatchScore 给候选字幕文件打分，分数越高越可能是这个视频的。
//
// 0 表示不匹配。分开打分而不是布尔判断，是因为同一个视频目录里
// 常有 `xxx.chs.srt` / `xxx.eng.srt` / `xxx.ass` 三份，需要挑一个。
func MatchScore(videoName, subName string) int {
	v := BaseName(videoName)
	s := BaseName(subName)
	if v == "" || s == "" {
		return 0
	}
	switch {
	case s == v:
		return 100
	case strings.HasPrefix(s, v+"."), strings.HasPrefix(s, v+"-"), strings.HasPrefix(s, v+"_"):
		// 带了语言/版本后缀。中文优先，其余次之。
		low := s[len(v):]
		if strings.Contains(low, "zh") || strings.Contains(low, "chs") ||
			strings.Contains(low, "cht") || strings.Contains(low, "chi") ||
			strings.Contains(low, "中文") || strings.Contains(low, "简") {
			return 90
		}
		if strings.Contains(low, "eng") || strings.Contains(low, "en") {
			return 70
		}
		return 60
	}
	return 0
}

// Normalize 把任意字幕文本转成 UTF-8 的 WebVTT。
//
// 编码判定顺序：BOM → UTF-8 合法性 → GBK 兜底。
// 国内资源里 GBK 的 srt 极多，直接当 UTF-8 输出就是满屏问号。
func Normalize(raw []byte) (string, error) {
	text := decode(raw)
	t := strings.TrimPrefix(text, "\ufeff")
	trimmed := strings.TrimLeft(t, " \r\n\t")

	switch {
	case strings.HasPrefix(strings.ToUpper(trimmed), "WEBVTT"):
		// 已经是 VTT：只补一个空行，别重排（原文件的时间轴/样式更可信）
		if !strings.HasSuffix(t, "\n\n") {
			t = strings.TrimRight(t, "\r\n") + "\n\n"
		}
		return t, nil
	case strings.Contains(trimmed, "[Script Info]") || strings.Contains(trimmed, "[Events]"):
		return assToVTT(t), nil
	case looksLikeSRT(trimmed):
		return srtToVTT(t), nil
	}
	return "", fmt.Errorf("认不出字幕格式")
}

// decode 处理 BOM 与 GBK。
func decode(raw []byte) string {
	if len(raw) >= 2 {
		// UTF-16：Windows 上另存为"Unicode"就是这个
		if raw[0] == 0xFF && raw[1] == 0xFE {
			return decodeUTF16(raw[2:], false)
		}
		if raw[0] == 0xFE && raw[1] == 0xFF {
			return decodeUTF16(raw[2:], true)
		}
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return string(raw[3:])
	}
	if isValidUTF8(raw) {
		return string(raw)
	}
	// GBK / GB18030：用 GB18030 解，它向下兼容 GBK 与 GB2312
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), raw)
	if err == nil {
		return string(out)
	}
	// 实在解不开就原样返回，交给上层报"认不出"
	return string(raw)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	var sb strings.Builder
	for i := 0; i < len(b); i += 2 {
		var r rune
		if bigEndian {
			r = rune(b[i])<<8 | rune(b[i+1])
		} else {
			r = rune(b[i+1])<<8 | rune(b[i])
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func isValidUTF8(b []byte) bool {
	return bytes.Equal(bytes.ToValidUTF8(b, []byte("\uFFFD")), b)
}

var srtTime = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})`)

func looksLikeSRT(s string) bool {
	// SRT 的第一条就是时间轴，或者"序号 + 时间轴"
	head := s
	if len(head) > 400 {
		head = head[:400]
	}
	return srtTime.MatchString(head) && strings.Contains(head, "-->")
}

// srtToVTT 把 SRT 转成 VTT。
//
// 只做必须做的三件事：
//  1. 加 WEBVTT 头（缺了浏览器整条 track 都不认）
//  2. 时间轴的 `,` 换成 `.`（VTT 只认点）
//  3. 去掉纯数字的序号行（VTT 不需要，留着会被当成 cue id，
//     虽然合法，但某些内核会把 cue id 显示出来）
//
// 不碰样式标签：<i>/<b>/<font color> 里前两个 VTT 认，font 不认但无害。
func srtToVTT(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	out = append(out, "WEBVTT", "")

	i := 0
	for i < len(lines) {
		ln := strings.TrimSpace(lines[i])
		// 跳过序号行：纯数字，且下一行是时间轴
		if isAllDigits(ln) && i+1 < len(lines) && strings.Contains(lines[i+1], "-->") {
			i++
			continue
		}
		if strings.Contains(ln, "-->") {
			out = append(out, fixCueTime(ln))
			i++
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return strings.Join(out, "\n")
}

// fixCueTime 修时间轴：逗号→点，并补足毫秒位数。
func fixCueTime(line string) string {
	return srtTime.ReplaceAllStringFunc(line, func(m string) string {
		parts := srtTime.FindStringSubmatch(m)
		if len(parts) != 5 {
			return m
		}
		ms := parts[4]
		for len(ms) < 3 {
			ms += "0"
		}
		return fmt.Sprintf("%02d:%02d:%02d.%s",
			atoi(parts[1]), atoi(parts[2]), atoi(parts[3]), ms)
	})
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// assTime 匹配 ASS 的 `0:00:01.23` 这种时间（小时不补零，百分秒）。
var assTime = regexp.MustCompile(`^(\d+):(\d{1,2}):(\d{1,2})[.:](\d{1,2})$`)

// assStyle 匹配 ASS 的花括号覆盖标签 `{\pos(1,2)\c&HFFFFFF&}`。
var assStyle = regexp.MustCompile(`\{[^}]*\}`)

// assToVTT 把 ASS/SSA 转成 VTT。
//
// ASS 的核心是 `[Events]` 里的 `Dialogue:` 行，字段顺序由上面的 `Format:` 行
// 决定（不同软件导出的顺序不一样，写死会错位）。这里先读 Format，
// 再按下标取 Start/End/Text —— 这是唯一的正确做法。
//
// 样式（颜色、位置、描边）一律丢弃：VTT 没有等价物，硬塞只会变成
// 屏幕上的一堆乱码。丢样式不影响"看得懂"，但丢时间轴就完全没用了。
func assToVTT(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	out = append(out, "WEBVTT", "")

	inEvents := false
	var fields []string
	for _, raw := range lines {
		ln := strings.TrimSpace(raw)
		if strings.HasPrefix(ln, "[") {
			inEvents = strings.EqualFold(ln, "[Events]")
			continue
		}
		if !inEvents {
			continue
		}
		if strings.HasPrefix(ln, "Format:") {
			fields = nil
			for _, f := range strings.Split(strings.TrimPrefix(ln, "Format:"), ",") {
				fields = append(fields, strings.ToLower(strings.TrimSpace(f)))
			}
			continue
		}
		if !strings.HasPrefix(ln, "Dialogue:") {
			continue
		}
		if len(fields) == 0 {
			// 没有 Format 行：按最常见的 10 段布局兜底
			fields = []string{"layer", "start", "end", "style", "name",
				"marginl", "marginr", "marginv", "effect", "text"}
		}
		body := strings.TrimPrefix(ln, "Dialogue:")
		// Text 是最后一个字段，且里面**允许有逗号** → 只切前 n-1 个
		parts := strings.SplitN(body, ",", len(fields))
		if len(parts) < len(fields) {
			continue
		}
		var start, end, text string
		for i, f := range fields {
			switch f {
			case "start":
				start = strings.TrimSpace(parts[i])
			case "end":
				end = strings.TrimSpace(parts[i])
			case "text":
				text = parts[i]
			}
		}
		st, ok1 := assCueTime(start)
		et, ok2 := assCueTime(end)
		if !ok1 || !ok2 {
			continue
		}
		text = assStyle.ReplaceAllString(text, "")
		text = strings.ReplaceAll(text, `\N`, "\n")
		text = strings.ReplaceAll(text, `\n`, "\n")
		text = strings.ReplaceAll(text, `\h`, " ")
		text = strings.TrimSpace(text)
		// ASS 里"空字幕"常留成 `,,` —— 切完字段后 text 可能只剩逗号，
		// 直接 TrimSpace 判不出空，会输出一条 `--> ,` 的幽灵字幕。
		if strings.Trim(text, ", \t") == "" {
			continue
		}
		out = append(out, st+" --> "+et, text, "")
	}
	if len(out) <= 2 {
		return ""
	}
	return strings.Join(out, "\n")
}

// assCueTime 把 `0:00:01.23` 转成 `00:00:01.230`。
func assCueTime(s string) (string, bool) {
	m := assTime.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) != 5 {
		return "", false
	}
	cs := m[4]
	for len(cs) < 2 {
		cs += "0"
	}
	// 百分秒 → 毫秒
	ms := cs + "0"
	return fmt.Sprintf("%02d:%02d:%02d.%s",
		atoi(m[1]), atoi(m[2]), atoi(m[3]), ms), true
}

// vttTime 匹配 VTT 的一个时间点 `HH:MM:SS.mmm`（小时可能只有一位）。
var vttTime = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})\.(\d{3})`)

// Shift 把整份 VTT 的时间轴整体后移 offsetSec 秒。
//
// 用途只有一个：长音频要切片送转写（网关对单文件有体积上限），
// 每片都是"从 0 开始"的时间轴，拼起来必须把第 N 片整体推后 N×片长。
// 不推的话，第二条字幕会从 00:00:00 重来一遍 —— 播放时表现为
// "字幕一直在开头那几句上打转"。
func Shift(vtt string, offsetSec float64) string {
	if offsetSec <= 0 {
		return vtt
	}
	off := int(offsetSec * 1000)
	lines := strings.Split(vtt, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, "-->") {
			continue
		}
		lines[i] = vttTime.ReplaceAllStringFunc(ln, func(m string) string {
			p := vttTime.FindStringSubmatch(m)
			if len(p) != 5 {
				return m
			}
			ms := atoi(p[1])*3600000 + atoi(p[2])*60000 + atoi(p[3])*1000 + atoi(p[4]) + off
			if ms < 0 {
				ms = 0
			}
			return fmt.Sprintf("%02d:%02d:%02d.%03d",
				ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
		})
	}
	return strings.Join(lines, "\n")
}

// Merge 把多段 VTT 合成一份（丢掉第二段起的 WEBVTT 头与空行）。
func Merge(parts []string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for _, p := range parts {
		for _, ln := range strings.Split(p, "\n") {
			t := strings.TrimSpace(ln)
			if t == "" || t == "WEBVTT" || strings.HasPrefix(t, "NOTE") {
				continue
			}
			if isAllDigits(t) {
				continue // 残留的序号行
			}
			b.WriteString(ln)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
