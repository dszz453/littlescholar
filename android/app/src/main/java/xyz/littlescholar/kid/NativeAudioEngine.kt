package xyz.littlescholar.kid

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioManager
import android.media.MediaPlayer
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import kotlin.math.abs

/**
 * 原生音频引擎（Android 系统 MediaPlayer）。
 *
 * ============ 为什么不再用 libVLC ============
 *
 * 1.6.0 引入 libVLC 的理由是"客户端要能自己解 wma"。但那套前提已经变了：
 * 服务端会把 wma/ape/amr 统一转成 **m4a（AAC in MP4）**，而 AAC 是 Android
 * 平台**内置**解码器，从 Android 5.0 起每台设备都有 —— 原生完全不需要
 * 自带解码库。
 *
 * 而 libVLC 在这批平板上一直没能连上服务端（2026-10-29 实录：Java 探针
 * 对同一地址拿到 206，VLC 12 秒里一条 TCP 都没建，服务端 access log 里
 * `/api/stream` 计数为 0；`--ipv4` 也没能救回来）。结果就是每次播放
 * **先白等 12 秒看门狗**才退回转码，比直接用系统播放器还慢。
 *
 * 换掉它还顺手解决三件事：
 *   · APK 从约 43MB 降到几 MB（libvlc.so 的 arm64 就有 43MB）；
 *   · 不用再担心 VLC 自带的 CA 库认不认 Let's Encrypt 新根；
 *   · 网络栈就是系统那一套，IPv4/IPv6 回退、代理、证书全跟着系统走。
 *
 * ============ 为什么由 Service 持有 ============
 *
 * 后台播放、锁屏控制、通知栏按钮都要在页面被挂起后继续工作。
 * 引擎由 [MediaPlaybackService] 持有，Activity 只转发指令与状态，
 * 这样"前台播放"和"切后台继续播"用的是**同一个**播放器，不需要再做交接。
 *
 * ============ 与 VlcAudioEngine 的关系 ============
 *
 * 对外接口完全一致（play/pause/resume/seekTo/stop/release/snapshot +
 * [State] 回调），所以服务层只是换了一个实现。唯一的扩展是 [State.preparing]：
 * MediaPlayer 的 `prepareAsync()` 在等服务端转码时可能几十秒不出声，
 * 页面需要知道"壳已经接手了，只是还在准备"，否则它会以为壳没反应而回退。
 */
