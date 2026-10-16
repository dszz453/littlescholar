package version

import (
	"regexp"
	"strings"
	"testing"
)

// semverRe 三段式版本号。只认 major.minor.patch，
// 不引 semver 库 —— 这个包的全部需求就是"别写错形状"。
var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func TestVersionLooksLikeSemver(t *testing.T) {
	if !semverRe.MatchString(Version) {
		t.Fatalf("Version = %q 不是 major.minor.patch 形状", Version)
	}
}

// 绊线：这里曾经写死 "1.0.0"，而项目已经改了十几轮，后台显示的版本号
// 从上线第一天起就是假的。谁把它改回去都会在这里失败。
//
// （为什么是"不等于 1.0.0"而不是"等于某个具体值"：后者每升一次版本都要改测试，
// 久了就会被人顺手删掉 —— 那才是真正危险的做法。）
func TestVersionIsNotTheOldHardcodedOne(t *testing.T) {
	if Version == "1.0.0" {
		t.Fatal(`Version 被改回了旧的硬编码假值 "1.0.0" —— ` +
			"后台「系统状态」会继续显示一个和实际代码无关的版本号")
	}
}

func TestShortCommit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空", "", ""},
		{"短于 7 位原样返回", "abc", "abc"},
		{"正好 7 位", "a1b2c3d", "a1b2c3d"},
		{"40 位取前 7", "a1b2c3d4e5f60718293a4b5c6d7e8f901a2b3c4d", "a1b2c3d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := Commit
			defer func() { Commit = old }()
			Commit = c.in
			if got := ShortCommit(); got != c.want {
				t.Fatalf("ShortCommit() = %q, want %q", got, c.want)
			}
		})
	}
}

// Full() 的四种退化形态都要对：绝不出现 "1.8.0 (, )" 这种半截括号。
func TestFullDegradesCleanly(t *testing.T) {
	cases := []struct {
		name string
		sha  string
		bt   string
		want string
	}{
		{name: "都有", sha: "a1b2c3d4e5f6", bt: "2026-10-16T06:41:00Z",
			want: Version + " (a1b2c3d, 2026-10-16T06:41:00Z)"},
		{name: "只有 sha", sha: "a1b2c3d4e5f6", want: Version + " (a1b2c3d)"},
		{name: "只有时间", bt: "2026-10-16T06:41:00Z", want: Version + " (built 2026-10-16T06:41:00Z)"},
		{name: "都没有", want: Version},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldS, oldB := Commit, BuildTime
			defer func() { Commit, BuildTime = oldS, oldB }()
			Commit, BuildTime = c.sha, c.bt

			got := Full()
			if got != c.want {
				t.Fatalf("Full() = %q, want %q", got, c.want)
			}
			// "1.8.0 (, )" 这类结果看起来无害，但会让人以为构建信息丢了是正常的
			for _, bad := range []string{"(, ", ", )", "()"} {
				if strings.Contains(got, bad) {
					t.Fatalf("Full() = %q 出现半截括号 %q", got, bad)
				}
			}
		})
	}
}
