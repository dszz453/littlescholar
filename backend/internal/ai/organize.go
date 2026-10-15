package ai

import (
	"fmt"
	"strings"
)

// OrganizeCatRef 提示词里用到的"现有目录"。
type OrganizeCatRef struct {
	ID       uint64
	Name     string
	ParentID uint64 // 0 = 一级
	Direct   int    // 直属资源数
}

// OrganizeRemoteRef 提示词里用到的"远端子目录"。
//
// 给 AI 这份清单是「资源自动匹配」成立的前提：不给的话它只知道一个目录 id，
// 对里面有什么一无所知，只能瞎编 remoteDirId，随后被校验全部拒掉 ——
// 表现就是"AI 从来不给映射建议"。
type OrganizeRemoteRef struct {
	ID   string
	Name string
}

// OrganizeResRef 提示词里用到的"现有资源"。
type OrganizeResRef struct {
	ID       uint64
	Title    string
	CategoryID uint64
}

// OrganizeData 整理助手的输入素材。
type OrganizeData struct {
	ScopeID    uint64
	ScopeName  string
	Breadcrumb string

	// Categories 范围内的目录（含范围根自身），供 AI 选择"移动到哪个目录"
	Categories []OrganizeCatRef
	// Resources 范围内的资源
	Resources []OrganizeResRef

	// CanCreateMapping 是否允许提出「新建资源映射」步骤。
	// 只有用户在界面上选好了接入源账号 + 远端目录前缀时才为 true ——
	// 否则 AI 编出来的远端路径一定对不上真实存储。
	CanCreateMapping bool
	AccountID        uint64
	RemoteDirPrefix  string
	// RemoteDirs 选中远端目录下的直接子目录。为空表示没能取到
	// （账号失效 / 超时 / 用户只填了前缀），此时不让 AI 提 createMapping。
	RemoteDirs []OrganizeRemoteRef

	// Instruction 用户额外补充的要求（可空）
	Instruction string
}

