package handler

import "testing"

// 「原画 / 流畅」的画质开关。
//
// 这个测试锁的是一条**产品约定**，不是实现细节：
// 不传 quality 时必须落到 original（不转码）。
//
// 为什么值得专门立个测试：这条链路曾经是"后端按容器自动决定要不要转码"，
// 结果是用户完全没有选择权 —— 只要被判为需要转码，每次点开都是"正在转码"，
// 而且转码产物一旦存在就再也播不回源文件。用户明确要求改成
// "默认原画不转码，选流畅再转码"，一旦有人把默认值改回去，
// 那个"一直提示在转码"的体验就会原样复现，而这类回归在人工测试里
// 很容易被忽略（要点开一个 rmvb 才看得出来）。
func TestParsePlayQuality(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		// —— 默认必须是不转码 ——
		{"空值默认原画", "", QualityOriginal},
		{"只有空格也默认原画", "   ", QualityOriginal},
		{"无法识别的值回落原画", "4k", QualityOriginal},
		{"乱填不报错，回落原画", "??", QualityOriginal},

		// —— 显式原画 ——
		{"显式 original", "original", QualityOriginal},
		{"大小写不敏感", "ORIGINAL", QualityOriginal},
		{"带空白也要认", "  original  ", QualityOriginal},

		// —— 流畅 ——
		{"显式 smooth", "smooth", QualitySmooth},
		{"大写 SMOOTH", "SMOOTH", QualitySmooth},
		{"别名 fluent", "fluent", QualitySmooth},
		{"别名 low", "low", QualitySmooth},
		{"带空白 smooth", " smooth ", QualitySmooth},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parsePlayQuality(c.raw); got != c.want {
				t.Errorf("parsePlayQuality(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// 常量值本身就是前后端契约，别被无声改掉。
func TestPlayQualityConstants(t *testing.T) {
	if QualityOriginal != "original" {
		t.Errorf("QualityOriginal 变了：%q —— 前端按字面量发参，改了会静默失效", QualityOriginal)
	}
	if QualitySmooth != "smooth" {
		t.Errorf("QualitySmooth 变了：%q —— 前端按字面量发参，改了会静默失效", QualitySmooth)
	}
	if QualityOriginal == QualitySmooth {
		t.Fatal("两种画质的取值不能相同")
	}
}
