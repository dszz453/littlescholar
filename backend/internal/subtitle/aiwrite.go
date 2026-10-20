package subtitle

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 这个文件负责「拿大模型给的文本 → 一条能播的 VTT」。
//
// 背景：用户实测把文件名丢给大模型，它能返回整份材料的原文和中文对照。
// 但**模型给不出时间戳** —— 它只有文本。所以真正的难点不是"生成字幕"，
// 而是"把 N 句话贴到 M 秒音频上"。
//
// 做法分两层，都不需要模型参与：
//  1. 音频里用 ffmpeg silencedetect 找出「有声区间」。朗读材料句与句之间
//     几乎一定有停顿，这些停顿就是天然的句子边界；
//  2. 按每句的字数把句子**映射到有声时间轴上**（而不是简单按总时长均分）。
//     跳过静音段，句子就不会落在"没人说话的地方"。
//
// 探测不到有声区间（纯音乐、探测失败）时退回按字数均分整段 ——
// 时间轴会糙一些，但正文的"中英对照"仍然成立，比没有字幕好。

// AILine 一行字幕：原文 + 对照译文。
//
// en/zh 是兼容字段：提示词要求 text/trans，但总有模型按 en/zh 回。
// 解析时归一到 text/trans，避免"模型明明答对了、我们却读空了"。
type AILine struct {
	Text  string `json:"text"`
	Trans string `json:"trans"`
	EN    string `json:"en,omitempty"`
	ZH    string `json:"zh,omitempty"`
}

// AIScript 模型返回的一份台本。
type AIScript struct {
	// Lang 原文语言（en / zh）。只用于展示与字幕的 lang 字段。
	Lang string `json:"lang"`
	// Lines 逐句原文 + 对照。
	Lines []AILine `json:"lines"`
	// Reason 模型为什么给不出内容 / 为什么被截断。
	Reason string `json:"reason"`
}

// Span 一段时间区间（秒）。
type Span struct {
	Start float64
	End   float64
}

// Cue 一条字幕。
type Cue struct {
	Start float64
	End   float64
	Text  string
	Trans string
}

// AIScriptSystemPrompt 系统提示词。
//
// ⚠️ 最重要的一条是「不确定就返回空」。这是给孩子跟读的材料，
// 模型编出来的"看起来很像的故事"比没有字幕更糟 —— 孩子会照着错的读，
// 而家长完全看不出来。所以宁可 empty，并在 reason 里说明原因。
const AIScriptSystemPrompt = `你是儿童英语学习资料的字幕整理员，为「小书童」这个给孩子用的学习系统工作。

用户会给你一份音视频资料的信息（标题、所在目录、时长）。这类资料通常是
英语分级读物、绘本朗读、儿歌或教材配套音频。

你的任务：按朗读顺序还原这份资料的原文，并逐句给出中文对照。

硬性要求：
1. **只输出你有把握的原文**。如果你并不确定这份资料的内容（标题太泛、
   不是你熟悉的那套书），lines 返回空数组，并在 reason 里写一句为什么。
   这些字幕是给孩子跟着读的，编出来的内容比没有更糟。
   **判断依据要把标题和目录一起看**：目录名里常常写着套系和级别
   （如「大猫英语分级阅读 · 三级」），标题只是一册的名字。
2. 一句话一条，短句优先：每行不超过 80 个字符；过长的句子在标点处拆开。
3. 保持原文的拼写、大小写和标点，不要改写、不要总结。
4. trans 是中文翻译：忠实、口语化、小学生能读懂；不要加解释性文字。
5. 如果原文本身是中文，text 放中文原文，trans 放对应的英文。
6. 只输出一个 JSON 对象，不要 markdown 代码块、不要任何额外说明：
   {"lang":"en","lines":[{"text":"...","trans":"..."}],"reason":""}
7. 最多 200 行。材料更长时只输出前 200 行，并在 reason 里写「已截断」。`

