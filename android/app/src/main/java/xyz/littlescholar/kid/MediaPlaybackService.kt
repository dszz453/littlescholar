package xyz.littlescholar.kid

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import android.os.PowerManager
import androidx.core.app.NotificationCompat

/**
 * 媒体播放前台服务。
 *
 * 存在的唯一理由：Android 8+ 中，App 退到后台后普通进程随时可能被回收，
 * 导致 WebView 里的音频被系统杀掉。挂一个前台服务可以把进程优先级提到
 * 最高，从而让"锁屏听故事"稳定工作。
 *
 * 它不接管播放控制 —— 播放控制由 WebView 内的 MediaSession 完成，
 * 通知栏的播放/暂停按钮由系统媒体面板（MediaStyle）呈现。
 *
 * ============ ⚠️ 2026-10-16：这个服务曾经**从来没有被启动过** ============
 *
 * `start()` / `stop()` 写好了、清单声明了、唤醒锁也写了，但**全项目没有
 * 任何一处调用** —— 一段完整的死代码。后果是"息屏播放"这件事表面上
 * 有实现、实际全靠运气：
 *   · 没有前台服务 → App 一进后台进程优先级就掉下去，可能被回收/冻结；
 *   · 唤醒锁随之**永远不会被 acquire**（服务都没起，谁来拿锁），
 *     `WAKE_LOCK` 权限白声明；
 *   · 表现就是「息屏播完一集不切下一集，点开提示网络错误」。
 *
 * 接线补在 [MainActivity] 的 PlaybackBridge（外壳侧），**没有动孩子端一行**：
 * 这个壳的职责本来就写着「息屏播放、返回键、下载/外链」。
 */
class MediaPlaybackService : Service() {

    companion object {
        const val CHANNEL_ID = "ls_playback"
        const val NOTI_ID = 1001

        const val ACTION_START = "xyz.littlescholar.kid.START"
        const val ACTION_STOP = "xyz.littlescholar.kid.STOP"
        // 通知栏上的三个按钮。它们不自己实现换集，而是通过 MediaBridge
        // 转给播放页 —— 换集逻辑只存在一份（见 MediaBridge 的注释）。
        const val ACTION_PREV = "xyz.littlescholar.kid.PREV"
        const val ACTION_TOGGLE = "xyz.littlescholar.kid.TOGGLE"
        const val ACTION_NEXT = "xyz.littlescholar.kid.NEXT"

        /**
         * 服务是不是真的在跑。
         *
         * ⚠️ 外面**不能靠自己的布尔字段判断** —— 通知栏按钮、Activity 重建、
         * 系统重启服务都会让两边的状态漂。漂了的后果很隐蔽：play 事件看到
         * "我以为还开着"就跳过 start，于是**整个播放期间一条保活都没有**，
         * 而日志上看不出任何异常。所以状态只有一份，放在服务的生命周期里。
         */
        @Volatile
        private var running = false

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
            }
        }

        fun stop(ctx: Context) {
            // ⚠️ 这里用 startService 送一个 ACTION_STOP，而不是 stopService()：
            // 我们要**先释放唤醒锁、再摘掉通知**，这段顺序逻辑在服务的
            // onStartCommand 里。而且此刻服务一定在运行（前台），
            // 所以不触发"后台不能启动服务"的限制。
            // 兜底：万一 startService 抛了（ROM 差异），退回 stopService，
            // 至少 onDestroy 还会释放锁。
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
    }

    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                releaseLocks()
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return START_NOT_STICKY
            }
            // 通知栏按钮：转给网页执行。服务本身不持播放状态，
            // 也不刷新通知文案 —— "正在播放哪一集"由页面文案承担，
            // 这里只做"按一下就换集"，简单可靠。
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
            }
        }
        // 被系统杀掉后自动重启，避免长音频中途断掉
        return START_STICKY
    }

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
            .setContentTitle(getString(R.string.noti_title))
            .setContentText(getString(R.string.noti_text))
            .setSmallIcon(R.drawable.ic_stat_play)
            .setContentIntent(pi)
            .setOngoing(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            // 上一首 / 播放暂停 / 下一首。
            // 系统媒体面板（MediaSession）也会出现这三个，两者并存不冲突：
            // 面板在锁屏与下拉通知里，这里的按钮则保证"通知卡片被展开/被折叠"
            // 的机型上也有入口。
            .addAction(0, getString(R.string.noti_prev), action(ACTION_PREV))
            .addAction(0, getString(R.string.noti_toggle), action(ACTION_TOGGLE))
            .addAction(0, getString(R.string.noti_next), action(ACTION_NEXT))

        // O 以下用大图样式占位；O+ 由系统媒体面板接管
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

    /**
     * 拿两道锁。
     *
     * ⚠️ **前台服务只解决"进程优先级"，不解决"CPU 睡下去"**，所以两件事都要做：
     *
     *   · `PARTIAL_WAKE_LOCK`：屏幕熄灭后 CPU 会进入 suspend。没有它，
     *     音频会在播到一半断掉，而且 `ended` 事件不再触发 —— 定时器停在半路，
     *     于是"播完一集不切下一集"，而用户点开 App 时看到的是页面超时报的错。
     *
     *   · WiFi 锁：WLAN 在息屏几分钟后会进省电模式。没有它，取流会卡住/超时，
     *     而**同一条 URL 在亮屏时完全正常** —— 这是最容易被误判成
     *     "网盘限速"或"服务端有问题"的那一类现象。
     *     模式要按版本挑：`WIFI_MODE_FULL_HIGH_PERF` 在 Android 10+ 已被 OEM
     *     普遍无视（就是它被 deprecated 的原因），拿它等于锁了个寂寞；
     *     API 29+ 用 `WIFI_MODE_FULL_LOW_LATENCY`（专为实时音视频设计，
     *     且要求调用方持有 mediaPlayback 前台服务 —— 我们正好有）。
     *
     * ⚠️ `acquire()` 不带超时会被 lint 报 `WakelockTimeout`。这里**故意**不给超时：
     * 服务的生命周期就是"播放期间"，由播放事件精确启停；而唤醒锁是**随进程
     * 一起释放**的，不存在"忘记释放导致一直耗电"的路径。给固定超时反而会在
     * 长音频播到一半时丢锁。
     */
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
            // 重复释放会抛异常，忽略
        }
        try {
            wifiLock?.let { if (it.isHeld) it.release() }
        } catch (_: RuntimeException) {
            // 同上
        }
        wakeLock = null
        wifiLock = null
    }

    override fun onDestroy() {
        running = false
        releaseLocks()
        super.onDestroy()
    }
}
