package subtitle

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ============================================================
//  解析模型返回
// ============================================================

// 模型经常不老实：```json 包裹、前后带一句寒暄、或者干脆只回数组。
// 三种都要能读出来 —— 否则"模型明明答对了，我们却判失败"。
func TestParseScriptTolerant(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		lines int
		lang  string
	}{
		{
			name:  "代码块包裹",
			raw:   "好的，以下是内容：\n```json\n{\"lang\":\"en\",\"lines\":[{\"text\":\"Hello.\",\"trans\":\"你好。\"},{\"text\":\"  \",\"trans\":\"空的\"}]}\n```\n以上。",
			lines: 1,
			lang:  "en",
		},
		{
			name:  "只回数组",
			raw:   `[{"text":"A.","trans":"甲"},{"en":"B.","zh":"乙"}]`,
			lines: 2,
			lang:  "",
		},
		{
			name:  "前后有解释文字",
			raw:   `这是结果：{"lang":"zh","lines":[{"text":"小猫","trans":"cat"}]} 希望有用`,
			lines: 1,
			lang:  "zh",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := ParseScript(c.raw)
			if err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			if len(s.Lines) != c.lines {
				t.Fatalf("句数 = %d，想要 %d（%+v）", len(s.Lines), c.lines, s.Lines)
			}
			if s.Lang != c.lang {
				t.Fatalf("lang = %q，想要 %q", s.Lang, c.lang)
			}
		})
	}
}

// en/zh 是模型常用的别名，必须归一到 text/trans，否则正文全空。
func TestParseScriptAliases(t *testing.T) {
	s, err := ParseScript(`[{"en":"The sun is up.","zh":"太阳升起来了。"}]`)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(s.Lines) != 1 {
		t.Fatalf("句数 = %d", len(s.Lines))
	}
	if s.Lines[0].Text != "The sun is up." || s.Lines[0].Trans != "太阳升起来了。" {
		t.Fatalf("别名没归一：%+v", s.Lines[0])
	}
}

// 「不确定」是这条链路最重要的输出之一：有 reason 无 lines 是**正常结果**，
// 不是错误（要落成 skipped，而不是 failed 让人一条条重试）。
func TestParseScriptEmptyWithReason(t *testing.T) {
	s, err := ParseScript(`{"lang":"en","lines":[],"reason":"标题信息不足"}`)
	if err != nil {
		t.Fatalf("空结果不该报错：%v", err)
	}
	if len(s.Lines) != 0 || s.Reason != "标题信息不足" {
		t.Fatalf("解析结果不对：%+v", s)
	}
	// 既没有句子也没有理由 = 无法解释的空，才当错误
	if _, err := ParseScript(`{"lines":[]}`); err == nil {
		t.Fatal("空且无理由应当报错")
	}
}

// ============================================================
//  拆长句
// ============================================================

// 模型偶尔整段返回（儿歌、韵文尤其常见）。不拆的话，
// 时间轴上只能从第一句显示到最后一句。
func TestPrepareLinesSplitsLongText(t *testing.T) {
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 6) // ~270 字符
	lines := PrepareLines([]AILine{{Text: long, Trans: "敏捷的棕色狐狸跳过了懒狗。"}})
	if len(lines) < 3 {
		t.Fatalf("长句没拆开：%d 段", len(lines))
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l.Text); n > aiMaxLineRunes {
			t.Fatalf("第 %d 段仍有 %d 字符（上限 %d）", i, n, aiMaxLineRunes)
		}
		if strings.TrimSpace(l.Text) == "" {
			t.Fatalf("第 %d 段是空的", i)
		}
	}
	// 译文要按比例跟着拆，不能全塞在第一段
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(l.Trans) != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 2 {
		t.Fatalf("译文没有跟着拆：%+v", lines)
	}
}

// 短句不该被改动。
func TestPrepareLinesKeepsShortLines(t *testing.T) {
	lines := PrepareLines([]AILine{
		{Text: "Hello.", Trans: "你好。"},
		{Text: "  ", Trans: "空行丢掉"},
	})
	if len(lines) != 1 || lines[0].Text != "Hello." {
		t.Fatalf("短句被改坏了：%+v", lines)
	}
}

// ============================================================
//  时间轴对齐
// ============================================================

