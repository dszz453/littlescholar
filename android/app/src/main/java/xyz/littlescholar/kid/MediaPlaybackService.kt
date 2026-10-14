package xyz.littlescholar.kid

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
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

        fun start(ctx: Context) {
            val i = Intent(ctx, MediaPlaybackService::class.java).apply {
                action = ACTION_START
            }
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                ctx.startForegroundService(i)
            } else {
                ctx.startService(i)
            }
        }

        fun stop(ctx: Context) {
            val i = Intent(ctx, MediaPlaybackService::class.java).apply {
                action = ACTION_STOP
            }
            ctx.startService(i)
        }
    }

    private var wakeLock: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                releaseWakeLock()
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
                acquireWakeLock()
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

    private fun acquireWakeLock() {
        if (wakeLock?.isHeld == true) return
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = pm.newWakeLock(
            PowerManager.PARTIAL_WAKE_LOCK,
            "littlescholar:playback"
        ).apply {
            // 不设超时 —— 由服务销毁时释放；设超时会导致长音频中途被唤醒锁丢弃
            setReferenceCounted(false)
            acquire()
        }
    }

    private fun releaseWakeLock() {
        wakeLock?.let {
            if (it.isHeld) {
                try {
                    it.release()
                } catch (_: RuntimeException) {
                    // 重复释放会抛异常，忽略
                }
            }
        }
        wakeLock = null
    }

    override fun onDestroy() {
        releaseWakeLock()
        super.onDestroy()
    }
}
