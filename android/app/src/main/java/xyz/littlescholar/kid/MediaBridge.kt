package xyz.littlescholar.kid

import android.webkit.WebView

/**
 * 原生 → 网页 的最小通道。
 *
 * 为什么需要它：通知栏 / 锁屏 / 车机上的「上一首、下一首、播放暂停」必须能
 * 作用到 WebView 里的播放器，而前台服务拿不到 Activity 里的 WebView 引用。
 *
 * 设计上刻意做得很小：
 *  - 持 **WeakReference**，Activity 销毁后不会把 WebView 拖住；
 *  - 网页没注册桥（不在播放页）时 `call` 静默返回 —— 通知按钮点了没反应，
 *    但绝不崩、也不弹错；
 *  - JS 用 `window.__kidMedia && ...` 形式调用，宿主不需要知道页面是否就绪。
 *
 * ⚠️ 换集逻辑只有一份：桥最终调到播放页里那几个函数（`Play.vue` 的
 * `window.__kidMedia`），与页面按钮、系统媒体面板共用同一套 prevTrackId /
 * nextTrackId。别在原生侧再实现一遍"下一集是谁"。
 */
object MediaBridge {

    @Volatile
    private var ref: java.lang.ref.WeakReference<WebView>? = null

    fun attach(webView: WebView) {
        ref = java.lang.ref.WeakReference(webView)
    }

    fun detach() {
        ref = null
    }

    /** 在 WebView 所在线程上执行一段 JS；失败静默（宿主不该被网页的问题拖崩）。 */
    fun call(js: String) {
        val wv = ref?.get() ?: return
        wv.post {
            runCatching { wv.evaluateJavascript(js, null) }
        }
    }

    fun prev() = call("window.__kidMedia && window.__kidMedia.prev && window.__kidMedia.prev()")

    fun next() = call("window.__kidMedia && window.__kidMedia.next && window.__kidMedia.next()")

    fun toggle() = call("window.__kidMedia && window.__kidMedia.toggle && window.__kidMedia.toggle()")
}