// 有可信的停顿探测时，句子必须落在**有声区间**里，不能从停顿时开始。
// 这条是整个功能"字幕能不能对得上"的核心，改坏了必须红。
func TestAlignCuesUsesSpeechSpans(t *testing.T) {
	lines := []AILine{
		{Text: "aaaa", Trans: "一"},
		{Text: "bbbb", Trans: "二"},
		{Text: "cccc", Trans: "三"},
	}
	speech := []Span{{Start: 0, End: 2}, {Start: 3, End: 5}, {Start: 6, End: 8}}
	cues := AlignCues(lines, 10, speech)
	if len(cues) != 3 {
		t.Fatalf("条数 = %d", len(cues))
	}
	want := []Span{{0, 2}, {3, 5}, {6, 8}}
	for i, c := range cues {
		if !near(c.Start, want[i].Start) || !near(c.End, want[i].End) {
			t.Fatalf("第 %d 条 = [%.2f, %.2f]，想要 [%.2f, %.2f]",
				i, c.Start, c.End, want[i].Start, want[i].End)
		}
	}
	// 顺带钉住"不许落进停顿"：[2,3) 与 [5,6) 是静音
	for _, c := range cues {
		for _, gap := range []Span{{2, 3}, {5, 6}} {
			if c.Start > gap.Start && c.Start < gap.End {
				t.Fatalf("字幕起点 %.2f 落在停顿 %v 里", c.Start, gap)
			}
		}
	}
}

// 探测不可信（段数太少）时按字数均分整段。
func TestAlignCuesFallbackEven(t *testing.T) {
	lines := []AILine{{Text: "aa", Trans: ""}, {Text: "bb", Trans: ""}, {Text: "cc", Trans: ""}}
	cues := AlignCues(lines, 9, nil)
	if len(cues) != 3 {
		t.Fatalf("条数 = %d", len(cues))
	}
	if !near(cues[0].Start, 0) || !near(cues[0].End, 3) {
		t.Fatalf("第一条 = [%.2f, %.2f]", cues[0].Start, cues[0].End)
	}
	if !near(cues[2].End, 9) {
		t.Fatalf("最后一条结束于 %.2f，想要 9", cues[2].End)
	}
	// 单调不重叠
	for i := 1; i < len(cues); i++ {
		if cues[i].Start < cues[i-1].End {
			t.Fatalf("第 %d 条与上一条重叠：%.2f < %.2f", i, cues[i].Start, cues[i-1].End)
		}
	}
}

// 拿不到时长（ffprobe 失败、资源没直链）也不能崩：按字数估一段总长。
func TestAlignCuesUnknownDuration(t *testing.T) {
	lines := []AILine{{Text: "Hello there.", Trans: "你好。"}, {Text: "Bye.", Trans: "再见。"}}
	cues := AlignCues(lines, 0, nil)
	if len(cues) != 2 {
		t.Fatalf("条数 = %d", len(cues))
	}
	if cues[0].Start != 0 {
		t.Fatalf("第一条不从 0 开始：%.2f", cues[0].Start)
	}
	if cues[1].Start <= cues[0].Start {
		t.Fatalf("时间轴没有前进：%.2f → %.2f", cues[0].Start, cues[1].Start)
	}
	for _, c := range cues {
		if c.End <= c.Start {
			t.Fatalf("时长为零：%+v", c)
		}
	}
}

// 句子数远多于停顿段数时，也不能出现"结束早于开始"或"越过总时长"的 cue。
func TestAlignCuesClampsOverflow(t *testing.T) {
	lines := make([]AILine, 12)
	for i := range lines {
		lines[i] = AILine{Text: strings.Repeat("word ", 5)}
	}
	speech := []Span{{0, 1}, {2, 3}, {4, 5}}
	cues := AlignCues(lines, 6, speech)
	if len(cues) == 0 {
		t.Fatal("一条都没生成")
	}
	for i, c := range cues {
		if c.End <= c.Start {
			t.Fatalf("第 %d 条时长为零：[%.2f, %.2f]", i, c.Start, c.End)
		}
		if c.End > 6.0001 {
			t.Fatalf("第 %d 条超出总时长：%.2f", i, c.End)
		}
	}
}

// AlignmentMode 必须和 AlignCues 的实际行为一致，否则页面上的说明是骗人的。
func TestAlignmentModeMatchesBehaviour(t *testing.T) {
	lines := []AILine{{Text: "aaaa"}, {Text: "bbbb"}, {Text: "cccc"}}
	if got := AlignmentMode(lines, 10, nil); got != "even" {
		t.Fatalf("无探测结果应为 even，得到 %q", got)
	}
	if got := AlignmentMode(lines, 10, []Span{{0, 2}}); got != "even" {
		t.Fatalf("只有一段应为 even，得到 %q", got)
	}
	speech := []Span{{0, 2}, {3, 5}, {6, 8}}
	if got := AlignmentMode(lines, 10, speech); got != "speech" {
		t.Fatalf("三段可信探测应为 speech，得到 %q", got)
	}
}

