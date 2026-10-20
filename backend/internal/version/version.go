// Package version 保管小书童**服务端**的版本信息。
//
// 规矩（用户 2026-10-16 定）：**每改动一次，版本号升 0.1**。
// 与安卓端同一套 major.minor.patch 语义（1.2.0 → 1.3.0 → …），
// versionCode 的公式（10000×major + 100×minor + patch）只对 APK 有意义，
// 服务端不涉及。
//
// # 为什么要有这个包
//
// 在此之前「版本」在代码里有两处硬编码：
//
//   - `handler.SystemHandler.Status` 回给后台的 `"version": "1.0.0"`
//   - `main.printBanner` 印在启动横幅上的 `v1.0.0`
//
// 两者都从项目第一个提交（ec9c10f）起一次也没动过。于是后台「系统状态」
// 里那行版本号从上线第一天就是假的 —— 而"线上到底跑的哪一版"恰恰是
// 排查问题时第一个要回答的问题（本次就是用户看着后台问出来的）。
//
// 现在全项目只有这一个来源，**升版本只改 Version 这一行**。
//
// # 构建期注入（可选）
//
//	go build -ldflags "-X github.com/littlescholar/core/internal/version.Version=1.8.0 \
//	                   -X github.com/littlescholar/core/internal/version.Commit=$(git rev-parse HEAD)" \
//	         -o core-server ./cmd/server
//
// 不注入也能跑，用下面的默认值；注入的意义是让「代码里的版本」
// 和「构建出来的二进制」永远对得上（CI 会带上 GIT_SHA）。
package version

import "fmt"

// Version 服务端版本号。**升版本只改这一行。**
//
// 版本历史（倒序，最近的在上）：
//
//	1.15.0 **AI 批量写字幕**：把「标题 + 目录名」交给大模型，让它复原原文并给中文
//	       对照，再按音频的停顿位置算时间轴（ffmpeg silencedetect），生成
//	       `.vtt`（原文/译文各一行）落进 resource_subtitles（origin='ai'）
//	       和 data/subtitle/ai/。后台「AI 配置 → 批量字幕」可勾选文件排队，
//	       并按每天时段（可跨零点，默认 22:00-06:00）自动消费队列。
//	       目录名是重要匹配要素：分级读物的套系/级别只写在目录上。
//	       默认不覆盖已有字幕；模型"不确定"就记 skipped，绝不编内容。
//	1.14.0 **撤除语音听写（whisper）**：这台 NAS（Celeron J1900，无 AVX/F16C）
//	       上只有 0.5 倍实时 —— 3 分钟绘本要转 6~7 分钟，还要独占一个核。
//	       代价（模型 30MB 进镜像、启动算力）与收益不匹配，整条链路删掉：
//	       镜像不再编 whisper.cpp、不再下模型、不再装 libstdc++；
//	       配置项 SUBTITLE_ASR_* / SUBTITLE_WHISPER_* 一并作废（留着不报错，只是没人读）。
//	       字幕回到三道来源：同目录 → 内嵌轨 → 联网匹配；都没有就回 204。
//	1.13.0 听写提速：模型换 tiny.en-q5_1（量化），解码改贪心 + 关温度回退。
//	       实测这台无 AVX/F16C 的 J1900 上：base.en(F16) 跑不动、
//	       tiny.en(F16) 100s/3s 音频、tiny.en-q5_1 62s，再加 -nf 到 52s
//	1.12.0 wma/ape 这类音频"点开放不出来"：音频转码的判据改看扩展名，
//	       不再要求 media_type==audio（存量行还写着 video），
//	       且 ext 为空时先 ffprobe 探一次再判类型
//	1.11.0 听写镜像去掉 AVX 指令集：线上 NAS 是 Celeron J1900（无 AVX），
//	       1.10.0 的 whisper-cli 一 init 就 Illegal instruction；模型改 base.en
//	1.10.0 字幕听写改用本机 whisper.cpp（镜像内置 whisper-cli + ggml 模型）：
//	       云端网关根本没有转写模型，四个 whisper 候选名全灭
//	1.8.0  版本号终于变成真值：后台「系统状态」不再硬编码 1.0.0，
//	       /api/health 也回带版本与构建信息
//	1.0.0  项目初始版本（此前的 1.0.0 是硬编码，不代表任何一次改动）
var Version = "1.15.0"

// Commit 构建时的 git 提交全哈希。可空 —— 本地 `go run` 就没有。
var Commit = ""

// BuildTime 构建时刻（UTC，RFC3339）。可空。
var BuildTime = ""

// ShortCommit 返回 7 位短哈希；没有构建信息时返回空串。
//
// 用 7 位而不是 40 位：这一串是给人看的（后台状态卡、启动横幅、健康检查），
// 用途是和 `git log` 对眼，太长反而看不清。git 自己也用 7 位。
func ShortCommit() string {
	if len(Commit) >= 7 {
		return Commit[:7]
	}
	return Commit
}

// Full 返回给人看的完整形态：
//
//	1.8.0 (a1b2c3d, 2026-10-16T06:41:00Z)
//
// 缺少构建信息时逐级退化，最小就是 Version 本身 ——
// 不会出现 "1.8.0 (, )" 这种半截括号。
func Full() string {
	switch {
	case ShortCommit() != "" && BuildTime != "":
		return fmt.Sprintf("%s (%s, %s)", Version, ShortCommit(), BuildTime)
	case ShortCommit() != "":
		return fmt.Sprintf("%s (%s)", Version, ShortCommit())
	case BuildTime != "":
		return fmt.Sprintf("%s (built %s)", Version, BuildTime)
	default:
		return Version
	}
}
