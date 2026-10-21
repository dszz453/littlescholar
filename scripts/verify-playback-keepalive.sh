#!/bin/sh
# 校验「息屏播放」这条链路**真的接上了**。
#
# 为什么单独一个脚本：这条链路的失效方式**全是静默的**，而且只在真机息屏时
# 才暴露。2026-10-16 那次的真实故障就是最好的例子 ——
# `MediaPlaybackService` 写好了、清单声明了、唤醒锁也写了，
# 但 `start()` / `stop()` **全项目零调用**，于是整套东西是一段完整的死代码：
#
#   · 没有前台服务 → App 一进后台进程优先级就掉下去，可能被回收 / 冻结；
#   · 唤醒锁随之**永远不会被 acquire**（服务都没起，谁来拿锁），
#     `WAKE_LOCK` 权限白声明；
#   · 孩子看到的是「息屏播完一集不切下一集，点开提示网络错误」。
#
# 而 Gradle 全绿、APK 装得上、亮屏播放一切正常 —— 靠人眼根本发现不了。
# 所以这几条必须在**产物**上断言。
#
# 用法：scripts/verify-playback-keepalive.sh [apk 路径]
#   默认 android/app/build/outputs/apk/release/app-release.apk
# 依赖：unzip、grep

set -eu

APK="${1:-android/app/build/outputs/apk/release/app-release.apk}"

fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1" >&2; fail=1; }

if [ ! -f "$APK" ]; then
	echo "APK 不存在：$APK" >&2
	echo "先跑：(cd android && ./gradlew assembleRelease)" >&2
	exit 1
fi
echo "校验息屏播放保活链：$APK"

WORK=$(mktemp -d)
# ⚠️ 清理绝不能影响退出码：POSIX sh 的退出码由"最后一条命令"决定，
# 而 EXIT trap 就在最后 —— 清理失败会让"校验全绿"也返回非 0。
cleanup() { rm -rf "$WORK" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

unzip -o -q "$APK" 'classes*.dex' -d "$WORK"

# ⚠️ `grep -c` 对二进制文件恒返回 1（整个文件算一行）→ 必须 `grep -ao | wc -l`。
# case 来数。
count() { grep -ao "$1" "$WORK"/classes*.dex 2>/dev/null | wc -l | tr -d ' '; }

# ---- 1. 桥真的挂上了（网页 → 原生）----
#
# `KidNative` 是 addJavascriptInterface 的名字。没有它，网页里
# `window.KidNative.setPlaying(...)` 恒抛异常（被 try 住）→ 服务永远不起来。
if [ "$(count KidNative)" -ge 1 ]; then
	ok "JS 桥已挂载（KidNative）"
else
	bad "dex 里找不到 KidNative —— addJavascriptInterface 没挂，服务永远不会被启动"
fi

# ---- 2. 注入脚本真的在包里 ----
#
# `__lsPlayWatch` 是注入脚本的幂等标记。它不在 = 播放页收不到监听，
# 于是"页面在播"这件事从来没人告诉外壳。
if [ "$(count __lsPlayWatch)" -ge 1 ]; then
	ok "播放监听脚本已打进包（__lsPlayWatch）"
else
	bad "dex 里找不到 __lsPlayWatch —— 没有注入播放监听，保活不会启动"
fi

# ---- 3. 两道锁真的会被拿（不是只声明权限）----
#
# 标签字符串是 newWakeLock / createWifiLock 的实参，只可能在"真的调用"时出现。
# 这一条专治"权限声明了却没人用"。
if [ "$(count 'littlescholar:playback')" -ge 1 ]; then
	ok "CPU 唤醒锁真的会被 acquire（littlescholar:playback）"
else
	bad "找不到 littlescholar:playback —— 唤醒锁没被使用，息屏后 CPU 会睡下去"
fi
if [ "$(count 'littlescholar:wifi')" -ge 1 ]; then
	ok "WiFi 高性能锁真的会被 acquire（littlescholar:wifi）"
else
	bad "找不到 littlescholar:wifi —— 息屏几分钟后 WLAN 进省电模式，取流会超时"
fi

# ---- 4. 原生音频（wma）这条链真的在包里 ----
#
# wma 这类格式 WebView 解不了，只能由壳里的 libVLC 播。三处失效都是静默的：
#   · libvlc.so 没打进包 → 点开 wma 没声音（构建照样绿）；
#   · 没有 nativeAudio 桥 → 页面永远走不到原生模式，退回服务端转码
#     （一本绘本等 20 秒以上，接口却完全正常）；
#   · 原生模式下仍允许"后台接管" → 页面把原文件交给 MediaPlayer，
#     支持 wma 的机型会与 VLC **同时出声**。
#
# 第三条的指纹取那句诊断文案：它只在"原生模式拒绝接管"这条分支里出现，
# 文案本身也是家长看诊断页时"为什么没接管"的唯一线索。
if unzip -l "$APK" | awk '{print $4}' | grep -qx 'lib/arm64-v8a/libvlc.so'; then
	ok "lib/arm64-v8a/libvlc.so 已打进 APK（原生解码 wma 靠它）"
else
	bad "lib/arm64-v8a/libvlc.so 缺失 —— wma 会没声音（libvlc 依赖没进包）"
fi
if [ "$(count nativeAudio)" -ge 1 ]; then
	ok "桥上有 nativeAudio（页面据此切到原生模式）"
else
	bad "dex 里没有 nativeAudio —— 页面永远走不到原生模式，wma 只能等服务端转码"
fi
if [ "$(count '原生模式不需要接管')" -ge 1 ]; then
	ok "原生模式下拒绝后台接管（不会与 VLC 抢出声）"
else
	bad "找不到「原生模式不需要接管」—— takeOver 没有拦 nativeMode，切后台可能双声道"
fi
if grep -aq 'org/videolan/libvlc/LibVLC' "$WORK"/classes*.dex; then
	ok "VlcAudioEngine 真的进包了（dex 引用了 org/videolan/libvlc/LibVLC）"
else
	bad "dex 里没有 libvlc 的类引用 —— 页面调 nativeAudio 也没人接"
fi

# ---- 5. 清单（AXML，字符串是 UTF-16LE）----
#
# ⚠️ 直接 `grep 'WAKE_LOCK'` 在 AXML 上**恒不命中**（每个 ASCII 字符后面跟着
#    一个 0 字节），看起来像"权限被删了"。必须先 `tr -d '\000'` 再比。
MF=""
if ! MF=$(unzip -p "$APK" AndroidManifest.xml 2>/dev/null | tr -d '\000'); then
	MF=""
fi
if printf '%s' "$MF" | grep -q 'WAKE_LOCK'; then
	ok "清单里有 WAKE_LOCK 权限（CPU 锁与 WiFi 锁都靠它）"
else
	bad "清单里没有 WAKE_LOCK —— 两道锁都会静默失败"
fi
if printf '%s' "$MF" | grep -q 'foregroundServiceType'; then
	ok "前台服务声明了 foregroundServiceType（Android 14+ 起必需）"
else
	bad "缺少 foregroundServiceType —— Android 14+ 上 startForeground 会抛异常"
fi
if printf '%s' "$MF" | grep -q 'MediaPlaybackService'; then
	ok "前台服务已在清单里注册"
else
	bad "清单里没有 MediaPlaybackService —— 服务根本起不来"
fi

echo
if [ "$fail" = 0 ]; then
	echo "息屏播放保活链校验通过"
else
	echo "息屏播放保活链校验未通过" >&2
	exit 1
fi
