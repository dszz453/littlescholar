package ai

import (
	"fmt"
	"strings"
)

// WeeklyAwardData 生成一张周奖状所需的素材。
//
// 全部来自系统里已有的真实数据（打卡记录 + 学习明细），
// 不做任何编造 —— 颁奖词里提到的"学了什么"必须是孩子真学过的。
type WeeklyAwardData struct {
	ChildName    string
	Grade        string
	WeekStart    string // 周一 YYYY-MM-DD
	WeekEnd      string // 周日 YYYY-MM-DD
	DaysChecked  int    // 本周达标天数
	PlanDays     int    // 本周计划应完成的天数（用于对比）
	TotalMinutes int    // 本周累计分钟
	Categories   []string
	Titles       []string
}

// WeeklyAwardResult AI 返回的奖状内容
type WeeklyAwardResult struct {
	Title   string `json:"title"`   // 称号，8 字以内
	Emoji   string `json:"emoji"`   // 一个表情符号
	Content string `json:"content"` // 颁奖词
	Color   string `json:"color"`   // 主色，从给定色板里选
}

// awardColorPalette 奖状主色板。
//
// 限定色板而不是让 AI 自由发挥：AI 给的十六进制色经常是低饱和度的
// 灰调，铺在奖状上很难看；给一组儿童向的高饱和度色让它挑，稳定得多。
var awardColorPalette = []string{"#F6BD16", "#5B8FF9", "#61DDAA", "#F08BB4", "#9270CA", "#FF9D4D"}

// BuildWeeklyAwardPrompt 构造「每周 AI 奖状」提示词。
//
// 与学情分析报告的区别：那份是给家长看的诊断书，要客观、要指出问题；
// 这份是给孩子的奖状，唯一目标是**让孩子开心、愿意下周继续**。
// 所以强行约束成"只正面表达"，不允许出现任何批评性措辞。
func BuildWeeklyAwardPrompt(d WeeklyAwardData) []Message {
	palette := strings.Join(awardColorPalette, " / ")

	systemPrompt := `你是一位特别会鼓励孩子的儿童教育老师，现在要为一位坚持学习了一周的孩子颁发一张奖状。

写作要求：
1. 只写正面的话。绝不允许出现任何批评、指正、提醒改进的内容 —— 这是奖状，不是评语。
2. 颁奖词要**具体**：提到孩子这一周真实学过的内容（下面会给出目录名与内容标题），
   而不是空泛的"你真棒"。具体才显得真诚。
3. 语气亲切、有仪式感，像真的在颁奖。可以有一点童趣的表达。
4. 适合孩子的年级理解水平，不要用生僻词。
5. 称号（title）要朗朗上口、8 个字以内，最好和本周学习内容有关，例如"英语小探险家"。

请严格按照以下 JSON 格式返回（不要有多余文字、不要用 markdown 代码块）：
{
  "title": "称号，8字以内",
  "emoji": "一个最能代表这张奖状的 emoji",
  "content": "颁奖词，80-140字，两到三句话",
  "color": "奖状主色，必须从这几个里选一个：` + palette + `"
}`

	cats := "（无记录）"
	if len(d.Categories) > 0 {
		cats = strings.Join(d.Categories, "、")
	}
	titles := "（无记录）"
	if len(d.Titles) > 0 {
		titles = strings.Join(d.Titles, "、")
	}

	userPrompt := fmt.Sprintf(`孩子姓名：%s
年级：%s
奖励周期：%s ~ %s

本周坚持情况：
- 达标天数：%d 天
- 累计学习时长：%d 分钟
- 涉及的学习主题：%s
- 学过的具体内容：%s

请为这个孩子颁发一张奖状。`, orDefault(d.ChildName, "小朋友"), orDefault(d.Grade, "小学"), d.WeekStart, d.WeekEnd,
		d.DaysChecked, d.TotalMinutes, cats, titles)

	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
}

// NormalizeAwardColor 把 AI 返回的颜色收敛到色板内。
//
// AI 有时会自作主张给个不在色板里的色（甚至给"金色"这种文字），
// 直接拿去当 CSS 颜色会渲染成透明。落到最近的色板色上，或者退第一个。
func NormalizeAwardColor(c string) string {
	c = strings.TrimSpace(strings.ToUpper(c))
	for _, p := range awardColorPalette {
		if strings.EqualFold(c, p) {
			return p
		}
	}
	return awardColorPalette[0]
}
