package handler

import (
	"strconv"
	"strings"

	"github.com/littlescholar/core/internal/model"
	"gorm.io/gorm"
)

// resolveCategoryPaths 把一批分类 ID 解析成「可读的目录路径」。
//
// 为什么要它：学习记录 / 播放历史原先只带 category_name（资源直接挂的那一级），
// 而孩子端的目录是三层（乐学英语 / 新概念英语 / 第一册），家长看到「第一册」
// 根本不知道是哪个系列。Category 表本来就存了物化路径 Path（形如 `/1/12/35/`），
// 顺着它把各级名字拼起来就行，不需要递归查库。
//
// 一次批量解析而不是逐行查：列表页一页几十行，逐行找祖先就是 N+1。
//
// 返回 map[分类ID]路径串；找不到的 ID 不出现在结果里（调用方自行回退到
// 记录里冗余的 category_name）。
func resolveCategoryPaths(db *gorm.DB, ids []uint64) map[uint64]string {
	out := make(map[uint64]string, len(ids))
	if db == nil || len(ids) == 0 {
		return out
	}

	// 去重：同一个目录下的多集资源会重复传同一个 ID
	seen := make(map[uint64]bool, len(ids))
	want := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return out
	}

	// 整行查，不用 Select("id","path")：只取子集再往下游传是踩过的坑
	// （没查的列全是零值，调用方拿到的是"看着有值、其实是空"的结构）。
	var cats []model.Category
	if err := db.Where("id IN ?", want).Find(&cats).Error; err != nil {
		return out
	}
	if len(cats) == 0 {
		return out
	}

	// 物化路径里出现的每一级都要有名字，攒齐一次查
	need := make(map[uint64]bool, len(cats)*3)
	for _, c := range cats {
		need[c.ID] = true
		for _, id := range splitPathIDs(c.Path) {
			need[id] = true
		}
	}
	all := make([]uint64, 0, len(need))
	for id := range need {
		all = append(all, id)
	}

	names := make(map[uint64]string, len(all))
	var live []model.Category
	if err := db.Where("id IN ?", all).Find(&live).Error; err == nil {
		for _, c := range live {
			names[c.ID] = c.Name
		}
	}
	// 被删掉的上级目录也要有名字：家长整理了目录树，历史记录不该变成
	// 「/ 第一册」这种断头路径。软删行要显式 Unscoped 才查得到。
	var gone []model.Category
	if err := db.Unscoped().Where("id IN ? AND deleted_at IS NOT NULL", all).Find(&gone).Error; err == nil {
		for _, c := range gone {
			if names[c.ID] == "" {
				names[c.ID] = c.Name
			}
		}
	}

	for _, c := range cats {
		chain := splitPathIDs(c.Path)
		if len(chain) == 0 {
			// Path 没维护好（历史脏数据）：至少给出自己这一级
			chain = []uint64{c.ID}
		}
		parts := make([]string, 0, len(chain))
		for _, id := range chain {
			// 目录名常带尾空格（导入自网盘文件夹名），不 trim 拼出来就是
			// 「Big Cat  / 11级」这种双空格，直接展示在孩子的学习记录里。
			if n := strings.TrimSpace(names[id]); n != "" {
				parts = append(parts, n)
			}
		}
		if len(parts) > 0 {
			out[c.ID] = strings.Join(parts, " / ")
		}
	}
	return out
}

// splitPathIDs 从物化路径 `/1/12/35/` 拆出 [1, 12, 35]
func splitPathIDs(path string) []uint64 {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	segs := strings.Split(trimmed, "/")
	out := make([]uint64, 0, len(segs))
	for _, s := range segs {
		if n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}
