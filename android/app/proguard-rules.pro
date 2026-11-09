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

# ============ 原生解码 ============
#
# 1.7.8 起不再引 libVLC（改用系统 android.media.MediaPlayer），
# 这里原先那组 `-keep class org.videolan.**` 随之删除。
#
# 留一句教训：凡是**被 native 用 GetMethodID 按名字回查**的 Java 成员，
# R8 都会当成死代码删掉/改名，然后第一次回调就 JNI abort、进程直接退出。
# 1.6.0 的 libVLC 踩过一次（dispatchEventFromNative 被删 → 点开 wma 闪退）。
# 以后再加任何带 .so 的库，先确认它的 aar 里有没有自带 proguard 规则。

# 去掉日志（release）
-assumenosideeffects class android.util.Log {
    public static *** d(...);
    public static *** v(...);
    public static *** i(...);
}