// ScriptContext 交给模型的"这是什么资料"。
//
// ⚠️ 标题和**目录名要一起给**。分级读物里标题往往只有一册的名字
// （The Helper Bird），套系和级别（大猫英语三级、牛津树 5）只写在目录上；
// 只给标题，模型经常定位不到具体是哪一册，只能瞎猜。
type ScriptContext struct {
	Title       string
	Category    string // 分类路径，如「英语 / 分级阅读 / 大猫英语三级」
	RemoteDir   string // 远端目录名（网盘/WebDAV 上的那一层），可为空
	Ext         string
	DurationSec int
	Extra       string // 管理员补充说明
}

// BuildScriptPrompt 拼出这一次请求的两段提示词。
func BuildScriptPrompt(ctx ScriptContext) (string, string) {
	var b strings.Builder
	b.WriteString("资料标题：")
	b.WriteString(strings.TrimSpace(ctx.Title))
	b.WriteString("\n所在目录：")
	if strings.TrimSpace(ctx.Category) == "" {
		b.WriteString("（未知）")
	} else {
		b.WriteString(strings.TrimSpace(ctx.Category))
	}
	if d := strings.TrimSpace(ctx.RemoteDir); d != "" {
		b.WriteString("\n远端目录：")
		b.WriteString(d)
	}
	if strings.TrimSpace(ctx.Ext) != "" {
		b.WriteString("\n文件类型：")
		b.WriteString(strings.ToLower(strings.TrimSpace(ctx.Ext)))
	}
	if ctx.DurationSec > 0 {
		b.WriteString("\n时长：")
		b.WriteString(formatClock(ctx.DurationSec))
	}
	b.WriteString("\n\n请按上面的要求输出 JSON。不确定就返回空 lines。")

	system := AIScriptSystemPrompt
	if strings.TrimSpace(ctx.Extra) != "" {
		system += "\n\n补充说明（来自管理员，优先遵守）：\n" + strings.TrimSpace(ctx.Extra)
	}
	return system, b.String()
}

// ParseScript 解析模型返回的 JSON。
//
// 模型不总是老老实实只回 JSON：常见的是 ```json 包裹、前后加一句
// "好的，以下是..."、或者干脆只回一个数组。三种都兼容 —— 这里多写十行，
// 换来的是"不用因为模型多说了半句话就整条失败"。
func ParseScript(raw string) (*AIScript, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("模型返回为空")
	}
	s = stripCodeFence(s)
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, fmt.Errorf("模型返回里没有 JSON")
	}
	end := strings.LastIndexAny(s, "}]")
	if end < start {
		return nil, fmt.Errorf("模型返回的 JSON 不完整")
	}
	body := s[start : end+1]

	script := &AIScript{}
	if strings.HasPrefix(body, "[") {
		// 只回了数组：当成 lines。
		if err := json.Unmarshal([]byte(body), &script.Lines); err != nil {
			return nil, fmt.Errorf("解析 lines 失败：%w", err)
		}
	} else {
		if err := json.Unmarshal([]byte(body), script); err != nil {
			return nil, fmt.Errorf("解析 JSON 失败：%w", err)
		}
	}
	script.normalize()
	if len(script.Lines) == 0 && strings.TrimSpace(script.Reason) == "" {
		return nil, fmt.Errorf("模型没有给出任何句子")
	}
	return script, nil
}

