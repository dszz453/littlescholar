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
         * 且原生模式不需要交还 —— 切前后台用的都是同一个原生播放器。
         *
         * ⚠️ 服务**已经在跑**时一律直调，绝不走 `startForegroundService` ——
         * 这是"息屏自动连播"的命门（2026-11-02）：
         *
         * 页面切下一集时先发 `stop` 再发 `play`。若两条都走 Intent，
         * `stop` 会把前台服务收掉（`stopForeground` + `stopSelf`），
         * 紧接着的 `play` 就是**从后台启动前台服务** —— Android 12+ 直接抛
         * `ForegroundServiceStartNotAllowedException`，被 catch 掉只留一行诊断，
         * 表现就是"这一集播完，再也没有下一集"（锁屏时 100% 复现，亮屏时偶发）。
         *
         * 服务活着的时候它本来就在前台，指令直接交给实例即可，
         * 既绕开后台启动限制，也让 stop→play 的顺序变成同步的。
         */
        fun nativeAudio(ctx: Context, json: String) {
            val live = instance
            if (live != null && running) {
                live.main.post { live.handleNative(json) }
                return
            }
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

    /**
     * 页面最后一次心跳的时间（`applyState` 每 5 秒一次）。
     *
     * 用来区分两件长得很像、处理方式却相反的事：
     *   · 页面活着，只是切集慢 → 兜底切集必须**让路**，否则两边同时出声；
     *   · 页面被系统冻结（息屏后 WebView 常见）→ 兜底必须**接手**，
     *     否则这一集播完就永远停住。
     *
     * 息屏时 JS 被冻，`evaluateJavascript` 只排队不执行，心跳会停 ——
     * 所以"心跳停了"正是"页面收不到指令"的可靠信号，比猜更准。
     */
    @Volatile
    private var lastPageBeatAt: Long = 0L

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
     * 原生解码引擎（系统 MediaPlayer）。**只有 nativeMode 为 true 时它才是"当前真正在播的那个"**。
     *
     * 与 [player]（后台接管用的另一个 MediaPlayer）是两条互斥的路：
     * 网页**能**解的格式走 WebView（音画同步、字幕、倍速都在页面里）；
     * 网页**解不了**的（服务端统一转成 m4a 产物）才由它播，
     * 页面退化成遥控器 + 字幕层。
     *
     * 1.7.8 起底层从 libVLC 换成系统 MediaPlayer：音频已经统一转成 AAC，
     * 平台自带解码器就够，不必再扛 43MB 的 libvlc.so（见 NativeAudioEngine）。
     */
    private var audioEngine: NativeAudioEngine? = null
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
                // 所以先把面板立起来，再让引擎去加载（可能要一两秒）。
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
            audioEngine?.resume()
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
            audioEngine?.pause()
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
        if (nativeMode) return (audioEngine?.positionMs ?: 0L) / 1000L
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
                    resourceId = o.optLong("resourceId", 0).coerceAtLeast(0),
                )
                nativeMode = true
                // 页面自己接上了新一集：预置队列作废（那是给"页面没反应"准备的，
                // 留着会让下一集播完后切到过期的条目上）。
                armedQueue.clear()
                cancelEndFallback()
                // 原生模式不存在"接管"这回事：播放器本来就在服务里。
                handoffMode = false
                handoffPreparing = false
                // 来了新一集：把"页面走了要收摊"和"播完没人接"两个计时器都撤掉，
                // 否则它们在息屏弱网下会抢在加载完成之前把服务/兜底关掉。
                cancelLeaveStop()
                cancelEndRetry()
                // 直调通道进来时可能还没进前台（首次指令走的是 onStartCommand，
                // 那条路已经 startForeground 过了）。这里补一次是幂等的。
                running = true
                session?.isActive = true
                runCatching { startForeground(NOTI_ID, buildNotification()) }
                acquireLocks()
                Diagnostics.add("原生播放", "原生加载：${url.take(80)}")
                // 必须接住 Throwable：引擎初始化失败抛的可能是 Error 而非
                // Exception，裸调用会把前台服务一起带走 —— 用户看到的就是
                // "点开音频客户端直接退出"。
                runCatching { engine().play(url, state.position * 1000L) }
                    .onFailure { onEngineBroken(it) }
                // 与播放并行地问一次同一个地址（只取 1KB）。
                // 系统的 MediaPlayer 报错只有 what/extra 两个数字，
                // "服务端 404/401" 和 "解码器不认这种格式" 在它眼里一模一样；
                // 这一问能把网络层的问题先排除掉，写进诊断。
                probeAsync(url)
                // 12 秒后把诊断回传一次：够走完 prepare 与首帧，
                // 又不会拖太久。只在真机排障期需要，见 reportDiag。
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
            // 预置"后面几集"。页面在起播时顺手把下一集的地址取好交过来，
            // 息屏后网页被系统挂起、切集链路断掉时，由原生接着播。
            // 见 scheduleEndFallback。
            "arm" -> handleArm(o)
            // ⚠️ 默认**只停播放器、不拆服务**（见 stopNative 的注释）。
            // 页面切集时也是发 stop —— 那之后 1~3 秒就要播下一集，
            // 这时候把前台服务收掉，下一集就得从后台重新启动前台服务，
            // Android 12+ 必被拒 → "播完这一集就没了"。
            // 只有页面真正离开（onBeforeUnmount）才带 `final:true`。
            "stop" -> stopNative(keepService = !o.optBoolean("final", false))
            else -> Diagnostics.add("原生播放", "未知指令：$cmd")
        }
    }

    private fun engine(): NativeAudioEngine =
        audioEngine ?: NativeAudioEngine(applicationContext) { st -> onNativeState(st) }
            .also { audioEngine = it }

    /**
     * 原生引擎起不来（引擎初始化失败 / 解码器缺失）。
     *
     * 这里只是兜底，只为保证**最坏情况也不闪退**：留一条诊断、把引擎丢掉，
     * 并推一个 error 给页面 —— 页面收到就退回 `info.url`（服务端转码地址）。
     * 慢一点，但一定出得来声。
     */
    private fun onEngineBroken(e: Throwable) {
        Diagnostics.add(
            "原生播放",
            "引擎起不来：${e.javaClass.simpleName}: ${e.message}",
        )
        nativeMode = false
        runCatching { audioEngine?.release() }
        audioEngine = null
        MediaBridge.nativeState(false, 0.0, 0.0, false, "NATIVE_UNAVAILABLE")
    }

    // ============================================================
    //  旁路探测：同一地址，用 Java 的 TLS 栈取 1KB
    // ============================================================

    /**
     * 为什么要它：系统 MediaPlayer 报错只有 `what`/`extra` 两个整数，
     * 「服务端返回 401/404」和「解码器不认这个容器」在它眼里都是
     * `MEDIA_ERROR_UNKNOWN`，光看诊断分不出来。
     *
     * 这里用同一套系统网络栈（HttpURLConnection）再问一次同一个地址：
     *   · 探测失败（4xx/5xx/握手失败）→ 问题在网络或票据层，与解码无关；
     *   · 探测成功（206 + Content-Type）而播放失败 → 问题在解码/容器。
     * 两种结论都写进诊断，下一次就不用猜了。
     *
     * 只取 1KB 就断开（Range），对服务端与流量都可忽略。
     */
    private val probeExec = java.util.concurrent.Executors.newSingleThreadExecutor { r ->
        Thread(r, "ls-native-probe").apply { isDaemon = true }
    }

    private fun probeAsync(url: String) {
        probeExec.execute {
            val verdict = runCatching { probeOnce(url) }
                .getOrElse { "FAIL ${it.javaClass.simpleName}: ${it.message}" }
            Diagnostics.add("原生播放", "探测 $verdict")
            // 只记录，**不主动打断播放**。
            //
            // 1.7.8 起引擎与探针用的是同一套系统网络栈，探针失败不再等于
            // 播放失败 —— 探针带的是自己的 UA、只取 1KB，被限流或拒 UA
            // 都可能失败，而正常播放压根没受影响。真播不出来时引擎自己
            // 会走 onError（带 what/extra），不必由探针代劳。
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
                val diag = Diagnostics.dump(ctx).takeLast(2400)
                // 1.7.8 起不再带 vlc.log：换成系统 MediaPlayer 后它不存在了，
                // 而 MediaPlayer 的错误码（what/extra）已经在 fail() 里
                // 转成人话写进 Diagnostics，这里少一段反而更好读。
                val msg = "[$tag]\n$diag"
                // ⚠️ 2026-10-29：原来把 2600 字符一次性塞进 query，实测**被截断**了 ——
                // access log 里那行只剩 2018 字节，而开头的关键记录（[原生播放]
                // 加载地址、探针结果）恰好是被切掉的那一头，等于白跑一趟。
                // 现在按 700 字符分段：每段编码后约 2.1KB，稳稳在 header 限制内，
                // 到服务端按 p=1/3 拼回去就行。
                val chunks = msg.chunked(700).take(8)
                val total = chunks.size
                for ((idx, c) in chunks.withIndex()) {
                    val enc = java.net.URLEncoder.encode(c, "UTF-8")
                    var sent = false
                    for (b in reportBases(base)) {
                        val ok = runCatching {
                            val conn = (URL(b.trimEnd('/') + "/api/health?kidlog=" + enc +
                                "&p=" + (idx + 1) + "/" + total)
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
                            true
                        }.getOrDefault(false)
                        if (ok) {
                            sent = true
                            break
                        }
                    }
                    if (!sent) break
                }
            }
        }
    }

    /**
     * 回传地址候选：先按家长填的地址，再退一份明文。
     *
     * 为什么要有第二份：8616 是 https，壳里的 HttpsURLConnection 走的是
     * **系统信任库**；而这台机器上 VLC 恰好就卡在"认不出证书"这类问题上，
     * 同一个坑没理由只坑 VLC。8093 是同一份孩子端的明文入口，票据校验一致。
     */
    private fun reportBases(base: String): List<String> {
        val out = mutableListOf(base)
        runCatching {
            val u = java.net.URI(base)
            if (u.scheme.equals("https", true) && u.port == 8616) {
                out.add("http://${u.host}:8093")
            }
        }
        return out
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
     * 原生引擎的状态回调（已切到主线程）。
     *
     * 三件事：刷新面板、把位置推给页面（进度条与字幕都靠它）、
     * 播完通知页面去切下一集。
     */
    private fun onNativeState(st: NativeAudioEngine.State) {
        if (st.error.isNotEmpty()) {
            Diagnostics.add("原生播放", "失败：${st.error}")
            // 失败也要告诉页面：它会退到"这一段放不了"的提示，
            // 而不是永远转圈等一个不会来的声音。
            MediaBridge.nativeState(false, 0.0, 0.0, false, st.error)
            return
        }
        if (!nativeMode) return
        // 已经接手、但还没 prepare 完（可能在等服务端把 m4a 转出来）。
        //
        // 单独一条：页面收到它会撤掉"壳没反应"的 12 秒看门狗，
        // 但**不会**当成"已经出声"去动状态。没有它的话，一本新书
        // 第一次播放（转码 20 秒）会让页面以为原生链路坏了，
        // 退回网页再发一次同样的请求，等于白等两遍。
        if (st.preparing) {
            MediaBridge.nativeState(
                false, 0.0, 0.0, false, "", state.resourceId, preparing = true,
            )
            return
        }
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
            state.resourceId,
        )
        // 播完这一条只推一次太脆：页面那条 evaluateJavascript 落空就没有第二次，
        // 表现是"反复停在同一集"。补几次重推，收到新的 play 就撤销。
        if (st.ended) {
            scheduleEndRetry()
            // 页面被系统挂起时，重推再多也没人接。兜底：过一会儿原生自己播下一集。
            scheduleEndFallback()
        }
    }

    /**
     * 停掉原生引擎。
     *
     * [keepService] = true 表示"只是换了/停了这一条，服务留着" ——
     * 页面切集走的就是这一条。为什么不能顺手 stopSelf：
     *
     *   切集的真实时序是 `stop`（旧书）→ 拉 playInfo → `play`（新书），
     *   中间隔着一次网络请求（息屏时可能好几秒）。这期间只要
     *   `stopForeground` + `stopSelf`，服务就不再是前台服务，
     *   而 Android 12+ **禁止 App 从后台 `startForegroundService`** ——
     *   下一集的 play 会被系统直接拒掉，异常还被 catch 吞了，
     *   用户看到的就是"一集播完就没声了，按播放也没反应"。
     *
     *   所以停播放器但保留前台状态与唤醒锁，由宽限计时器兜"页面真的走了"
     *   这种情况（见 scheduleLeaveStop）。
     */
    private fun stopNative(keepService: Boolean = false) {
        if (!nativeMode && audioEngine == null) {
            if (keepService) return
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
            return
        }
        nativeMode = false
        runCatching { audioEngine?.stop() }
        MediaBridge.nativeState(false, 0.0, 0.0, false, "")
        cancelEndRetry()
        cancelEndFallback()
        // 页面主动 stop = 它要换一条了（切集 / 退回转码 / 离开）。
        // 预置队列作废：留着会在下一集播完后切到一条已经过时的地址上。
        armedQueue.clear()
        if (keepService) {
            state = state.copy(playing = false)
            updateSession()
            // 页面没在播了，但服务继续留着等下一集。
            scheduleLeaveStop()
            return
        }
        cancelLeaveStop()
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
     * 亮屏实测 1~3 秒）。以前没有这个计时器，只要处于接管态就无限续命 ——
     * 孩子退出播放页、甚至退出 App 之后，声音还在响。
     *
     * ⚠️ 息屏时给 60 秒而不是 12 秒（2026-11-02）：
     * 锁屏后 WebView 的后台网络被降优先级，拉一次 playInfo 花十几秒是常事，
     * 12 秒的宽限会在"下一集的接口还没回来"时把服务收掉 ——
     * 声音断在这里，而且再也起不来（后台启动前台服务被系统禁止）。
     * 亮屏时仍按 12 秒收，保证退出播放页后通知栏不会赖着不走。
     */
    private val leaveGraceMs: Long
        get() = if (screenInteractive()) 12_000L else 60_000L

    private fun scheduleLeaveStop() {
        if (leaveStop != null) return
        val ms = leaveGraceMs
        val r = Runnable {
            leaveStop = null
            shutdownPlayback("页面已离开 ${ms / 1000} 秒")
        }
        leaveStop = r
        main.postDelayed(r, ms)
    }

    private fun cancelLeaveStop() {
        leaveStop?.let { main.removeCallbacks(it) }
        leaveStop = null
    }

    private fun screenInteractive(): Boolean = try {
        (getSystemService(Context.POWER_SERVICE) as? PowerManager)?.isInteractive ?: true
    } catch (e: Exception) {
        true
    }

    // ============================================================
    //  播完 → 切下一集：原生侧的重试兜底
    // ============================================================

    /**
     * "这一集播完了"推给页面之后的重试。
     *
     * 现状只有一次机会：VLC 报 EndReached → `nativeState(ended=true)` →
     * 页面 `onNativeAudioEnded()` → 切下一集。这条 `evaluateJavascript`
     * 一旦落空（页面刚被重新加载、闭包换掉、WebView 那一瞬间在忙），
     * 就没有第二次 —— 用户看到的就是"反复停在同一集"。
     *
     * 所以补成"推三次"：5s / 11s / 17s。收到新的 play 指令就撤销
     * （正常切集 1~2 秒就回来了，多推的两下根本不会发生）。
     *
     * ⚠️ 第一次刻意等到 5 秒：**服务器上可能还跑着旧版页面**，那一版没有
     * "同一集只切一次"的闸门。切集链路（拉接口 → 起播）要是有一次超过
     * 2 秒，重推就会被旧页面当成新的一次播完，连跳两集。5 秒的余量足够
     * 让页面的 `stop` 先到（那一条会撤销本计时器）。
     */
    private var endRetry: Runnable? = null
    private var endRetryLeft = 0

    private fun scheduleEndRetry() {
        cancelEndRetry()
        endRetryLeft = 3
        val r = object : Runnable {
            override fun run() {
                endRetry = null
                if (endRetryLeft <= 0) return
                endRetryLeft--
                Diagnostics.add("原生播放", "重推「播完了」（剩 $endRetryLeft 次）")
                MediaBridge.nativeState(false, state.duration.toDouble(), state.duration.toDouble(), true, "")
                if (endRetryLeft > 0) {
                    endRetry = this
                    main.postDelayed(this, 6000L)
                }
            }
        }
        endRetry = r
        main.postDelayed(r, 5000L)
    }

    private fun cancelEndRetry() {
        endRetry?.let { main.removeCallbacks(it) }
        endRetry = null
        endRetryLeft = 0
    }

    // ============================================================
    //  「页面失联时自己接上」—— 预置队列 + 兜底切集
    // ============================================================

    private data class ArmedItem(
        val url: String,
        val resourceId: Long,
        val title: String,
        val subtitle: String,
        val poster: String,
        val duration: Long,
    )

    /**
     * 页面预置的"后面几集"。
     *
     * 为什么需要它：切集本来只该有一份实现（见 MediaBridge 顶部），但那份
     * 跑在 WebView 里。**息屏之后系统会冻结网页的 JS**：页面收不到 ended、
     * 也跑不完"拉接口 → 发 play"这条链路，表现就是"这一集播完就停住"。
     * 小米 / OPPO 这类 ROM 上尤其明显，因为它们的后台管控更激进。
     *
     * 所以让页面在**还醒着的时候**把下一集地址先取好交过来；真到那一刻
     * 页面没接上，由原生自己播，播完再把 resourceId 推回去让页面把路由对齐。
     */
    private var armedQueue: MutableList<ArmedItem> = mutableListOf()
    private var endFallback: Runnable? = null

    /**
     * 兜底延迟。
     *
     * 要够长：页面醒着但网络慢时，"收到 ended → 下一集出声"实测 3~10 秒，
     * 抢在它前面就会两边同时播同一集（听起来是从头放两遍）。
     * 又不能太长：锁屏的孩子会以为故事就这么结束了。
     */
    private val endFallbackMs = 14_000L

    /**
     * 页面心跳多久之内算"还活着"（见 [scheduleEndFallback]）。
     *
     * 页面每 5 秒推一次状态，所以 20 秒 = 连续漏掉 4 次，基本可以断定
     * 已经被系统冻结（息屏后最常见），而不是"刚好在忙"。
     */
    private val PAGE_BEAT_ALIVE_MS = 20_000L

    private fun handleArm(o: JSONObject) {
        val arr = o.optJSONArray("items")
        val q = mutableListOf<ArmedItem>()
        if (arr != null) {
            for (i in 0 until arr.length()) {
                val it = arr.optJSONObject(i) ?: continue
                val u = it.optString("url", "")
                if (u.isBlank()) continue
                q.add(
                    ArmedItem(
                        url = u.take(4096),
                        resourceId = it.optLong("resourceId", 0).coerceAtLeast(0),
                        title = it.optString("title", "").take(120),
                        subtitle = it.optString("subtitle", "").take(120),
                        poster = it.optString("poster", "").take(2048),
                        duration = it.optLong("duration", 0).coerceIn(0, 24 * 3600),
                    )
                )
            }
        }
        armedQueue = q
        Diagnostics.add("原生播放", "预置下一集 ${q.size} 条")
    }

    private fun scheduleEndFallback() {
        cancelEndFallback()
        val r = Runnable {
            endFallback = null
            // 页面还活着就先让它切。
            //
            // 这条判据以前是 `if (!nativeMode) return`，只对"原生在播"生效 ——
            // 于是**接管路径**（页面 `<audio>` 被交过来播）播完永远没人接：
            // 页面冻着收不到 ended，壳这边又直接 return，声音就停在那儿。
            //
            // 改成看心跳：页面每 5 秒推一次 applyState，真活着就一定会刷新；
            // 息屏后 WebView 冻结、心跳停摆，才是该接手的信号。
            // 20 秒的窗口足以覆盖"页面醒着但网络慢、切集要几秒"的情况，
            // 又不至于让孩子对着静音干等太久。
            val sinceBeat = System.currentTimeMillis() - lastPageBeatAt
            if (lastPageBeatAt > 0 && sinceBeat < PAGE_BEAT_ALIVE_MS) {
                Diagnostics.add(
                    "原生播放",
                    "播完了，页面还活着（${sinceBeat / 1000}s 前有心跳），等它切",
                )
                return@Runnable
            }
            if (armedQueue.isEmpty()) {
                Diagnostics.add("原生播放", "播完了，没有预置下一集，等页面")
                return@Runnable
            }
            val next = armedQueue.removeAt(0)
            Diagnostics.add("原生播放", "页面没接上，兜底播下一集：${next.title.take(30)}")
            playArmed(next)
        }
        endFallback = r
        main.postDelayed(r, endFallbackMs)
    }

    private fun cancelEndFallback() {
        endFallback?.let { main.removeCallbacks(it) }
        endFallback = null
    }

    /**
     * 原生自己接下一集。
     *
     * 走的是和页面点播同一个引擎入口，只是地址来自预置队列 ——
     * 不依赖页面、不依赖接口请求，所以息屏也照播。
     */
    private fun playArmed(item: ArmedItem) {
        // 兜底可能从"接管"路径过来（页面冻死时，接管用的播放器刚播完）。
        // 先把它收掉，否则新旧两个播放器会同时出声 —— 听起来像重音。
        runCatching { releasePlayer() }
        state = state.copy(
            active = true,
            playing = true,
            title = item.title.ifBlank { state.title },
            subtitle = item.subtitle,
            poster = item.poster.ifBlank { state.poster },
            duration = if (item.duration > 0) item.duration else 0,
            position = 0,
            url = item.url,
            video = false,
            resourceId = item.resourceId,
        )
        nativeMode = true
        handoffMode = false
        handoffPreparing = false
        cancelLeaveStop()
        running = true
        session?.isActive = true
        runCatching { startForeground(NOTI_ID, buildNotification()) }
        acquireLocks()
        updateSession()
        // 先告诉页面"现在播的是第几条"：回到前台时它据此改路由，
        // 否则页面还显示着上一集的标题，进度条也会从 0 开始跳。
        MediaBridge.nativeState(true, 0.0, item.duration.toDouble(), false, "", item.resourceId)
        runCatching { engine().play(item.url, 0L) }.onFailure { onEngineBroken(it) }
    }

    /**
     * 收摊：停掉**所有**可能出声的东西，然后收掉前台服务。
     *
     * 三路都要停 —— 少停一路就是"退出后还在响"：
     *   · [player]      后台接管用的 MediaPlayer（mp4/mp3）
     *   · [audioEngine] 原生引擎（服务端转好的 m4a）
     *   · 前台服务本身 + 唤醒锁 + 媒体面板
     */
    private fun shutdownPlayback(reason: String) {
        Diagnostics.add("播放", "收工：$reason")
        cancelLeaveStop()
        cancelEndRetry()
        runCatching { releasePlayer() }
        nativeMode = false
        handoffMode = false
        handoffPreparing = false
        runCatching { audioEngine?.stop() }
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
                // 但页面可能已经被系统冻结（息屏后 WebView 的 JS 不执行，
                // evaluateJavascript 只排队），这时"交还"等于没人接这一棒。
                // 补一次兜底：页面还活着会让路，冻住了才由原生自己播下一集。
                scheduleEndFallback()
            }
            p.setOnErrorListener { _, what, extra ->
                Diagnostics.add("接管", "原生播放失败 what=$what extra=$extra，交还网页")
                val pos = nativePositionSec()
                releasePlayer()
                MediaBridge.giveBack(pos, ended = false)
                false
            }
            // 接管超时（2026-11-02）：`prepareAsync` 在某些网络下**永远不回调**
            // （网盘直链排队、DNS 卡住、Wi-Fi 刚断）。没有这个计时器，
            // 页面就一直停在"等待交接"里，而壳这边也没有播放器在响 ——
            // 用户看到的就是"息屏后它自己停了，怎么点都没声"。
            val guard = Runnable {
                if (token != handoffToken || !handoffPreparing) return@Runnable
                Diagnostics.add("接管", "15 秒仍未就绪，放弃接管、交还网页")
                handoffPreparing = false
                releasePlayer()
                MediaBridge.giveBack(nativePositionSec(), ended = false)
            }
            main.postDelayed(guard, 15_000L)
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
                    audioEngine?.seekTo(pos)
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
        // 页面还活着（心跳）。息屏后 JS 被冻结，这里就不会再更新 ——
        // 兜底切集靠它判断"该不该让路"，见 lastPageBeatAt。
        lastPageBeatAt = System.currentTimeMillis()
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
        cancelEndRetry()
        releasePlayer()
        runCatching { audioEngine?.release() }
        audioEngine = null
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
    /**
     * 当前在播的资源 ID。
     *
     * 为什么原生也要知道：息屏后网页被系统挂起，切集这条链路（页面拉接口
     * 再发 play）会断在这里。兜底切集是原生自己做的，做完必须告诉页面
     * "现在播的是哪一条"，否则回到前台时页面显示的还是上一集的标题和进度。
     */
    val resourceId: Long = 0,
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
                    resourceId = o.optLong("resourceId", 0).coerceAtLeast(0),
                )
            } catch (e: Exception) {
                null
            }
        }
    }
}
