package handler

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/littlescholar/core/internal/ai"
	"github.com/littlescholar/core/internal/model"
	"github.com/littlescholar/core/internal/util"
)

// ============ 小书童的「资源感知」 ============
//
// 改之前小书童只有一张目录名清单，而且是 `level <= 2` 的前两级。
// 由此带来三个问题，用户反馈的"不够智能"基本都出在这：
//
//  1. **它不知道库里有什么**。孩子问「有没有恐龙的书」，它只能泛泛地回
//     "可以去乐学英语看看"——因为它的全部信息来源就是十几个目录名。
//  2. **推荐的资源卡片几乎永远是空的**。原来的做法是全库取 view_count 前 100，
//     再看标题片段有没有出现在 AI 回复里；可 AI 压根没被告知过任何标题，
//     自然不会提到，于是匹配恒为空。
//  3. **没做权限过滤**。目录名清单是全站前两级，孩子只被授权了一个目录，
//     也能看到其余所有目录的名字。这既是越权，也让推荐不准。
//
// 这里补三件事：按权限取目录概况、提问时先检索一遍、卡片只从候选里挑。

// tutorCategoryBrief 一个目录的一句话概况。
type tutorCategoryBrief struct {
	ID    uint64
	Name  string
	Level int
	Count int64
}

// visibleCategoryIDs 当前请求方能看到哪些目录。
//
// perm 未注入时（单测、或历史上只 new 了 AIHandler 的场景）退回"不限"，
// 保持旧行为不炸；线上 main.go 与 router 两处都注入了 perm，双保险。
func (h *AIHandler) visibleCategoryIDs(c *gin.Context) (ids []uint64, unrestricted bool, err error) {
	if h.perm == nil {
		return nil, true, nil
	}
	if util.IsAdmin(c) {
		return nil, true, nil
	}
	u := currentUser(c)
	if u == nil {
		// 与 resource.go 的口径一致：拿不到用户就按"什么都看不到"处理，
		// 宁可少推荐，也不因为中间件疏漏而泄露目录名。
		return []uint64{}, false, nil
	}
	ids, all, err := h.perm.AccessibleCategoryIDs(u)
	if err != nil {
		return nil, false, err
	}
	return ids, all, nil
}

// tutorBriefs 取可见目录的资源数。
//
// hidden 的目录（「搜全网」这类自动收录容器）不算在内：它们本来就不作为
// 学习板块露给孩子，写进提示词只会让 AI 推荐一些"看不到入口"的东西。
func (h *AIHandler) tutorBriefs(scopeIDs []uint64, unrestricted bool) []tutorCategoryBrief {
	q := h.db.Model(&model.Category{}).
		Select("categories.id, categories.name, categories.level, count(resources.id) AS cnt").
		Joins("LEFT JOIN resources ON resources.category_id = categories.id AND resources.deleted_at IS NULL").
		// COALESCE：AutoMigrate 给存量行补的 hidden 列是 NULL 而不是 false
		Where("COALESCE(categories.hidden, false) = false")
	if !unrestricted {
		if len(scopeIDs) == 0 {
			return nil
		}
		q = q.Where("categories.id IN ?", scopeIDs)
	}
	// sort_order 不在 GROUP BY 里，PG 不允许 ORDER BY 引用它 → 包一层 MAX
	q = q.Group("categories.id, categories.name, categories.level").
		Order("categories.level ASC, MAX(categories.sort_order) ASC")

	rows, err := q.Rows()
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []tutorCategoryBrief
	for rows.Next() {
		var b tutorCategoryBrief
		// 必须手工按序 Scan：Model().Select("… AS 别名") 配 Scan(&自定义结构)
		// 会按结构体的 schema 挑列，名字对不上就**静默丢列**（单测 SQLite 能过、
		// 线上 PG 全是零值）。这条踩过一次，见 study.go 的同一处理。
		if err := rows.Scan(&b.ID, &b.Name, &b.Level, &b.Count); err != nil {
			continue
		}
		if b.Count == 0 {
			continue // 空目录写进提示词只是噪音
		}
		out = append(out, b)
	}
	return out
}

// formatBriefs 把概况排成缩进文本，让 AI 看得出目录的层级关系。
func formatBriefs(briefs []tutorCategoryBrief) []string {
	out := make([]string, 0, len(briefs))
	for _, b := range briefs {
		indent := ""
		for i := 1; i < b.Level; i++ {
			indent += "  "
		}
		out = append(out, fmt.Sprintf("%s%s（%d 个）", indent, b.Name, b.Count))
	}
	return out
}

