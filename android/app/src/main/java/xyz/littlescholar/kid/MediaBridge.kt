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
 *
 * ============ 后台播放的两个新回调 ============
 *
 * `takeOverAck` / `giveBack` 是"把播放所有权在原生与网页之间交接"用的：
 *   · App 切后台 → 服务用原生 MediaPlayer 接着播 → 调 [takeOverAck]，
 *     网页收到后**把自己暂停并静音**（否则两边同时出声）；
 *   · 回到前台 → 服务调 [giveBack]，把播放位置还回去，
 *     网页 seek 到那个位置继续播。
 * 见 MediaPlaybackService 顶部那段"为什么上一版后台不播"。
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

    /** 统一的入口片段：网页没注册桥就整段短路，不报错。 */
    private fun callMedia(fn: String) = call("window.__kidMedia && window.__kidMedia.$fn")

    fun prev() = callMedia("prev && window.__kidMedia.prev()")

    fun next() = callMedia("next && window.__kidMedia.next()")

    /**
     * 播放 / 暂停分开，**不要用 toggle**。
     *
     * 系统发 `onPlay` 时页面可能已经在播（耳机重连、蓝牙切车机都会重复发），
     * 用 toggle 会把正在播的暂停掉 —— 用户看到的就是"点播放反而停了"。
     */
    fun play() = callMedia("play && window.__kidMedia.play()")

    fun pause() = callMedia("pause && window.__kidMedia.pause()")

    /** 兼容旧调用点（页面里按钮仍是 toggle 语义）。 */
    fun toggle() = callMedia("toggle && window.__kidMedia.toggle()")

    /**
     * 原生引擎的状态回推（时间轴靠它）。
     *
     * wma 这类格式由 VLC 在服务里播，页面连 `<audio>` 都没有 ——
     * 进度条、时间、字幕全靠这一路。**次数很密**（VLC 每 250ms 一次 TimeChanged），
     * 所以参数用基本类型、拼一次 JSON，别在原生侧再做节流：
     * 页面自己按 `Math.floor(sec * 2)` 去重比这里猜频率可靠。
     */
    fun nativeState(
        playing: Boolean,
        position: Double,
        duration: Double,
        ended: Boolean,
        error: String,
    ) {
        // 用 JSONObject 拼而不是手写字符串：error 里可能带引号/换行，
        // 手拼一次就是一个"页面整段脚本不执行"的悬案。
        val o = org.json.JSONObject()
            .put("playing", playing)
            .put("position", if (position.isFinite()) position else 0.0)
            .put("duration", if (duration.isFinite()) duration else 0.0)
            .put("ended", ended)
            .put("error", error)
            .toString()
        call("window.__lsNative && window.__lsNative.onState && window.__lsNative.onState($o)")
    }

    /** 原生已接管：网页把自己静音并暂停，别两边一起出声。 */
    fun takeOverAck() {
        callMedia("takeOverAck && window.__kidMedia.takeOverAck()")
        // 壳自己注入的那份交接（见 MainActivity.INJECT_JS）：页面是旧构建、
        // 压根没有 __kidMedia 时靠它把网页静音 —— 否则原生一开播就是双声道。
        call("window.__lsHandoff && window.__lsHandoff.ack && window.__lsHandoff.ack()")
    }

    /**
     * 交还播放：把位置还给网页。
     *
     * `ended=true` 表示这一集播完了（原生播放器跑到结尾），
     * 网页据此走"自动下一集"；否则只是回来的普通续播。
     */
    fun giveBack(sec: Long, ended: Boolean) {
        val s = if (sec < 0) 0 else sec
        callMedia("giveBack && window.__kidMedia.giveBack($s, ${if (ended) "true" else "false"})")
        call("window.__lsHandoff && window.__lsHandoff.giveBack && window.__lsHandoff.giveBack($s)")
    }

    /**
     * 系统媒体面板拖动进度条。
     *
     * 秒数由系统给（PlaybackStateCompat 的 position），这里只做范围钳制后
     * 交给页面 —— 页面自己知道时长与当前播放器，别在原生侧再算一遍。
     */
    fun seekTo(sec: Long) {
        if (sec < 0) return
        callMedia("seekTo && window.__kidMedia.seekTo($sec)")
    }
}
