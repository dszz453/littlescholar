package util

import (
	"fmt"
	"strings"
)

// NaturalCompare 按「数字自然序」比较两个字符串，返回 -1 / 0 / 1。
//
// 用途是播放列表升序。资源标题里普遍带集号（"牛津树 1-2"、"第10集"），
// 直接按字典序排会把 1-10 排到 1-2 前面 —— 对孩子来说顺序就乱了，
// 而"顺序播放"一旦建立在错的顺序上，下一集就是乱的。
//
// 做法是把字符串切成「数字段 / 非数字段」交替的序列，再逐段比较：
//   - 数字段按数值比，跟位数无关（2 < 10）；
//   - 其余按小写字典序比，所以不区分大小写；
//   - 前导零不影响数值，"01" 与 "1" 视为相等。
//
// 相等时返回 0，调用方要稳定顺序得自己补一个次级键（比如 ID）——
// 这个函数不做全序保证，因为"两个标题同样"时该按什么排是业务决定。
func NaturalCompare(a, b string) int {
	ta, tb := naturalTokens(a), naturalTokens(b)
	n := len(ta)
	if len(tb) < n {
		n = len(tb)
	}
	for i := 0; i < n; i++ {
		if ta[i] != tb[i] {
			if ta[i] < tb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ta) < len(tb):
		return -1
	case len(ta) > len(tb):
		return 1
	}
	return 0
}

// NaturalLess 是 NaturalCompare 的便利封装，供 sort 比较函数直接用。
func NaturalLess(a, b string) bool { return NaturalCompare(a, b) < 0 }

// naturalTokens 把字符串拆成可直接做字典序比较的键序列。
//
// 关键在数字段的编码："0" + 定宽三位长度 + 去前导零的数字。
// 先比长度再比数字本身，于是 2 < 10 这种数值序在**字符串比较**下也成立，
// 不必把数字解析成 int —— 也就不会在超长数字串（标题里偶有纯数字文件名）
// 上溢出。
//
// 前缀 "0" / "1" 用来区分数字段和非数字段，保证同一位置上的比较总是有定义的。
func naturalTokens(s string) []string {
	rs := []rune(s)
	out := make([]string, 0, 8)
	for i := 0; i < len(rs); {
		j := i
		if isASCIIDigit(rs[i]) {
			for j < len(rs) && isASCIIDigit(rs[j]) {
				j++
			}
			digits := strings.TrimLeft(string(rs[i:j]), "0")
			if digits == "" {
				digits = "0"
			}
			out = append(out, fmt.Sprintf("0%03d%s", len(digits), digits))
		} else {
			for j < len(rs) && !isASCIIDigit(rs[j]) {
				j++
			}
			out = append(out, "1"+strings.ToLower(string(rs[i:j])))
		}
		i = j
	}
	return out
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
