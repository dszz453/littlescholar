package adapter

import (
	"strings"
	"sync"
	"testing"
)

// 提取码是给**人**看的：家长在一台设备上看到，再到另一台敲进去。
// 所以这份测试关心的不是熵，而是"有没有那种一看就错、一敲就错的字符"。
//
// 放在 adapter 包而不是 quark 包里，是因为字符集本身属于公共能力
// （百度/UC 若将来也要创建分享，用的是同一个生成器）。
func TestRandomPasscodeShape(t *testing.T) {
	// 0/O、1/l/I 是 OCR 与手抄最常混的一组；把它们排除掉是刻意的取舍。
	const confusing = "0O1lI"

	for i := 0; i < 500; i++ {
		p := RandomPasscode(4)
		if len(p) != 4 {
			t.Fatalf("长度 = %d，期望 4", len(p))
		}
		if strings.ContainsAny(p, confusing) {
			t.Fatalf("提取码 %q 含易混字符", p)
		}
		for _, c := range p {
			if !strings.ContainsRune(sharePasscodeAlphabet, c) {
				t.Fatalf("提取码 %q 含字符集外的 %q", p, c)
			}
		}
	}
}

// TestRandomPasscodeNotDegenerate 分布检查。
//
// 只断言"长度对、字符合法"会漏掉一类很隐蔽的坏法：生成器恒返回同一个
// 字符（比如取模写错、随机源没接上），每个码看起来都完全合法，
// 但所有权都在一个值上 —— 等于没有提取码。
func TestRandomPasscodeNotDegenerate(t *testing.T) {
	seen := make(map[string]int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				p := RandomPasscode(4)
				mu.Lock()
				seen[p]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) < 100 {
		t.Fatalf("1600 次只生成了 %d 个不同的码，随机源可能没接上", len(seen))
	}
}

// TestRandomPasscodeZeroOrNegativeFallsBack 传 0 / 负数时给默认长度，
// 而不是返回空串 —— 空提取码会被网盘当成"不需要提取码"，
// 悄悄把一条私密分享变成公开链接。
func TestRandomPasscodeZeroOrNegativeFallsBack(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if p := RandomPasscode(n); len(p) != 4 {
			t.Errorf("RandomPasscode(%d) = %q，期望退回 4 位", n, p)
		}
	}
}
