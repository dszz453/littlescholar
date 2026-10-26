# ============ JS 桥必须原样保留 ============
#
# `window.KidNative` 由 addJavascriptInterface 挂上，它上面的方法
# （setPlaying / mediaState / takeOver / nativeAudio / giveBack）
# **只被 JavaScript 调用** —— 从 R8 的静态分析看没有任何 Java 调用者，
# 于是 release 包（minifyEnabled=true）里它们会被当死代码删掉或改名。
#
# 这条以前是注释着的（当时还没有 @JavascriptInterface），后来加了桥
# 却忘了打开。这类规则缺了不会编译报错、页面也不会白屏，只会**静默少能力**
# —— 和 libVLC 那批 JNI 回调被删是同一类坑（那个更狠，直接闪退）。
-keep class xyz.littlescholar.kid.MainActivity$PlaybackBridge { *; }
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}

# Kotlin 元数据
-keep class kotlin.Metadata { *; }

# ============ libVLC（wma / ape 原生解码）============
#
# ⚠️ 这一条不能用"省体积"的思路去缩：libvlcjni.so 是用 **GetMethodID 按方法名**
# 回查 Java 的，而 `libvlc-all` 的 aar 里**没有自带 proguard 规则**
# （2026-10-21 解开 aar 确认：只有 classes.jar / jni / R.txt，没有 proguard.txt）。
# 于是 R8 会把这些"只被 native 调用"的成员当成死代码：
#
#   VLCObject.dispatchEventFromNative(int,long,long,float,String)  → 直接删掉
#   VLCObject.onReleaseNative()                                    → 改名成 b
#   LibVLC.onEventNative(int,long,long,float,String)               → 删掉/改名
#   Media.createAudioTrackFromNative(...) / MediaPlayer.createTitleFromNative(...)
#
# 结果：第一次收到 VLC 事件时 JNI 拿到 null 方法句柄 → native 层 abort →
# **点开 wma 客户端立刻退出**（日志里是 JNI DETECTED ERROR IN APPLICATION /
# SIGABRT）。1.6.0 与 PRO 2.6.0 都踩了这一个坑。
#
# 改完后 `build/outputs/mapping/release/mapping.txt` 里必须能看到
#   void dispatchEventFromNative(...) -> dispatchEventFromNative
# 这类"映射到自身"的行；verify 脚本就靠它防止回退。
-keep class org.videolan.** { *; }
-keep interface org.videolan.** { *; }
-dontwarn org.videolan.**

# 去掉日志（release）
-assumenosideeffects class android.util.Log {
    public static *** d(...);
    public static *** v(...);
    public static *** i(...);
}
