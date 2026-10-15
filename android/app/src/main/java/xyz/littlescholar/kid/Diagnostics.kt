package xyz.littlescholar.kid

import android.content.Context
import android.os.Build
import android.webkit.WebSettings
import android.webkit.WebView
import java.text.SimpleDateFormat
import java.util.ArrayDeque
import java.util.Date
import java.util.Locale

/**
 * 客户端诊断收集器。
 *
 * 为什么需要它：孩子端连续几轮反馈都只有「不行」三个字，拿不到报错文本就只能猜 ——
 * 上一轮的真因（旧 Service Worker 把 `index.html` 缓存住，客户端一直在跑旧 JS）
 * 就是靠猜才拖了三轮。这个类把三类信息攒在内存里（环形缓冲，最多 400 条）：
 *
 *   1. **环境**：WebView 内核版本、UA、Android 版本、机型、当前地址
 *   2. **运行时**：页面 console 的 error/warn、未捕获异常
 *   3. **资源**：子资源加载失败、HTTP 4xx/5xx、SSL 证书错误
 *   4. **页面自报家门**：实际在跑哪几个 chunk —— 判断「是不是最新版」的关键证据
 *
 * 家长长按屏幕 →「诊断信息」即可看到全文，点「复制全部」发出来。
 *
 * 之所以放在**原生侧**而不是网页里：网页本身可能被旧 Service Worker 缓存住，
 * 连诊断代码一起下不去 —— 而「新代码下不去」恰恰是最需要诊断的那件事。
 */
object Diagnostics {

    private const val MAX_LINES = 400

    private val buf = ArrayDeque<String>()
    private val timeFmt = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)
    private val stampFmt = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US)

    /** 环境信息只在首次调用时采集一次。 */
    private var env: String = ""

    /** 启动时调用一次，采集环境。幂等。 */
    fun start(ctx: Context) {
        if (env.isEmpty()) env = buildEnv(ctx)
    }

    /** 上一条的指纹与重复次数 —— 用来把刷屏的同类报错折叠成一行。 */
    private var lastKey = ""
    private var lastCount = 0

    /**
     * 记一条事件。tag 用中文短标签，方便家长一眼看出类型。
     *
     * **连续重复的内容会被折叠**（显示 `(×12)`）：console 的报错常常一刷几百条，
     * 不做折叠的话环形缓冲会被同类噪音占满，真正有用的前几条反而被挤掉 ——
     * 而"页面刚加载时报了什么错"通常才是关键。
     */
    fun add(tag: String, msg: String) {
        val text = oneLine(msg)
        val key = tag + "|" + text
        if (key == lastKey) {
            lastCount++
            if (buf.isNotEmpty()) buf.removeLast()
            buf.addLast(timeFmt.format(Date()) + " [" + tag + "] " + text + "  (×" + lastCount + ")")
            return
        }
        lastKey = key
        lastCount = 1
        if (buf.size >= MAX_LINES) buf.removeFirst()
        buf.addLast(timeFmt.format(Date()) + " [" + tag + "] " + text)
    }

    fun clear() {
        buf.clear()
        lastKey = ""
        lastCount = 0
        add("诊断", "已清空历史事件")
    }

    /** 满文导出（环境 + 全部事件），给「复制全部」用。 */
    fun dump(ctx: Context): String {
        start(ctx)
        val sb = StringBuilder()
        sb.append("小书童 · 客户端诊断\n")
        sb.append("导出时间: ").append(stampFmt.format(Date())).append('\n')
        sb.append("--- 环境 ---\n").append(env)
        sb.append("--- 事件 (").append(buf.size).append(" 条) ---\n")
        if (buf.isEmpty()) {
            sb.append("(暂无，可点「刷新快照」重新采集)\n")
        } else {
            buf.forEach { sb.append(it).append('\n') }
        }
        return sb.toString()
    }

    private fun buildEnv(ctx: Context): String {
        val sb = StringBuilder()
        sb.append("App: ").append(BuildConfig.VERSION_NAME)
            .append(" (").append(BuildConfig.VERSION_CODE).append(")\n")
        sb.append("Android: ").append(Build.VERSION.RELEASE)
            .append(" (API ").append(Build.VERSION.SDK_INT).append(")\n")
        sb.append("机型: ").append(Build.MANUFACTURER).append(' ').append(Build.MODEL).append('\n')
        sb.append("ABI: ").append(Build.SUPPORTED_ABIS.joinToString(",")).append('\n')

        // 内核版本 —— 排查「是不是内核太老」时最想看的那个数字。
        // getCurrentWebViewPackage 是 API 26 起的方法，低版本只能靠 UA 猜。
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val pkg = WebView.getCurrentWebViewPackage()
            if (pkg != null) {
                sb.append("内核: ").append(pkg.packageName)
                    .append(' ').append(pkg.versionName).append('\n')
            } else {
                sb.append("内核: (取不到)\n")
            }
        } else {
            sb.append("内核: (API<26，请看下面 UA 里的 Chrome/xx)\n")
        }
        sb.append("UA: ")
            .append(runCatching { WebSettings.getDefaultUserAgent(ctx) }.getOrDefault("?"))
            .append('\n')
        sb.append("地址: ").append(ServerConfig.get(ctx).ifEmpty { "(未设置)" }).append('\n')
        return sb.toString()
    }

    /** 事件里的换行会把一条拆成多行、干扰阅读，压成一行。 */
    private fun oneLine(s: String): String =
        s.replace('\n', ' ').replace('\r', ' ').trim()

    /**
     * 让网页自报家门：跑的是哪几个 chunk、Service Worker 是谁、localStorage 有哪些键。
     *
     * **这段必须是同步的** —— `evaluateJavascript` 不会等 Promise，
     * 写成 async 只会拿到 `{}`。所以这里只用同步 API。
     */
    val SNAPSHOT_JS: String = """
        (function () {
          try {
            var scripts = [];
            var tags = document.querySelectorAll('script[src]');
            for (var i = 0; i < tags.length; i++) {
              scripts.push(String(tags[i].getAttribute('src')));
            }
            var ctrl = navigator.serviceWorker && navigator.serviceWorker.controller;
            return JSON.stringify({
              url: location.href,
              title: document.title,
              scripts: scripts,
              sw: ctrl ? ctrl.scriptURL : '(无 SW 控制器)',
              ls: Object.keys(localStorage),
              readyState: document.readyState
            });
          } catch (e) {
            return 'ERR:' + e.message;
          }
        })()
    """.trimIndent()

    /**
     * 注销全部 Service Worker 并清空 CacheStorage。
     *
     * 刻意**不动 localStorage** —— 登录令牌在里面，清了孩子就要重新登录。
     * 清完由调用方 reload，让页面重新从网络取 HTML 并注册新 SW。
     */
    val HEAL_JS: String = """
        (async function () {
          var out = { sw: 0, cache: 0, err: '' };
          try {
            var regs = await navigator.serviceWorker.getRegistrations();
            for (var i = 0; i < regs.length; i++) { await regs[i].unregister(); out.sw++; }
          } catch (e) { out.err += 'sw:' + e.message + '; '; }
          try {
            var ks = await caches.keys();
            for (var j = 0; j < ks.length; j++) { await caches.delete(ks[j]); out.cache++; }
          } catch (e) { out.err += 'cache:' + e.message + '; '; }
          window.__kidHeal = JSON.stringify(out);
          return window.__kidHeal;
        })()
    """.trimIndent()
}
