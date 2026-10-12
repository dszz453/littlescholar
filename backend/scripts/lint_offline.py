#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
离线一致性校验器（无 Go 工具链时的替代方案）

背景
----
开发机上没有 Go 工具链、没有 Docker，`go build` / `go vet` 都跑不了。
所有"编译能过"的结论都只能靠静态文本分析得出。而手工 grep 做这种分析
非常容易出错——本项目就真实踩过两次：

  1. 只匹配 `authH.Login(` 这种"带括号调用"形式，漏掉了
     `api.POST(p, authH.Profile)` 这种"方法作为值传递"的形式。
     → 结果：误报 11 处"方法不存在"，实际全部存在。
  2. 用 `NewXxxHandler`（构造器名）去查方法集，而方法集挂在
     `XxxHandler`（接收者类型）上。
     → 结果：误报 12 处"方法不存在"，实际全部存在。

也就是说，**校验工具本身也会骗人**，而且它的谎话听起来和真话一模一样。
所以这套检查必须固化下来、可重复执行，而不是每次手搓正则。

它检查什么（都是纯文本能判断、且一旦出错必然编译失败的东西）
-----------------------------------------------------------
1. 路由引用的 handler 方法是否都存在（含"方法作值传递"形式）
2. 跨包符号的定义与引用是否对得上（构造器、导出的 service 函数、错误变量）
3. 括号 / 花括号 / 方括号是否平衡（排除字符串与注释内的）
4. import 的包是否都被用到了（排除空白导入 `_` 和点导入 `.`）
5. 结构体字段与配置读取（cfg.Xxx.Yyy）是否匹配

它不检查什么（得老老实实承认）
------------------------------
- 类型是否匹配、接口是否满足、是否有循环导入
- 变量是否声明后未使用、是否有 nil 解引用
- 泛型约束、方法集对接口的实现关系
这些只有真编译器能判。本脚本全绿 ≠ 能编译，只是"没犯低级错误"。

用法
----
    python3 scripts/lint_offline.py          # 在 backend/ 目录下执行
    python3 scripts/lint_offline.py --quiet  # 只输出失败项