// normalize 去空白、补别名、丢掉空行。
func (s *AIScript) normalize() {
	if s == nil {
		return
	}
	out := make([]AILine, 0, len(s.Lines))
	for _, l := range s.Lines {
		text := strings.TrimSpace(l.Text)
		trans := strings.TrimSpace(l.Trans)
		if text == "" {
			text = strings.TrimSpace(l.EN)
		}
		if trans == "" {
			trans = strings.TrimSpace(l.ZH)
		}
		// 模型有时会把中文也塞进 text 里、或者把翻译和原文写在一起。
		// 这里只做"换行/连续空格压平"，不做语言判断 —— 猜错了反而更难查。
		text = squashSpace(text)
		trans = squashSpace(trans)
		if text == "" {
			continue
		}
		out = append(out, AILine{Text: text, Trans: trans})
	}
	s.Lines = out
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	// 去掉语言标记（json / JSON / 空）
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

func squashSpace(s string) string {
	if s == "" {
		return ""
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return strings.Join(fields, " ")
}

// 单行字幕的字符上限。超过就在标点处拆开 —— 孩子端是一行英文一行中文，
// 一行太长会占满整个屏幕，反而挡画面。
const (
	aiMaxLineRunes  = 80
	aiMaxLines      = 200
	aiMinCueSeconds = 0.6
	aiMaxCueSeconds = 15.0
	aiCueGapSeconds = 0.04
)

// mapSpeechEnd 把「有声时间轴上的一点」换算成真实时间，**用于结尾**。
//
// 与 locateSpan（起点）的边界语义相反：t 恰好等于某段长度时，结尾就该收在
// 这一段的末尾，而不是跳到下一段开头。
func mapSpeechEnd(spans []Span, t float64) float64 {
	if t <= 0 || len(spans) == 0 {
		return spans[0].Start
	}
	for _, s := range spans {
		d := s.End - s.Start
		if t <= d {
			return s.Start + t
		}
		t -= d
	}
	return spans[len(spans)-1].End
}

// PrepareLines 清洗 + 拆长句 + 截断。
//
// 拆句不是"为了好看"：模型偶尔会整段返回（尤其是儿歌、韵文），
// 一整段塞进一条字幕，时间轴上就只能从第一句显示到最后一句。
func PrepareLines(lines []AILine) []AILine {
	out := make([]AILine, 0, len(lines))
	for _, l := range lines {
		text := squashSpace(l.Text)
		if text == "" {
			continue
		}
		trans := squashSpace(l.Trans)
		parts := splitText(text, aiMaxLineRunes)
		if len(parts) == 1 {
			out = append(out, AILine{Text: parts[0], Trans: trans})
			continue
		}
		// 原文被拆成几段时，译文按字数比例也拆开，尽量一句对一句。
		weights := make([]int, len(parts))
		for i, p := range parts {
			weights[i] = utf8.RuneCountInString(p)
		}
		transParts := splitByWeights(trans, weights)
		for i, p := range parts {
			out = append(out, AILine{Text: p, Trans: transParts[i]})
		}
		if len(out) >= aiMaxLines {
			break
		}
	}
	if len(out) > aiMaxLines {
		out = out[:aiMaxLines]
	}
	return out
}

// splitText 在标点处把超长文本拆成若干不超过 max 字符的片段。
//
// 优先句末标点（. ! ? 。！？），其次逗号（, ; : ，；、），
// 都没有才硬切 —— 硬切至少保证一行不会长到无法显示。
func splitText(s string, max int) []string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return []string{s}
	}
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		if len(runes) <= max {
			out = append(out, strings.TrimSpace(string(runes)))
			break
		}
		cut := -1
		// 在 [max/2, max] 之间从后往前找标点
		for i := max; i >= max/2 && i > 0; i-- {
			if i-1 < len(runes) && isBreakRune(runes[i-1]) {
				cut = i
				break
			}
		}
		if cut <= 0 {
			cut = max
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
		// 片段可能以空格开头，去掉免得每行前面多一个空格
		for len(runes) > 0 && (runes[0] == ' ' || runes[0] == '\t') {
			runes = runes[1:]
		}
	}
	// 拆完可能留下空片段
	clean := out[:0]
	for _, p := range out {
		if p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return []string{s}
	}
	return clean
}

func isBreakRune(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？', ';', '；':
		return true
	}
	return false
}

// isSoftBreakRune 次级断点：句内停顿。
func isSoftBreakRune(r rune) bool {
	switch r {
	case ',', '，', ';', '；', ':', '：', '、':
		return true
	}
	return false
}

