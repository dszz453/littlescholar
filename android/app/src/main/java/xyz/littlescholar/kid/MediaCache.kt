package xyz.littlescholar.kid

import android.content.Context
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import kotlin.math.abs

/**
 * 媒体本地缓存：**先下载、后播放**。
 *
 * ============ 为什么必须走这条路（2026-11-20 真机实锤） ============
 *
 * 荣耀 PGT-AN10（Android 16）上，`MediaPlayer.setDataSource(url)` 的原生
 * HTTP 数据源会在 **60ms 内报 `错误码 -38`（INVALID_OPERATION）**，
 * 而且服务端 access log 里**没有那条请求** —— 请求根本没发出去。
 * 同一时刻：
 *   · WebView（Chromium 网络栈）从同一地址加载页面、取 playInfo 全部正常；
 *   · 页面 `<audio>` 用 Range 请求同一音频 URL，206 正常出声；
 *   · 壳里的探针（HttpURLConnection + Range）对同一 URL 也 206。
 *
 * 也就是说这台设备上 **MediaPlayer 自带的网络栈是坏的，其他所有栈都好**。
 * 之前两轮修（8616 反代挂 GET → 换 8093 明文）只解释了一小部分 ——
 * 明文 8093 同样 -38，问题从来就不在服务端。
 *
 * 所以把"取数据"这件事从 MediaPlayer 手里拿走：用 HttpURLConnection
 * （真机上反复验证可靠的那条栈）把音频整份下载到本地，再把**本地文件**
 * 交给 MediaPlayer。顺便一并解决：
 *   · 票据 4 小时过期 —— 下载完就不再依赖 URL；
 *   · 反代/隧道对大响应的行为 —— 下载带 Range，逐块拿；
 *   · 息屏切集 —— 下一集提前下好，切集时不需要网络。
 *
 * ============ 缓存策略 ============
 *
 * · 键 = **resourceId**（稳定，不随票据刷新变化）；resourceId 缺失时退回
 *   按 URL 路径做散列。
 * · m4a 产物一条 1~3MB，缓存上限 1GB（约几百集），超限按最旧访问时间淘汰。
 * · 下载写 `.part` 临时文件，断点续传（Range: bytes=N-），完成后改名，
 *   改名成功即"这份缓存可用"——绝不会有半截文件被当成成品。
 */
object MediaCache {

    /** 串行下载：当前这条永远最先下，预加载的排在后面，不抢带宽。 */
    private val io: ExecutorService = Executors.newSingleThreadExecutor()

    /** 缓存总上限。m4a 一条 1~3MB，1GB 约几百集。 */
    private const val MAX_TOTAL_BYTES = 1024L * 1024 * 1024

    /** 单文件上限：超过就当异常拒绝（音频产物不该这么大）。 */
    private const val MAX_FILE_BYTES = 200L * 1024 * 1024

    /** 正在下载的任务（按缓存键），防止同一个文件下两遍。 */
    private val running = HashMap<String, Listener>()

    interface Listener {
        /** 下载进度。total 为 0 表示响应头没给长度。 */
        fun onProgress(bytes: Long, total: Long) {}
        fun onDone(file: File) {}
        fun onFail(msg: String) {}
    }

    // ------------------------------------------------------------------
    //  键与文件
    // ------------------------------------------------------------------

    fun dir(ctx: Context): File =
        File(ctx.cacheDir, "media").apply { runCatching { mkdirs() } }

    /** resourceId 优先；没有就用 URL 路径散列（resourceId=0 的旧指令兜底）。 */
    fun keyFor(resourceId: Long, url: String): String =
        if (resourceId > 0) "r$resourceId"
        else "u" + abs(url.substringBefore('?').hashCode().toLong())

    fun fileFor(ctx: Context, resourceId: Long, url: String): File =
        File(dir(ctx), keyFor(resourceId, url) + ".m4a")

    fun has(ctx: Context, resourceId: Long, url: String): Boolean =
        fileFor(ctx, resourceId, url).let { it.isFile && it.length() > 0 }

    /** 缓存坏了（MediaPlayer 解不开）时作废重下用。 */
    fun invalidate(ctx: Context, resourceId: Long, url: String) {
        runCatching { fileFor(ctx, resourceId, url).delete() }
    }

    // ------------------------------------------------------------------
    //  下载
    // ------------------------------------------------------------------

