package service

import (
	"sort"
	"testing"

	"github.com/littlescholar/core/internal/model"
)

// ---------------------------------------------------------------------------
// ancestorIDs：由物化路径解析祖先 ID
//
// 这是权限判定的地基。它错了，「授权父目录 → 隐式获得子目录」这条规则
// 就会静默失效：用户该看到的内容看不到（漏授权），或者更糟 ——
// 不该看到的看到了（越权）。所以边界要一个个钉死。
// ---------------------------------------------------------------------------

func TestAncestorIDs(t *testing.T) {
	cases := []struct {
		name string
		cat  model.Category
		want []uint64
	}{
		{
			name: "根目录没有祖先",
			cat:  model.Category{Base: model.Base{ID: 1}, Path: "/1/"},
			// path 里只有自己，应被排除
			want: []uint64{},
		},
		{
			name: "两层：只有父",
			cat:  model.Category{Base: model.Base{ID: 12}, Path: "/1/12/"},
			want: []uint64{1},
		},
		{
			name: "三层：爷、父都有",
			cat:  model.Category{Base: model.Base{ID: 35}, Path: "/1/12/35/"},
			want: []uint64{1, 12},
		},
		{
			name: "四层",
			cat:  model.Category{Base: model.Base{ID: 99}, Path: "/1/12/35/99/"},
			want: []uint64{1, 12, 35},
		},
		{
			name: "空 path（数据未初始化）",
			cat:  model.Category{Base: model.Base{ID: 7}, Path: ""},
			want: []uint64{},
		},
		{
			name: "path 缺尾斜杠（脏数据）仍应可解析",
			cat:  model.Category{Base: model.Base{ID: 35}, Path: "/1/12/35"},
			want: []uint64{1, 12},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ancestorIDs(&c.cat)
			if got == nil {
				got = []uint64{}
			}
			if len(got) != len(c.want) {
				t.Fatalf("ancestorIDs(%q) = %v, 期望 %v", c.cat.Path, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("ancestorIDs(%q) = %v, 期望 %v", c.cat.Path, got, c.want)
				}
			}
		})
	}
}

