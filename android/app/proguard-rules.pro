# WebView + JS 接口不需要额外规则；
# 但开启 minify 后，JS 注入桥接类若将来新增需在此保留。

# 保留 JS 桥（当前无 @JavascriptInterface，占位以防将来添加）
# -keepclassmembers class xyz.littlescholar.kid.** {
#     @android.webkit.JavascriptInterface <methods>;
# }

# Kotlin 元数据
-keep class kotlin.Metadata { *; }

# 去掉日志（release）
-assumenosideeffects class android.util.Log {
    public static *** d(...);
    public static *** v(...);
    public static *** i(...);
}
