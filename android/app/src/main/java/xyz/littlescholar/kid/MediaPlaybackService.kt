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
import android.os.IBinder
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
                giveBack()
                releaseLocks()
                session?.isActive = false
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
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
                doPause()
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

    private fun doPause() {
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
            override fun onPause() = doPause()
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

            override fun onStop() = doPause()
        })
        s.isActive = true
        session = s
        updateSession()
    }

    /** 把最新状态灌进 session（元数据 + 播放状态 + 通知）。 */
    fun applyState(st: MediaState) {
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

    override fun onDestroy() {
        running = false
        instance = null
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