// ============================================================
//  输出格式
// ============================================================

// 孩子端的解析器按"时间轴行 + 直到空行"取正文，所以格式错一处就整条不显示。
func TestBuildVTTStructure(t *testing.T) {
	vtt := BuildVTT([]Cue{
		{Start: 0, End: 2.5, Text: "Hello.", Trans: "你好。"},
		{Start: 2.5, End: 4, Text: "Bye.", Trans: ""},
	})
	if !strings.HasPrefix(vtt, "WEBVTT\n\n") {
		t.Fatalf("缺 WEBVTT 头：%q", vtt)
	}
	if n := CountCues(vtt); n != 2 {
		t.Fatalf("cue 数 = %d，想要 2", n)
	}
	// 原文一行、译文一行（同一句里上下两行）
	if !strings.Contains(vtt, "00:00:00.000 --> 00:00:02.500\nHello.\n你好。\n\n") {
		t.Fatalf("第一条格式不对：%q", vtt)
	}
	// 没有译文时不能多出空行（空行 = cue 结束）
	if !strings.Contains(vtt, "00:00:02.500 --> 00:00:04.000\nBye.\n\n") {
		t.Fatalf("第二条格式不对：%q", vtt)
	}
}

// 正文里的空行 / --> 会破坏 VTT 结构，必须清掉。
func TestBuildVTTSanitizesText(t *testing.T) {
	vtt := BuildVTT([]Cue{{Start: 0, End: 1, Text: "a\n\nb --> c", Trans: ""}})
	if CountCues(vtt) != 1 {
		t.Fatalf("正文里的空行把一条切成了多条：%q", vtt)
	}
	if strings.Count(vtt, "-->") != 1 {
		t.Fatalf("正文里的 --> 没清掉：%q", vtt)
	}
}

// ============================================================
//  探测输出解析
// ============================================================

func TestParseSilenceDetect(t *testing.T) {
	stderr := `[silencedetect @ 0x55f] silence_start: 2
[silencedetect @ 0x55f] silence_end: 3.5 | silence_duration: 1.5
[silencedetect @ 0x55f] silence_start: 7
[silencedetect @ 0x55f] silence_end: 7.5 | silence_duration: 0.5
`
	spans := ParseSilenceDetect(stderr, 10)
	if len(spans) != 3 {
		t.Fatalf("有声区间数 = %d，想要 3：%+v", len(spans), spans)
	}
	want := []Span{{0, 2}, {3.5, 7}, {7.5, 10}}
	for i, s := range spans {
		if !near(s.Start, want[i].Start) || !near(s.End, want[i].End) {
			t.Fatalf("第 %d 段 = %+v，想要 %+v", i, s, want[i])
		}
	}
}

// 结尾一直静音（只有 silence_start 没有 end）时，有声区间到它为止。
func TestParseSilenceDetectOpenEnded(t *testing.T) {
	spans := ParseSilenceDetect("[silencedetect @ 0x1] silence_start: 8\n", 10)
	if len(spans) != 1 || !near(spans[0].End, 8) {
		t.Fatalf("开区间静音解析错：%+v", spans)
	}
}

// ============================================================
//  提示词
// ============================================================

// 目录名是重要匹配要素：分级读物的套系/级别只写在目录上，
// 只给标题模型定位不到具体是哪一册。这条钉住"目录必须出现在提示词里"。
func TestBuildScriptPromptIncludesDirectory(t *testing.T) {
	sys, user := BuildScriptPrompt(ScriptContext{
		Title:       "The Helper Bird",
		Category:    "英语 / 分级阅读 / 大猫英语三级",
		RemoteDir:   "大猫英语 / 三级",
		Ext:         "M4A",
		DurationSec: 192,
		Extra:       "这是 Collins Big Cat 的三级读物",
	})
	for _, want := range []string{"The Helper Bird", "英语 / 分级阅读 / 大猫英语三级", "大猫英语 / 三级", "m4a", "3 分 12 秒"} {
		if !strings.Contains(user, want) {
			t.Fatalf("提示词里缺少 %q：\n%s", want, user)
		}
	}
	if !strings.Contains(sys, "Collins Big Cat") {
		t.Fatalf("补充说明没进系统提示词：\n%s", sys)
	}
	// 系统提示词必须强调"不确定就返回空" —— 这是防止编造的最后一道闸
	if !strings.Contains(sys, "空数组") || !strings.Contains(sys, "编出来的内容比没有更糟") {
		t.Fatalf("系统提示词缺少防编造约束：\n%s", sys)
	}
}

func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.01
}
