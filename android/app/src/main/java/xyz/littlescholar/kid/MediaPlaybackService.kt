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
    }

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
        MediaBridge.play()
    }

    private fun doPause() {
        if (handoffMode) {
            runCatching { player?.pause() }
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
        val p = player ?: return state.position
        return try {
            (p.currentPosition / 1000L).coerceAtLeast(0)
        } catch (e: Exception) {
            state.position
        }
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
        if (st.url.isBlank() || !st.playing) return
        if (st.url.startsWith("blob:", true) || st.url.startsWith("data:", true)) {
            Diagnostics.add("接管", "blob/data 地址无法交给原生，保持网页播放")
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
        // ⚠️ 后台接管期间，网页是"暂停并静音"的，它推来的 playing/position
        // 会把面板刷成"已暂停"。这段时间以原生播放器为准，忽略这两项。
        state = if (handoffMode) {
            st.copy(playing = state.playing, position = nativePositionSec())
        } else {
            st
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
                )
            } catch (e: Exception) {
                null
            }
        }
    }
}
