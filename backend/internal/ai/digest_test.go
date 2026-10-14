package ai

import (
	"strings"
	"testing"
)

func TestNormalizeDigestResult(t *testing.T) {
	r := DigestResult{
		Intro:    "  这里有整套分级读物  ",
		Points:   []string{"  从第一级开始  ", "", "从第一级开始", "第二条"},
		Keywords: []string{"牛津树", "  ", "分级阅读", "牛津树"},
	}
	NormalizeDigestResult(&r)

	if r.Intro != "这里有整套分级读物" {
		t.Fatalf("intro 未去空格: %q", r.Intro)
	}
	if len(r.Points) != 2 {
		t.Fatalf("空项与重复项应被去掉，得到 %v", r.Points)
	}
	if r.Points[0] != "从第一级开始" {
		t.Fatalf("要点未去空格: %q", r.Points[0])
	}
	if len(r.Keywords) != 2 {
		t.Fatalf("关键词去重失败: %v", r.Keywords)
	}
}

func TestNormalizeDigestResultCapsLongItems(t *testing.T) {
	long := strings.Repeat("字", 200)
	r := DigestResult{Points: []string{long, "b", "c", "d", "e"}}
	NormalizeDigestResult(&r)

	if len(r.Points) > 4 {
		t.Fatalf("要点最多 4 条，得到 %d", len(r.Points))
	}
	if n := len([]rune(r.Points[0])); n > 60 {
		t.Fatalf("单条要点应截断到 60 字，得到 %d", n)
	}
}

// TestBuildDigestPromptCarriesInput 提示词必须带上范围名与内容清单，
// 否则 AI 只能靠目录名猜。
func TestBuildDigestPromptCarriesInput(t *testing.T) {
	msgs := BuildDigestPrompt(DigestData{
		Name:          "牛津树英语",
		Breadcrumb:    "乐学英语 / 牛津树英语",
		ResourceCount: 465,
		ChildNames:    []string{"第一级"},
		Titles:        []string{"牛津树第 1 集", "牛津树第 2 集"},
	})
	if len(msgs) != 2 {
		t.Fatalf("应产出 system + user 两条消息，得到 %d", len(msgs))
	}
	user := msgs[1].Content
	for _, want := range []string{"牛津树英语", "乐学英语 / 牛津树英语", "465", "第一级", "牛津树第 1 集"} {
		if !strings.Contains(user, want) {
			t.Fatalf("提示词缺少 %q：\n%s", want, user)
		}
	}

	// 全站总览走另一条分支
	global := BuildDigestPrompt(DigestData{IsGlobal: true, ResourceCount: 900})
	if !strings.Contains(global[1].Content, "整个学习乐园") {
		t.Fatalf("全站总览的提示词不对：%s", global[1].Content)
	}
}
