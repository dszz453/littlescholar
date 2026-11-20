package xyz.littlescholar.kid

import android.content.Context
import android.media.AudioAttributes
import android.media.AudioManager
import android.media.MediaPlayer
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import java.io.File
import kotlin.math.abs

/**
 * 原生音频引擎（Android 系统 MediaPlayer）。
 *
 * ============ 1.7.10：MediaPlayer 不再碰网络 ============
 *
 * 真机实锤（2026-11-20，荣耀 PGT-AN10 / Android 16）：
 * `setDataSource(url)` 的原生 HTTP 数据源 60ms 内报 `错误码 -38`
 * （INVALID_OPERATION），请求**根本没发出去**（服务端 access log 无记录）；
 * 而同一时刻 WebView、页面 `<audio>`、HttpURLConnection 探针对同一地址
 * 全部正常。这台设备的 MediaPlayer 网络栈是坏的。
 *
 * 所以播放链路改为：[MediaCache]（HttpURLConnection，真机上唯一可靠的栈）
 * 先把音频**整份下载到本地**，再把本地文件交给 MediaPlayer。
 * 附带收益：票据过期不再影响播放中/待播内容、切集零网络等待
 * （下一集在播当前集时就预加载好了）。
 *
 * ============ 其余历史决策（保留） ============
 *
 * 1.6.0 引入 libVLC 的理由是"客户端要能自己解 wma"。但服务端会把
 * wma/ape/amr 统一转成 **m4a（AAC in MP4）**，AAC 是 Android 平台内置
 * 解码器 —— 原生不需要自带解码库，APK 从 43MB 降到 1MB 以内。
 * 引擎由 [MediaPlaybackService] 持有，前台与切后台用**同一个**播放器。
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
        /** 已收到 play 指令、正在下载/prepareAsync（可能在等服务端转码）。 */
        val preparing: Boolean = false,
    )

    private val appCtx = context.applicationContext
    private val main = Handler(Looper.getMainLooper())
    private val audioManager: AudioManager? =
        appCtx.getSystemService(AudioManager::class.java)

    private companion object {
        /**
         * prepareAsync 的超时。现在"准备期"= 下载（几 MB，通常几秒）+ prepare，
         * 仍可能碰上服务端现场转码（首次播放 20 秒级），保留宽超时。
         */
        const val PREPARE_TIMEOUT_MS = 120_000L

        /** 进度轮询间隔。MediaPlayer 没有 TimeChanged 事件，只能自己问。 */
        const val TICK_MS = 500L

        /** 同一条资源再次 play 时，位置差多少以内算"接着播"，不必 seek。 */
        const val SEEK_TOLERANCE_MS = 1500L
    }

    private var player: MediaPlayer? = null
    private var durationMs = 0L

    /** 播起来之后要跳到的位置（prepare 完成前 seek 会被丢弃）。 */
    private var pendingSeekMs = 0L

    /** prepare 阶段超时看门狗。 */
    private var prepareGuard: Runnable? = null

    /** 当前资源的地址；用于"同一条资源再 play 只 seek/继续"与看门狗重启。 */
    var currentUrl: String = ""
        private set

    /** 当前资源 id（缓存键）。 */
    var currentResourceId: Long = 0L
        private set

    /** 本条资源最近一次失败的原因；空串表示没失败。 */
    private var lastError = ""

    /** 已发 play、还没 prepare 完（含下载阶段）。会透传给页面（State.preparing）。 */
    var isPreparing: Boolean = false
        private set

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
            // ⚠️ 永久失焦也可能是**假的**：小米/OPPO 这类 ROM 在息屏时会给所有
            // 正在出声的播放器发一条 LOSS 做省电，之后就再也不补 GAIN。
            // 只暂停、并安排重试抢回焦点；抢到就接着播。
            // 不会误伤用户主动暂停：`pausedByFocus` 只在"正在播时被打断"才置位。
            AudioManager.AUDIOFOCUS_LOSS -> {
                if (isPlaying) {
                    pausedByFocus = true
                    hasFocus = false
                    pauseInternal()
                    scheduleFocusRetry()
                }
            }
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT -> {
                if (isPlaying) {
                    pausedByFocus = true
                    hasFocus = false
                    pauseInternal()
                }
            }
            AudioManager.AUDIOFOCUS_GAIN -> {
                if (pausedByFocus) {
                    pausedByFocus = false
                    resume()
                }
            }
            // CAN_DUCK 不处理：儿童绘本是朗读内容，压低音量反而听不清。
            else -> Unit
        }
    }

    /** LOSS 之后的重试：抢回焦点就继续播，抢不到就安静待着，5 秒一问。 */
    private var focusRetry: Runnable? = null

    private fun scheduleFocusRetry() {
        cancelFocusRetry()
        val r = object : Runnable {
            override fun run() {
                focusRetry = null
                // 期间用户自己按了暂停 / 换了源 / 已经恢复 —— 都不该再抢
                if (!pausedByFocus || player == null) return
                if (requestFocus()) {
                    pausedByFocus = false
                    Diagnostics.add("原生播放", "重新拿到音频焦点，继续播放")
                    resume()
                } else {
                    focusRetry = this
                    main.postDelayed(this, 5_000L)
                }
            }
        }
        focusRetry = r
        main.postDelayed(r, 3_000L)
    }

    private fun cancelFocusRetry() {
        focusRetry?.let { main.removeCallbacks(it) }
        focusRetry = null
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
     * 同一条资源正在播时只做 seek/继续，**不重建播放器** —— 重建会带来
     * 1~2 秒静音和"从头开始"，切前后台时正好撞上。
     *
     * 1.7.10 起流程 = 先取 [MediaCache] 缓存（有则直接用，无则下载），
     * 拿到本地文件后再 setDataSource(file)。下载期间 `preparing=true`。
     */
    fun play(resourceId: Long, url: String, startMs: Long) {
        if (url.isBlank()) {
            fail("地址为空")
            return
        }
        val p = player
        if (p != null && currentResourceId == resourceId && resourceId > 0 && !isPreparing) {
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
        currentResourceId = resourceId
        currentUrl = url
        lastError = ""
        durationMs = 0
        pendingSeekMs = startMs.coerceAtLeast(0)
        isPreparing = true
        // 先告诉页面"壳已经接手，还在准备"（现在是下载阶段）。
        emit()

        MediaCache.fetch(appCtx, resourceId, url, object : MediaCache.Listener {
            override fun onDone(file: File) {
                main.post { openLocal(file, resourceId, url, startMs, retried = false) }
            }

            override fun onFail(msg: String) {
                main.post {
                    if (currentResourceId == resourceId) {
                        fail("下载失败：$msg")
                    }
                }
            }
        })
    }

    /** 预加载（不播放）：把下一条提前下载到本地。 */
    fun preload(resourceId: Long, url: String) {
        if (resourceId <= 0 || url.isBlank()) return
        MediaCache.preload(appCtx, resourceId, url)
    }

    /**
     * 用本地文件开播放器。
     *
     * 缓存文件损坏（下载中断点错位、磁盘问题）时 MediaPlayer 会抛
     * IOException —— 作废缓存重下一次；再不行才报错给页面。
     */
    private fun openLocal(file: File, resourceId: Long, url: String, startMs: Long, retried: Boolean) {
        // 等 download 期间用户已经切走/停了：这条作废
        if (currentResourceId != resourceId || currentUrl != url) return
        if (!requestFocus()) {
            isPreparing = false
            fail("拿不到音频焦点")
            return
        }

        val mp = MediaPlayer()
        player = mp
        try {
            mp.setAudioAttributes(audioAttrs)
            // 锁屏继续播：让 MediaPlayer 自己持唤醒锁。
            mp.setWakeMode(appCtx, PowerManager.PARTIAL_WAKE_LOCK)
            mp.setDataSource(file.path)
        } catch (e: Exception) {
            isPreparing = false
            releasePlayer()
            if (!retried) {
                Diagnostics.add("原生播放", "缓存文件打不开（${e.javaClass.simpleName}），作废重下")
                MediaCache.invalidate(appCtx, resourceId, url)
                MediaCache.fetch(appCtx, resourceId, url, object : MediaCache.Listener {
                    override fun onDone(f: File) {
                        main.post { openLocal(f, resourceId, url, startMs, retried = true) }
                    }

                    override fun onFail(msg: String) {
                        main.post {
                            if (currentResourceId == resourceId) fail("下载失败：$msg")
                        }
                    }
                })
                return
            }
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
            isPreparing = false
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
                isPreparing = false
                fail("准备失败：${it.javaClass.simpleName}")
            }
    }

    fun pause() {
        // 页面/用户主动暂停：**清掉"因焦点暂停"的标记**，否则挂断电话后
        // 会自己响起来（用户明明按了暂停）。
        pausedByFocus = false
        cancelFocusRetry()
        pauseInternal()
    }

    private fun pauseInternal() {
        stopTick()
        runCatching { if (isPlaying) player?.pause() }
        emit()
    }

    fun resume() {
        val p = player ?: return
        if (isPreparing) return
        if (!requestFocus()) return
        runCatching { p.start() }
        startTick()
        emit()
    }

    fun seekTo(ms: Long) {
        val p = player ?: return
        val target = ms.coerceAtLeast(0)
        if (isPreparing) {
            // 还没 prepared，seek 会被吞掉 —— 记下来等 onPrepared 再跳
            pendingSeekMs = target
            return
        }
        runCatching { p.seekTo(target.toInt()) }
        emit()
    }

    fun stop() {
        currentUrl = ""
        currentResourceId = 0
        releasePlayer()
        emit()
    }

    fun release() {
        releasePlayer()
        abandonFocus()
    }

    fun snapshot(): State = State(isPlaying, positionMs, lengthMs, false, lastError, isPreparing)

    // ------------------------------------------------------------------
    //  进度轮询
    // ------------------------------------------------------------------

    private val ticker = object : Runnable {
        override fun run() {
            if (player == null) return
            // ⚠️ 这里**不能**因为 `!isPlaying` 就 return 掉。
            //
            // 暂停时 `pause()` 已经 `stopTick()` 了，所以能跑到这里的
            // "没在播"只可能是**瞬时**的（缓冲、系统调度、刚 start 还没生效）。
            // 一旦在这里 return 且不重新 postDelayed，轮询就永久停了。
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
            if (player !== mp || !isPreparing) return@Runnable
            isPreparing = false
            // 不释放 player：让它继续试着准备。
            fail("等了 ${PREPARE_TIMEOUT_MS / 1000} 秒还没开始")
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
        cancelFocusRetry()
        pausedByFocus = false
        isPreparing = false
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
     * 转成话之后诊断日志能直接定位。
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
        isPreparing = false
        lastError = msg
        Diagnostics.add("原生播放", msg)
        emit(error = msg)
    }

    /**
     * 把回调线程切回主线程，页面拿到的状态才是有序的。
     *
     * ⚠️ `error` 的默认值必须是**空串**，不能是 `lastError`。
     * 曾经写成 `error: String = lastError`，于是"这条资源失败过一次"之后，
     * 之后每 500ms 的进度上报都会把那条旧错误再带一遍。
     * 失败只由 [fail] 显式上报一次。
     */
    private fun emit(ended: Boolean = false, error: String = "") {
        val st = State(
            playing = isPlaying,
            positionMs = if (ended) lengthMs else positionMs,
            durationMs = lengthMs,
            ended = ended,
            error = error,
            preparing = isPreparing,
        )
        main.post { onState(st) }
    }
}