// splitByWeights 按权重把文本切成 n 段。
//
// 用于原句被拆开后把译文也跟着拆。找不到标点就按字数等比切 ——
// 译文和原文本来就不是逐字对应，等比已经比"全塞在第一段"好得多。
func splitByWeights(s string, weights []int) []string {
	n := len(weights)
	if n <= 1 {
		return []string{s}
	}
	if strings.TrimSpace(s) == "" {
		return make([]string, n)
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	if total <= 0 {
		total = n
	}
	runes := []rune(s)
	out := make([]string, n)
	pos := 0
	acc := 0
	for i := 0; i < n; i++ {
		acc += weights[i]
		var end int
		if i == n-1 {
			end = len(runes)
		} else {
			end = int(math.Round(float64(len(runes)) * float64(acc) / float64(total)))
			if end < pos {
				end = pos
			}
			// 尽量在标点/空格处断开
			if cut := softenCut(runes, pos, end); cut > pos {
				end = cut
			}
		}
		if i == n-1 {
			out[i] = strings.TrimSpace(string(runes[pos:]))
		} else if end > pos {
			out[i] = strings.TrimSpace(string(runes[pos:end]))
			pos = end
		}
	}
	return out
}

// softenCut 在 [from, to) 里从后往前找软断点，返回断点位置（找不到返回 0）。
func softenCut(runes []rune, from, to int) int {
	lo := from + (to-from)/2
	for i := to; i > lo; i-- {
		if i-1 >= 0 && i-1 < len(runes) {
			r := runes[i-1]
			if isSoftBreakRune(r) || isBreakRune(r) || r == ' ' {
				return i
			}
		}
	}
	return 0
}

// AlignCues 把句子贴到时间轴上。
//
// speech 是 ffmpeg 探出来的「有声区间」。有就用它（跳过静音，句子落在
// 真正有人说话的地方）；没有就退回整段均分。
func AlignCues(lines []AILine, duration float64, speech []Span) []Cue {
	if len(lines) == 0 {
		return nil
	}
	total := duration
	if total <= 0 {
		total = estimateTotal(lines)
	}
	if total <= 0 {
		total = float64(len(lines)) * 2
	}

	spans := usableSpans(total, speech)
	spanTotal := 0.0
	for _, s := range spans {
		spanTotal += s.End - s.Start
	}
	if spanTotal <= 0 {
		spans = []Span{{Start: 0, End: total}}
		spanTotal = total
	}

	weights := make([]float64, len(lines))
	var wsum float64
	for i, l := range lines {
		w := float64(utf8.RuneCountInString(l.Text))
		if w < 1 {
			w = 1
		}
		weights[i] = w
		wsum += w
	}

	cues := make([]Cue, 0, len(lines))
	var acc float64
	for i, l := range lines {
		startT := spanTotal * acc / wsum
		endT := spanTotal * (acc + weights[i]) / wsum
		acc += weights[i]

		// 起点落在哪一段有声区间里（边界归下一段）。
		//
		// ⚠️ 结尾**不能**截在"这一段有声区间"的末尾。踩过的坑：绘本音频
		// 底噪低、语速慢，silencedetect 会切出上百个 0.5 秒左右的碎片；
		// 模型只给 20 句时，每句的时间份额横跨好几个碎片，一截断就只剩
		// 0.6 秒（aiMinCueSeconds 兜底）—— 22 条字幕只覆盖 30 秒，
		// 剩下 4 分钟整段空白。用户的原话就是"字幕在客户端上看不到"。
		//
		// 正确做法：结尾按**有声时间轴**累加换算成真实时间（允许跨过停顿），
		// 一条字幕自然持续到它该结束的位置。
		idx, offset := locateSpan(spans, startT)
		start := spans[idx].Start + offset
		end := mapSpeechEnd(spans, endT)
		if maxEnd := start + aiMaxCueSeconds; end > maxEnd {
			// 极长的分享（模型给的句子远少于实际内容，或中间是大段音乐）：
			// 让字幕停留十几秒就够了，再久反而挡画面。
			end = maxEnd
		}
		if end <= start+aiMinCueSeconds {
			end = start + aiMinCueSeconds
		}
		if end > total {
			end = total
		}
		if start > total {
			start = total
		}
		cues = append(cues, Cue{Start: start, End: end, Text: l.Text, Trans: l.Trans})
	}

	cues = normalizeCues(cues, total)
	// 兜底：按停顿对齐之后，如果字幕加起来覆盖不到一半时长，说明探测结果
	// 把语音切碎了（碎片多、每段极短），按它排出来的字幕会"闪一下就没了"。
	// 这时整段均分反而稳定 —— 时间轴糙一点，但孩子一直能看到字幕。
	if len(spans) > 1 && total > 0 && coveredSeconds(cues) < total*0.5 {
		return AlignCues(lines, total, nil)
	}
	return cues
}

// coveredSeconds 统计这一组字幕总共占了多少秒（用于"覆盖太差不该信"的兜底）。
func coveredSeconds(cues []Cue) float64 {
	var sum float64
	for _, c := range cues {
		if c.End > c.Start {
			sum += c.End - c.Start
		}
	}
	return sum
}

// normalizeCues 保证时间轴单调、不重叠、不超出总时长。
func normalizeCues(cues []Cue, total float64) []Cue {
	out := cues[:0]
	prevEnd := 0.0
	first := true
	for _, c := range cues {
		// 第一条不要被"间隔"推离 0 秒：从 0.04 秒开始会让孩子端第一条闪一下。
		if !first && c.Start < prevEnd+aiCueGapSeconds {
			c.Start = prevEnd + aiCueGapSeconds
		}
		if c.End <= c.Start {
			c.End = c.Start + aiMinCueSeconds
		}
		if total > 0 && c.End > total {
			c.End = total
		}
		if total > 0 && c.Start >= total {
			// 已经超出音频了：后面几条都放不下，直接丢掉，
			// 免得生成一堆"时长为 0"的 cue 让播放器显示空白。
			break
		}
		out = append(out, c)
		prevEnd = c.End
		first = false
	}
	return out
}

// usableSpans 过滤出可信的有声区间，不可信就退回一整段。
func usableSpans(total float64, speech []Span) []Span {
	if out, ok := usableSpeech(total, speech); ok {
		return out
	}
	return []Span{{Start: 0, End: total}}
}

// AlignmentMode 返回这条字幕的时间轴是怎么来的：speech（按停顿对齐）/ even（按字数均分）。
//
// 和 AlignCues 共用同一套判定 —— 页面上的"按停顿对齐"如果和实际行为不一致，
// 出了问题就没人能靠它定位。
func AlignmentMode(lines []AILine, duration float64, speech []Span) string {
	total := duration
	if total <= 0 {
		total = estimateTotal(lines)
	}
	if _, ok := usableSpeech(total, speech); ok {
		return "speech"
	}
	return "even"
}

// usableSpeech 判定探测到的有声区间是否可信。
//
// 两个条件缺一不可：
//   - 至少 3 段：一两段说明不了句子边界（可能只是开头/结尾的静音）；
//   - 有声总长占整段 >= 40%：太低说明探测把语音也判成了静音
//     （纯音乐、噪声、音量过低），按它对齐只会更歪。
func usableSpeech(total float64, speech []Span) ([]Span, bool) {
	if total <= 0 || len(speech) < 3 {
		return nil, false
	}
	out := make([]Span, 0, len(speech))
	sum := 0.0
	for _, s := range speech {
		start := math.Max(0, s.Start)
		end := math.Min(total, s.End)
		if end-start < 0.15 {
			continue
		}
		out = append(out, Span{Start: start, End: end})
		sum += end - start
	}
	if len(out) < 3 || sum < total*0.4 {
		return nil, false
	}
	return out, true
}

// estimateTotal 拿不到时长时按字数估一个总长。
func estimateTotal(lines []AILine) float64 {
	total := 0.0
	for _, l := range lines {
		total += estimateLineSeconds(l.Text)
	}
	return total
}

// locateSpan 找出「有声时间轴」上的位置落在哪一段，以及段内偏移。
//
// 边界值归到**下一段**：t 恰好等于第 1 段长度时，说明这一句应该从
// 第 2 段开头开始，而不是贴在第 1 段末尾 —— 否则每句都会拖进后面的停顿里。
func locateSpan(spans []Span, t float64) (int, float64) {
	if t <= 0 || len(spans) == 0 {
		return 0, 0
	}
	for i, s := range spans {
		d := s.End - s.Start
		if t < d {
			return i, t
		}
		t -= d
	}
	last := len(spans) - 1
	return last, spans[last].End - spans[last].Start
}

// estimateLineSeconds 拿不到时长时按字数估每句多长。
func estimateLineSeconds(text string) float64 {
	n := utf8.RuneCountInString(text)
	// 朗读约每秒 12 个字符（英文约 2 词/秒）；留 0.6 秒给停顿。
	sec := float64(n)/12.0 + 0.6
	if sec < 1.5 {
		sec = 1.5
	}
	return sec
}

// BuildVTT 生成 WebVTT。
//
// 每条 cue 是两行：原文一行、译文一行。孩子端 parseVTT 会把 "\n" 转成 <br>，
// 所以中英对照是"同一句里上下两行"，不会出现两条字幕互相覆盖。
func BuildVTT(cues []Cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		b.WriteString(vttTimestamp(c.Start))
		b.WriteString(" --> ")
		b.WriteString(vttTimestamp(c.End))
		b.WriteString("\n")
		b.WriteString(cleanCueText(c.Text))
		if t := cleanCueText(c.Trans); t != "" {
			b.WriteString("\n")
			b.WriteString(t)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

// cleanCueText 去掉会破坏 VTT 结构的东西。
//
// 空行是 cue 的分隔符：正文里留一个空行，播放器会把它当成"这条结束了"，
// 后半句就永远不显示。--> 同理，会被当成时间轴行。
func cleanCueText(s string) string {
	s = squashSpace(s)
	s = strings.ReplaceAll(s, "-->", "→")
	return strings.TrimSpace(s)
}

func vttTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	ms := int(math.Round(sec * 1000))
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

// ParseSilenceDetect 解析 ffmpeg `silencedetect` 的输出，返回**有声**区间。
//
// ffmpeg 输出形如（在 stderr 里）：
//
//	[silencedetect @ 0x...] silence_start: 3.2
//	[silencedetect @ 0x...] silence_end: 4.6 | silence_duration: 1.4
//
// 有声区间 = 全部时长的补集。只有 silence_start 没有对应的 silence_end
// （结尾一直静音）时，静音延续到结尾。
func ParseSilenceDetect(stderr string, total float64) []Span {
	var out []Span
	speechFrom := 0.0   // 当前这段有声区间的起点
	silenceFrom := -1.0 // >=0 表示"此刻在静音里"，值是静音起点
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.Contains(line, "silence_") {
			continue
		}
		if v, ok := silenceValue(line, "silence_start:"); ok {
			if v > speechFrom {
				out = append(out, Span{Start: speechFrom, End: v})
			}
			silenceFrom = v
			continue
		}
		if v, ok := silenceValue(line, "silence_end:"); ok {
			// 有声从静音结束处继续
			if silenceFrom >= 0 && v > silenceFrom {
				speechFrom = v
			}
			silenceFrom = -1
		}
	}
	// 结尾没在静音里，说明最后还有一段有声（要总时长才补得出来）
	if silenceFrom < 0 && total > speechFrom {
		out = append(out, Span{Start: speechFrom, End: total})
	}
	// 去掉长度为零的片段
	clean := out[:0]
	for _, s := range out {
		if s.End-s.Start > 0.05 {
			clean = append(clean, s)
		}
	}
	return clean
}

// silenceValue 从一行里取出 `key <数字>` 的值。
func silenceValue(line, key string) (float64, bool) {
	i := strings.Index(line, key)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(line[i+len(key):])
	// 取到第一个空格或竖线为止
	if j := strings.IndexAny(rest, " |"); j >= 0 {
		rest = rest[:j]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// ParseDuration 解析 ffprobe 打印的秒数。
func ParseDuration(out string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// formatClock 秒 → "3 分 12 秒"，只用于提示词里让人/模型看得懂。
func formatClock(sec int) string {
	if sec <= 0 {
		return "未知"
	}
	m := sec / 60
	s := sec % 60
	if m <= 0 {
		return fmt.Sprintf("%d 秒", s)
	}
	return fmt.Sprintf("%d 分 %d 秒", m, s)
}
