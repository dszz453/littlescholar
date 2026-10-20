package subtitle

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// ============================================================
//  标题解析（AI 不可用时的兜底，也是给 AI 的"最后一层保险"）
// ============================================================

func TestParseTitleLocal(t *testing.T) {
	cases := []struct {
		raw     string
		name    string
		season  int
		episode int
		series  bool
	}{
		{"小猪佩奇 第03集 国语 1080p", "小猪佩奇", 0, 3, true},
		{"Peppa Pig EP05 1080p WEB-DL", "Peppa Pig", 0, 5, true},
		{"Bluey.S01E07.2160p.HEVC", "Bluey", 1, 7, true},
		{"唐诗三百首", "唐诗三百首", 0, 0, false},
	}
	for _, c := range cases {
		p := ParseTitleLocal(c.raw)
		if p.Name != c.name {
			t.Errorf("%q 片名解析成 %q，期望 %q", c.raw, p.Name, c.name)
		}
		if p.Season != c.season || p.Episode != c.episode {
			t.Errorf("%q 季/集解析成 %d/%d，期望 %d/%d", c.raw, p.Season, p.Episode, c.season, c.episode)
		}
		if p.IsSeries != c.series {
			t.Errorf("%q IsSeries=%v，期望 %v", c.raw, p.IsSeries, c.series)
		}
	}
}

// 画质/字幕组这些噪音必须被剥掉，否则拿去搜什么也搜不到。
func TestParseTitleStripsNoise(t *testing.T) {
	p := ParseTitleLocal("海底小纵队 第一集 1080p HEVC x265 高清 蓝光")
	if strings.Contains(p.Name, "1080p") || strings.Contains(p.Name, "HEVC") ||
		strings.Contains(p.Name, "蓝光") || strings.Contains(p.Name, "高清") {
		t.Fatalf("噪音没剥干净：%q", p.Name)
	}
	if !strings.Contains(p.Name, "海底小纵队") {
		t.Fatalf("片名丢了：%q", p.Name)
	}
}

// ⚠️ 这条是最容易写反的：S01E02 里既有季也有集，
// 如果先匹 "EPn" 那套正则，会把 01 当成集数（或把 E02 的 02 当成季）。
func TestParseTitleSeasonBeforeEpisode(t *testing.T) {
	p := ParseTitleLocal("汪汪队立大功 S03E11 中文")
	if p.Season != 3 || p.Episode != 11 {
		t.Fatalf("S03E11 解析成 %d季%d集", p.Season, p.Episode)
	}
}

// AI 输出带代码块/解释是很常见的，必须能容忍。
func TestParseTitleAIToleratesChatter(t *testing.T) {
	fake := func(ctx context.Context, system, user string) (string, error) {
		return "好的，解析结果如下：\n```json\n{\"name\":\"小猪佩奇\",\"season\":2,\"episode\":8,\"lang\":\"zh\",\"isSeries\":true}\n```\n希望有帮助！", nil
	}
	p := ParseTitleAI(context.Background(), fake, "小猪佩奇 第二季 第08集")
	if p.Name != "小猪佩奇" || p.Season != 2 || p.Episode != 8 || p.Lang != "zh" {
		t.Fatalf("没从带废话的输出里解出 JSON：%+v", p)
	}
}

// AI 挂了要退回本地解析，而不是整条链路失败。
func TestParseTitleAIFallsBack(t *testing.T) {
	boom := func(ctx context.Context, system, user string) (string, error) {
		return "", errors.New("网关超时")
	}
	p := ParseTitleAI(context.Background(), boom, "小猪佩奇 第09集")
	if p.Episode != 9 {
		t.Fatalf("AI 失败后没退回本地解析：%+v", p)
	}

	garbage := func(ctx context.Context, system, user string) (string, error) {
		return "我不知道", nil
	}
	p2 := ParseTitleAI(context.Background(), garbage, "小猪佩奇 第09集")
	if p2.Episode != 9 {
		t.Fatalf("AI 乱答后没退回本地解析：%+v", p2)
	}
}

// ============================================================
//  候选打分
// ============================================================

// 头名领先足够多时**不该**去问 AI：白花额度，还会被模型改坏。
func TestPickCandidateSkipsAIWhenClearLeader(t *testing.T) {
	var calls int32
	chat := func(ctx context.Context, system, user string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return `{"index":1,"confidence":80}`, nil
	}
	cands := []SiblingCandidate{
		{Name: "第01集.srt", Score: 100},
		{Name: "第01集.eng.srt", Score: 70},
	}
	idx, conf := PickCandidateAI(context.Background(), chat, "第01集.mp4", cands)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("头名领先 30 分还去问了 AI —— 白花额度")
	}
	if idx != 0 || conf != 100 {
		t.Fatalf("选了 %d/%d，期望 0/100", idx, conf)
	}
}

// 分不开时交给 AI，并且**尊重 AI 的 -1**（都不对就别硬凑）。
func TestPickCandidateUsesAIWhenTied(t *testing.T) {
	chat := func(ctx context.Context, system, user string) (string, error) {
		// 提示词里应该能看到候选，否则模型无从判断
		if !strings.Contains(user, "第01集.chs.srt") || !strings.Contains(user, "第01集.eng.srt") {
			t.Errorf("提示词里没有候选列表：%q", user)
		}
		return `{"index":1,"confidence":88,"reason":"英文版更清晰"}`, nil
	}
	cands := []SiblingCandidate{
		{Name: "第01集.chs.srt", Score: 90},
		{Name: "第01集.eng.srt", Score: 88},
	}
	idx, conf := PickCandidateAI(context.Background(), chat, "第01集.mp4", cands)
	if idx != 1 || conf != 88 {
		t.Fatalf("没用 AI 的选择：%d/%d", idx, conf)
	}
}

func TestPickCandidateHonorsReject(t *testing.T) {
	chat := func(ctx context.Context, system, user string) (string, error) {
		return `{"index":-1,"confidence":0,"reason":"集数对不上"}`, nil
	}
	cands := []SiblingCandidate{
		{Name: "第21集.chs.srt", Score: 90},
		{Name: "第22集.chs.srt", Score: 89},
	}
	idx, _ := PickCandidateAI(context.Background(), chat, "第20集.mp4", cands)
	if idx != -1 {
		t.Fatalf("AI 说都不对，却返回了 %d", idx)
	}
}

// 没配 AI 也要能用：退回相似度最高的那个。
func TestPickCandidateWithoutAI(t *testing.T) {
	cands := []SiblingCandidate{
		{Name: "a.srt", Score: 90},
		{Name: "b.srt", Score: 88},
	}
	idx, conf := PickCandidateAI(context.Background(), nil, "x.mp4", cands)
	if idx != 0 || conf != 90 {
		t.Fatalf("没有 AI 时没退回本地最高分：%d/%d", idx, conf)
	}
}

// 置信度门槛：太低的不入库（错字幕比没字幕更伤）。
func TestMinConfidenceGate(t *testing.T) {
	if MinConfidence < 60 {
		t.Fatal("门槛被改低了：60 以下是「仅仅同名同目录」都算不准的区间")
	}
}