class NativeAudioEngine(
    context: Context,
    /** 状态变化回调。**在主线程**调用。 */
    private val onState: (State) -> Unit,
) {
    /** 一次状态快照。页面靠它驱动进度条与字幕。 */
    data class State(
        val playing: Boolean,
        val positionMs: Long,
        val durationMs: Long,
        val ended: Boolean = false,
        val error: String = "",
        /** 已收到 play 指令、正在 prepareAsync（可能在等服务端转码）。 */
        val preparing: Boolean = false,
    )

    private val appCtx = context.applicationContext
    private val main = Handler(Looper.getMainLooper())
    private val audioManager: AudioManager? =
        appCtx.getSystemService(AudioManager::class.java)

    private companion object {
        /**
         * prepareAsync 的超时。
         *
         * 比 VLC 时代（15 秒）宽得多，因为这条路上**可能真的在等转码**：
         * `/api/audio/:id` 是同步接口，第一次请求要现场取源 + ffmpeg（实测
         * 3 分钟绘本 21.6 秒）。定太短会把"正在转"当成"打不开"，
         * 然后页面回退到 `<audio>` 又把同一个请求重来一遍 —— 白等两遍。
         *
         * 也不能无限等：真连不上时要让页面尽快改用网页播放。
         */
        const val PREPARE_TIMEOUT_MS = 60_000L

        /** 进度轮询间隔。MediaPlayer 没有 VLC 的 TimeChanged 事件，只能自己问。 */
        const val TICK_MS = 500L

        /** 同一个地址再次 play 时，位置差多少以内算"接着播"，不必 seek。 */
        const val SEEK_TOLERANCE_MS = 1500L
    }

    private var player: MediaPlayer? = null
    private var durationMs = 0L

    /** 播起来之后要跳到的位置（prepare 完成前 seek 会被丢弃）。 */
    private var pendingSeekMs = 0L

    /** prepare 阶段超时看门狗。 */
    private var prepareGuard: Runnable? = null

    /** 当前资源地址；同一条资源再次 play 时只 seek/继续，不重建播放器。 */
    var currentUrl: String = ""
        private set

    /** 本条资源最近一次失败的原因；空串表示没失败。 */
    private var lastError = ""

    /** 已发 play、还没 prepare 完。会透传给页面（见 State.preparing）。 */
    private var preparing = false

    /** 当前是否因为"丢了音频焦点"（来电、别的 App 唱歌）而自动暂停。 */
    private var pausedByFocus = false

    private var hasFocus = false
    private var focusRequest: Any? = null

    private val audioAttrs: AudioAttributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_MEDIA)
        .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
        .build()

    private val focusListener = AudioManager.OnAudioFocusChangeListener { change ->
        when (change) {
            AudioManager.AUDIOFOCUS_LOSS -> {
                pausedByFocus = false
                pause()
            }
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT -> {
                if (isPlaying) {
                    pausedByFocus = true
                    pause()
                }
            }
            AudioManager.AUDIOFOCUS_GAIN -> {
                if (pausedByFocus) {
                    pausedByFocus = false
                    resume()
                }
            }
            // CAN_DUCK 不处理：儿童绘本是朗读内容，压低音量反而听不清，
            // 让系统的默认行为（对方也降）更适合这里。
            else -> Unit
        }
    }

    val isPlaying: Boolean
        get() = runCatching { player?.isPlaying == true }.getOrDefault(false)

    val positionMs: Long
        get() = runCatching { player?.currentPosition?.toLong() ?: 0L }.getOrDefault(0L)

    val lengthMs: Long
        get() = if (durationMs > 0) durationMs
        else runCatching { player?.duration?.toLong() ?: 0L }.getOrDefault(0L)

    /**
     * 播放一条音频。
     *
     * 同一条 url 正在播时只做 seek/继续，**不重建播放器** —— 重建会带来
     * 1~2 秒静音和"从头开始"，切前后台时正好撞上。
     */
    fun play(url: String, startMs: Long) {
        if (url.isBlank()) {
            fail("地址为空")
            return
        }
        val p = player
        if (p != null && currentUrl == url && !preparing) {
            if (startMs > 0 && abs(p.currentPosition - startMs) > SEEK_TOLERANCE_MS) {
                runCatching { p.seekTo(startMs.toInt()) }
            }
            if (!isPlaying) {
                if (!requestFocus()) {
                    fail("拿不到音频焦点")
                    return
                }
                runCatching { p.start() }
            }
            emit()
            return
        }

        releasePlayer()
        currentUrl = url
        lastError = ""
        durationMs = 0
        pendingSeekMs = startMs.coerceAtLeast(0)
        preparing = true

        if (!requestFocus()) {
            preparing = false
            fail("拿不到音频焦点")
            return
        }

        val mp = MediaPlayer()
        player = mp
        try {
            mp.setAudioAttributes(audioAttrs)
            // 锁屏继续播：让 MediaPlayer 自己持唤醒锁。前台服务也持有，
            // 但那是另一个锁 —— 服务被系统降级时这个仍能撑住音频输出。
            mp.setWakeMode(appCtx, PowerManager.PARTIAL_WAKE_LOCK)
            mp.setDataSource(url)
        } catch (e: Exception) {
            preparing = false
            releasePlayer()
            fail("打不开这段音频：${e.javaClass.simpleName}")
            return
        }

        mp.setOnPreparedListener { p2 ->
            // 已经换了源或已 release：这条回调作废，立刻释放，
            // 否则会有两个播放器同时在响（切集时最典型）。
            if (player !== p2) {
                runCatching { p2.release() }
                return@setOnPreparedListener
            }
            disarmPrepareGuard()
            preparing = false
            val d = runCatching { p2.duration.toLong() }.getOrDefault(0L)
            if (d > 0) durationMs = d
            pendingSeekMs.takeIf { it > 0 }?.let {
                runCatching { p2.seekTo(it.toInt()) }
                pendingSeekMs = 0
            }
            runCatching { p2.start() }
                .onFailure {
                    fail("播放失败：${it.javaClass.simpleName}")
                    return@setOnPreparedListener
                }
            startTick()
            emit()
        }
        mp.setOnCompletionListener {
            stopTick()
            player = null
            emit(ended = true)
            runCatching { it.release() }
        }
        mp.setOnErrorListener { _, what, extra ->
            fail(describeError(what, extra))
            true
        }

        armPrepareGuard(mp)
        runCatching { mp.prepareAsync() }
            .onFailure {
                preparing = false
                fail("准备失败：${it.javaClass.simpleName}")
            }
        // 先告诉页面"壳已经接手，还在准备"。没有这一句，页面那 12 秒
        // 首状态看门狗会在转码期间到点，回退去重发一次同样的请求。
        emit()
    }

    fun pause() {
        stopTick()
        runCatching { if (isPlaying) player?.pause() }
        emit()
    }

    fun resume() {
        val p = player ?: return
        if (preparing) return
        if (!requestFocus()) return
        runCatching { p.start() }
        startTick()
        emit()
    }

    fun seekTo(ms: Long) {
        val p = player ?: return
        val target = ms.coerceAtLeast(0)
        if (preparing) {
            // 还没 prepared，seek 会被吞掉 —— 记下来等 onPrepared 再跳
            pendingSeekMs = target
            return
        }
        runCatching { p.seekTo(target.toInt()) }
        emit()
    }

    fun stop() {
        currentUrl = ""
        releasePlayer()
        emit()
    }

    fun release() {
        releasePlayer()
        abandonFocus()
    }

    fun snapshot(): State = State(isPlaying, positionMs, lengthMs, false, lastError, preparing)

    // ------------------------------------------------------------------
    //  进度轮询
    // ------------------------------------------------------------------

    private val ticker = object : Runnable {
        override fun run() {
            if (player == null) return
            if (!isPlaying) return
            emit()
            main.postDelayed(this, TICK_MS)
        }
    }

    private fun startTick() {
        main.removeCallbacks(ticker)
        main.postDelayed(ticker, TICK_MS)
    }

    private fun stopTick() {
        main.removeCallbacks(ticker)
    }

    // ------------------------------------------------------------------
    //  准备阶段看门狗
    // ------------------------------------------------------------------

    private fun armPrepareGuard(mp: MediaPlayer) {
        disarmPrepareGuard()
        val r = Runnable {
            if (player !== mp || !preparing) return@Runnable
            preparing = false
            // 不释放 player：让它继续试着准备。真要一直出不来，
            // 页面会先退回 `<audio>`，而服务端那次转码已经把它跑完了 ——
            // 网页拿到的就是缓存，反而更快。
            fail("等了 ${PREPARE_TIMEOUT_MS / 1000} 秒还没开始（可能服务端还在转码）")
        }
        prepareGuard = r
        main.postDelayed(r, PREPARE_TIMEOUT_MS)
    }

    private fun disarmPrepareGuard() {
        prepareGuard?.let { main.removeCallbacks(it) }
        prepareGuard = null
    }

    // ------------------------------------------------------------------
    //  音频焦点
    // ------------------------------------------------------------------

    /** @return 是否真的拿到了焦点。 */
    private fun requestFocus(): Boolean {
        val am = audioManager ?: return true
        if (hasFocus) return true
        val ok = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val req = android.media.AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
                .setAudioAttributes(audioAttrs)
                .setOnAudioFocusChangeListener(focusListener, main)
                // 朗读内容是长音频，允许系统延迟恢复
                .setWillPauseWhenDucked(true)
                .build()
            focusRequest = req
            am.requestAudioFocus(req) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
        } else {
            @Suppress("DEPRECATION")
            am.requestAudioFocus(
                focusListener,
                AudioManager.STREAM_MUSIC,
                AudioManager.AUDIOFOCUS_GAIN,
            ) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
        }
        hasFocus = ok
        return ok
    }

    private fun abandonFocus() {
        val am = audioManager ?: return
        if (!hasFocus) return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            (focusRequest as? android.media.AudioFocusRequest)?.let { am.abandonAudioFocusRequest(it) }
        } else {
            @Suppress("DEPRECATION")
            am.abandonAudioFocus(focusListener)
        }
        focusRequest = null
        hasFocus = false
    }

    // ------------------------------------------------------------------
    //  收尾
    // ------------------------------------------------------------------

    private fun releasePlayer() {
        disarmPrepareGuard()
        stopTick()
        preparing = false
        val p = player
        player = null
        runCatching {
            p?.setOnPreparedListener(null)
            p?.setOnCompletionListener(null)
            p?.setOnErrorListener(null)
            p?.reset()
            p?.release()
        }
        durationMs = 0
        pendingSeekMs = 0
    }

    /**
     * MediaPlayer 的错误码只有 what/extra 两个整数，直接扔掉等于没有线索。
     * 这两个值在官方文档里有明确含义，转成话之后诊断日志能直接定位。
     */
    private fun describeError(what: Int, extra: Int): String {
        val w = when (what) {
            MediaPlayer.MEDIA_ERROR_UNKNOWN -> "未知错误"
            MediaPlayer.MEDIA_ERROR_SERVER_DIED -> "解码服务挂了"
            MediaPlayer.MEDIA_ERROR_NOT_VALID_FOR_PROGRESSIVE_PLAYBACK -> "不支持边下边播"
            MediaPlayer.MEDIA_ERROR_IO -> "读写/网络错误"
            MediaPlayer.MEDIA_ERROR_MALFORMED -> "文件格式不对"
            MediaPlayer.MEDIA_ERROR_UNSUPPORTED -> "解码器不支持这种格式"
            MediaPlayer.MEDIA_ERROR_TIMED_OUT -> "超时"
            else -> "错误码 $what"
        }
        return "$w (what=$what extra=$extra)"
    }

    private fun fail(msg: String) {
        disarmPrepareGuard()
        stopTick()
        preparing = false
        lastError = msg
        Diagnostics.add("原生播放", msg)
        emit(error = msg)
    }

    /** 把回调线程切回主线程，页面拿到的状态才是有序的。 */
    private fun emit(ended: Boolean = false, error: String = lastError) {
        val st = State(
            playing = isPlaying,
            positionMs = if (ended) lengthMs else positionMs,
            durationMs = lengthMs,
            ended = ended,
            error = error,
            preparing = preparing,
        )
        main.post { onState(st) }
    }
}
