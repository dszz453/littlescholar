package ai

import (
	"fmt"
	"strings"
)

// DigestData 供「知识沉淀」使用的目录素材。
type DigestData struct {
	// Name 目录名；全站总览时为「整个学习乐园」
	Name string
	// Breadcrumb 祖先面包屑，如「乐学英语 / 新概念英语」
	Breadcrumb string
	// IsGlobal 全站总览
	IsGlobal bool
	// ResourceCount 该范围内（含子孙）的资源总数
	ResourceCount int
	// ChildNames 下级目录名
	ChildNames []string
	// Titles 资源标题（已截断，见 DigestMaxTitles）
	Titles []string
	// SampleDescriptions 少量资源描述样本（很多资源根本没有描述）
	SampleDescriptions []string
}

// DigestResult AI 返回的沉淀结构。
type DigestResult struct {
	Intro    string   `json:"intro"`
	Points   []string `json:"points"`
	Keywords []string `json:"keywords"`
}

// DigestMaxTitles 单次送进提示词的标题上限。
//
// 上限的意义不只是省 token：目录里塞了 900 集动画时，把 900 个标题全丢给
// 模型，它对"这个目录是干什么的"的判断并不会更准，反而容易在长列表里
// 走神，产出一堆"包含多个视频文件"之类的废话。挑前 80 个已经足够概括。
const DigestMaxTitles = 80

// BuildDigestPrompt 构造知识沉淀提示词。
//
// 语气面向孩子与家长：孩子看到的是「小书童说」，家长看到的是「这个目录
// 到底值不值得让孩子点进去」。
func BuildDigestPrompt(d DigestData) []Message {
	systemPrompt := `你是「小书童」，一个陪伴小学生学习的内容管家。

现在你要把一批**文件名**读成人话，写一段"知识沉淀"，让家长和孩子一眼看懂：
这个文件夹里装的是什么、适合什么年龄段、应该按什么顺序学。

硬性要求：
1. 只根据给出的目录名与文件名来写，**绝对不要编造**没有出现过的内容、
   书名、作者、集数或知识点。看不出来是什么就说得笼统些。
2. intro 写 60~120 字，一句话说清"这里是什么 + 适合谁 + 怎么用"。
   不要以"这个文件夹里"开头，直接切入内容。
3. points 写 2~4 条，每条不超过 30 字，是**可执行的建议**
   （比如"先看第 01 集建立兴趣，再按顺序往下听"），不要写"内容丰富"
   这类空洞评价。
4. keywords 写 2~6 个词，都是名词，用于检索。
5. 如果素材里明显是同一套资源的多个分集，points 要体现"按顺序"。
6. 语气温暖、口语化，不要用"本目录""旨在""涵盖"这类公文词。

请严格按以下 JSON 返回，不要有任何多余文字：
{
  "intro": "……",
  "points": ["……", "……"],
  "keywords": ["……"]
}`

	var sb strings.Builder
	if d.IsGlobal {
		sb.WriteString("范围：整个学习乐园（全站总览）\n")
	} else {
		fmt.Fprintf(&sb, "目录名：%s\n", d.Name)
		if d.Breadcrumb != "" {
			fmt.Fprintf(&sb, "所在位置：%s\n", d.Breadcrumb)
		}
	}
	fmt.Fprintf(&sb, "内容数量：约 %d 个\n", d.ResourceCount)

	if len(d.ChildNames) > 0 {
		fmt.Fprintf(&sb, "\n下级目录：%s\n", strings.Join(d.ChildNames, "、"))
	}

	if len(d.Titles) > 0 {
		fmt.Fprintf(&sb, "\n内容清单（最多 %d 个）：\n", DigestMaxTitles)
		for _, t := range d.Titles {
			fmt.Fprintf(&sb, "- %s\n", t)
		}
	}

	if len(d.SampleDescriptions) > 0 {
		sb.WriteString("\n部分内容说明：\n")
		for _, s := range d.SampleDescriptions {
			fmt.Fprintf(&sb, "- %s\n", s)
		}
	}

	sb.WriteString("\n请生成这个范围的知识沉淀。")

	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: sb.String()},
	}
}

// NormalizeDigestResult 收敛 AI 的返回，防止空字段 / 超长内容直接落库。
func NormalizeDigestResult(r *DigestResult) {
	if r == nil {
		return
	}
	r.Intro = strings.TrimSpace(r.Intro)
	// 有些模型会把整段话塞进单条 point，长度兜一下
	r.Points = normalizeStringList(r.Points, 4, 60)
	r.Keywords = normalizeStringList(r.Keywords, 6, 20)
}

// normalizeStringList 去空、去重、截断、限量，并保持原顺序。
func normalizeStringList(in []string, maxItems, maxRunes int) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		s = truncateRunes(s, maxRunes)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