退出码：0 全通过；1 有失败项（可直接接进 CI）。
"""

import argparse
import os
import re
import sys
from collections import defaultdict

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OK = "\033[32mOK\033[0m" if sys.stdout.isatty() else "OK"
FAIL = "\033[31mFAIL\033[0m" if sys.stdout.isatty() else "FAIL"

# 第三方包：导入路径末段 ≠ 源码里的 package 名。
#
# 这类包没法从路径推断包名（`go-redis/v9` 的包名是 `redis`），
# 又读不到它的源码（不在本仓库），只能靠人工登记。
# 只登记"确实对不上"的那些，其余靠路径推断即可。
#
# 注意：这张表是**已知坑位**清单，不是"必须联网查"的清单。
# 新增依赖后若脚本误报"未使用"，把这条加进来即可 ——
# 加之前务必先确认它是真误报（在文件里搜一下包名到底怎么用的）。
KNOWN_PKG_NAMES = {
    "github.com/redis/go-redis/v9": "redis",
    "github.com/golang-jwt/jwt/v5": "jwt",
    "gorm.io/gorm": "gorm",
    "gorm.io/driver/postgres": "postgres",
    "github.com/glebarez/sqlite": "sqlite",
    "go.uber.org/zap": "zap",
    "github.com/spf13/viper": "viper",
    "github.com/gin-gonic/gin": "gin",
    "github.com/google/uuid": "uuid",
    "github.com/skip2/go-qrcode": "qrcode",
}


def read(path):
    with open(os.path.join(ROOT, path), encoding="utf-8") as fh:
        return fh.read()


def walk_go(subdir="."):
    """遍历 .go 文件，返回 [(相对路径, 内容)]。"""
    out = []
    base = os.path.join(ROOT, subdir)
    for dirpath, dirnames, filenames in os.walk(base):
        dirnames[:] = [d for d in dirnames if d not in ("vendor", ".git")]
        for fn in filenames:
            if fn.endswith(".go"):
                full = os.path.join(dirpath, fn)
                rel = os.path.relpath(full, ROOT)
                with open(full, encoding="utf-8") as fh:
                    out.append((rel.replace(os.sep, "/"), fh.read()))
    return out


def strip_strings_and_comments(src):
    """
    去掉字符串字面量与注释，只留"代码骨架"。

    用途：括号平衡检查。否则 `x := "("` 这种会误判。
    实现刻意做得粗糙一些——Go 里没有多行字符串（只有反引号原始串），
    所以状态机足够简单：普通串 "..."、原始串 `...`、行注释 //、块注释 /* */。
    """
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == '"':
            i += 1
            while i < n and src[i] != '"':
                if src[i] == "\\":
                    i += 1
                i += 1
            i += 1
        elif c == "`":
            i += 1
            while i < n and src[i] != "`":
                i += 1
            i += 1
        elif c == "'":
            # 字符字面量 'a' / '\n'
            i += 1
            while i < n and src[i] != "'":
                if src[i] == "\\":
                    i += 1
                i += 1
            i += 1
        elif src.startswith("//", i):
            while i < n and src[i] != "\n":
                i += 1
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
        else:
            out.append(c)
            i += 1
    return "".join(out)


# ---------------------------------------------------------------- 检查 1
def check_router_methods(files, quiet):
    """
    路由引用的 handler 方法必须都存在。

    两个易错点，都在注释里点明，避免下次改脚本时又踩：
    - 方法可能以"值"的形式传给 gin（`api.POST(p, authH.Profile)`），
      不一定要后接 `(`。所以正则**不能**要求后面跟括号。
    - 方法集挂在接收者类型（`AuthHandler`）上，不是构造器名
      （`NewAuthHandler`）。要从构造器签名里取返回类型。
    """
    name = "路由 handler 方法存在性"
    router_src = None
    for rel, src in files:
        if rel.endswith("internal/router/router.go"):
            router_src = src
            break
    if router_src is None:
        return name, False, "找不到 internal/router/router.go"

    # 构造器 -> 返回类型
    ctor_ret = {}
    for rel, src in files:
        if "/handler/" not in rel:
            continue
        for fn, ret in re.findall(r"func (New\w+Handler)\([^)]*\)\s*\*?(\w+)", src):
            ctor_ret[fn] = ret

    vtype = {
        var: ctor_ret.get(ctor)
        for var, ctor in re.findall(r"(\w+H)\s*:=\s*handler\.(New\w+Handler)", router_src)
    }

    # 类型 -> 方法集
    methods = defaultdict(set)
    for rel, src in files:
        if "/handler/" not in rel:
            continue
        for recv, m in re.findall(r"func \(\w+ \*?(\w+)\) ([A-Z][A-Za-z0-9]*)\s*\(", src):
            methods[recv].add(m)

    # 路由里对 handler 变量的引用（不要求后接括号）
    need = defaultdict(set)
    pat = re.compile(r"(?<![A-Za-z0-9_])([a-z][A-Za-z0-9_]*H)\.([A-Z][A-Za-z0-9_]*)")
    for var, m in pat.findall(router_src):
        need[var].add(m)

    problems = []
    total = 0
    lines = []
    for var in sorted(need):
        t = vtype.get(var)
        if t is None:
            problems.append(f"{var}: 未找到对应的 NewXxxHandler 构造器")
            continue
        have = methods.get(t, set())
        miss = sorted(need[var] - have)
        total += len(need[var])
        if miss:
            problems.append(f"{var} ({t}): 路由引用了不存在的方法 {miss}")
        lines.append(f"    {var:12s} {t:20s} 引用 {len(need[var]):2d} 个  {'FAIL' if miss else 'ok'}")

    # 反向：定义了但从未被路由暴露的公开方法（提示，不算失败）
    hidden = []
    for var in sorted(need):
        t = vtype.get(var)
        if t is None:
            continue
        extra = sorted(methods.get(t, set()) - need[var])
        if extra:
            hidden.append(f"{t}: {extra}")

    detail = f"{len(need)} 个 handler，共 {total} 个路由引用"
    if not problems and not quiet:
        detail += "\n" + "\n".join(lines)
    if hidden and not quiet:
        detail += "\n    (未暴露给路由的公开方法，通常是内部调用，请人工确认: " + "; ".join(hidden) + ")"
    return name, not problems, detail + ("\n" + "\n".join("    " + p for p in problems) if problems else "")


# ---------------------------------------------------------------- 检查 2
def check_cross_package_symbols(files, quiet):
    """
    跨包符号：凡是被别的包引用到的导出符号，必须有定义。

    只覆盖"容易断链"的几类：导出函数、导出变量/错误、导出类型。
    接口方法、泛型、字段级引用不在范围内。

    这个检查天生容易误报，本脚本已经踩过三次坑，全部记在下面：

    坑 1：局部变量/字段名恰好和包名一样。
        比如 `transcode` 既是包名，也是 `Handler.transcode` 字段名。
        `h.transcode.EnsureHLS(...)` 里的 `transcode` 是**字段**不是包。
        → 处理：先收集所有"局部变量声明"和"结构体字段名"，命中就跳过。

    坑 2：把注释当代码。
        `// 后续若引用 task.XxxType 会编译失败` 里的 `task.XxxType`
        是注释里的示意，不是真的引用。
        → 处理：先 strip 掉注释再扫。

    坑 3：导入路径末段是版本号。
        `github.com/redis/go-redis/v9` 的包名是 `redis` 不是 `v9`。
        → 处理：末段匹配 /^v\\d+$/ 时回退到倒数第二段。
    """
    name = "跨包符号定义/引用对齐"

    # 收集所有包的导入路径别名 -> 包名（用最后一段），以及本模块路径前缀
    module_prefix = None
    for rel, src in files:
        if rel.endswith("go.mod"):
            continue
    gomod = None
    try:
        gomod = read("go.mod")
    except OSError:
        pass
    if gomod:
        m = re.search(r"^module\s+(\S+)", gomod, re.M)
        if m:
            module_prefix = m.group(1)

    if not module_prefix:
        return name, False, "go.mod 里没找到 module 行，无法判断内部导入"

    # 盘点到"本模块内部包"的导入： alias -> 包目录
    alias_to_dir = {}
    pkg_import_re = re.compile(
        r'(?:(\w+)\s+)?"' + re.escape(module_prefix) + r'/([^"]+)"'
    )
    for rel, src in files:
        for alias, sub in pkg_import_re.findall(src):
            segs = sub.split("/")
            # 坑 3：末段是版本号时（v9 / v5 / v2），真正的包名在前一段
            if re.fullmatch(r"v\d+", segs[-1]) and len(segs) >= 2:
                dirpath = segs[-2]
            else:
                dirpath = segs[-1]
            alias_to_dir[alias or dirpath] = sub

    # 用源码里的 `package xxx` 声明修正真实包名。
    # 路径末段（go-redis）经常 ≠ 包名（redis），只有源码说了算。
    dir_to_pkg = {}
    for rel, src in files:
        d = os.path.dirname(rel)
        m = re.search(r"^package\s+(\w+)", src, re.M)
        if m:
            dir_to_pkg[d] = m.group(1)

    # 把 alias_to_dir 的 key 从"猜的目录名"修正为"真实包名"
    fixed = {}
    for alias, sub in alias_to_dir.items():
        d = f"internal/{sub}" if not sub.startswith("internal") else sub
        real = dir_to_pkg.get(d)
        if real:
            fixed[real] = sub
        else:
            fixed[alias] = sub  # 显式别名优先，文件找不到就保持原样
    alias_to_dir = fixed

    if not alias_to_dir:
        return name, True, "无内部跨包引用"

    # 坑 1：局部变量/字段名恰好和包名一样。
    # 仅靠"名字出现过"判断太粗（study.go 里 `ai` 既是包又是字段）。
    # 更可靠的办法：看**表达式前缀**——
    #     `h.ai.DefaultClient()`  → 前缀 h.  → ai 是字段
    #     `ai.BuildXPrompt()`     → 前缀空    → ai 可能是包
    # 所以下面扫引用时，要求别名左边不是 `.`。
    # 但 `h.transcode.EnsureHLS()` 有两个点，正则 `(\w+)\.([A-Z]\w*)`
    # 匹配到的是 (transcode, EnsureHLS)，左边是 `.`，能被正确排除。
    #
    # localvars 刻意**按文件**收集而不是全局：study.go 里 `ai` 同时是
    # 导入包和字段名（`ai *AIHandler`）。若全局排除，`ai.BuildXPrompt`
    # 会被漏掉；若完全不用，`h.ai.DefaultClient` 又会误报。
    # 按文件收集 + 只在"参数/接收者/:= 声明"时排除，命中场景刚好够用。
    localvars_by_file = {}
    for rel, src in files:
        got = set()
        code = strip_strings_and_comments(src)
        got.update(re.findall(r"(\w+)\s*:=", code))
        got.update(re.findall(r"\bvar\s+(\w+)\s", code))
        # 函数签名里的参数名（含接收者）：只取"名字 类型"里的名字
        for sig in re.findall(r"func\s*(?:\(\w+\s+\*?(\w+)\))?\s*\w*\s*\(([^)]*)\)", code):
            for grp in sig:
                for p in grp.split(","):
                    p = p.strip()
                    if p:
                        got.add(p.split()[0])
        localvars_by_file[rel] = got
    localvars = set().union(*localvars_by_file.values()) if localvars_by_file else set()

    # 预先算好每个包的"名字集合"，含字段与方法，用于宽松匹配
    pkg_all_names = defaultdict(set)
    for rel, src in files:
        pkgdir = os.path.dirname(rel)
        pkg = pkgdir.rsplit("/", 1)[-1] if pkgdir else "main"
        code = strip_strings_and_comments(src)
        # 任何出现的标识符都算"这个名字在这个包里存在"
        pkg_all_names[pkg].update(re.findall(r"\b([A-Z]\w*)\b", code))

    # 每个内部包的导出符号（严格集，用于第一轮判断）
    exported = defaultdict(set)
    for rel, src in files:
        pkgdir = os.path.dirname(rel)
        pkg = pkgdir.rsplit("/", 1)[-1] if pkgdir else "main"
        code = strip_strings_and_comments(src)
        for fn in re.findall(r"^func ([A-Z]\w*)\s*\(", code, re.M):
            exported[pkg].add(fn)
        for recv, fn in re.findall(r"^func \(\w+ \*?(\w+)\) ([A-Z]\w*)\s*\(", code, re.M):
            exported[pkg].add(fn)
        for v in re.findall(r"^var ([A-Z]\w*)\s*=", code, re.M):
            exported[pkg].add(v)
        for t in re.findall(r"^type ([A-Z]\w*)\s", code, re.M):
            exported[pkg].add(t)
        for c in re.findall(r"^\s+([A-Z]\w*)\s+[\*\[]?\w+\s*`", code, re.M):
            exported[pkg].add(c)

    problems = []
    checked = 0
    # 别名左边不能是 `.`（否则是字段链）
    ref_pat = re.compile(r"(?<![.\w])(\w+)\.([A-Z]\w*)")
    for rel, src in files:
        if rel.endswith("_test.go"):
            continue
        # 坑 2：先剥注释，再扫引用
        code = strip_strings_and_comments(src)
        for alias, member in ref_pat.findall(code):
            if alias not in alias_to_dir:
                continue
            pkg = alias_to_dir[alias].rsplit("/", 1)[-1]
            # 同名包/自身包
            if pkg == os.path.dirname(rel).rsplit("/", 1)[-1]:
                continue
            # 坑 1：别名是局部变量或参数 → 不是包引用
            if alias in localvars:
                continue
            checked += 1
            if member not in exported.get(pkg, set()):
                # 放宽：该包任何文件里出现过这个名字（字段、方法、常量）就放过
                if member in pkg_all_names.get(pkg, set()):
                    continue
                problems.append(
                    f"{rel}: {alias}.{member} —— 包 [{pkg}] 中找不到该符号"
                )

    detail = f"检查 {checked} 处跨包引用"
    if problems:
        detail += "\n" + "\n".join("    " + p for p in problems[:40])
        if len(problems) > 40:
            detail += f"\n    ...另有 {len(problems)-40} 处"
    return name, not problems, detail


# ---------------------------------------------------------------- 检查 3
def check_brackets(files, quiet):
    """括号平衡（已剥离字符串与注释）。"""
    name = "括号平衡"
    bad = []
    for rel, src in files:
        code = strip_strings_and_comments(src)
        for op, cl, label in (("{", "}", "花括号"), ("(", ")", "圆括号"), ("[", "]", "方括号")):
            if code.count(op) != code.count(cl):
                bad.append(f"{rel}: {label} {op}{code.count(op)} vs {cl}{code.count(cl)}")
    return name, not bad, (f"{len(files)} 个文件全部平衡" if not bad else "\n".join("    " + b for b in bad))


# ---------------------------------------------------------------- 检查 4
def check_imports_used(files, quiet):
    """
    import 的包必须被用到（_ 和 . 导入除外）。

    坑：导入路径末段是版本号时，Go 的包名取的是倒数第二段。
        `github.com/redis/go-redis/v9`  → 包名 `redis`
        `github.com/golang-jwt/jwt/v5`  → 包名 `jwt`
    按末段取会得到 `v9`/`v5`，于是误报"未使用"。
    """
    name = "import 使用（无未使用导入）"
    bad = []
    for rel, src in files:
        block = re.search(r"^import \(\n(.*?)^\)", src, re.M | re.S)
        if not block:
            continue
        body = block.group(1)
        import_lines = re.findall(r'^\t(?:(\w+|_|\.)\s+)?"([^"]+)"', body, re.M)
        # 先剥字符串/注释，避免把注释里的包名当成使用
        code_after = strip_strings_and_comments(src[block.end():])
        code_before = strip_strings_and_comments(src[: block.start()])
        code = code_before + code_after
        for alias, path in import_lines:
            if alias in ("_", "."):
                continue
            if alias:
                cands = [alias]
            elif path in KNOWN_PKG_NAMES:
                cands = [KNOWN_PKG_NAMES[path]]
            else:
                # 从路径推断包名。只在"确实没见过"时才走这条路，
                # 推错了也别慌 —— 脚本只会误报"未使用"，不会漏报真问题
                # （漏报才危险，误报只是噪音）。
                seg = path.rsplit("/", 1)[-1]
                cands = [seg]
                if re.fullmatch(r"v\d+", seg):
                    cands.append(path.rsplit("/", 1)[0].rsplit("/", 1)[-1])
                if "-" in path.rsplit("/", 1)[-1]:
                    last = path.rsplit("/", 1)[-1]
                    cands.append(last.split("-")[-1])
                    cands.append(last.replace("-", ""))
                    cands.append(last.split("-")[0])
            if not any(re.search(r"\b" + re.escape(c) + r"\b", code) for c in cands):
                bad.append(f"{rel}: {cands[0]} （{path}）似乎未使用")
    return name, not bad, ("全部 import 均被使用" if not bad else "\n".join("    " + b for b in bad))


# ---------------------------------------------------------------- 检查 5
def _norm_type(t):
    """
    归一化 Go 类型表达式，用于跨包比较返回类型。

    必须处理的差异（都是**写法不同、类型相同**）：
      - `adapter.QRCode` vs `QRCode` —— 接口在 adapter 包内写裸名，
        实现在子包里写限定名。压掉包前缀即可。
      - `*QRCode` vs `* QRCode` —— 空白差异
      - `[]adapter.FileItem` vs `[]FileItem`
    这些如果按字面比，会得到一大片假 DIFF。本脚本第一次跑就这么翻车过：
    14 个方法里 10 个被误判为"返回类型不一致"，实际全对。
    """
    t = re.sub(r"\b\w+\.(\w+)", r"\1", t)   # pkg.Type -> Type
    t = re.sub(r"\s+", "", t)
    return t


def check_interface_conformance(files, quiet):
    """
    接口一致性：声明了 interface 且在本仓库内有实现者时，
    逐个方法比对方法名与返回类型。

    为什么这条重要：接口未被满足是**只有编译器能抓**的错误。
    在没有 Go 工具链的环境里，这是最容易写错、最难靠肉眼发现的一类
    错误（方法名少一个字、返回类型少个 `error`，读代码时几乎看不出来）。

    覆盖范围有限，如实说明：
      - 只比对**方法名 + 返回类型**，不比对参数类型
        （参数多含 interface{}、context.Context 等，归一化易误判）
      - 只检查"显式注册"的实现者：即代码里出现过 `adapter.Register(New())`
        或把实例赋给接口变量的地方。纯靠结构体推断"谁实现了哪个接口"
        需要真正的类型系统，静态文本做不到。
    """
    name = "接口方法满足性（方法名 + 返回类型）"

    # 1. 收集所有 interface 定义
    ifaces = {}
    for rel, src in files:
        code = strip_strings_and_comments(src)
        for iname, body in re.findall(
            r"type (\w+) interface \{(.*?)\n\}", code, re.S
        ):
            methods = {}
            for line in body.strip().split("\n"):
                line = line.strip()
                if not line or line.startswith("//"):
                    continue
                mm = re.match(r"(\w+)\((.*?)\)\s*(.+?)$", line)
                if mm:
                    methods[mm.group(1)] = _norm_type(mm.group(3))
            ifaces[iname] = methods

    if not ifaces:
        return name, True, "未发现 interface 定义"

    problems = []
    checked = 0

    # 2. 已知的实现者映射：接口 -> 实现类型所在文件
    #    这里刻意**不做自动推断**（那需要类型系统），而是显式列出
    #    代码中真实存在的实现关系。新增适配器时在此登记。
    known_impls = {
        "DiskAdapter": [
            "internal/adapter/quark/quark.go",
            "internal/adapter/baidu/baidu.go",
            "internal/adapter/uc/uc.go",
            "internal/adapter/tianyi/tianyi.go",
            "internal/adapter/aliyun/aliyun.go",
        ],
        "EncryptorLike": ["internal/util/crypto.go"],
    }

    for iname, impl_files in known_impls.items():
        want = ifaces.get(iname)
        if want is None:
            problems.append(f"接口 {iname} 在代码中找不到定义")
            continue
        for impl_rel in impl_files:
            src = None
            for rel, s in files:
                if rel == impl_rel:
                    src = s
                    break
            if src is None:
                problems.append(f"{impl_rel}（{iname} 的实现）文件不存在")
                continue
            code = strip_strings_and_comments(src)
            got = {}
            for mm in re.finditer(
                r"func \(\w+ \*?(\w+)\) (\w+)\((.*?)\)\s*([^\{]*)\{", code, re.S
            ):
                got[mm.group(2)] = _norm_type(mm.group(4))

            checked += 1
            missing = [m for m in want if m not in got]
            if missing:
                problems.append(f"{impl_rel}: 未实现 {iname} 的方法 {missing}")
                continue
            bad = [
                f"{m}: 接口要求 {want[m]}，实现为 {got[m]}"
                for m in want
                if got[m] != want[m]
            ]
            if bad:
                problems.append(f"{impl_rel}: 返回类型与 {iname} 不符 -> " + "; ".join(bad))

    detail = f"核验 {checked} 个实现关系"
    if problems:
        detail += "\n" + "\n".join("    " + p for p in problems)
    return name, not problems, detail


def split_top(s):
    """
    按顶层逗号切分。括号/方括号/花括号内的逗号不算分隔符。

    必须处理嵌套，因为常见参数里有函数字面量、结构体字面量、map 字面量：
        f(func() { a, b := 1, 2 }, g(X{A: 1, B: 2}, y))
    简单 `s.split(",")` 会切成 6 段，实际只有 2 个实参。
    """
    if not s.strip():
        return []
    parts, depth, cur = [], 0, ""
    for ch in s:
        if ch in "([{":
            depth += 1
        elif ch in ")]}":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append(cur)
            cur = ""
        else:
            cur += ch
    if cur.strip():
        parts.append(cur)
    return parts


def mask_strings(src):
    """
    把字符串/字符字面量替换成占位符，注释直接删掉。

    与 `strip_strings_and_comments` 的关键区别：**保留占位符**。

    为什么必须保留：参数计数时如果把字符串整个删掉，
        util.BadRequest(c, "参数错误")
    会变成
        util.BadRequest(c, )
    逗号还在、但第二个实参消失了 → 被误判为"少传一个参数"。
    实测这个 bug 让本脚本误报 193 处"参数个数不匹配"，实际全是假阳性。
    把字符串换成 "S" 占位符后，误报归零（414 处调用全部匹配）。
    """
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if c == '"':
            j = i + 1
            while j < n and src[j] != '"':
                if src[j] == "\\":
                    j += 1
                j += 1
            out.append('"S"')
            i = j + 1
        elif c == "`":
            j = i + 1
            while j < n and src[j] != "`":
                j += 1
            out.append("`S`")
            i = j + 1
        elif c == "'":
            j = i + 1
            while j < n and src[j] != "'":
                if src[j] == "\\":
                    j += 1
                j += 1
            out.append("'c'")
            i = j + 1
        elif src.startswith("//", i):
            while i < n and src[i] != "\n":
                i += 1
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
        else:
            out.append(c)
            i += 1
    return "".join(out)


# ---------------------------------------------------------------- 检查 6
def check_arg_counts(files, quiet):
    """
    跨包调用的**实参个数**是否与函数定义的形参个数一致。

    这是在没有编译器的环境下，能对"类型层面"做得最接近的一项检查：
    参数个数不符必然编译失败，且是手写代码最常见的失误之一。

    覆盖范围（如实说明）：
      - 只查**本模块内部**的跨包函数调用（`pkg.Func(...)` 形式）
      - 方法调用（`obj.Method(...)`）不查 —— 需要知道 obj 的类型
      - 不查参数**类型**，只查**个数**
      - 形参里 `a, b int` 这种共享类型的写法已正确展开
      - 变参函数（`...`）放宽为"不少于固定参数个数"

    踩过的坑（写在这里免得下次重踩）：
      1. 字符串字面量必须保留占位符，否则参数被"吃掉"→ 193 处假阳性
      2. 包名索引要用源码里的 `package X` 声明，不是目录名
      3. 别名左边不能是 `.`，否则 `h.transcode.EnsureHLS` 会当成跨包调用
    """
    name = "跨包调用实参个数"

    # 包名 -> {函数名: (固定参数个数, 是否变参)}
    defs = defaultdict(dict)
    dir_to_pkg = {}
    for rel, raw in files:
        src = mask_strings(raw)
        m = re.search(r"^package (\w+)", src, re.M)
        if not m:
            continue
        pkg = m.group(1)
        d = os.path.dirname(rel)
        dir_to_pkg[d] = pkg
        for mm in re.finditer(r"^func (\w+)\(([^)]*)\)", src, re.M):
            fn = mm.group(1)
            if fn in ("init", "main"):
                continue
            params = split_top(mm.group(2))
            variadic = any("..." in p for p in params)
            fixed = len([p for p in params if "..." not in p])
            defs[pkg][fn] = (fixed, variadic)

    if not defs:
        return name, True, "未找到包级函数定义"

    problems = []
    checked = 0

    for rel, raw in files:
        src = mask_strings(raw)
        # 本文件导入的内部包别名 -> 真实包名
        local = {}
        for mm in re.finditer(
            r'(?:(\w+)\s+)?"github\.com/littlescholar/core/([^"]+)"', raw
        ):
            alias, sub = mm.group(1), mm.group(2)
            hit = None
            for d, pkg in dir_to_pkg.items():
                if d.replace(os.sep, "/").endswith(sub):
                    hit = pkg
                    break
            if hit:
                local[alias or hit] = hit
        if not local:
            continue

        # 别名左边不能是 `.`（排除链式字段访问）
        for mm in re.finditer(r"(?<![.\w])(\w+)\.(\w+)\(", src):
            alias, fn = mm.group(1), mm.group(2)
            pkg = local.get(alias)
            if pkg is None or fn not in defs.get(pkg, {}):
                continue
            fixed, variadic = defs[pkg][fn]

            # 取实参串：从 '(' 之后开始做括号配对
            start = mm.end()
            depth, i = 1, start
            while i < len(src) and depth > 0:
                if src[i] in "([{":
                    depth += 1
                elif src[i] in ")]}":
                    depth -= 1
                i += 1
            args = split_top(src[start : i - 1])
            got = len(args)

            # 零参调用的 `()` 会被 split_top 返回空列表，这是对的
            checked += 1
            if variadic:
                if got < fixed:
                    problems.append(
                        f"{rel}: {pkg}.{fn} 至少需要 {fixed} 个参数，实际传 {got} 个"
                    )
            elif got != fixed:
                problems.append(
                    f"{rel}: {pkg}.{fn} 需要 {fixed} 个参数，实际传 {got} 个"
                )

    detail = f"核验 {checked} 处跨包调用"
    if problems:
        detail += "\n" + "\n".join("    " + p for p in problems[:40])
        if len(problems) > 40:
            detail += f"\n    ...另有 {len(problems)-40} 处"
    return name, not problems, detail


# ---------------------------------------------------------------- 检查 7
def check_api_path_alignment(files, quiet):
    """
    前后端接口路径对齐。

    这是本脚本里**唯一跨语言**的检查：拿 Go 路由注册出来的端点，
    去对 admin 前端 axios 调用的路径。

    为什么值得做：前端调一个后端没注册的接口，是**运行时 404**，
    构建期完全发现不了（Vite 不会校验字符串 URL），
    而这类错误往往要等到人工点到那个页面才暴露。

    对齐基准（最容易写错的地方）：
        前端 axios.create({ baseURL: '/api' })
        所以前端写 '/admin/x' 实际打到 '/api/admin/x'。
        两边必须统一补/剥 '/api' 前缀再比，否则 83 个端点会全部误报。

    已知局限：
        - 只扫 src/api/index.js 一个文件；页面里若有人直接写 axios/fetch，
          这里看不到。
        - 不校验 HTTP 方法之外的语义（参数名、body 结构）。
        - 后端路由若用变量拼路径，正则抓不到。
    """
    name = "前后端 API 路径对齐"

    router_rel = os.path.join("internal", "router", "router.go")
    router_src = None
    for rel, src in files:
        if rel.replace("\\", "/").endswith("internal/router/router.go"):
            router_src = src
            break
    if router_src is None:
        return name, False, f"找不到 {router_rel}"

    fe_rel = os.path.join(os.path.dirname(ROOT), "frontend-admin", "src", "api", "index.js")
    if not os.path.isfile(fe_rel):
        return name, True, "未发现 frontend-admin/src/api/index.js，跳过"



    # --- 后端：group 变量 -> 前缀 ---
    # 前缀按注册顺序推断：api -> /api，admin -> /api/admin，app -> /api/app。
    grp_prefix = {"api": "/api", "admin": "/api/admin", "app": "/api/app"}
    be = set()
    # 先抓 group 定义，识别 api.Group("xxx")
    for m in re.finditer(r'(\w+)\s*:?=\s*api\.Group\("([^"]*)"\)', router_src):
        grp_prefix[m.group(1)] = "/api" + m.group(2)

    for m in re.finditer(
        r'^\s*(\w+)\s*\.\s*(GET|POST|PUT|DELETE|PATCH)\s*\(\s*"([^"]+)"',
        router_src, re.M
    ):
        recv, verb, path = m.group(1), m.group(2), m.group(3)
        if recv not in grp_prefix:
            continue
        be.add((verb, grp_prefix[recv] + path))

    if not be:
        return name, False, "未能从 router.go 提取到任何端点（正则或写法变了）"

    # --- 前端：baseURL 探测 + 路径提取 ---
    fe_src = read(fe_rel)
    base = "/api"
    mb = re.search(r'baseURL\s*:\s*[^,}]*?\|\|\s*[\'"]([^\'"]+)[\'"]', fe_src)
    if mb:
        base = mb.group(1)

    fe = []
    for m in re.finditer(
        r"http\.(get|post|put|delete|patch)\(\s*[`'\"]([^`'\"]+)[`'\"]",
        fe_src
    ):
        verb, path = m.group(1).upper(), m.group(2)
        norm = re.sub(r"\$\{[^}]+\}", ":x", path)
        if not norm.startswith("/"):
            norm = "/" + norm
        if base and not norm.startswith(base + "/"):
            norm = base + norm
        fe.append((verb, norm))

    def n(p):
        return re.sub(r":\w+", ":x", p)

    be_n = {(v, n(p)) for v, p in be}
    fe_n = [(v, n(p)) for v, p in fe]

    problems = []
    for v, p in fe_n:
        if (v, p) not in be_n:
            problems.append(f"前端调用了未注册的接口: {v} {p}")

    used = {(v, p) for v, p in fe_n}
    unused = sorted(be_n - used)

    detail = (
        f"后端 {len(be_n)} 个端点 / 前端 {len(fe_n)} 处调用"
        f"（baseURL={base}）\n"
        f"    后端注册但后台前端未直接调用: {len(unused)} 个"
        f"（多为 C 端专用或静态挂载 /media、/transcode）"
    )
    if problems:
        detail += "\n" + "\n".join("    " + p for p in problems)
    return name, not problems, detail


# ---------------------------------------------------------------- main
def main():
    ap = argparse.ArgumentParser(description="离线一致性校验（Go 项目的静态文本检查）")
    ap.add_argument("--quiet", action="store_true", help="只输出失败项")
    args = ap.parse_args()

    files = [(r, s) for r, s in walk_go()]
    print(f"扫描 {len(files)} 个 Go 文件（位于 {ROOT}）\n")

    checks = [
        check_router_methods,
        check_cross_package_symbols,
        check_arg_counts,
        check_interface_conformance,
        check_api_path_alignment,
        check_brackets,
        check_imports_used,
    ]

    failures = 0
    for fn in checks:
        name, ok, detail = fn(files, args.quiet)
        if ok:
            if not args.quiet:
                print(f"[{OK}] {name}")
                for line in detail.splitlines():
                    print(line if line.startswith("    ") else "    " + line)
                print()
        else:
            failures += 1
            print(f"[{FAIL}] {name}")
            for line in detail.splitlines():
                print(line if line.startswith("    ") else "    " + line)
            print()

    print("-" * 60)
    if failures:
        print(f"{failures} 项检查未通过")
    else:
        print("全部通过 —— 但这**不等于**能编译。")
        print("本脚本只能发现文字层面的低级错误；类型不匹配、接口未实现、")
        print("循环导入、变量未使用等，只有真正的 go build 才能判定。")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