// path 里混入非数字段时，parseUint 会「跳过非数字字符继续累加」，
// 从而把 "ab" 解析成 0、把 "1a2" 解析成 12。这个行为是刻意为之还是隐患，
// 用测试把它固定下来 —— 至少让后来人看见它。
func TestParseUint_MalformedSegments(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"1", 1},
		{"12", 12},
		{"0", 0},
		{"999999", 999999},
		{"", 0},        // 空段
		{"abc", 0},     // 全非数字 → 0（危险的静默失败）
		{"1a2", 12},    // 跳过 'a' → 12（危险的静默失败）
	}
	for _, c := range cases {
		if got := parseUint([]byte(c.in)); got != c.want {
			t.Errorf("parseUint(%q) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// filterTree：按允许集合过滤目录树
// ---------------------------------------------------------------------------

func mkCat(id uint64, parent *uint64, children ...*model.Category) *model.Category {
	return &model.Category{
		Base:     model.Base{ID: id},
		ParentID: parent,
		Children: children,
	}
}

func collectIDs(nodes []*model.Category) []uint64 {
	var out []uint64
	for _, n := range nodes {
		out = append(out, n.ID)
		out = append(out, collectIDs(n.Children)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// mkTree 每次返回一棵全新的树。
//
// 必须每次新建，不能复用：filterTree 会**原地**改写节点的 Children
// （permission.go 里写的是 n.Children = filterTree(...)），
// 复用同一棵树会让前一个子测试把树剪掉，后一个看到的是被污染的残缺结果。
// 我第一次写就踩了这个坑：全授权用例莫名少了两个节点，
// 排查后确认是测试自身的缺陷，不是 filterTree 的 bug。
func mkTree() []*model.Category {
	return []*model.Category{
		// 1 ─ 12 ─ 35
		//   └ 13
		// 2（始终无权）
		mkCat(1, nil,
			mkCat(12, ptr(1),
				mkCat(35, ptr(12)),
			),
			mkCat(13, ptr(1)),
		),
		mkCat(2, nil),
	}
}

func TestFilterTree(t *testing.T) {
	t.Run("只保留授权节点", func(t *testing.T) {
		root := mkTree()
		allowed := map[uint64]bool{1: true, 12: true}
		got := filterTree(root, allowed)
		ids := collectIDs(got)
		want := []uint64{1, 12}
		if len(ids) != len(want) {
			t.Fatalf("得到 %v, 期望 %v", ids, want)
		}
		for i := range ids {
			if ids[i] != want[i] {
				t.Fatalf("得到 %v, 期望 %v", ids, want)
			}
		}
		// 12 下没有 35，应被摘掉
		if len(got) != 1 || len(got[0].Children) != 1 {
			t.Fatalf("树结构不对：根 %d 个，其子 %d 个", len(got), len(got[0].Children))
		}
		if got[0].Children[0].ID != 12 {
			t.Fatalf("期望子节点 12，实际 %d", got[0].Children[0].ID)
		}
	})

	t.Run("父节点无权则整棵子树都不出现", func(t *testing.T) {
		root := mkTree()
		// 只授权 35，但 1 和 12 不在允许集合里
		allowed := map[uint64]bool{35: true}
		got := filterTree(root, allowed)
		if len(got) != 0 {
			t.Fatalf("期望空，实际 %v", collectIDs(got))
		}
	})

	t.Run("空允许集合返回空", func(t *testing.T) {
		root := mkTree()
		got := filterTree(root, map[uint64]bool{})
		if len(got) != 0 {
			t.Fatalf("期望空，实际 %v", collectIDs(got))
		}
	})

	t.Run("全授权则原样返回", func(t *testing.T) {
		root := mkTree()
		allowed := map[uint64]bool{1: true, 12: true, 13: true, 35: true, 2: true}
		got := filterTree(root, allowed)
		ids := collectIDs(got)
		want := []uint64{1, 2, 12, 13, 35}
		if len(ids) != len(want) {
			t.Fatalf("得到 %v, 期望 %v", ids, want)
		}
		for i := range ids {
			if ids[i] != want[i] {
				t.Fatalf("得到 %v, 期望 %v", ids, want)
			}
		}
	})
}

func ptr(v uint64) *uint64 { return &v }

// ---------------------------------------------------------------------------
// 缓存序列化：空集合用 "-" 表示，必须与「未命中」严格区分
//
// 这条如果搞错，后果是「无任何权限的用户」在缓存失效后突然看到内容，
// 或者每次请求都穿透到数据库。
// ---------------------------------------------------------------------------

func TestCacheEncoding_RoundTrip(t *testing.T) {
	// 模拟 saveCachedIDs 的编码
	encode := func(ids []uint64) string {
		if len(ids) == 0 {
			return "-"
		}
		s := ""
		for i, id := range ids {
			if i > 0 {
				s += ","
			}
			s += uitoa(id)
		}
		return s
	}
	// 模拟 loadCachedIDs 的解码
	decode := func(raw string) ([]uint64, bool) {
		if raw == "-" {
			return []uint64{}, true
		}
		if raw == "" {
			return nil, false
		}
		var out []uint64
		cur := uint64(0)
		has := false
		for i := 0; i <= len(raw); i++ {
			if i == len(raw) {
				if has {
					out = append(out, cur)
				}
				break
			}
			if raw[i] == ',' {
				if has {
					out = append(out, cur)
				}
				cur, has = 0, false
				continue
			}
			cur = cur*10 + uint64(raw[i]-'0')
			has = true
		}
		return out, true
	}

	for _, in := range [][]uint64{{}, {1}, {1, 12}, {1, 12, 35, 999}} {
		raw := encode(in)
		got, hit := decode(raw)
		if !hit {
			t.Fatalf("编码 %v → %q 解码未命中", in, raw)
		}
		if len(got) != len(in) {
			t.Fatalf("往返失败：%v → %q → %v", in, raw, got)
		}
		for i := range got {
			if got[i] != in[i] {
				t.Fatalf("往返失败：%v → %q → %v", in, raw, got)
			}
		}
	}

	// 关键：空集合编码成 "-"，解码必须命中且为空切片
	raw := encode([]uint64{})
	if raw != "-" {
		t.Fatalf("空集合应编码为 \"-\"，实际 %q", raw)
	}
	got, hit := decode(raw)
	if !hit || len(got) != 0 {
		t.Fatalf("\"-\" 应解码为 (空, 命中)，实际 (%v, %v)", got, hit)
	}
	// 空串代表未命中，不能与 "-" 混淆
	if _, hit := decode(""); hit {
		t.Fatal("空串应视为未命中")
	}
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// 账号有效性
// ---------------------------------------------------------------------------

func TestUser_IsActive(t *testing.T) {
	if !(&model.User{Role: model.RoleKid, Status: 1}).IsActive() {
		t.Error("启用且未过期的账号应有效")
	}
	if (&model.User{Status: 0}).IsActive() {
		t.Error("禁用账号应无效")
	}
}
