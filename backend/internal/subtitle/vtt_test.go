package subtitle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 一份真实的 SRT（带 BOM、CRLF、序号、逗号时间轴）。
const srtSample = "\ufeff1\r\n00:00:01,000 --> 00:00:03,500\r\n小兔子乖乖\r\n\r\n" +
	"2\r\n00:00:04,000 --> 00:00:06,20\r\n把门儿开开\r\n"

func TestSRTToVTT(t *testing.T) {
	out, err := Normalize([]byte(srtSample))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !strings.HasPrefix(out, "WEBVTT") {
		t.Fatalf("缺 WEBVTT 头\n%s", out)
	}
	if strings.Contains(out, "00:00:01,000") {
		t.Error("时间轴里的逗号没换成点，浏览器会整条丢掉")
	}
	if !strings.Contains(out, "00:00:01.000 --> 00:00:03.500") {
		t.Errorf("时间轴转换不对\n%s", out)
	}
	// 毫秒不足 3 位要补齐
	if !strings.Contains(out, "00:00:06.200") {
		t.Errorf("毫秒没补齐\n%s", out)
	}
	// 序号行必须去掉
	for _, ln := range strings.Split(out, "\n") {
		if ln == "1" || ln == "2" {
			t.Errorf("残留了序号行 %q", ln)
		}
	}
	if !strings.Contains(out, "小兔子乖乖") || !strings.Contains(out, "把门儿开开") {
		t.Error("正文丢了")
	}
}

func TestGBKSRT(t *testing.T) {
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(strings.TrimPrefix(srtSample, "\ufeff")))
	if err != nil {
		t.Fatalf("编码样本失败: %v", err)
	}
	out, err := Normalize(gbk)
	if err != nil {
		t.Fatalf("Normalize(GBK): %v", err)
	}
	// 这行是关键：GBK 当 UTF-8 解会变成一堆替换字符
	if !strings.Contains(out, "小兔子乖乖") {
		t.Errorf("GBK 字幕没解对，内容成了：%q", out)
	}
	if strings.ContainsRune(out, '\uFFFD') {
		t.Error("输出里有替换字符（乱码）")
	}
}

func TestASSToVTT(t *testing.T) {
	ass := `[Script Info]
Title: 测试
ScriptType: v4.00+

[V4+ Styles]
Format: Name, Fontname, Fontsize
Style: Default,Arial,20

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:03.50,Default,,0,0,0,,{\pos(10,20)}小兔子乖乖
Dialogue: 0,0:00:04.00,0:00:06.20,Default,,0,0,0,,把门儿开开\N快点儿开开
Dialogue: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,,
`
	out, err := Normalize([]byte(ass))
	if err != nil {
		t.Fatalf("Normalize(ASS): %v", err)
	}
	if !strings.Contains(out, "00:00:01.000 --> 00:00:03.500") {
		t.Errorf("ASS 时间轴转换不对\n%s", out)
	}
	if !strings.Contains(out, "00:00:06.200") {
		t.Errorf("百分秒 → 毫秒 转换不对\n%s", out)
	}
	if strings.Contains(out, `\pos`) || strings.Contains(out, "{") {
		t.Errorf("样式标签没去掉\n%s", out)
	}
	// \N 必须变成真换行
	if !strings.Contains(out, "把门儿开开\n快点儿开开") {
		t.Errorf(`\N 没转成换行\n%s`, out)
	}
	// 空文本行不能留
	if strings.Count(out, "-->") != 2 {
		t.Errorf("有效字幕条数不对：%d", strings.Count(out, "-->"))
	}
}

// ASS 的 Format 顺序不是固定的，字段必须按 Format 行来取。
func TestASSFormatOrder(t *testing.T) {
	// ⚠️ 这里刻意把 Start/End/Text 放在**非前三位**：
	// 如果实现里写死 `parts[0]/[1]/[2]`，这个用例就会红。
	// 第一版把 Format 写成 Start,End,Text 打头，硬编码下标也能过 —— 假绿。
	ass := `[Events]
Format: Layer, Style, Start, End, Text
Dialogue: 0,Default,0:00:02.00,0:00:03.00,顺序打乱也得对
`
	out, err := Normalize([]byte(ass))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !strings.Contains(out, "00:00:02.000 --> 00:00:03.000") || !strings.Contains(out, "顺序打乱也得对") {
		t.Errorf("按 Format 取字段失败（写死下标就会这样）\n%s", out)
	}
}

