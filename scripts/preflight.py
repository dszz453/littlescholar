#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
部署前体检（deploy preflight）

为什么需要这个
--------------
项目里有两类问题：一类是"本机缺工具"（比如没装 Go），换台机器就好了；
另一类是"仓库里缺文件"（比如没提交 go.sum），**换任何机器都好不了**。

第二类最坑：它不会在本地开发时暴露（因为本地压根不跑构建），
一直潜伏到部署那一刻才炸，而且报错信息往往指向别的地方
（比如"校验和不匹配"，而不是"你少了 go.sum"）。

本脚本把这两个已发现的阻断项固化成自动检查，避免以后又忘。

用法
----
    python3 scripts/preflight.py          # 在项目根目录执行
    python3 scripts/preflight.py --fix-hint  # 附带修复命令（默认就带）

退出码：0 全部就绪；1 存在阻断项。
"""

import argparse
import os
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RED = "\033[31m" if sys.stdout.isatty() else ""
YEL = "\033[33m" if sys.stdout.isatty() else ""
GRN = "\033[32m" if sys.stdout.isatty() else ""
RST = "\033[0m" if sys.stdout.isatty() else ""

blockers = []   # 必须修，否则构建必失败
warnings = []   # 建议修
notes = []      # 仅供参考


def check_go_sum():
    """go.sum 是否存在；以及 go.mod 里有无 indirect 块。"""
    gomod = os.path.join(ROOT, "backend", "go.mod")
    gosum = os.path.join(ROOT, "backend", "go.sum")

    if not os.path.isfile(gomod):
        blockers.append(("backend/go.mod 不存在", "项目结构不完整"))
        return

    if not os.path.isfile(gosum):
        blockers.append((
            "backend/go.sum 缺失",
            "Dockerfile 里 `go mod tidy && go build` 要在构建容器里现场解析"
            "整棵依赖树，网络抖动或版本漂移都会导致构建失败。\n"
            "    修复：cd backend && go mod tidy  然后把 go.sum 提交进仓库"
        ))
        return

    # go.sum 存在，但 go.mod 里一条 indirect 都没有 —— 可疑
    with open(gomod, encoding="utf-8") as fh:
        content = fh.read()
    if "// indirect" not in content:
        warnings.append((
            "backend/go.mod 里没有任何 `// indirect` 依赖",
            "11 个直接依赖每个都会拖入数十个间接依赖，正常 tidy 过后"
            "必然出现 indirect 块。建议重跑一次 go mod tidy 并对比。"
        ))
    else:
        notes.append("go.sum 存在，go.mod 含 indirect 块")


def check_gradle_wrapper():
    """gradle wrapper 是否齐备；以及 wrapper 里的 Gradle 版本是否满足 AGP 要求。"""
    aw = os.path.join(ROOT, "android")
    if not os.path.isdir(aw):
        notes.append("无 android/ 目录，跳过")
        return

    needed = {
        "gradlew": "Unix 启动脚本",
        "gradlew.bat": "Windows 启动脚本",
        os.path.join("gradle", "wrapper", "gradle-wrapper.jar"): "wrapper jar",
        os.path.join("gradle", "wrapper", "gradle-wrapper.properties"): "wrapper 配置",
    }
    missing = [f for f in needed if not os.path.isfile(os.path.join(aw, f))]
    if missing:
        blockers.append((
            f"android/ 缺少 gradle wrapper：{', '.join(missing)}",
            "android/README.md 给的命令是 `./gradlew assembleRelease`，"
            "但 gradlew 不存在，命令必然报 No such file or directory。\n"
            "    修复：cd android && gradle wrapper --gradle-distribution-url "
            "<镜像>/gradle-8.9-bin.zip --distribution-type bin"
            "（需 Gradle 8.9；用 --gradle-version 会去连 services.gradle.org"
            "验证 URL，国内会超时），然后把生成的文件提交进仓库"
        ))
        return

    notes.append("gradle wrapper 齐备")
    _check_agp_gradle_compat(aw)


# AGP → 要求的最低 Gradle 版本（官方兼容矩阵，按需补充）
AGP_MIN_GRADLE = {
    "8.0": "8.0", "8.1": "8.0", "8.2": "8.2", "8.3": "8.4",
    "8.4": "8.6", "8.5": "8.7", "8.6": "8.7", "8.7": "8.9",
}


def _parse_ver(s):
    """把 "8.5.2" 解析成 (8, 5, 2)；解析失败返回 ()。"""
    parts = []
    for seg in str(s).split("."):
        num = ""
        for ch in seg:
            if ch.isdigit():
                num += ch
            else:
                break
        if not num:
            break
        parts.append(int(num))
    return tuple(parts)


def _check_agp_gradle_compat(android_dir):
    """校验 wrapper 里的 Gradle 版本是否满足 build.gradle 里 AGP 的最低要求。

    这个检查来自一次真实的构建失败：AGP 8.5.2 要求 Gradle >= 8.7，
    而文档和 wrapper 都写的 8.5，构建报
    "Minimum supported Gradle version is 8.7"。
    纯静态盘点看不出这类问题，但版本号本身是可静态比对的 —— 所以固化下来。
    """
    root_build = os.path.join(android_dir, "build.gradle")
    props = os.path.join(android_dir, "gradle", "wrapper",
                         "gradle-wrapper.properties")
    if not (os.path.isfile(root_build) and os.path.isfile(props)):
        return

    # 提取 AGP 版本：classpath 'com.android.tools.build:gradle:8.5.2'
    agp = None
    with open(root_build, encoding="utf-8") as fh:
        for line in fh:
            if "com.android.tools.build:gradle:" in line:
                tail = line.split("com.android.tools.build:gradle:", 1)[1]
                agp = tail.strip().strip("'\"").rstrip("'\"")
                break
    if not agp:
        return

    # 提取 wrapper 的 Gradle 版本：.../gradle-8.9-bin.zip
    gradle_ver = None
    with open(props, encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("distributionUrl") and "gradle-" in line:
                tail = line.split("gradle-", 1)[1]
                gradle_ver = tail.split("-", 1)[0].strip()
                break
    if not gradle_ver:
        return

    agp_major_minor = ".".join(agp.split(".")[:2])
    need = AGP_MIN_GRADLE.get(agp_major_minor)
    if not need:
        notes.append(f"AGP {agp} 的 Gradle 下限不在表内，跳过版本校验")
        return

    if _parse_ver(gradle_ver) < _parse_ver(need):
        blockers.append((
            f"AGP {agp} 要求 Gradle >= {need}，但 wrapper 里是 {gradle_ver}",
            "构建会在 com.android.internal.version-check 这一步直接失败，"
            "报 `Minimum supported Gradle version is {0}`。\n"
            "    修复：改 android/gradle/wrapper/gradle-wrapper.properties 的 "
            "distributionUrl 指向 gradle-{0}-bin.zip".format(need)
        ))
    else:
        notes.append(f"AGP {agp} ↔ Gradle {gradle_ver} 版本匹配（需 >= {need}）")


def check_toolchain():
    """本机工具链 —— 缺了不算阻断（可能在别的机器上部署），只做提示。"""
    tools = {
        "go": "编译后端",
        "docker": "本地跑 docker compose",
        "ffmpeg": "本地验证转码参数",
        "node": "构建前端",
        "java": "构建安卓",
    }
    have, lack = [], []
    for t, why in tools.items():
        if shutil.which(t):
            have.append(t)
        else:
            lack.append(f"{t}（{why}）")

    # PATH 里没有，但本机存了一份免安装（绿色版）—— 一样能用。
    # 这里逐个工具单独处理，不要写成通用循环：
    # 每类工具的"安装形态"和检索路径完全不同，混在一起查会漏。
    for tool, finder in (("go", find_local_go), ("java", find_local_java)):
        found = finder()
        if found and any(x.startswith(tool + "（") for x in lack):
            have.append(f"{tool}（免安装副本: {found}）")
            lack = [x for x in lack if not x.startswith(tool + "（")]

    if have:
        notes.append("本机已有：" + ", ".join(have))
    if lack:
        warnings.append((
            "本机缺少：" + "; ".join(lack),
            "在本机无法完成对应步骤，但不影响把代码拿到别的机器上构建"
        ))


def find_local_go():
    """找一份免安装 Go（zip 解压即用的那种），找不到返回 None。

    检索顺序：环境变量 > 常见盘符根目录。这是为了适配
    "用户机器上没装 go，但项目里放了一份绿色版" 的情况。
    """
    for env in ("GO_LOCAL_ROOT", "GOROOT"):
        p = os.environ.get(env)
        if p:
            exe = os.path.join(p, "bin", "go.exe" if os.name == "nt" else "go")
            if os.path.isfile(exe):
                return exe

    for drive in ("D:", "C:", "E:", "F:"):
        for sub in ("tools/gopath", "tools/go", "go"):
            exe = os.path.join(drive + os.sep, sub, "bin",
                               "go.exe" if os.name == "nt" else "go")
            if os.path.isfile(exe):
                return exe
    return None


def find_local_java():
    """找一份免安装 JDK，找不到返回 None。

    和 find_local_go 的区别：Go 解压后目录结构固定（<root>/bin/go），
    而 JDK 解压后多一层带版本号的目录（<root>/jdk-17.0.20.1+1/bin/java），
    所以这里要多扫一层子目录。
    """
    def ok(root):
        exe = os.path.join(root, "bin", "java.exe" if os.name == "nt" else "java")
        return exe if os.path.isfile(exe) else None

    for env in ("JAVA_LOCAL_ROOT", "JAVA_HOME"):
        p = os.environ.get(env)
        if p:
            hit = ok(p)
            if hit:
                return hit

    for drive in ("D:", "C:", "E:", "F:"):
        for sub in ("tools/jdk", "tools/java", "jdk"):
            base = os.path.join(drive + os.sep, sub)
            hit = ok(base)
            if hit:
                return hit
            # 再往里扫一层：jdk-17.0.20.1+1 / jdk-17 之类的版本目录
            if os.path.isdir(base):
                try:
                    for name in sorted(os.listdir(base), reverse=True):
                        hit = ok(os.path.join(base, name))
                        if hit:
                            return hit
                except OSError:
                    pass
    return None


def check_config_files():
    """几个关键配置/编排文件的存在性。"""
    for rel, why in [
        ("docker-compose.yml", "服务编排"),
        ("backend/Dockerfile", "后端镜像定义"),
        ("backend/scripts/lint_offline.py", "离线校验器"),
        (".github/workflows/lint-and-build.yml", "CI"),
    ]:
        if not os.path.isfile(os.path.join(ROOT, rel)):
            warnings.append((f"{rel} 缺失", why))


def main():
    ap = argparse.ArgumentParser(description="部署前体检")
    ap.add_argument("--quiet", action="store_true", help="只输出阻断项")
    args = ap.parse_args()

    check_go_sum()
    check_gradle_wrapper()
    check_config_files()
    check_toolchain()

    if not args.quiet:
        print(f"体检目录: {ROOT}\n")

    for title, detail in warnings:
        if not args.quiet:
            print(f"{YEL}[警告]{RST} {title}")
            print(f"    {detail}\n")

    for title, detail in blockers:
        print(f"{RED}[阻断]{RST} {title}")
        print(f"    {detail}\n")

    if not args.quiet:
        for n in notes:
            print(f"{GRN}[信息]{RST} {n}")

    print()
    if blockers:
        print(f"{RED}发现 {len(blockers)} 个构建阻断项 —— 不处理的话，"
              f"在任何机器上构建都会失败。{RST}")
    else:
        print(f"{GRN}无构建阻断项。{RST}")
        print("注意：这只说明「仓库该有的文件都在」，")
        print("不代表代码能编译 —— 那需要真的跑一次 go build / gradle build。")
    return 1 if blockers else 0


if __name__ == "__main__":
    sys.exit(main())
