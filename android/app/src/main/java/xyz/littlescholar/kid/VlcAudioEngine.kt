package xyz.littlescholar.kid

import android.content.Context
import android.net.Uri
import android.os.Handler
import android.os.Looper
import org.videolan.libvlc.LibVLC
import org.videolan.libvlc.Media
import org.videolan.libvlc.MediaPlayer
import kotlin.math.abs

/**
 * 原生音频引擎（libVLC）。
 *
 * ============ 为什么需要它 ============
 *
 * WebView 里的 `<audio>` 由 Chromium 解码，而 Chromium **完全不支持 wma**
 * —— `canPlayType('audio/x-ms-wma')` 返回空串，加载直接报
 * `DEMUXER_ERROR_COULD_NOT_OPEN`（2026-10-21 实测）。
 *
 * 以前的做法是让服务端把 wma 转成 m4a 再给网页。问题是这台 NAS（Celeron
 * J1900）转一本 3 分钟的绘本要 **21.6 秒**（取源 1.8s + 转码 21.6s，实测），
 * 长一点的书就是用户说的"慢一分钟"，而且每本都要多占一份磁盘。
 *
 * 所以把解码搬到客户端：VLC 自带全套解码器，wma/ape/amr 都能直接播，
 * 一本绘本点开就出声，服务端不用转、也不用缓存。
 *
 * ============ 为什么由 Service 持有 ============
 *
 * 后台播放、锁屏控制、通知栏按钮都要在页面被挂起后继续工作。
 * 引擎由 [MediaPlaybackService] 持有，Activity 只转发指令与状态，
 * 这样"前台播放"和"切后台继续播"用的是**同一个**播放器，不需要再做交接 ——
 * 这是它比"网页放 + 切后台再接管"简单的地方。
 */
class VlcAudioEngine(
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
    )

    private val appCtx = context.applicationContext
    private val main = Handler(Looper.getMainLooper())

    private val libVlc: LibVLC by lazy {
        LibVLC(
            appCtx,
            arrayListOf(
                // 纯音频：省掉视频输出管线（也避开少数设备初始化 Surface 失败）
                "--no-video",
                "--no-video-title-show",
                "--audio-time-stretch",
                // 网络源缓冲 2 秒：再大就"按了播放半天不响"，再小容易卡
                "--network-caching=2000",
            ),
        )
    }

    private var player: MediaPlayer? = null
    private var durationMs = 0L
    private var lastError = ""

    /** 播起来之后要跳到的位置（刚 play() 时 seek 会被丢掉）。 */
    private var pendingSeekMs = 0L

    /** 当前资源地址；同一条资源再次 play 时只 seek/继续，不重建播放器。 */
    var currentUrl: String = ""
        private set

    val isPlaying: Boolean get() = runCatching { player?.isPlaying == true }.getOrDefault(false)
    val positionMs: Long get() = runCatching { player?.time ?: 0L }.getOrDefault(0L)
    val lengthMs: Long
        get() = if (durationMs > 0) durationMs
        else runCatching { player?.length ?: 0L }.getOrDefault(0L)

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
        if (p != null && currentUrl == url) {
            if (startMs > 0 && abs(p.time - startMs) > 1500) p.time = startMs
            if (!p.isPlaying) p.play()
            emit()
            return
        }

        releasePlayer()
        currentUrl = url
        lastError = ""
        durationMs = 0
        pendingSeekMs = startMs.coerceAtLeast(0)

        val mp = MediaPlayer(libVlc)
        player = mp
        mp.setEventListener { e ->
            when (e.type) {
                MediaPlayer.Event.Playing -> emit()
                MediaPlayer.Event.Paused -> emit()
                MediaPlayer.Event.Stopped -> emit()
                MediaPlayer.Event.LengthChanged -> {
                    if (e.lengthChanged > 0) durationMs = e.lengthChanged
                    applyPendingSeek(mp)
                    emit()
                }
                MediaPlayer.Event.TimeChanged -> {
                    applyPendingSeek(mp)
                    emit()
                }
                MediaPlayer.Event.EndReached -> emit(ended = true)
                MediaPlayer.Event.EncounteredError -> fail("这段音频解不开")
                else -> Unit
            }
        }
        val media = Media(libVlc, Uri.parse(url)).apply {
            // 音频不需要硬解；关掉能避开部分国产 ROM 的解码器初始化崩溃
            setHWDecoderEnabled(false, false)
            addOption(":network-caching=2000")
        }
        mp.media = media
        media.release()
        mp.play()
        emit()
    }

    /** 时长够了才跳：长度未知时 seek 会被 VLC 忽略，跳了等于没跳。 */
    private fun applyPendingSeek(mp: MediaPlayer) {
        if (pendingSeekMs <= 0) return
        if (mp.length <= 0) return
        runCatching { mp.time = pendingSeekMs }
        pendingSeekMs = 0
    }

    fun pause() {
        runCatching { player?.pause() }
        emit()
    }

    fun resume() {
        runCatching { player?.play() }
        emit()
    }

    fun seekTo(ms: Long) {
        runCatching { player?.time = ms.coerceAtLeast(0) }
        emit()
    }

    fun stop() {
        releasePlayer()
        currentUrl = ""
        emit()
    }

    fun release() {
        releasePlayer()
        runCatching { libVlc.release() }
    }

    fun snapshot(): State = State(isPlaying, positionMs, lengthMs, false, lastError)

    private fun releasePlayer() {
        runCatching {
            player?.setEventListener(null)
            player?.stop()
            player?.release()
        }
        player = null
        durationMs = 0
        pendingSeekMs = 0
    }

    private fun fail(msg: String) {
        lastError = msg
        emit(error = msg)
    }

    /** 把 VLC 的事件线程切回主线程，页面拿到的状态才是有序的。 */
    private fun emit(ended: Boolean = false, error: String = lastError) {
        val st = State(
            playing = isPlaying,
            positionMs = positionMs,
            durationMs = lengthMs,
            ended = ended,
            error = error,
        )
        main.post { onState(st) }
    }
}
