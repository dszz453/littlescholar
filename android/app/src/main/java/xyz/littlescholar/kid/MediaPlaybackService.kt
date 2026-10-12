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

        // O 以下用大图样式占位；O+ 由系统媒体面板接管
        return builder.build()
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
