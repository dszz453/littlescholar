package xyz.littlescholar.kid

import android.content.Context

/**
 * 孩子端服务器地址的本地存储。
 *
 * 为什么做成「应用内可填」而不是打包时写死：
 *
 *   同一个 APK 要能装到家里任何一台平板/手机上，而地址是会变的 ——
 *   内网 IP 随路由器分配变、公网域名或端口也可能换。写死意味着
 *   每换一次地址就要重新打包、重新分发、重新安装，家长很难自己完成。
 *
 *   做成可填之后：装完首次打开填一次即可；换地址时长按屏幕就能改，
 *   不用重装。
 *
 * `BuildConfig.SITE_URL`（构建时 `-PsiteUrl=` 可覆盖）不再决定"用哪个地址"，
 * 只作为**首次打开时的预填建议**。
 */
object ServerConfig {

    private const val PREF = "littlescholar"
    private const val KEY = "site_url"

    /** 首次预填用的默认地址（构建期注入）。 */
    val default: String get() = BuildConfig.SITE_URL

    /** 已保存的地址；没有则返回空串。 */
    fun get(ctx: Context): String =
        ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE)
            .getString(KEY, "")
            ?.trim()
            .orEmpty()

    /** 保存并返回归一化后的地址。 */
    fun set(ctx: Context, raw: String): String {
        val url = normalize(raw)
        if (url.isNotEmpty()) {
            ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE)
                .edit().putString(KEY, url).apply()
        }
        return url
    }

    /** 清掉保存的地址（下次打开重新进入设置页）。 */
    fun clear(ctx: Context) {
        ctx.getSharedPreferences(PREF, Context.MODE_PRIVATE).edit().remove(KEY).apply()
    }

    /**
     * 归一化用户输入。家长十有八九不会老老实实带 `http://`，也没必要要求他带：
     *
     *   "192.168.31.220:8093"        → "http://192.168.31.220:8093"
     *   "fn.19860819.xyz:8093/"      → "http://fn.19860819.xyz:8093"
     *
     * 结尾斜杠必须去掉 —— 站内/站外链接的判断是 `url.startsWith(siteUrl)`，
     * 留着斜杠会让"站内首页"被判成站外，一点就跳出到浏览器。
     */
    fun normalize(raw: String): String {
        var s = raw.trim()
        if (s.isEmpty()) return ""
        if (!s.startsWith("http://") && !s.startsWith("https://")) {
            s = "http://$s"
        }
        return s.trimEnd('/')
    }
}
