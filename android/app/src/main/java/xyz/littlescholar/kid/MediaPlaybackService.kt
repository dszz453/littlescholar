package xyz.littlescholar.kid

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import android.support.v4.media.MediaMetadataCompat
import android.support.v4.media.session.MediaSessionCompat
import android.support.v4.media.session.PlaybackStateCompat
import androidx.core.app.NotificationCompat
import androidx.media.app.NotificationCompat as MediaNotificationCompat
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors

/**
 * 媒体播放前台服务（保活 + 系统媒体控制中心 + **后台接管播放**）。
 *
 * ============ ⚠️ 为什么上一版"后台不播、点播放没反应" ============
 *
 * 上一版这个服务只是网页的**镜子**：真正的 `<video>`/`<audio>` 在 WebView 里，
 * 通知栏按钮和媒体面板的每一个回调都要 `evaluateJavascript` 打到网页去执行。
 * 这有两个致命后果：
 *
 *  ① App 退到后台，Chromium 会把 WebView 的 JS 执行挂起 → 那声
 *     `evaluateJavascript` 打进去没人执行 → **按钮点了没反应**；
 *  ② 网页一旦被挂起/回收，播放本身就停了 —— 通知还在，但已经没有任何
 *     东西在播。孩子看到的就是"有图标、点不动、切后台就没声"。
 *
 * 所以这一版把**后台的播放所有权**拿过来了：网页在切后台前把
 * `url + 位置` 交给服务（[takeOver]），服务用原生 MediaPlayer 接着播；
 * 回到前台再交还（[giveBack]）。后台期间：
 *   · 通知/媒体面板的播放暂停 → 直接控 MediaPlayer，**不经过网页**；
 *   · 网页那边会把自己静音并暂停（[MediaBridge.takeOverAck]），
 *     避免两边同时出声。
 *
 * ============ 为什么以前"没有常驻控制中心" ============
 *
 * 旧实现只 `addAction(上一集/暂停/下一集)` 建了一条**普通通知**：
 *   · 没有 `MediaStyle` → 系统不认这是媒体通知；
 *   · 没有 `setMediaSession(token)` → Android 11+ 的媒体控制区根本不知道有东西在播；
 *   · 网页里的 `navigator.mediaSession` 在 WebView 里**不会**注册到系统。
 */
class MediaPlaybackService : Service() {

