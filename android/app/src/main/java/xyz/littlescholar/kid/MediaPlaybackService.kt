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
 * 媒体播放前台服务（保活 + 系统媒体控制中心）。
 *
 * 两个职责，缺一不可：
 *
 *  1. **保活**：Android 8+ 里，App 退到后台后进程随时可能被回收，
 *     WebView 里的音频会被系统杀掉。前台服务把进程优先级提到最高。
 *     再加上 CPU 唤醒锁与 WiFi 锁，锁屏播放才真的稳。
 *
 *  2. **系统媒体控制中心**：这是 2.4.0 补的。
 *
 * ============ ⚠️ 为什么以前"没有常驻控制中心" ============
 *
 * 旧实现只 `addAction(上一集/暂停/下一集)` 建了一条**普通通知**：
 *   · 没有 `MediaStyle` → 系统不认这是媒体通知，
 *     下拉通知栏不会展开成大卡片，也没有进度条；
 *   · 没有 `setMediaSession(token)` → Android 11+ 的媒体控制区
 *     （通知栏顶部那块、锁屏、蓝牙/车机）**根本不知道有东西在播**；
 *   · 网页里的 `navigator.mediaSession` 在 WebView 里**不会**注册到系统
 *     （那是 Chrome 浏览器自己的能力），所以等它也等不到。
 *
 * 结果就是：播着音频，通知栏只有一条普通消息，锁屏上什么都没有。
 *
 * 现在补上 MediaSessionCompat + MediaStyle：标题/副标题/封面/进度/
 * 上一集/暂停/下一集 全在系统面板里，锁屏、耳机线控、车机都能控制。
 */
class MediaPlaybackService : Service() {

    companion object {
        const val CHANNEL_ID = "ls_playback"
        const val NOTI_ID = 1001

        const val ACTION_START = "xyz.littlescholar.kid.START"
        const val ACTION_STOP = "xyz.littlescholar.kid.STOP"
        const val ACTION_PREV = "xyz.littlescholar.kid.PREV"
        const val ACTION_TOGGLE = "xyz.littlescholar.kid.TOGGLE"
        const val ACTION_NEXT = "xyz.littlescholar.kid.NEXT"

        /**
         * 服务是不是真的在跑。
         *
         * ⚠️ 外面**不能靠自己的布尔字段判断** —— 通知栏按钮、Activity 重建、
         * 系统重启服务都会让两边的状态漂。漂了的后果很隐蔽：play 事件看到
         * "我以为还开着"就跳过 start，于是整个播放期间一条保活都没有。
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
                // Android 12+ 从后台启动前台服务有限制。失败不该让播放挂掉，
                // 只是没有保活而已 —— 所以吞掉，由播放链路自己继续。
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

        /**
         * 网页推来的播放状态（标题/封面/进度…）。
         *
         * 走静态方法而不是 bind：Activity 与服务本来就不该有生命周期耦合，
         * 而页面调用发生在 JavaBridge 线程，只需要一份"最新状态"。
         */
        fun pushState(json: String) {
            val st = MediaState.parse(json) ?: return
            instance?.applyState(st)
        }
    }

    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    private var session: MediaSessionCompat? = null
    private var state: MediaState = MediaState()
    private var lastPosterUrl = ""
    private var poster: Bitmap? = null
    private val io = Executors.newSingleThreadExecutor()

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
            ACTION_TOGGLE -> {
                MediaBridge.toggle()
                return START_STICKY
            }
            ACTION_NEXT -> {
                MediaBridge.next()
                return START_STICKY
            }
            else -> {
                startForeground(NOTI_ID, buildNotification())
                acquireLocks()
                running = true
                session?.isActive = true
            }
        }
        return START_STICKY
    }

    // ============================================================
    //  MediaSession：系统媒体控制中心
    // ============================================================

    private fun createSession() {
        val s = MediaSessionCompat(this, "LittleScholar")
        s.setCallback(object : MediaSessionCompat.Callback() {
            // 这些回调来自**系统**（锁屏、耳机线控、车机、蓝牙），
            // 一律转给网页执行 —— 换集/播放逻辑只存在一份。
            override fun onPlay() = MediaBridge.toggle()
            override fun onPause() = MediaBridge.toggle()
            override fun onSkipToNext() = MediaBridge.next()
            override fun onSkipToPrevious() = MediaBridge.prev()
            override fun onSeekTo(pos: Long) = MediaBridge.seekTo(pos)
            override fun onStop() = MediaBridge.toggle()
        })
        s.isActive = true
        session = s
        updateSession()
    }

    /** 把最新状态灌进 session（元数据 + 播放状态 + 通知）。 */
    fun applyState(st: MediaState) {
        state = st
        if (st.poster.isNotEmpty() && st.poster != lastPosterUrl) {
            lastPosterUrl = st.poster
            loadPoster(st.poster)
        }
        updateSession()
        // 通知要跟着换（标题、封面、按钮），但只在服务已经在前台时刷新
        if (running) {
            val mgr = getSystemService(NotificationManager::class.java)
            mgr?.notify(NOTI_ID, buildNotification())
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
            NotificationManager.IMPORTANCE_LOW // LOW：不响铃、不震动，避免打扰
        ).apply {
            description = getString(R.string.noti_channel_desc)
            setShowBadge(false)
            // 媒体通知必须让它显示在锁屏上（Android 10+ 由系统媒体区接管）
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
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
        var compact = mutableListOf<Int>()
        if (state.hasPrev) {
            compact.add(actions.size)
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_prev), action(ACTION_PREV)))
        }
        compact.add(actions.size)
        actions.add(NotificationCompat.Action(0, getString(R.string.noti_toggle), action(ACTION_TOGGLE)))
        if (state.hasNext) {
            compact.add(actions.size)
            actions.add(NotificationCompat.Action(0, getString(R.string.noti_next), action(ACTION_NEXT)))
        }
        actions.forEach { builder.addAction(it) }

        // ⚠️ 这一行才是"常驻控制中心"的关键：
        // 没有 MediaStyle + setMediaSession，系统不会把这 App 当成正在播放的媒体。
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
 * 解析失败就返回 null（当作"没收到"），绝不让桥抛异常 ——
 * 桥一抛，页面的后续脚本就断了。
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
                )
            } catch (e: Exception) {
                null
            }
        }
    }
}