// OrganizeStepResult 是 AI 返回的单条步骤（字段名与后端 API DTO 对齐）。
type OrganizeStepResult struct {
	Type string `json:"type"`

	Ref    string `json:"ref,omitempty"`
	Name   string `json:"name,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Parent string `json:"parent,omitempty"`

	ResourceIDs []uint64 `json:"resourceIds,omitempty"`
	Target      string   `json:"target,omitempty"`

	CategoryID uint64 `json:"categoryId,omitempty"`
	NewName    string `json:"newName,omitempty"`

	RemoteDirID   string `json:"remoteDirId,omitempty"`
	RemoteDirName string `json:"remoteDirName,omitempty"`

	Reason string `json:"reason,omitempty"`
}

// OrganizePlanResult AI 返回的整理方案。
type OrganizePlanResult struct {
	Summary string               `json:"summary"`
	Steps   []OrganizeStepResult `json:"steps"`
}

// OrganizeMaxResources 单次送进提示词的资源上限。
//
// 与知识沉淀同理：几百条之后再多给也只是把列表拉长，
// 对"这些内容该怎么分文件夹"的判断没有边际收益，还容易把模型绕晕。
const OrganizeMaxResources = 300

// OrganizeMaxCategories 单次送进提示词的目录上限
const OrganizeMaxCategories = 120

// OrganizeMaxRemoteDirs 单次送进提示词的远端子目录上限
const OrganizeMaxRemoteDirs = 60

// BuildOrganizePrompt 构造整理助手的提示词。
//
// 这个提示词的要害是**引用语法**：AI 不能凭空写目录 ID（它不知道新目录的
// 主键是多少），所以要求它给新建的目录起一个自己命名的 ref，
// 后续步骤用 ref:<名字> 去引用；引用已有目录则用 cat:<id>。
// 这样后端就能按顺序解析，不需要 AI 猜主键。
func BuildOrganizePrompt(d OrganizeData) []Message {
	systemPrompt := `你是一位非常擅长整理儿童学习资源的图书管理员。

你的任务：把一批混在一个文件夹里的资源，重新规划成清晰好用的子文件夹结构。

你只能做四件事：
1. createCategory —— 在范围内新建子目录
2. renameCategory —— 给范围内某个已有目录改名
3. moveResource   —— 把范围内的资源移动到某个目录
4. createMapping  —— 为某个目录登记一条"远端存储目录 → 系统目录"的映射（仅在允许时）

引用语法（非常重要，必须严格遵守）：
- 引用**已存在**的目录：写 "cat:<目录ID>"，ID 必须来自下面给出的目录清单。
- 引用**你本方案中新建**的目录：先给这个新建步骤起一个 ref 名字（如 c1、c2），
  后续步骤写 "ref:<那个名字>"。
- createCategory 的 parent 字段：选择范围内的目录时写 "cat:<目录ID>"；
  想挂在范围根本身时写 "scope"；挂在新建目录下写 "ref:<名字>"。
- 被引用的新建步骤**必须排在引用它的步骤之前**。

硬性要求：
- resourceIds 只能从下面给出的资源清单里原样抄，**绝对不能编造 ID**。
- 一个资源只能出现在一个 moveResource 步骤里，不许重复移动。
- 目录层级不要超过 3 层，宁可少分几类也不要分得琐碎。
- 只有在原结构确实混乱、或者能做成分集/分级时才有必要动；
  已经很整齐就返回空 steps，不要为了显得在干活而瞎折腾。
- 不要修改资源的标题，你没有这个权限。
- 每个步骤都要写 reason，一句话说清为什么。

请严格按以下 JSON 返回，不要有任何多余文字：
{
  "summary": "整体整理思路，60 字以内",
  "steps": [
    {"type":"createCategory","ref":"c1","name":"第一级","icon":"book","parent":"scope","reason":"……"},
    {"type":"moveResource","resourceIds":[1,2,3],"target":"ref:c1","reason":"……"}
  ]
}`

	var sb strings.Builder
	if d.ScopeName != "" {
		fmt.Fprintf(&sb, "【整理范围】%s", d.ScopeName)
		if d.Breadcrumb != "" {
			fmt.Fprintf(&sb, "（位置：%s）", d.Breadcrumb)
		}
		sb.WriteString("\n")
	}
	fmt.Fprintf(&sb, "【范围根目录 ID】%d\n", d.ScopeID)

	sb.WriteString("\n【范围内的目录】（id | 名称 | 直属资源数 | 父目录 id）\n")
	if len(d.Categories) == 0 {
		sb.WriteString("（没有子目录）\n")
	}
	for i, c := range d.Categories {
		if i >= OrganizeMaxCategories {
			fmt.Fprintf(&sb, "…（其余 %d 个目录省略）\n", len(d.Categories)-i)
			break
		}
		fmt.Fprintf(&sb, "%d | %s | %d | %d\n", c.ID, c.Name, c.Direct, c.ParentID)
	}

	fmt.Fprintf(&sb, "\n【范围内的资源】（id | 标题 | 当前目录 id）共 %d 个\n", len(d.Resources))
	for i, r := range d.Resources {
		if i >= OrganizeMaxResources {
			fmt.Fprintf(&sb, "…（其余 %d 个资源省略）\n", len(d.Resources)-i)
			break
		}
		fmt.Fprintf(&sb, "%d | %s | %d\n", r.ID, r.Title, r.CategoryID)
	}

	if d.CanCreateMapping && len(d.RemoteDirs) > 0 {
		fmt.Fprintf(&sb, "\n【远端存储】接入源账号 ID = %d，选中的远端目录 = %q\n", d.AccountID, d.RemoteDirPrefix)
		fmt.Fprintf(&sb, "它下面有这些子目录（remoteDirId | 名称），共 %d 个：\n", len(d.RemoteDirs))
		for i, r := range d.RemoteDirs {
			if i >= OrganizeMaxRemoteDirs {
				fmt.Fprintf(&sb, "…（其余 %d 个省略）\n", len(d.RemoteDirs)-i)
				break
			}
			fmt.Fprintf(&sb, "%s | %s\n", r.ID, r.Name)
		}
		sb.WriteString("提出 createMapping 时：\n" +
			"  remoteDirId 只能从上面这份清单里**原样抄**，一个字都不能改，也不许自己拼；\n" +
			"  remoteDirName 用清单里的名称；\n" +
			"  target 写这条映射要登记到哪个系统目录（写法同上面的引用语法）。\n" +
			"如果某个远端子目录在系统里没有对应位置，就先 createCategory 建一个、再映射过去 ——\n" +
			"这正是「把网盘内容自动归到合适目录」的做法，鼓励这么做。\n")
	} else if d.CanCreateMapping {
		fmt.Fprintf(&sb, "\n【远端存储】接入源账号 ID = %d，远端目录 = %q\n", d.AccountID, d.RemoteDirPrefix)
		sb.WriteString("**没有取到远端目录清单**（账号可能失效、超时，或只填了前缀）。\n" +
			"这种情况下不要编造 remoteDirId，也不要提 createMapping；\n" +
			"只在 summary / reason 里说明你无法确认远端内容即可。\n")
	} else {
		sb.WriteString("\n【注意】本次不提供接入源信息，请不要提出 createMapping 步骤。\n")
	}

	if strings.TrimSpace(d.Instruction) != "" {
		fmt.Fprintf(&sb, "\n【用户额外要求】%s\n", strings.TrimSpace(d.Instruction))
	}

	sb.WriteString("\n请给出整理方案。")

	return []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: sb.String()},
	}
}

// NormalizeOrganizePlan 收敛 AI 的返回：丢掉不认识的步骤类型、清掉空白、
// 给步骤补上序号。真正的合法性校验（ID 是否存在、是否越界）在 handler 里做，
// 因为那需要查库。
func NormalizeOrganizePlan(r *OrganizePlanResult) {
	if r == nil {
		return
	}
	r.Summary = strings.TrimSpace(r.Summary)
	out := make([]OrganizeStepResult, 0, len(r.Steps))
	for _, s := range r.Steps {
		s.Type = strings.TrimSpace(s.Type)
		s.Ref = strings.TrimSpace(s.Ref)
		s.Name = strings.TrimSpace(s.Name)
		s.Parent = strings.TrimSpace(s.Parent)
		s.Target = strings.TrimSpace(s.Target)
		s.NewName = strings.TrimSpace(s.NewName)
		s.RemoteDirID = strings.TrimSpace(s.RemoteDirID)
		s.RemoteDirName = strings.TrimSpace(s.RemoteDirName)
		s.Reason = strings.TrimSpace(s.Reason)

		switch s.Type {
		case OrganizeTypeCreateCategory, OrganizeTypeRenameCategory,
			OrganizeTypeMoveResource, OrganizeTypeCreateMapping:
			out = append(out, s)
		default:
			// 未知类型直接丢：与其带着它去执行（必然失败并中断整批），
			// 不如当作模型自由发挥的噪音忽略掉。
		}
	}
	r.Steps = out
}

// 步骤类型常量（与 handler、前端共用同一套字面量）
const (
	OrganizeTypeCreateCategory = "createCategory"
	OrganizeTypeRenameCategory = "renameCategory"
	OrganizeTypeMoveResource   = "moveResource"
	OrganizeTypeCreateMapping  = "createMapping"
)