    companion object {
        const val CHANNEL_ID = "ls_playback"
        const val NOTI_ID = 1001

        const val ACTION_START = "xyz.littlescholar.kid.START"
        const val ACTION_STOP = "xyz.littlescholar.kid.STOP"
        const val ACTION_PREV = "xyz.littlescholar.kid.PREV"
        const val ACTION_PLAY = "xyz.littlescholar.kid.PLAY"
        const val ACTION_PAUSE = "xyz.littlescholar.kid.PAUSE"
        const val ACTION_NEXT = "xyz.littlescholar.kid.NEXT"

        /** 页面点名要用原生引擎播一条音频（wma/ape 这类 WebView 解不了的）。 */
        const val ACTION_NATIVE = "xyz.littlescholar.kid.NATIVE_AUDIO"
        const val EXTRA_JSON = "json"

        /**
         * 服务是不是真的在跑。
         *
         * ⚠️ 外面**不能靠自己的布尔字段判断** —— 通知栏按钮、Activity 重建、
         * 系统重启服务都会让两边的状态漂。
         */
        @Volatile
        private var running = false

        @Volatile
        private var instance: MediaPlaybackService? = null

        fun isRunning(): Boolean = running

        fun start(ctx: Context) {
            val i = Intent(ctx, MediaPlaybackService::class.java).apply {
                action = ACTION_START
            }
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    ctx.startForegroundService(i)
                } else {
                    ctx.startService(i)
                }
            } catch (e: Exception) {
                // Android 12+ 从后台启动前台服务有限制。失败不该让播放挂掉。
                Diagnostics.add("媒体", "前台服务启动失败：${e.message}")
            }
        }

        fun stop(ctx: Context) {
            try {
                ctx.startService(
                    Intent(ctx, MediaPlaybackService::class.java).apply { action = ACTION_STOP }
                )
            } catch (e: Exception) {
                try {
                    ctx.stopService(Intent(ctx, MediaPlaybackService::class.java))
                } catch (e2: Exception) {
                    // 都不行就只能算了：锁随进程释放，不会一直耗电
                }
            }
        }

        /** 网页推来的播放状态（标题/封面/进度/地址…）。 */
        fun pushState(json: String) {
            val st = MediaState.parse(json) ?: return
            instance?.applyState(st)
        }

        /**
         * 网页切后台前把播放交过来。
         *
         * 失败一律**静默交还**给网页：宁可回到"前台能播、后台不播"，
         * 也不要出现"两边都没声音"。
         */
        fun takeOver(json: String) {
            val st = MediaState.parse(json) ?: return
            instance?.takeOver(st) ?: run {
                // 服务还没起来：起一个，等 onStartCommand 里再补
                pending = st
            }
        }

        /** 回到前台，把播放还给网页。 */
        fun giveBack() {
            instance?.giveBack()
        }

        /**
         * 页面要求用**原生引擎**播一条音频。
         *
         * 与 [takeOver] 的区别：接管是"网页已经在播、切后台时交给原生接着放"；
         * 这条是"网页根本播不了（wma），从头就由原生播"。两者互斥，
         * 且原生模式不需要交还 —— 切前后台用的都是同一个 VLC 播放器。
         *
         * 走 Intent 而不是 instance 直调：服务没起来时也要能一条命令把它拉起来，
         * 否则第一次点开 wma 就是"没声"。
         */
        fun nativeAudio(ctx: Context, json: String) {
            val i = Intent(ctx, MediaPlaybackService::class.java).apply {
                action = ACTION_NATIVE
                putExtra(EXTRA_JSON, json)
            }
            try {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                    ctx.startForegroundService(i)
                } else {
                    ctx.startService(i)
                }
            } catch (e: Exception) {
                Diagnostics.add("原生播放", "启动失败：${e.message}")
            }
        }

        /** 服务没起来时先把状态存着，`onCreate` 之后补接管。 */
        @Volatile
        private var pending: MediaState? = null

        internal fun consumePending(): MediaState? {
            val p = pending
            pending = null
            return p
        }

        /** 只读一份当前状态（诊断页/Activity 用）。 */
        fun snapshot(): MediaState? = instance?.state

        /**
         * 后台接管是不是正在生效。
         *
         * ⚠️ 这个必须给外面看：接管期间网页是**暂停**的（它把声音让出来了），
         * 壳里"播放结束 → 停服务"那套判断会误以为该收工了，
         * 宽限期一到就把服务停掉，后台播放跟着断。见 MainActivity.maybeStopPlayback。
         */
        fun isHandingOff(): Boolean = instance?.handingOffNow == true

        /**
         * 原生引擎是不是正在播（wma 这类）。
         *
         * 与 [isHandingOff] 一样，是给 MainActivity 的"该不该收服务"判断用的：
         * 原生模式下页面里没有 `<audio>`，它永远报"没在播"。
         */
        fun isNativePlaying(): Boolean = instance?.nativeMode == true

        /**
         * 壳里主动接管：不依赖网页的 `visibilitychange`。
         *
         * 为什么还需要这条：网页那一声 `takeOver` 是异步过桥的，
         * 而 `visibilitychange` 在 WebView 里报不报、什么时候报，
         * 各家 ROM/内核都不一样。一旦没报，JS 很快被冻结，
         * 就再也没有机会调第二次了 —— 表现就是"切后台就断"。
         *
         * 这里用**最近一次真的在播**的快照（含绝对地址与位置）兜底，
         * 60 秒内有效；用户自己按过暂停的不接（否则"我暂停了它自己又响"）。
         */
        fun takeOverFromState(): Boolean {
            val s = instance ?: return false
            if (s.handingOffNow) return true
            // 原生引擎已经在播了 —— 这时候"接管"只会凭空多出一个 MediaPlayer。
            if (s.nativeMode) return true
            val st = s.lastPlaying ?: return false
            if (System.currentTimeMillis() - s.lastPlayingAt > 60_000L) return false
            if (st.paused) return false
            Diagnostics.add("接管", "壳里主动接管（网页没交接）：@${st.position}s")
            s.takeOver(st)
            return true
        }
    }

    /** 最近一次"在播"的快照，见 applyState 的注释。 */
    @Volatile
    private var lastPlaying: MediaState? = null

    /** 上面那份快照的时间，太老就不拿来接管了。 */
    @Volatile
    private var lastPlayingAt: Long = 0L

    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    /** 主线程 Handler：页面离开计时要在这里排，才能和 stopSelf 有序。 */
    private val main = Handler(Looper.getMainLooper())

    private var session: MediaSessionCompat? = null
    internal var state: MediaState = MediaState()
        private set
    private var lastPosterUrl = ""
    private var poster: Bitmap? = null
    private val io = Executors.newSingleThreadExecutor()

    /**
     * 后台接管用的原生播放器。
     *
     * ⚠️ 只有 [handoffMode] 为 true 时它才是"当前真正在播的那个"；
     * 前台时它是 null，播放归 WebView 里的 <video>/<audio> 管。
     */
    private var player: MediaPlayer? = null
    private var handoffMode = false
    private var handoffPreparing = false

    /**
     * 接管批次号。
     *
     * ============ ⚠️ 为什么必须有它（2026-10-22 的真故障） ============
     *
     * `prepareAsync()` 是异步的，而"切后台→马上切回来"在真机上非常常见
     * （看一眼通知、锁屏又解锁、误触 Home）。原来的代码里：
     *
     *   onStop  → takeOver() → prepareAsync()     // 还没准备好
     *   onResume→ giveBack() → releasePlayer()    // 把播放器 release 了
     *   ……然后 prepare 回调照样会来……
     *   onPrepared → handoffMode = true + MediaBridge.takeOverAck()
     *                → 网页被静音并 pause，同时一个"幽灵播放器"开始出声
     *
     * 用户看到的就是**"正播着突然自己暂停了"**，而且按播放键也没用
     * （页面被 mute 了）。更糟的是这个幽灵播放器没人释放，
     * 退出 App 之后声音还在响 —— 正是"退出之后还在播放"。
     *
     * 所以每次 takeOver/giveBack 都让批次号 +1，回调里对不上就直接丢弃。
     */
    private var handoffToken = 0

    /**
     * 页面明确说"我不在播了"之后，多久真正收摊。
     *
     * 为什么要等：切集时页面会先 cleanupMedia（发 active=false）再 load
     * 下一集，中间有 1~3 秒空窗；立刻停会把下一集的服务一起停掉。
     * 但也不能像以前那样"只要还在接管就无限续命" ——
     * 那样孩子退出播放页、退出 App 之后声音会一直响下去。
     */
    private var leaveStop: Runnable? = null

    /**
     * 原生解码引擎（libVLC）。**只有 nativeMode 为 true 时它才是"当前真正在播的那个"**。
     *
     * 与 [player]（MediaPlayer，用于后台接管 mp4/mp3）是两条互斥的路：
     * 网页**能**解的格式走 WebView（音画同步、字幕、倍速都在页面里）；
     * 网页**解不了**的（wma/ape/amr…）才由它播，页面退化成遥控器 + 字幕层。
     */
    private var vlc: VlcAudioEngine? = null
    private var nativeMode = false

    /** 给 `isHandingOff()` 用（准备中也算，避免准备期被误停服务）。 */
    internal val handingOffNow: Boolean get() = handoffMode || handoffPreparing

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        createChannel()
        createSession()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                // 页面主动要求收工：先把位置交还（页面可能还活着，要落进度），
                // 再把三路声音全部停掉 —— 少停一路就是"退出后还在响"。
                giveBack()
                shutdownPlayback("收到 STOP")
                return START_NOT_STICKY
            }
            ACTION_PREV -> {
                MediaBridge.prev()
                return START_STICKY
            }
            ACTION_NEXT -> {
                MediaBridge.next()
                return START_STICKY
            }
            ACTION_NATIVE -> {
                // startForegroundService 要求 5 秒内 startForeground，
                // 所以先把面板立起来，再让 VLC 去加载（加载可能要一两秒）。
                running = true
                session?.isActive = true
                acquireLocks()
                startForeground(NOTI_ID, buildNotification())
                handleNative(intent.getStringExtra(EXTRA_JSON).orEmpty())
                return START_STICKY
            }
            ACTION_PLAY -> {
                doPlay()
                return START_STICKY
            }
            ACTION_PAUSE -> {
                doPause("通知栏")
                return START_STICKY
            }
            else -> {
                startForeground(NOTI_ID, buildNotification())
                acquireLocks()
                running = true
                session?.isActive = true
                // 服务是被"接管请求"顺带起来的：补上那一步
                consumePending()?.let { takeOver(it) }
            }
        }
        return START_STICKY
    }

    // ============================================================
    //  播放：后台走原生，前台走网页
    // ============================================================

    /**
     * 播放。
     *
     * 后台接管中 → 直接控 MediaPlayer；否则走网页那条桥。
     * ⚠️ 这里**必须是幂等的**（已经是播放态就什么都不做）。
     * 上一版把 `onPlay` 映射成 `toggle()`，系统只要多发一次 onPlay
     * （耳机拔插、蓝牙重连都会），就会把正在播的暂停掉 —— 用户看到的就是
     * "点播放反而停了"。
     */
    private fun doPlay() {
        if (handoffMode) {
            runCatching { player?.start() }
            markPlaying(true)
            return
        }
        if (nativeMode) {
            vlc?.resume()
            markPlaying(true)
            return
        }
        MediaBridge.play()
    }

    /**
     * 暂停。
     *
     * [from] 只用于诊断：孩子说"它自己停了"时，第一件要回答的事就是
     * "这条暂停指令到底是谁发的" —— 用户按的、通知栏、耳机/蓝牙、
     * 系统媒体面板，还是页面自己。以前这些来源在日志里长得一模一样。
     */
    private fun doPause(from: String = "未知") {
        Diagnostics.add("暂停", "来源=$from")
        if (handoffMode) {
            runCatching { player?.pause() }
            markPlaying(false)
            return
        }
        if (nativeMode) {
            vlc?.pause()
            markPlaying(false)
            return
        }
        MediaBridge.pause()
    }

    /** 本地把播放态改掉并刷新面板（后台接管时用，不依赖网页推送）。 */
    private fun markPlaying(on: Boolean) {
        state = state.copy(playing = on, position = nativePositionSec())
        updateSession()
        if (running) {
            getSystemService(NotificationManager::class.java)?.notify(NOTI_ID, buildNotification())
        }
    }

    private fun nativePositionSec(): Long {
        if (nativeMode) return (vlc?.positionMs ?: 0L) / 1000L
        val p = player ?: return state.position
        return try {
            (p.currentPosition / 1000L).coerceAtLeast(0)
        } catch (e: Exception) {
            state.position
        }
    }

    // ============================================================
    //  原生引擎（wma / ape / amr …）
    // ============================================================

    /**
     * 页面点名的原生播放。协议是单条 JSON：
     *
     * ```json
     * {"cmd":"play","url":"https://…wma?t=…","position":12,"title":"…",
     *  "subtitle":"…","poster":"…","duration":217}
     * {"cmd":"pause"} / {"cmd":"resume"} / {"cmd":"seek","position":30} / {"cmd":"stop"}
     * ```
     *
     * 为什么让页面来点名、而不是壳自己按扩展名判断：
     * 页面上那个地址可能是 `/api/stream`（直链）、PRO 的 `/edge/stream`
     * （本地反代），也可能是一集视频。壳里猜错一次就是"没声"，而页面
     * 手里有服务端给的类型（`nativeAudio`），这个判断只该有一处。
     */
    private fun handleNative(json: String) {
        if (json.isBlank()) return
        val o = runCatching { JSONObject(json) }.getOrNull() ?: return
        val cmd = o.optString("cmd", "play")
        if (cmd != "stop" && cmd != "play" && !nativeMode) {
            // 还没进入原生模式就收到 pause/seek：多半是页面重进后补发的
            // 状态心跳，直接忽略，别把别的资源打断。
            return
        }
        when (cmd) {
            "play" -> {
                val url = o.optString("url", "")
                if (url.isBlank()) {
                    Diagnostics.add("原生播放", "地址为空，忽略")
                    return
                }
                state = state.copy(
                    active = true,
                    playing = true,
                    title = o.optString("title", state.title).take(120),
                    subtitle = o.optString("subtitle", state.subtitle).take(120),
                    poster = o.optString("poster", state.poster),
                    duration = o.optLong("duration", state.duration),
                    position = o.optDouble("position", 0.0).toLong().coerceAtLeast(0),
                    url = url,
                    video = false,
                )
                nativeMode = true
                // 原生模式不存在"接管"这回事：播放器本来就在服务里。
                handoffMode = false
                handoffPreparing = false
                Diagnostics.add("原生播放", "VLC 加载：${url.take(80)}")
                // ⚠️ 必须接住 Throwable：libvlcjni 的 JNI 注册失败抛的是
                // UnsatisfiedLinkError（Error 不是 Exception），裸调用会把
                // 前台服务一起带走 —— 用户看到的就是"点开 wma 客户端直接退出"。
                runCatching { engine().play(url, state.position * 1000L) }
                    .onFailure { onEngineBroken(it) }
                // 与 VLC 并行地问一次同一个地址（只取 1KB）。
                // 3.6.5 的 libVLC 不给日志回调，失败只有一个 EncounteredError，
                // 分不清"证书不被信任""DNS 卡住""服务端 4xx"——这一问就是答案。
                probeAsync(url)
                // 12 秒后把诊断 + VLC 日志尾部回传一次：够 VLC 走完 demux
                // 与首帧，又不会拖太久。只在真机排障期需要，见 reportDiag。
                Thread {
                    runCatching { Thread.sleep(12_000) }
                    reportDiag("native+12s")
                }.start()
            }
            "pause" -> {
                engine().pause()
                markPlaying(false)
            }
            "resume" -> {
                engine().resume()
                markPlaying(true)
            }
            "seek" -> {
                val sec = o.optDouble("position", 0.0).toLong().coerceAtLeast(0)
                engine().seekTo(sec * 1000L)
                state = state.copy(position = sec)
                updateSession()
            }
            "stop" -> stopNative()
            else -> Diagnostics.add("原生播放", "未知指令：$cmd")
        }
    }

    private fun engine(): VlcAudioEngine =
        vlc ?: VlcAudioEngine(applicationContext) { st -> onNativeState(st) }.also { vlc = it }

    /**
     * 原生引擎起不来（JNI 注册失败 / so 加载失败 / 解码器缺失）。
     *
     * 真正该挡住它的是 proguard 里的 `-keep class org.videolan.**`；这里是
     * 兜底，只为保证**最坏情况也不闪退**：留一条诊断、把引擎丢掉，并推一个
     * error 给页面 —— 页面收到就退回 `info.url`（服务端转码地址）。
     * 慢一点，但一定出得来声。
     */
    private fun onEngineBroken(e: Throwable) {
        Diagnostics.add(
            "原生播放",
            "引擎起不来：${e.javaClass.simpleName}: ${e.message}",
        )
        nativeMode = false
        runCatching { vlc?.release() }
        vlc = null
        MediaBridge.nativeState(false, 0.0, 0.0, false, "NATIVE_UNAVAILABLE")
    }

    // ============================================================
    //  旁路探测：同一地址，用 Java 的 TLS 栈取 1KB
    // ============================================================

    /**
     * 为什么要它：2026-10-26 标准版点开 wma **完全没有反应** ——
     * 页面上连通话记录都没有，只有"点了播放没动静"。
     *
     * 探针已经证明前端把 `https://…/api/stream/9?t=…` 交给了壳，
     * 所以问题只可能在壳拉这个地址这一步。而 libVLC 3.6.5 失败时只抛
     * 一个 `EncounteredError`，不带原因 —— 常见的三种完全不同的病
     * （系统 CA 里缺根证书、DNS 卡住、票据过期 401）在用户眼里长得一模一样。
     *
     * 这里用 **Java 自己的网络栈**（HttpsURLConnection，走 Android 系统
     * TrustManager）再问一次同一个地址：
     *   · 探测失败 → 网络/证书层就过不去，与 VLC 解码无关；
     *   · 探测成功而 VLC 不播 → 问题在 VLC 内部（参数/解码器），
     *     与 URL 无关。
     * 两种结论都直接写进诊断，下一次就不用猜了。
     *
     * 探测只取 1KB 就断开（Range），对服务端与流量都可忽略。
     */
    private val probeExec = java.util.concurrent.Executors.newSingleThreadExecutor { r ->
        Thread(r, "ls-native-probe").apply { isDaemon = true }
    }

    private fun probeAsync(url: String) {
        probeExec.execute {
            val verdict = runCatching { probeOnce(url) }
                .getOrElse { "FAIL ${it.javaClass.simpleName}: ${it.message}" }
            Diagnostics.add("原生播放", "探测 $verdict")
            // 已经出声了就不打扰；还没出声就立刻让页面降级，
            // 不用干等 15 秒的看门狗。
            if (verdict.startsWith("FAIL") && nativeMode && !engine().isPlaying) {
                onNativeState(
                    VlcAudioEngine.State(
                        playing = false,
                        positionMs = 0,
                        durationMs = state.duration * 1000L,
                        ended = false,
                        error = verdict.take(120),
                    ),
                )
            }
        }
    }

    /**
     * 把诊断（**含 VLC 自己的日志尾部**）回传到服务端，直接从 nginx access log 读。
     *
     * 为什么走这条路：家长不会连 USB 抓 logcat；标准版也没有设置页
     * （诊断面板只有 PRO 有）。那就把"需要用户配合取证"压缩成
     * "点一次播放" —— 12 秒后自动上传，定位靠
     * `grep 'LittleScholar-Kid-Diag'`。
     *
     * 用 GET + query 而不是 POST：服务端不必为此新增接口，
     * nginx 本来就会把整个 $request 记进日志。
     */
    private fun reportDiag(tag: String) {
        probeExec.execute {
            runCatching {
                val ctx = applicationContext
                val base = ServerConfig.get(ctx)
                if (base.isBlank()) return@runCatching
                val diag = Diagnostics.dump(ctx).takeLast(700)
                val vlc = runCatching {
                    val f = java.io.File(ctx.filesDir, "vlc.log")
                    if (f.exists()) f.readText().takeLast(1500) else "(vlc.log 不存在)"
                }.getOrDefault("(读取失败)")
                val msg = "[$tag]\n$diag\n--- vlc.log ---\n$vlc"
                val enc = java.net.URLEncoder.encode(msg.take(1500), "UTF-8")
                val conn = (URL(base.trimEnd('/') + "/api/health?kidlog=" + enc)
                    .openConnection() as HttpURLConnection).apply {
                    connectTimeout = 8000
                    readTimeout = 8000
                    setRequestProperty("User-Agent", "LittleScholar-Kid-Diag")
                }
                try {
                    runCatching { conn.inputStream.close() }
                } finally {
                    runCatching { conn.disconnect() }
                }
            }
        }
    }

    /** 返回 `OK 206 audio/x-ms-wma len=…` 或 `FAIL SSLHandshakeException: …`。 */    private fun probeOnce(url: String): String {
        val conn = (java.net.URL(url).openConnection() as java.net.HttpURLConnection).apply {
            connectTimeout = 8000
            readTimeout = 8000
            instanceFollowRedirects = true
            setRequestProperty("Range", "bytes=0-1023")
            setRequestProperty("User-Agent", "LittleScholar-Probe/1.0")
        }
        try {
            val code = conn.responseCode
            val ct = conn.contentType ?: "-"
            val len = conn.getHeaderField("Content-Length") ?: "-"
            return if (code in 200..299) "OK $code $ct len=$len" else "FAIL HTTP $code $ct len=$len"
        } finally {
            runCatching { conn.disconnect() }
        }
    }

    /**
     * VLC 的状态回调（已切到主线程）。
     *
     * 三件事：刷新面板、把位置推给页面（进度条与字幕都靠它）、
     * 播完通知页面去切下一集。
     */
    private fun onNativeState(st: VlcAudioEngine.State) {
        if (st.error.isNotEmpty()) {
            Diagnostics.add("原生播放", "失败：${st.error}")
            // 失败也要告诉页面：它会退到"这一段放不了"的提示，
            // 而不是永远转圈等一个不会来的声音。
            MediaBridge.nativeState(false, 0.0, 0.0, false, st.error)
            return
        }
        if (!nativeMode) return
        state = state.copy(
            playing = st.playing,
            position = st.positionMs / 1000L,
            duration = if (st.durationMs > 0) st.durationMs / 1000L else state.duration,
        )
        updateSession()
        if (running) {
            getSystemService(NotificationManager::class.java)?.notify(NOTI_ID, buildNotification())
        }
        MediaBridge.nativeState(
            st.playing,
            st.positionMs / 1000.0,
            st.durationMs / 1000.0,
            st.ended,
            "",
        )
    }

    private fun stopNative() {
        if (!nativeMode && vlc == null) {
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return
        }
        nativeMode = false
        runCatching { vlc?.stop() }
        MediaBridge.nativeState(false, 0.0, 0.0, false, "")
        releaseLocks()
        session?.isActive = false
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    // ============================================================
    //  页面离开 / 任务被划掉：必须有人收尾
    // ============================================================

    /**
     * 页面报"不在播"之后多久真正停。
     *
     * 只用来覆盖"切集"那段空窗（页面 cleanup → router.replace → 新一集 play，
     * 实测 1~3 秒）。以前没有这个计时器，只要处于接管态就无限续命 ——
     * 孩子退出播放页、甚至退出 App 之后，声音还在响。
     */
    private val leaveGraceMs = 12_000L

    private fun scheduleLeaveStop() {
        if (leaveStop != null) return
        val r = Runnable {
            leaveStop = null
            shutdownPlayback("页面已离开 ${leaveGraceMs / 1000} 秒")
        }
        leaveStop = r
        main.postDelayed(r, leaveGraceMs)
    }

    private fun cancelLeaveStop() {
        leaveStop?.let { main.removeCallbacks(it) }
        leaveStop = null
    }

    /**
     * 收摊：停掉**所有**可能出声的东西，然后收掉前台服务。
     *
     * 三路都要停 —— 少停一路就是"退出后还在响"：
     *   · [player]  后台接管用的 MediaPlayer（mp4/mp3）
     *   · [vlc]     原生引擎（wma/ape）
     *   · 前台服务本身 + 唤醒锁 + 媒体面板
     */
    private fun shutdownPlayback(reason: String) {
        Diagnostics.add("播放", "收工：$reason")
        cancelLeaveStop()
        runCatching { releasePlayer() }
        nativeMode = false
        handoffMode = false
        handoffPreparing = false
        runCatching { vlc?.stop() }
        releaseLocks()
        session?.isActive = false
        if (running) {
            runCatching { stopForeground(STOP_FOREGROUND_REMOVE) }
        }
        stopSelf()
    }

    /**
     * 接管：用原生播放器接着播网页正在播的那个地址。
     *
     * 为什么不接管 `blob:` —— blob 地址只存在于 WebView 进程内，原生读不到。
     * 离线缓存播放走的就是 blob，那种情况让它留在网页里（后台能不能续播
     * 取决于系统，但至少不会"接管成功却播不出来"）。
     */
    fun takeOver(st: MediaState) {
        if (handoffMode || handoffPreparing) return
        // 原生模式**不能被接管**：播放器已经是常驻的 VLC，再起一个 MediaPlayer
        // 拉同一个地址就是两条声轨（支持 wma 的机型上会真的同时出声），
        // 不支持 wma 的机型则 prepare 直接失败、面板闪一下又退回暂停。
        //
        // 页面侧 1.18.0 起已经在 `canHandoff()` 里挡了这一下，这里再挡一次是
        // **纵深防御**：设备上可能还跑着 Service Worker 预缓存下来的旧 chunk，
        // 那段时间里页面仍然是"会交出去"的旧逻辑。
        if (nativeMode) {
            // 留一条诊断：真机上"为什么切后台没接管"只看日志是猜不出来的，
            // 而这一条恰恰是**正常**行为。
            Diagnostics.add("接管", "原生模式不需要接管（播放器在服务里）")
            return
        }
        if (st.url.isBlank() || !st.playing) return
        if (st.url.startsWith("blob:", true) || st.url.startsWith("data:", true)) {
            Diagnostics.add("接管", "blob/data 地址无法交给原生，保持网页播放")
            return
        }
        // ⚠️ 相对路径（`/api/stream/123?t=...`）会让 MediaPlayer 当成本地文件名：
        // setDataSource 不报错、prepare 也"成功"，但一点声音都没有。
        // 网页侧已经补成绝对地址，这里再拦一道，并把原因写进诊断。
        if (!st.url.startsWith("http://", true) && !st.url.startsWith("https://", true)) {
            Diagnostics.add("接管", "地址不是绝对 URL，无法交给原生：${st.url.take(60)}")
            return
        }
        state = st
        handoffPreparing = true
        val token = ++handoffToken
        Diagnostics.add("接管", "后台接管播放：${st.url.take(80)} @ ${st.position}s")
        try {
            val p = MediaPlayer()
            p.setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(
                        if (st.video) AudioAttributes.CONTENT_TYPE_MOVIE
                        else AudioAttributes.CONTENT_TYPE_MUSIC
                    )
                    .build()
            )
            // 锁屏继续播：让 MediaPlayer 自己持唤醒锁，比只靠 Service 的更稳
            p.setWakeMode(applicationContext, PowerManager.PARTIAL_WAKE_LOCK)
            p.setDataSource(st.url)
            p.setOnPreparedListener { mp ->
                // ⚠️ 批次号/身份对不上 = 这次接管已经被取消了（页面已交还、
                // 或已经换了另一条资源）。此时**什么都不能做**：
                // 一旦继续 ack，网页会被静音暂停，而这个播放器没人管 ——
                // 用户看到"自己停了"，退出后声音还在。见 handoffToken 的注释。
                if (token != handoffToken || !handoffPreparing) {
                    Diagnostics.add("接管", "丢弃过期的准备回调（已交还或已换源）")
                    runCatching { mp.release() }
                    return@setOnPreparedListener
                }
                handoffPreparing = false
                handoffMode = true
                player = mp
                if (st.position > 0) {
                    runCatching { mp.seekTo((st.position * 1000L).toInt()) }
                }
                runCatching { mp.start() }
                // ⚠️ 先 ack 再更新面板：ack 会让网页把自己静音，
                // 顺序反了会出现短暂的双声道。
                MediaBridge.takeOverAck()
                markPlaying(true)
            }
            p.setOnCompletionListener {
                // 播完：交还给网页，由网页去切下一集（换集逻辑只有一份）
                val pos = (it.duration / 1000L).coerceAtLeast(0)
                releasePlayer()
                MediaBridge.giveBack(pos, ended = true)
                markPlaying(false)
            }
            p.setOnErrorListener { _, what, extra ->
                Diagnostics.add("接管", "原生播放失败 what=$what extra=$extra，交还网页")
                val pos = nativePositionSec()
                releasePlayer()
                MediaBridge.giveBack(pos, ended = false)
                false
            }
            p.prepareAsync()
        } catch (e: Exception) {
            handoffPreparing = false
            Diagnostics.add("接管", "接管失败：${e.message}")
            releasePlayer()
        }
    }

    /** 交还：停掉原生播放器，把位置还给网页。 */
    fun giveBack() {
        if (!handoffMode && !handoffPreparing) return
        val pos = nativePositionSec()
        releasePlayer()
        MediaBridge.giveBack(pos, ended = false)
        Diagnostics.add("接管", "交还网页 @ ${pos}s")
    }

    private fun releasePlayer() {
        // 批次号 +1：把在飞的 prepare 回调一次性作废（见 handoffToken）
        handoffToken++
        handoffMode = false
        handoffPreparing = false
        val p = player
        player = null
        if (p == null) return
        runCatching { if (p.isPlaying) p.stop() }
        runCatching { p.reset() }
        runCatching { p.release() }
    }

    // ============================================================
    //  MediaSession：系统媒体控制中心
    // ============================================================

    private fun createSession() {
        val s = MediaSessionCompat(this, "LittleScholar")
        s.setCallback(object : MediaSessionCompat.Callback() {
            // 这些回调来自**系统**（锁屏、耳机线控、车机、蓝牙）。
            // 播放/暂停按语义分发，**不再用 toggle**（见 doPlay 的注释）。
            override fun onPlay() = doPlay()
            override fun onPause() = doPause("系统媒体面板")
            override fun onSkipToNext() = MediaBridge.next()
            override fun onSkipToPrevious() = MediaBridge.prev()
            override fun onSeekTo(pos: Long) {
                if (nativeMode) {
                    vlc?.seekTo(pos)
                    state = state.copy(position = pos / 1000L)
                    updateSession()
                    return
                }
                if (handoffMode) {
                    runCatching { player?.seekTo(pos.toInt()) }
                    state = state.copy(position = pos / 1000L)
                    updateSession()
                } else {
                    MediaBridge.seekTo(pos / 1000L)
                }
            }

            override fun onStop() = doPause("系统 onStop")
        })
        s.isActive = true
        session = s
        updateSession()
    }

    /** 把最新状态灌进 session（元数据 + 播放状态 + 通知）。 */
    fun applyState(st: MediaState) {
        // 页面说"我不在播了"（离开播放页、退出 App、切集时的过渡）：
        // 给一个短宽限，到点还没回来就真的收摊。
        //
        // ⚠️ 这段是"退出之后还在播放"的正解：以前只要处于接管态，
        // maybeStopPlayback 就会一轮一轮续命，页面早就不在了，
        // 声音还在响，通知栏也一直在。宽限期只该覆盖"切集"那 1~3 秒。
        if (!st.active) {
            scheduleLeaveStop()
        } else {
            cancelLeaveStop()
        }
        // 记下"最近一次真的在播"的样子：切后台时如果网页没来得及交接
        // （visibilitychange 没触发、或 JS 已经被冻结），壳里靠它主动接管。
        // 只记在播且地址可用的，避免被"暂停/空地址"的心跳覆盖掉。
        if (st.playing && st.url.startsWith("http", true)) {
            lastPlaying = st.copy(active = true, playing = true)
            lastPlayingAt = System.currentTimeMillis()
        }
        // ⚠️ 后台接管期间，网页是"暂停并静音"的，它推来的 playing/position
        // 会把面板刷成"已暂停"。这段时间以原生播放器为准，忽略这两项。
        state = when {
            handoffMode -> st.copy(playing = state.playing, position = nativePositionSec())
            // 原生模式下网页是"暂停"的（它连 <audio> 都没有），
            // 让它推来的 playing/position 覆盖面板会让通知栏一直显示"已暂停"。
            nativeMode -> st.copy(
                playing = state.playing,
                position = nativePositionSec(),
                url = state.url,
            )
            else -> st
        }
        if (st.poster.isNotEmpty() && st.poster != lastPosterUrl) {
            lastPosterUrl = st.poster
            loadPoster(st.poster)
        }
        updateSession()
        if (running) {
            getSystemService(NotificationManager::class.java)?.notify(NOTI_ID, buildNotification())
        }
    }

    private fun updateSession() {
        val s = session ?: return
        val md = MediaMetadataCompat.Builder()
            .putString(MediaMetadataCompat.METADATA_KEY_TITLE, state.title.ifBlank { "小书童" })
            .putString(
                MediaMetadataCompat.METADATA_KEY_ARTIST,
                state.subtitle.ifBlank { "小书童 · 学习空间" }
            )
            .putString(MediaMetadataCompat.METADATA_KEY_ALBUM, "小书童")
            .putLong(MediaMetadataCompat.METADATA_KEY_DURATION, state.duration * 1000)
            .apply {
                poster?.let { putBitmap(MediaMetadataCompat.METADATA_KEY_ALBUM_ART, it) }
            }
            .build()
        s.setMetadata(md)

        var actions = PlaybackStateCompat.ACTION_PLAY or
            PlaybackStateCompat.ACTION_PAUSE or
            PlaybackStateCompat.ACTION_PLAY_PAUSE or
            PlaybackStateCompat.ACTION_SEEK_TO or
            PlaybackStateCompat.ACTION_STOP
        if (state.hasPrev) actions = actions or PlaybackStateCompat.ACTION_SKIP_TO_PREVIOUS
        if (state.hasNext) actions = actions or PlaybackStateCompat.ACTION_SKIP_TO_NEXT

        val pb = PlaybackStateCompat.Builder()
            .setActions(actions)
            .setState(
                if (state.playing) PlaybackStateCompat.STATE_PLAYING
                else PlaybackStateCompat.STATE_PAUSED,
                state.position * 1000,
                if (state.playing) 1f else 0f,
                SystemClock.elapsedRealtime()
            )
            .build()
        s.setPlaybackState(pb)
    }

    private fun loadPoster(url: String) {
        io.execute {
            try {
                val conn = (URL(url).openConnection() as HttpURLConnection).apply {
                    connectTimeout = 4000
                    readTimeout = 6000
                    instanceFollowRedirects = true
                }
                conn.inputStream.use { input ->
                    val bmp = BitmapFactory.decodeStream(input)
                    if (bmp != null) {
                        poster = bmp
                        updateSession()
                        if (running) {
                            getSystemService(NotificationManager::class.java)
                                ?.notify(NOTI_ID, buildNotification())
                        }
                    }
                }
                conn.disconnect()
            } catch (e: Exception) {
                // 封面只是锦上添花，失败了通知仍然可用
            }
        }
    }

    // ============================================================
    //  通知
    // ============================================================

    private fun createChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val mgr = getSystemService(NotificationManager::class.java)
        if (mgr.getNotificationChannel(CHANNEL_ID) != null) return
        val ch = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.noti_channel_name),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = getString(R.string.noti_channel_desc)
            setShowBadge(false)
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
            setSound(null, null)
        }
        mgr.createNotificationChannel(ch)
    }

    private fun buildNotification(): Notification {
        val pi = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val builder = NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(state.title.ifBlank { getString(R.string.noti_title) })
            .setContentText(state.subtitle.ifBlank { getString(R.string.noti_text) })
            .setSmallIcon(R.drawable.ic_stat_play)
            .setContentIntent(pi)
            .setOngoing(true)
            .setSilent(true)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setOnlyAlertOnce(true)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.LOLLIPOP) {
            builder.setCategory(NotificationCompat.CATEGORY_TRANSPORT)
        }
        poster?.let { builder.setLargeIcon(it) }

        // 进度条：只在有总时长时给（直播/未知时长给了会是 0/0）
        if (state.duration > 0) {
            builder.setProgress(
                state.duration.toInt(),
                state.position.coerceIn(0, state.duration).toInt(),
                false
            )
        }

        val actions = mutableListOf<NotificationCompat.Action>()
        val compact = mutableListOf<Int>()
        if (state.hasPrev) {
            compact.add(actions.size)
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_prev), action(ACTION_PREV)))
        }
        compact.add(actions.size)
        // ⚠️ 按钮跟着真实状态走：播放中给「暂停」，暂停了给「播放」。
        // 上一版无论什么状态都写「暂停」并发 toggle，暂停时点它等于再暂停一次。
        if (state.playing) {
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_pause), action(ACTION_PAUSE)))
        } else {
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_play), action(ACTION_PLAY)))
        }
        if (state.hasNext) {
            compact.add(actions.size)
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_next), action(ACTION_NEXT)))
        }
        actions.forEach { builder.addAction(it) }

        // ⚠️ 这一行才是"常驻控制中心"的关键
        val style = MediaNotificationCompat.MediaStyle()
            .setShowActionsInCompactView(*compact.take(3).toIntArray())
        session?.sessionToken?.let { style.setMediaSession(it) }
        builder.setStyle(style)

        return builder.build()
    }

    /** 通知按钮用的 PendingIntent：回到本服务处理。 */
    private fun action(act: String): PendingIntent {
        val i = Intent(this, MediaPlaybackService::class.java).apply { action = act }
        return PendingIntent.getService(
            this,
            act.hashCode(),
            i,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    @android.annotation.SuppressLint("WakelockTimeout")
    private fun acquireLocks() {
        if (wakeLock?.isHeld != true) {
            try {
                val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
                wakeLock = pm.newWakeLock(
                    PowerManager.PARTIAL_WAKE_LOCK,
                    "littlescholar:playback"
                ).apply {
                    setReferenceCounted(false)
                    acquire()
                }
            } catch (e: Exception) {
                Diagnostics.add("保活", "CPU 唤醒锁失败：${e.message}")
            }
        }
        if (wifiLock?.isHeld != true) {
            try {
                val wm = applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager
                wifiLock = wm?.createWifiLock(wifiLockMode(), "littlescholar:wifi")?.apply {
                    setReferenceCounted(false)
                    acquire()
                }
            } catch (e: Exception) {
                Diagnostics.add("保活", "WiFi 锁失败：${e.message}")
            }
        }
    }

    @Suppress("DEPRECATION")
    private fun wifiLockMode(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            WifiManager.WIFI_MODE_FULL_LOW_LATENCY
        } else {
            WifiManager.WIFI_MODE_FULL_HIGH_PERF
        }

    private fun releaseLocks() {
        try {
            wakeLock?.let { if (it.isHeld) it.release() }
        } catch (_: RuntimeException) {
        }
        try {
            wifiLock?.let { if (it.isHeld) it.release() }
        } catch (_: RuntimeException) {
        }
        wakeLock = null
        wifiLock = null
    }

    /**
     * 任务被从"最近任务"里划掉。
     *
     * ⚠️ 以前没有这个回调。而 ACTIVITY 的 onDestroy 只管 Activity，
     * **前台服务不会跟着死** —— 于是"划掉卡片"之后 MediaPlayer/VLC 继续播，
     * 通知栏还在，声音一直响。用户表述就是"退出了还在播"。
     * 这是系统给服务的最后一条通知，必须在这里收摊。
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        Diagnostics.add("播放", "任务被划掉 → 停止播放")
        shutdownPlayback("任务被划掉")
        super.onTaskRemoved(rootIntent)
    }

    override fun onDestroy() {
        running = false
        instance = null
        cancelLeaveStop()
        releasePlayer()
        runCatching { vlc?.release() }
        vlc = null
        releaseLocks()
        session?.release()
        session = null
        io.shutdownNow()
        super.onDestroy()
    }
}

/**
 * 页面推来的一次播放状态。
 *
 * 解析一律容错：参数来自 WebView 里的 JS，可能被改成任何东西。
 * 解析失败就返回 null（当作"没收到"），绝不让桥抛异常。
 */
data class MediaState(
    val active: Boolean = false,
    val playing: Boolean = false,
    val title: String = "",
    val subtitle: String = "",
    val poster: String = "",
    val duration: Long = 0,
    val position: Long = 0,
    val hasPrev: Boolean = false,
    val hasNext: Boolean = false,
    /** 当前在播的地址。后台接管要用，所以必须由网页带过来。 */
    val url: String = "",
    /** 是不是视频（决定 AudioAttributes 的 content type）。 */
    val video: Boolean = false,
    /**
     * 是不是**用户自己按的暂停**。
     *
     * 切后台时系统也会让网页的 `<video>` 触发 pause，跟用户按暂停长得一模一样；
     * 不区分的话，壳里"按最近状态主动接管"会把用户刚暂停的内容又播起来。
     */
    val paused: Boolean = false,
) {
    companion object {
        fun parse(json: String?): MediaState? {
            if (json.isNullOrBlank()) return null
            return try {
                val o = JSONObject(json)
                MediaState(
                    active = o.optBoolean("active", false),
                    playing = o.optBoolean("playing", false),
                    title = o.optString("title", "").take(120),
                    subtitle = o.optString("subtitle", "").take(120),
                    poster = o.optString("poster", "").take(2048),
                    duration = o.optLong("duration", 0).coerceIn(0, 24 * 3600),
                    position = o.optLong("position", 0).coerceIn(0, 24 * 3600),
                    hasPrev = o.optBoolean("hasPrev", false),
                    hasNext = o.optBoolean("hasNext", false),
                    // 地址长度给足：PRO 的本地加速流会带一长串票据参数
                    url = o.optString("url", "").take(4096),
                    video = o.optBoolean("video", false),
                    paused = o.optBoolean("paused", false),
                )
            } catch (e: Exception) {
                null
            }
        }
    }
}