    /**
     * 取一份媒体：有缓存直接回调完成；没有就开始下载。
     * 回调都在**调用者线程**（本对象不切线程，切换交给调用方）。
     */
    fun fetch(ctx: Context, resourceId: Long, url: String, l: Listener) {
        val key = keyFor(resourceId, url)
        val f = File(dir(ctx), "$key.m4a")
        if (f.isFile && f.length() > 0) {
            runCatching { f.setLastModified(System.currentTimeMillis()) }
            l.onDone(f)
            return
        }
        synchronized(running) {
            if (running.containsKey(key)) return // 已在下载，进度走第一个监听者
            running[key] = l
        }
        io.execute {
            val result = download(ctx, key, url)
            val listener: Listener
            synchronized(running) {
                listener = running.remove(key) ?: return@execute
            }
            result.fold(
                { listener.onDone(it) },
                { listener.onFail(it.message ?: "下载失败") },
            )
        }
    }

    /** 预加载：只要文件，不关心进度与结果（失败只记诊断）。 */
    fun preload(ctx: Context, resourceId: Long, url: String) {
        val key = keyFor(resourceId, url)
        val f = File(dir(ctx), "$key.m4a")
        if (f.isFile && f.length() > 0) return
        synchronized(running) {
            if (running.containsKey(key)) return
            running[key] = object : Listener {}
        }
        io.execute {
            val result = download(ctx, key, url)
            result.fold(
                { Diagnostics.add("预加载", "下一集已就绪：${it.length() / 1024}KB") },
                { Diagnostics.add("预加载", "失败：${it.message}") },
            )
            synchronized(running) { running.remove(key) }
        }
    }

    /**
     * 下载本体。带断点续传：本地已有 `.part` 就从那个位置接着要。
     * 200 / 206 都接受（200 = 服务端不支持 Range，从头写）。
     */
    private fun download(ctx: Context, key: String, url: String): Result<File> {
        val dir = dir(ctx)
        val part = File(dir, "$key.part")
        val done = File(dir, "$key.m4a")
        return runCatching {
            var attempt = 0
            while (true) {
                val ex = tryOnce(url, part)
                if (ex == null) break
                attempt++
                if (attempt >= 2) throw IllegalStateException(ex)
                Diagnostics.add("下载", "第 $attempt 次失败（$ex），重试")
                Thread.sleep(1500L)
            }
            if (!part.renameTo(done)) {
                // Windows 式改名失败（Linux 罕见）：复制兜底
                part.copyTo(done, overwrite = true)
                part.delete()
            }
            trimIfNeeded(dir)
            done
        }
    }

    /** @return null = 成功；非空 = 失败原因。 */
    private fun tryOnce(url: String, part: File): String? {
        return try {
            val have = if (part.isFile) part.length() else 0L
            val conn = (URL(url).openConnection() as HttpURLConnection).apply {
                connectTimeout = 10_000
                readTimeout = 30_000
                instanceFollowRedirects = true
                setRequestProperty("User-Agent", "LittleScholar-Cache/1.0")
                if (have > 0) setRequestProperty("Range", "bytes=$have-")
            }
            val code = conn.responseCode
            if (code !in 200..299) {
                runCatching { conn.disconnect() }
                return "HTTP $code"
            }
            val total = conn.getHeaderField("Content-Length")?.toLongOrNull() ?: 0L
            val fromZero = have == 0L || code == 200
            val append = !fromZero
            if (fromZero) part.delete()
            val out = FileOutputStream(part, append)
            try {
                val input = conn.inputStream
                val buf = ByteArray(64 * 1024)
                var bytes = if (append) have else 0L
                var lastReport = 0L
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                    bytes += n
                    if (bytes > MAX_FILE_BYTES) {
                        runCatching { out.close() }
                        runCatching { conn.disconnect() }
                        part.delete()
                        return "文件超大(${bytes / 1024 / 1024}MB)"
                    }
                    if (bytes - lastReport > 512 * 1024) {
                        lastReport = bytes
                        Diagnostics.add(
                            "下载",
                            "${bytes / 1024}KB${if (total > 0) " / ${total / 1024}KB" else ""}",
                        )
                    }
                }
                out.flush()
            } finally {
                runCatching { out.close() }
                runCatching { conn.disconnect() }
            }
            if (part.length() <= 0) return "空响应"
            null
        } catch (e: Exception) {
            "${e.javaClass.simpleName}: ${e.message?.take(60)}"
        }
    }

    /** 超限时按 lastModified 淘汰最旧的。 */
    private fun trimIfNeeded(dir: File) {
        runCatching {
            val files = dir.listFiles { f -> f.isFile && f.name.endsWith(".m4a") }
                ?: return
            var total = files.sumOf { it.length() }
            if (total <= MAX_TOTAL_BYTES) return
            val ordered = files.sortedBy { it.lastModified() }
            for (f in ordered) {
                if (total <= MAX_TOTAL_BYTES) break
                val sz = f.length()
                if (f.delete()) total -= sz
            }
        }
    }
}
