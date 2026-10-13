package util

import (
	"sort"
	"testing"
)

// 播放列表的排序规则：标题里的数字按数值比，其余按字典序。
//
// 这一条直接决定"顺序播放"播出来是不是一集接一集，
// 所以把容易出错的边角都钉在测试里。
func TestNaturalCompareNumericAware(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// —— 核心用例：字典序会把 1-10 排到 1-2 前面，自然序不能 ——
		{"牛津树 1-2", "牛津树 1-10", -1},
		{"第2集", "第10集", -1},
		{"2", "10", -1},
		{"第99集", "第100集", -1},

		// 位数差很大的也要对（纯字符串比较最容易在这里翻车）
		{"9", "10000000000000000000", -1},

		// 数值相等：前导零不影响
		{"01", "1", 0},
		{"01.mp3", "1.mp3", 0},

		// 字母不区分大小写
		{"abc", "ABC", 0},
		{"A10", "a2", 1},

		// 纯文字按字典序
		{"Apple", "Banana", -1},

		// 一段是另一段的前缀：短的在前
		{"第1集", "第1集下", -1},

		// 中英混排里，数字段优先于文字段（同位置比较，结果稳定即可）
		{"1A", "A1", -1},
	}

	for _, c := range cases {
		got := NaturalCompare(c.a, c.b)
		if got != c.want {
			t.Errorf("NaturalCompare(%q, %q) = %d, 期望 %d", c.a, c.b, got, c.want)
		}
		// 反对称性：反向必须给出相反的结果
		if rev := NaturalCompare(c.b, c.a); rev != -got {
			t.Errorf("NaturalCompare(%q, %q) = %d，与正向 %d 不构成反对称",
				c.b, c.a, rev, got)
		}
	}
}

// 实际排一遍：这是用户能看见的那份列表。
func TestNaturalSortPlaylist(t *testing.T) {
	in := []string{
		"牛津树 1-10.mp3", "牛津树 1-2.mp3", "牛津树 1-1.mp3",
		"牛津树 2-1.mp3", "牛津树 1-9.mp3", "牛津树 1-3.mp3",
	}
	sort.SliceStable(in, func(i, j int) bool { return NaturalLess(in[i], in[j]) })

	want := []string{
		"牛津树 1-1.mp3", "牛津树 1-2.mp3", "牛津树 1-3.mp3",
		"牛津树 1-9.mp3", "牛津树 1-10.mp3", "牛津树 2-1.mp3",
	}
	for i := range want {
		if in[i] != want[i] {
			t.Fatalf("排序结果第 %d 项是 %q，期望 %q\n实际完整结果: %v", i, in[i], want[i], in)
		}
	}
}

// 排序必须是稳定的：同一份输入跑多少次结果都一样。
// （调用方还会用 ID 兜底，但比较函数本身不能被随机性污染。）
func TestNaturalCompareIsDeterministic(t *testing.T) {
	pairs := [][2]string{
		{"第1集", "第01集"},
		{"A", "a"},
		{"1A", "1a"},
	}
	for _, p := range pairs {
		for i := 0; i < 5; i++ {
			if got := NaturalCompare(p[0], p[1]); got != 0 {
				t.Fatalf("NaturalCompare(%q, %q) 第 %d 次 = %d，期望 0",
					p[0], p[1], i+1, got)
			}
		}
	}
}