func TestVTTUntouched(t *testing.T) {
	src := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n原样\n"
	out, err := Normalize([]byte(src))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !strings.Contains(out, "原样") || !strings.HasPrefix(out, "WEBVTT") {
		t.Errorf("已有 VTT 被改坏了\n%s", out)
	}
}

// ⚠️ 这条是本轮最容易写错的一条：
// `第1集` 是 `第10集` 的前缀，按"前缀匹配"配字幕就会把第 10 集的字幕
// 配到第 1 集上 —— 孩子看到的是完全对不上的内容。
func TestMatchScorePrefixTrap(t *testing.T) {
	// 真正的陷阱是"没补零的编号"：EP1 是 EP10 的前缀。
	// （中文的「第1集」反而不是 —— 中间的「集」把前缀切断了，
	//   这也解释了为什么第一版用例改坏代码也照样全绿。）
	if sc := MatchScore("EP1.mp4", "EP10.srt"); sc != 0 {
		t.Errorf("EP10 的字幕被配给了 EP1，分数 %d", sc)
	}
	if sc := MatchScore("01.mp4", "010.srt"); sc != 0 {
		t.Errorf("010 的字幕被配给了 01，分数 %d", sc)
	}
	if sc := MatchScore("第1集.mp4", "第1集.srt"); sc != 100 {
		t.Errorf("同名字幕分数应为 100，得到 %d", sc)
	}
	if sc := MatchScore("第1集.mp4", "第1集.chs.srt"); sc != 90 {
		t.Errorf("中文后缀应优先（90），得到 %d", sc)
	}
	if sc := MatchScore("第1集.mp4", "第1集.eng.srt"); sc != 70 {
		t.Errorf("英文后缀应为 70，得到 %d", sc)
	}
	if sc := MatchScore("Movie.2020.mkv", "Other.2020.srt"); sc != 0 {
		t.Errorf("完全无关的字幕不该匹配，得到 %d", sc)
	}
}

func TestFindNearby(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "Subs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "第1集.mp4"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "第10集.mp4"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(sub, "第10集.srt"), []byte(srtSample), 0o644)
	os.WriteFile(filepath.Join(sub, "第1集.chs.srt"), []byte(srtSample), 0o644)

	got, score := FindNearby(filepath.Join(dir, "第1集.mp4"))
	if score < 90 {
		t.Fatalf("没找到第1集的字幕（score=%d, path=%s）", score, got)
	}
	if !strings.Contains(filepath.Base(got), "第1集") {
		t.Errorf("配错集了：%s", got)
	}
}

func TestSplitTitle(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		episode int
	}{
		{"小猪佩奇 第01集", "小猪佩奇", 1},
		{"Peppa.Pig.S01E03.1080p.BluRay", "Peppa Pig", 3},
		{"海底小纵队 EP12 中字", "海底小纵队", 12},
		{"西游记【高清】第5集", "西游记", 5},
	}
	for _, c := range cases {
		got, season, ep := SplitTitle(c.in)
		if ep != c.episode {
			t.Errorf("%q 集数=%d，期望 %d", c.in, ep, c.episode)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%q 片名=%q，期望包含 %q", c.in, got, c.want)
		}
		_ = season
	}
}

func TestIsSubtitle(t *testing.T) {
	for _, n := range []string{"a.srt", "a.ASS", "a.ssa", "a.vtt", "a.sub"} {
		if !IsSubtitle(n) {
			t.Errorf("%s 应该认作字幕", n)
		}
	}
	for _, n := range []string{"a.mp4", "a.srt.bak", "srt", "a.epub"} {
		if IsSubtitle(n) {
			t.Errorf("%s 不该认作字幕", n)
		}
	}
}