// tutorStopWords 提问里高频但对检索没用的词。
//
// 中文没有空格可用，只能先把这些词抠掉，剩下的再切 2-gram，
// 否则「有没有关于恐龙的英语绘本」整句拿去 LIKE 什么也匹配不到。
var tutorStopWords = []string{
	"有没有", "是不是", "能不能", "可以", "帮我", "一下", "一些", "这个", "那个",
	"这些", "那些", "还有", "或者", "以及", "关于", "介绍", "讲讲", "推荐", "什么",
	"怎么", "如何", "哪些", "哪个", "适合", "比较", "非常", "特别", "看看", "找找",
	"搜索", "孩子", "小孩", "宝宝", "学习", "请问", "问我", "问题",
	"的", "了", "有", "吗", "呢", "吧", "啊", "呀", "我", "你", "他", "她", "它",
	"想", "要", "请", "问", "是", "在", "和", "与", "跟", "给", "对", "把", "会",
	"这", "那", "个", "些", "很", "都", "也", "就", "还", "又", "再", "能", "好",
}

// tutorKeywords 从提问里切出可用于 LIKE 检索的词。
//
// 策略很朴素但够用：抠掉停用词 → 按标点切段 → 短段直接用，长段切 2-gram。
// 中文没分词器，2-gram 是业界兜底；多余的 gram（如「龙英」）查不到东西，
// 但也无害，命中靠的是「恐龙」「英语」这种真词。
func tutorKeywords(question string) []string {
	clean := question
	for _, sw := range tutorStopWords {
		clean = strings.ReplaceAll(clean, sw, " ")
	}

	// 按"标点 / 空白 / 符号"切段（保留字母数字汉字）
	parts := strings.FieldsFunc(clean, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})

	seen := make(map[string]bool, 16)
	out := make([]string, 0, 16)
	add := func(s string) {
		n := len([]rune(s))
		if n < 2 || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	for _, p := range parts {
		rs := []rune(p)
		if len(rs) <= 4 {
			add(p)
			continue
		}
		// 步长 2：相邻 gram 不重复覆盖，够用了，也避免 SQL 条件过多
		for i := 0; i+2 <= len(rs); i += 2 {
			add(string(rs[i : i+2]))
		}
	}

	// 上限 8 个词：再多就是一堆 OR，收益递减而查询变慢
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// tutorHits 按提问检索站内资源，作为 AI 的"事实依据"。
//
// 返回的资源已预加载 Category，供卡片展示目录名。
func (h *AIHandler) tutorHits(question string, scopeIDs []uint64, unrestricted bool, limit int) []model.Resource {
	if limit <= 0 {
		limit = 12
	}
	kws := tutorKeywords(question)
	if len(kws) == 0 {
		return nil
	}

	q := h.db.Model(&model.Resource{}).Preload("Category")
	if !unrestricted {
		if len(scopeIDs) == 0 {
			return nil
		}
		q = q.Where("category_id IN ?", scopeIDs)
	}

	// 方言分流：PG 只有 ILIKE 命中 pg_trgm 的 GIN 索引，SQLite 没有 ILIKE
	op := likeOp(h.db)
	inner := h.db.Where("title "+op+" ?", "%"+kws[0]+"%")
	for _, kw := range kws[1:] {
		inner = inner.Or("title "+op+" ?", "%"+kw+"%")
	}
	// 用 gorm 的"传 *gorm.DB 做条件分组"写法，生成 AND (… OR …)
	q = q.Where(inner)

	// 短标题优先：同一个词命中时，「恐龙大百科」比「恐龙大百科 第03集 高清」
	// 更可能是孩子想找的那本。
	q = q.Order("view_count DESC, LENGTH(title) ASC, id DESC").Limit(limit)

	var hits []model.Resource
	if err := q.Find(&hits).Error; err != nil {
		return nil
	}
	return hits
}

// toHints 转成 ai 包认识的候选结构。
func toHints(hits []model.Resource) []ai.TutorResourceHint {
	out := make([]ai.TutorResourceHint, 0, len(hits))
	for _, r := range hits {
		cat := ""
		if r.Category != nil {
			cat = r.Category.Name
		}
		out = append(out, ai.TutorResourceHint{Title: r.Title, Category: cat})
	}
	return out
}

// pickFromHits 从"这次检索到的候选"里挑出 AI 真正提到的资源。
//
// 只认候选集，不再扫全库：候选集本身就是按提问检索出来的，
// AI 若提到了什么，大概率就在里面；而扫全库既慢，也容易把不相干的
// 热门资源（"某合集 第1集"）误判成被推荐。
func (h *AIHandler) pickFromHits(reply string, hits []model.Resource) []gin.H {
	out := make([]gin.H, 0, 5)
	for _, r := range hits {
		if len(out) >= 5 {
			break
		}
		if !containsAnyFragment(reply, r.Title) {
			continue
		}
		out = append(out, resourceCard(r))
	}
	return out
}

func resourceCard(r model.Resource) gin.H {
	name := ""
	if r.Category != nil {
		name = r.Category.Name
	}
	return gin.H{
		"id":           r.ID,
		"title":        r.Title,
		"categoryId":   r.CategoryID,
		"categoryName": name,
		"cover":        r.Cover,
		"mediaType":    r.MediaType,
	}
}
