package xyz.littlescholar.kid

import android.Manifest
import android.annotation.SuppressLint
import android.app.ActivityManager
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.net.http.SslError
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.view.View
import android.view.ViewGroup
import android.webkit.ConsoleMessage
import android.webkit.JavascriptInterface
import android.webkit.PermissionRequest
import android.webkit.RenderProcessGoneDetail
import android.webkit.SslErrorHandler
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/**
 * 小书童 · 孩子端容器
 *
 * 设计取舍说明：
 *  1. 采用 WebView 壳而非原生重写 —— 前端已是完整的 PWA，壳只需解决三件事：
 *     息屏播放、返回键、下载/外链。
 *  2. 服务器地址由**家长在应用内填写**并存在本地（见 ServerConfig）：
 *     首次打开进设置页，之后长按屏幕即可改。BuildConfig.SITE_URL 只作为
 *     首次的预填建议 —— 地址会变（内网 IP、公网端口都可能换），
 *     写死就意味着每次都要重新打包分发。
 *  3. 不做离线缓存 —— 学习内容强依赖服务端鉴权与直链，离线包意义有限，
 *     且 PWA 自身的 Service Worker 已覆盖壳静态资源。
 */
class MainActivity : AppCompatActivity() {

    private companion object {
        /**
         * 暂停/播完之后，前台服务**再多活这么久**才真的停。
         *
         * ⚠️ 这个宽限期不是"保险"，是**刚需**：播完一集，播放页会立刻
         * `router.replace('/play/{下一集}')`，中间那 1~3 秒里
         * `<video>`/`<audio>` 会先 `ended`、新的一集才 `play`。
         * 如果"收到 ended 立刻停"，服务正好停在空窗里 ——
         * 而下一集要拉 `/api/app/play/{id}`，此刻进程已经没有前台优先级，
         * 被冻住就取不到 → 孩子看到的是「网络异常」。
         *
         * 也不能设太长：用户真不听了之后，通知栏那条要如期消失。
         * 90 秒足够覆盖切集、拖动、缓冲重试。
         */
        const val KEEPALIVE_GRACE_MS = 90_000L

        /**
         * 注入给播放页的监听脚本。
         *
         * ⚠️ 为什么必须"注入"而不是"让孩子端自己调"：
         * 这个壳的职责本来就写着「息屏播放、返回键、下载/外链」，
         * 而孩子端 PWA 是**另一份产物**（还有线上部署的那一份）——
         * 让 PWA 去调一个只有 APK 里才存在的桥，等于把壳的职责推给网页，
         * 而且线上那份不该知道有 App 这回事。所以接线放在壳里。
         *
         * 媒体事件（play/pause/ended…）**不冒泡**，但会走**捕获阶段**，
         * 所以在 document 上挂捕获监听能收到页面里**所有** video/audio 的事件，
         * 不需要知道播放页把播放器放在哪一层、是 `<video>` 还是 `<audio>`
         * （孩子端两种都用：音频走 `<audio>`、视频走 `<video>`）。
         * `__lsPlayWatch` 保证只装一次（SPA 路由切换不会重装）。
         */
        const val INJECT_JS = """
(function () {
  if (window.__lsPlayWatch) { return; }
  window.__lsPlayWatch = 1;

  // 原生音频引擎（wma 这类 WebView 解不了的格式）的状态回调。
  // 页面是新构建时自己会定义 __lsNative；旧构建上这里兜个空实现，
  // 免得原生一推状态就报 undefined 把后面的脚本带断。
  if (!window.__lsNative) { window.__lsNative = { onState: function () {} }; }

  var send = function (on) {
    try { window.KidNative.setPlaying(!!on); } catch (e) { /* 纯浏览器里没有这个桥 */ }
  };

  // ---- 当前在播的那个元素 ----
  // 媒体事件不冒泡，但走捕获阶段，在 document 上挂着就能覆盖页面里所有
  // <video>/<audio>，不需要知道播放页把它们放在哪一层。
  var media = null;
  var lastPush = 0;

  var isMedia = function (t) {
    return !!t && (t.tagName === 'VIDEO' || t.tagName === 'AUDIO');
  };

  // 后端给的是 /api/stream/123?t=… 这种相对地址。浏览器按当前页面补全，
  // 而原生的 MediaPlayer 收到相对路径会当成本地文件名 —— 不报错但没声音。
  var absolute = function (u) {
    if (!u) { return ''; }
    if (/^(https?|blob|data|file):/i.test(u)) { return u; }
    try { return new URL(u, location.href).href; } catch (e) { return u; }
  };

  var payload = function (el, playing) {
    return JSON.stringify({
      active: true,
      playing: !!playing,
      title: (document.title || '').slice(0, 120),
      duration: Math.round((el && el.duration) || 0),
      position: Math.round((el && el.currentTime) || 0),
      url: absolute(el && (el.currentSrc || el.src)),
      video: !!el && el.tagName === 'VIDEO',
      paused: !playing
    });
  };

  // 主动把状态推给壳：壳里那条"onStop 兜底接管"靠的就是这份快照。
  // timeupdate 一秒来好几次，节流到 2 秒 —— 只为了离开那一刻的位置别太旧。
  var push = function (el, playing) {
    try { window.KidNative.mediaState(payload(el, playing)); } catch (e) { /* noop */ }
  };

  ['play', 'playing', 'pause', 'ended', 'emptied', 'error'].forEach(function (name) {
    document.addEventListener(name, function (e) {
      if (!isMedia(e.target)) { return; }
      var playing = (name === 'play' || name === 'playing') && !e.target.paused;
      send(playing);
      if (playing) { media = e.target; }
      push(e.target, playing);
    }, true);
  });

  // 播放中定期刷新快照（timeupdate 一秒来好几次，这里节流到 2 秒 ——
  // 只为了"离开那一刻"的位置别太旧，onStop 兜底接管要靠它）。
  document.addEventListener('timeupdate', function (e) {
    if (!isMedia(e.target) || e.target.paused) { return; }
    media = e.target;
    if (Date.now() - lastPush > 2000) {
      lastPush = Date.now();
      push(media, true);
    }
  }, true);

  // ---- 全局接管钩子 ----
  // 原生接管后要让页面**闭嘴**（否则两边一起出声），交还时再恢复。
  // 孩子端新版自己有一套 window.__kidMedia.xxx；这里的钩子是给
  // **旧版页面 / 任何不认识这套约定的页面**兜底的，两条都跑不冲突：
  // 页面自己处理过的，这里再 mute/pause 一次是幂等的。
  window.__lsHandoff = {
    ack: function () {
      var el = media;
      if (!el) { return; }
      try { el.muted = true; el.pause(); } catch (e) { /* noop */ }
    },
    giveBack: function (sec) {
      var el = media;
      if (!el) { return; }
      try {
        if (sec > 0 && Math.abs(sec - (el.currentTime || 0)) > 1.5) {
          el.currentTime = sec;
        }
        el.muted = false;
      } catch (e) { /* noop */ }
      try { el.play(); } catch (e) { /* 没有用户手势时可能被拒，下一次点击会恢复 */ }
    }
  };

  // ---- 切后台 / 回前台 ----
  // 这是**壳自己的**交接：不依赖页面里有没有那段 JS。
  // 线上实测过：孩子端某次部署是旧构建（页面上压根没有 takeOver 这段），
  // 于是"标准版不能后台播放、PRO 却能" —— 因为 PRO 把前端产物嵌进了 APK。
  // 接线放在壳里，页面新旧都能用。
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') {
      var el = media;
      if (!el || el.paused) { return; }
      try { window.KidNative.takeOver(payload(el, true)); } catch (e) { /* noop */ }
    } else {
      try { window.KidNative.giveBack(); } catch (e) { /* noop */ }
    }
  });
})();
"""
    }

    private lateinit var webView: WebView
    private lateinit var swipe: SwipeRefreshLayout
    private lateinit var root: FrameLayout

    /**
     * 我们**希望**前台服务开着（网页里有东西在播）。
     *
     * ⚠️ 它只表达"意愿"，**不能**用来判断服务是否真的在跑 ——
     * 通知栏按钮、Activity 重建、系统重启服务都会让两边状态漂。
     * 真正的事实只有一份：`MediaPlaybackService.isRunning()`。
     */
    private var playbackWanted = false
    private val playbackHandler = Handler(Looper.getMainLooper())
    private val stopPlayback = Runnable { maybeStopPlayback() }

    /** 当前生效的服务器地址（来自本地存储，家长可改）。 */
    private var siteUrl: String = ""

    /** 设置页；为 null 表示未显示。 */
    private var setupView: ServerSetupView? = null

    /**
     * 本次启动是否要做「PWA 缓存自愈」（注销旧 SW + 清 CacheStorage）。
     * 只在 **APK 版本号变化** 时置位，见 [healWebCacheIfUpgraded]。
     */
    private var pendingHeal = false

    /**
     * 扫码结果只**回填**到设置页的输入框，不自动连接。
     * 扫到的可能是公网、也可能是内网地址，让家长看一眼再点「连接」；
     * 扫错了还能手动改，自动连接反而会把人卡在一个连不上的界面上。
     */
    private val scanLauncher = registerForActivityResult(ScanContract()) { result ->
        val text = result.contents
        if (!text.isNullOrBlank()) {
            setupView?.setUrl(text)
        }
    }

    /** 相机权限被拒时明确告知去哪儿开，不做静默失败。 */
    private val cameraPermLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) {
                launchScan()
            } else {
                Toast.makeText(
                    this,
                    "没有相机权限，扫不了码。\n可到「设置 → 应用 → 小书童 → 权限」里打开相机。",
                    Toast.LENGTH_LONG
                ).show()
            }
        }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // --- 根布局：SwipeRefreshLayout 包 WebView ---
        swipe = SwipeRefreshLayout(this).apply {
            setColorSchemeResources(R.color.brand)
            setProgressBackgroundColorSchemeResource(android.R.color.white)
        }
        webView = WebView(this)
        swipe.addView(
            webView,
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.MATCH_PARENT
        )
        // 全屏视频播放的占位容器（设置页也挂在它上面）
        root = FrameLayout(this).apply {
            addView(
                swipe,
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT
            )
        }
        setContentView(root)

        setupWebView(root)

        // 诊断：先采环境（含 WebView 内核版本），后面所有报错都会挂到它上面
        Diagnostics.start(this)
        healWebCacheIfUpgraded()

        // 让"最近任务（任务中心）"里的卡片显示「小书童」+ 应用图标 + 品牌色，
        // 而不是把网页标题或一片白当成卡片预览。不带这行的表现是：卡片上
        // 只有 URL / 空白，家长在多任务里认不出这个 App。
        setTaskDescription(
            ActivityManager.TaskDescription(
                getString(R.string.app_name),
                R.mipmap.ic_launcher,
                ContextCompat.getColor(this, R.color.brand)
            )
        )

        // 通知栏 / 锁屏的上一首、下一首要作用到 WebView 里的播放器
        MediaBridge.attach(webView)

        swipe.setOnRefreshListener {
            webView.reload()
        }

        // --- 系统返回键：优先让网页处理，否则退栈，最后才退出 App ---
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                // 设置页开着时，返回 = 关掉设置页（别把 App 退出）
                if (setupView != null) {
                    hideSetup()
                } else if (webView.canGoBack()) {
                    webView.goBack()
                } else {
                    isEnabled = false
                    onBackPressedDispatcher.onBackPressed()
                }
            }
        })

        // 家长入口：长按屏幕任意处打开设置页（孩子不会误触长按）
        swipe.setOnLongClickListener {
            showSetup(null)
            true
        }

        siteUrl = ServerConfig.get(this)
        if (siteUrl.isEmpty()) {
            // 首次使用：先让家长填地址，别直接白屏
            showSetup(null)
        } else {
            loadSite()
        }
    }

    /** 加载已配置的地址。 */
    private fun loadSite() {
        if (siteUrl.isEmpty()) return
        swipe.isRefreshing = false
        webView.loadUrl(siteUrl)
    }

    /** 显示「填写服务器地址」设置页。msg 非空时作为错误提示展示。 */
    private fun showSetup(msg: String?) {
        val existing = setupView
        if (existing != null) {
            existing.showError(msg)
            existing.focusInput()
            return
        }
        val view = ServerSetupView(
            this,
            onSave = { url ->
                siteUrl = ServerConfig.set(this, url)
                hideSetup()
                loadSite()
            },
            onScan = { requestScan() },
            onDiagnostics = { showDiagnostics() }
        )
        view.showError(msg)
        setupView = view
        root.addView(
            view,
            FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT
            )
        )
        view.focusInput()
    }

    /** 扫码入口：先确认相机权限，再拉起扫码界面。 */
    private fun requestScan() {
        val granted = ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA) ==
            PackageManager.PERMISSION_GRANTED
        if (granted) {
            launchScan()
        } else {
            cameraPermLauncher.launch(Manifest.permission.CAMERA)
        }
    }

    private fun launchScan() {
        val opts = ScanOptions()
        opts.setDesiredBarcodeFormats(ScanOptions.QR_CODE)
        opts.setPrompt("对准电脑屏幕上的二维码")
        opts.setBeepEnabled(false)
        opts.setOrientationLocked(true)
        scanLauncher.launch(opts)
    }

    private fun hideSetup() {
        val v = setupView ?: return
        root.removeView(v)
        setupView = null
    }

    // ==================== 诊断 ====================

    /**
     * APK 升级后的「PWA 缓存自愈」。
     *
     * 为什么必须有：孩子端装成 App 后常年不关，PWA 的 Service Worker 一旦把
     * 首页缓存住，服务端发了新版客户端也永远跑旧 JS —— 只有手动清应用数据
     * 能破（"鸡生蛋"）。这个坑已经真实发生过一次，不能每次都靠人工记着清。
     *
     * 触发条件刻意限定为 **versionCode 变化**（不是每次启动都清，那样太浪费）：
     * 只要重新装过 APK，就顺手把上一次的 SW 和 CacheStorage 抹掉。
     *
     * **不动 localStorage** —— 登录令牌在里面，清了孩子要重新登录，
     * 而"免登录"正是上一轮刚修好的东西。清 SW/Cache 不影响登录态。
     */
    private fun healWebCacheIfUpgraded() {
        val sp = getSharedPreferences("kid_update", Context.MODE_PRIVATE)
        val seen = sp.getInt("seen_version", 0)
        if (seen == BuildConfig.VERSION_CODE) return
        sp.edit().putInt("seen_version", BuildConfig.VERSION_CODE).apply()

        // seen == 0 说明是全新安装，本来就没有旧缓存要清
        if (seen == 0) {
            Diagnostics.add("自愈", "全新安装（v${BuildConfig.VERSION_CODE}），无需清理")
            return
        }

        pendingHeal = true
        webView.clearCache(true)
        Diagnostics.add(
            "自愈",
            "检测到升级 v$seen → v${BuildConfig.VERSION_CODE}，首屏加载后将注销 SW 并清空 CacheStorage"
        )
    }

    /** 打开诊断面板：先让网页自报家门（跑的是哪份 JS），再展示。 */
    private fun showDiagnostics() {
        Diagnostics.add("诊断", "家长打开了诊断面板")
        if (webView.url != null) {
            webView.evaluateJavascript(Diagnostics.SNAPSHOT_JS) { r ->
                Diagnostics.add("快照", r ?: "(null)")
                presentDiagnosticsDialog()
            }
        } else {
            presentDiagnosticsDialog()
        }
    }

    private fun presentDiagnosticsDialog() {
        val body = TextView(this).apply {
            typeface = Typeface.MONOSPACE
            textSize = 10.5f
            setTextIsSelectable(true)
            setPadding(dp(14), dp(14), dp(14), dp(14))
            text = Diagnostics.dump(this@MainActivity)
        }
        val scroller = ScrollView(this).apply {
            addView(body, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        }
        AlertDialog.Builder(this)
            .setTitle("诊断信息")
            .setView(scroller)
            .setPositiveButton("复制全部") { _, _ -> copyDiagnostics() }
            .setNeutralButton("清空") { _, _ ->
                Diagnostics.clear()
                presentDiagnosticsDialog()
            }
            .setNegativeButton("关闭", null)
            .show()
    }

    private fun copyDiagnostics() {
        val cm = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(
            ClipData.newPlainText("小书童诊断", Diagnostics.dump(this))
        )
        Toast.makeText(this, "已复制，粘贴发出来即可", Toast.LENGTH_SHORT).show()
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()

    private fun setupWebView(root: FrameLayout) {
        val s = webView.settings

        s.javaScriptEnabled = true
        s.domStorageEnabled = true
        s.databaseEnabled = true

        // 关键组合：让 <video>/<audio> 无需用户手势即可播放，
        // 这是"息屏后继续播放"能在多数国产 ROM 上生效的前提之一
        s.mediaPlaybackRequiresUserGesture = false

        // 自适应屏幕，避免孩子端布局被放大
        s.useWideViewPort = true
        s.loadWithOverviewMode = true
        s.setSupportZoom(false)
        s.builtInZoomControls = false

        // 缓存策略：有网走网络，无网读缓存（纯提速，不代表离线可用）
        s.cacheMode = WebSettings.LOAD_DEFAULT

        // 混合内容：站点若为 https 而直链为 http，需要允许
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.LOLLIPOP) {
            s.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
        }

        // 允许跨域，否则 PWA fetch 直链会被拦
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.JELLY_BEAN) {
            s.allowUniversalAccessFromFileURLs = true
            s.allowFileAccessFromFileURLs = true
        }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.LOLLIPOP) {
            s.mediaPlaybackRequiresUserGesture = false
        }

        // ============ 播放保活桥 ============
        //
        // ⚠️ 2026-10-16 补的：在此之前 MediaPlaybackService 的 start()/stop()
        // **全项目零调用** —— 前台服务和唤醒锁都是一段完整的死代码，
        // 于是"息屏播放"全靠运气，现象是「息屏播完一集不切下一集，提示网络错误」。
        //
        // ⚠️ `addJavascriptInterface` 一挂上，**任何被加载的页面**都能调它。
        // 所以这里两点必须守住：
        //   ① 暴露面做到最小 —— 只有一个"我在播/我不播了"的布尔量，
        //      拿不到任何本机信息、也执行不了任何跳转；
        //   ② 配合 [shouldOverrideUrlLoading] 的导航闸门 —— 只有配置地址
        //      （`siteUrl` 前缀）下的页面留在 WebView 里，外链一律走系统浏览器。
        webView.addJavascriptInterface(PlaybackBridge(), "KidNative")

        webView.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(
                view: WebView,
                request: WebResourceRequest
            ): Boolean {
                val url = request.url.toString()
                // 站内导航留在 WebView，外部链接交给系统浏览器。
                // 必须用**当前配置的**地址，不是 BuildConfig 里那个预填值。
                return if (siteUrl.isNotEmpty() && url.startsWith(siteUrl)) {
                    false
                } else {
                    startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                    true
                }
            }

            override fun onPageFinished(view: WebView, url: String) {
                swipe.isRefreshing = false
                Diagnostics.add("页面", "加载完成 $url")

                // 注入"播放保活 + 后台交接"监听（细节见 INJECT_JS）。
                // ⚠️ 必须**每次**加载完都注入：脚本自己不重复装（靠 __lsPlayWatch），
                //    但页面重载后 window 是全新的，不注入就退回"没有保活"。
                injectPlaybackWatch(view)

                // 出问题时第一个要回答的问题永远是「客户端到底跑的是哪份 JS」。
                // 让网页自报家门并存进诊断，省得每次都靠猜。
                view.evaluateJavascript(Diagnostics.SNAPSHOT_JS) { r ->
                    Diagnostics.add("快照", r ?: "(null)")
                }

                // 版本升级后的自愈：注销旧 SW + 清 CacheStorage，然后重新加载。
                // 必须放在这里 —— 这两个 API 需要页面上下文，且只能在此刻安全调用。
                if (pendingHeal) {
                    pendingHeal = false
                    view.evaluateJavascript(Diagnostics.HEAL_JS) { r ->
                        Diagnostics.add("自愈", "旧 SW / CacheStorage 已清理 $r")
                    }
                    view.postDelayed({ runCatching { view.reload() } }, 1500)
                }
            }

            /**
             * 主文档加载失败（地址填错 / 服务器没起 / 不在同一网络）时回到设置页。
             *
             * 没有这条兜底，孩子看到的是一片白屏，家长也不知道该改什么 ——
             * "填错了能自己走回来"是这个页面的核心价值。
             * 只对**主文档**且**当前配置地址下的**失败生效，
             * 免得页面内某个子资源失败（比如某张封面图）也弹设置页。
             *
             * 但**记录**是对所有请求都做的：子资源失败同样有价值
             * （封面图挂了、pdf.js 的 cmaps 取不到、直链 403 都在这里现形）。
             */
            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: WebResourceError
            ) {
                val code =
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) error.errorCode else -1
                val desc =
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                        error.description?.toString().orEmpty()
                    } else {
                        ""
                    }
                Diagnostics.add(
                    if (request.isForMainFrame) "主文档失败" else "子资源失败",
                    "${request.url} code=$code $desc"
                )

                if (!request.isForMainFrame) return
                val failed = request.url.toString()
                if (siteUrl.isEmpty() || !failed.startsWith(siteUrl)) return
                showSetup("连不上 $siteUrl\n请检查：设备是否和服务器在同一网络、地址和端口是否写对、服务是否在运行。")
            }

            /**
             * HTTP 4xx/5xx。
             *
             * 这类失败**不会**走 onReceivedError（HTTP 层是通的，只是状态码不对），
             * 所以必须单独记 —— 鉴权 401、静态资源 404、直链 403 全靠它。
             */
            override fun onReceivedHttpError(
                view: WebView,
                request: WebResourceRequest,
                errorResponse: WebResourceResponse
            ) {
                Diagnostics.add(
                    "HTTP错误",
                    "${errorResponse.statusCode} ${errorResponse.reasonPhrase} ${request.url}"
                )
            }

            /**
             * SSL 证书校验失败。
             *
             * **不覆写这个方法时，WebView 的默认行为是静默取消请求** ——
             * 家长和孩子只会看到一片白屏，一个字的提示都没有，极难排查。
             * 这里是个人家庭应用、服务器也是自建的（证书可能是自签或链不全），
             * 所以如实把错误摆出来，由家长显式决定要不要继续。
             */
            override fun onReceivedSslError(
                view: WebView,
                handler: SslErrorHandler,
                error: SslError
            ) {
                Diagnostics.add("SSL错误", "primaryError=${error.primaryError} ${error.url}")
                AlertDialog.Builder(this@MainActivity)
                    .setTitle("服务器证书有问题")
                    .setMessage(
                        "证书校验没通过（错误码 ${error.primaryError}）。\n\n" +
                            "如果是你自己的服务器，可以点「继续访问」；\n" +
                            "否则请点「取消」。"
                    )
                    .setCancelable(false)
                    .setPositiveButton("继续访问") { _, _ -> handler.proceed() }
                    .setNegativeButton("取消") { _, _ -> handler.cancel() }
                    .show()
            }

            /**
             * 渲染进程被系统回收。
             *
             * Android 8.0 起，WebView 的渲染器跑在独立沙箱进程里，内存紧张的平板上
             * 被系统杀掉是常态。**不覆写这个方法，App 会跟着一起崩** ——
             * 孩子的体感就是"看着看着突然退出了"，而日志里什么都没有。
             *
             * 返回 true 表示"我处理了"：销毁旧 WebView、换一个新的重新加载。
             */
            override fun onRenderProcessGone(
                view: WebView,
                detail: RenderProcessGoneDetail
            ): Boolean {
                val crashed =
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) detail.didCrash() else true
                Diagnostics.add("渲染进程", "被系统回收 didCrash=$crashed，重建 WebView")
                Toast.makeText(this@MainActivity, "页面已重新加载", Toast.LENGTH_SHORT).show()

                swipe.removeView(view)
                runCatching { view.destroy() }
                webView = WebView(this@MainActivity)
                swipe.addView(
                    webView,
                    ViewGroup.LayoutParams.MATCH_PARENT,
                    ViewGroup.LayoutParams.MATCH_PARENT
                )
                MediaBridge.attach(webView)
                setupWebView(root)
                loadSite()
                return true
            }
        }

        webView.webChromeClient = object : WebChromeClient() {
            /**
             * 页面 console 的 error/warn 全部收进诊断。
             *
             * 这是"看到网页真实报错"的**唯一**入口 —— 未捕获的 JS 异常在 WebView
             * 里也会走这里（level = ERROR），页面自己 catch 住的则不会
             * （所以 Play.vue 的错误态里也在同时把原始报错显示到界面上）。
             * 只收 ERROR/WARNING：log/info/tip 都是噪音，会把环形缓冲冲掉。
             */
            override fun onConsoleMessage(msg: ConsoleMessage): Boolean {
                val level = msg.messageLevel()
                if (level == ConsoleMessage.MessageLevel.ERROR ||
                    level == ConsoleMessage.MessageLevel.WARNING
                ) {
                    Diagnostics.add(
                        "console/$level",
                        "${msg.message()}  @${msg.sourceId()}:${msg.lineNumber()}"
                    )
                }
                return false // 仍按默认行为输出到 logcat
            }

            // 视频全屏：把自定义 View 挂到 root 上
            private var fullScreenView: View? = null
            private var customViewCallback: CustomViewCallback? = null

            override fun onShowCustomView(view: View, callback: CustomViewCallback) {
                if (fullScreenView != null) {
                    onHideCustomView()
                    return
                }
                fullScreenView = view
                customViewCallback = callback
                root.addView(
                    view,
                    FrameLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT,
                        ViewGroup.LayoutParams.MATCH_PARENT
                    )
                )
                swipe.visibility = View.GONE
                view.visibility = View.VISIBLE
            }

            override fun onHideCustomView() {
                val v = fullScreenView ?: return
                root.removeView(v)
                swipe.visibility = View.VISIBLE
                fullScreenView = null
                customViewCallback?.onCustomViewHidden()
                customViewCallback = null
            }

            // 授予摄像头/麦克风（当前功能未用，留作将来朗读打卡）
            override fun onPermissionRequest(request: PermissionRequest) {
                request.grant(request.resources)
            }
        }
    }

    /**
     * 应用退到后台时不暂停 WebView 的媒体播放。
     *
     * 实测要点：WebView 内部一旦进入 paused 状态，<audio>/<video> 会被强制停，
     * "息屏听故事"直接失效。所以这里 **不调用** webView.onPause()。
     *
     * ⚠️ 不暂停 WebView 只是**必要条件之一**，另外两条同样不能少
     * （2026-10-16 才补齐，此前"息屏播放"其实一直没真正成立）：
     *   ① 前台服务得真的**被启动**（`MediaPlaybackService.start()`）；
     *   ② 服务里得真的持 CPU 唤醒锁 + WiFi 锁。
     * 这两条现在由 [setPlaybackActive] / [maybeStopPlayback] 驱动。
     *
     * 注意：这仍是"后台播放"的必要条件，不是充分条件。
     * 部分国产 ROM（MIUI / EMUI / ColorOS）还会额外杀后台，
     * 需要在系统设置里把小书童的电池策略设为"无限制"，或引导家长加白名单。
     */
    override fun onPause() {
        super.onPause()
        // 刻意留空：见上方说明，不暂停 WebView 以维持媒体播放
    }

    override fun onStop() {
        super.onStop()
        // 切后台：这里**主动**把播放交给原生，不等网页那一回调。
        //
        // 网页的 `visibilitychange → takeOver` 仍是主路径（位置更准），
        // 但它在 WebView 里报不报、什么时候报都不确定；一旦没报，
        // JS 很快被冻结，就再也没有第二次机会 —— 表现就是"切后台就断"。
        // 这里用最近一次在播的状态兜底，两条路只要成一条，声音就不会断。
        if (MediaPlaybackService.takeOverFromState()) {
            Diagnostics.add("后台", "已切到原生播放（壳里主动接管）")
        }
    }

    override fun onResume() {
        super.onResume()
        webView.onResume()
    }

    override fun onDestroy() {
        // ⚠️ 一定要摘掉挂在主线程 Handler 上的那个 Runnable：
        // 它捕获了 this，留着就是一条"Activity 已销毁但还被 Handler 引用"的泄漏链，
        // 而它在宽限期到点时还会去碰已经没了的 Activity。
        playbackHandler.removeCallbacks(stopPlayback)
        MediaBridge.detach()
        webView.destroy()
        super.onDestroy()
    }

    // ============================================================
    //  播放保活（前台服务 + 唤醒锁的启停）
    // ============================================================

    /**
     * 暴露给网页的最小桥：**只有"我在播 / 我不播了"这一件事**。
     *
     * ⚠️ 方法跑在 WebView 的 JavaBridge 线程上，碰 UI/服务的都要 `runOnUiThread`。
     * ⚠️ 参数来自页面，一律当不可信输入 —— 这里只有一个 Boolean，
     * 不存在注入点；但也正因如此，**不要往这个类里加任何能读本机信息
     * 或能发网络请求的方法**（它挂在任何被加载的页面上）。
     */
    inner class PlaybackBridge {
        @JavascriptInterface
        fun setPlaying(on: Boolean) {
            runOnUiThread { setPlaybackActive(on) }
        }

        /**
         * 页面推来的播放详情（标题、封面、时长、进度、有没有上一集/下一集）。
         *
         * 只往系统媒体面板里送，**不读任何本机信息、不发网络请求** ——
         * 这个桥挂在任何被加载的页面上，能做的事越少越安全。
         *
         * 解析失败静默（见 [MediaState.parse]）：桥里抛异常会把页面后续
         * 脚本一起带断，而这只是"面板上少显示一行字"的程度。
         */
        @JavascriptInterface
        fun mediaState(json: String?) {
            try {
                MediaPlaybackService.pushState(json ?: "")
            } catch (e: Exception) {
                Diagnostics.add("媒体", "状态推送失败：${e.message}")
            }
        }

        /**
         * 网页切到后台，把播放交给原生接着播。
         *
         * 为什么不让原生自己判断：`onStop` 比页面的 `visibilitychange` 晚，
         * 而这段时间里 WebView 可能已经被挂起，页面就没机会把 url 送出来了。
         * 由页面主动交，拿到的位置也最准。
         *
         * 服务没起来时先存着，`onCreate` 后补 —— 见 `MediaPlaybackService.consumePending`。
         */
        @JavascriptInterface
        fun takeOver(json: String?) {
            try {
                MediaPlaybackService.takeOver(json ?: "")
            } catch (e: Exception) {
                Diagnostics.add("接管", "接管请求失败：${e.message}")
            }
        }

        /**
         * 页面点名用**原生引擎**播一条音频（wma/ape 这类 WebView 解不了的）。
         *
         * 只做转发：真正的播放器在 [MediaPlaybackService] 里，切后台/锁屏
         * 用的还是它，因此不需要 takeOver/giveBack 那一套交接。
         */
        @JavascriptInterface
        fun nativeAudio(json: String?) {
            try {
                MediaPlaybackService.nativeAudio(this@MainActivity, json ?: "")
            } catch (e: Exception) {
                Diagnostics.add("原生播放", "指令失败：${e.message}")
            }
        }

        /** 回到前台，把播放还回网页。 */
        @JavascriptInterface
        fun giveBack() {
            try {
                MediaPlaybackService.giveBack()
            } catch (e: Exception) {
                Diagnostics.add("接管", "交还失败：${e.message}")
            }
        }
    }

    /**
     * 把 [INJECT_JS] 塞进当前页面。
     *
     * ⚠️ 用 `evaluateJavascript` 而不是 `loadUrl("javascript:…")`：前者不刷新页面、
     * 也不受 URL 长度与转义影响。它**不等待 Promise** —— 但这段脚本是同步的。
     * 失败只记诊断：保活是优化，不是功能，绝不能让它把页面搞挂。
     */
    private fun injectPlaybackWatch(view: WebView) {
        try {
            view.evaluateJavascript(INJECT_JS, null)
            Diagnostics.add("保活", "已注入播放监听（含后台交接）")
        } catch (e: Exception) {
            Diagnostics.add("保活", "注入失败：${e.message}")
        }
    }

    /**
     * 播放状态变了。`on = true` 表示"网页里某个播放器正在播"。
     *
     * 开：马上起服务（用户此刻在操作，前台启动一定被允许）。
     * 关：**不马上停**，交给 [maybeStopPlayback] —— 详见宽限期那段注释。
     */
    private fun setPlaybackActive(on: Boolean) {
        playbackHandler.removeCallbacks(stopPlayback)
        if (on) {
            playbackWanted = true
            if (!MediaPlaybackService.isRunning()) {
                Diagnostics.add("保活", "开始播放 → 起前台服务与唤醒锁")
                MediaPlaybackService.start(this)
            }
            return
        }
        if (!playbackWanted) return
        playbackHandler.postDelayed(stopPlayback, KEEPALIVE_GRACE_MS)
    }

    /**
     * 宽限期到了，判断到底该不该停。
     *
     * ⚠️ **息屏时不停**，再等一轮。
     *
     * 因为"息屏"在这个 App 里基本等价于"用户正在听"（孩子端播放页上就写着
     * 「播放中直接锁屏也不会停，把手机翻过来听也行」）。这时候把服务收掉
     * 有两个后果，而且是叠加的：
     *   ① 进程立刻失去前台优先级，可能被回收/冻结 —— 音频就断了；
     *   ② 想再起来就得**从后台**调 `startForegroundService`，
     *      Android 12+ 直接抛 `ForegroundServiceStartNotAllowedException`
     *      —— 也就是说**这一集结束了就再也切不到下一集了**，
     *      而不是"断一下能恢复"。
     * 所以熄屏期间一直续着，等亮屏之后的那一轮再收。
     *
     * 这也顺手解决了"息屏播完一集切下一集"：切集时 `ended` → `play` 之间
     * 本来就落在宽限期里，服务根本不会停。
     */
    private fun maybeStopPlayback() {
        if (!playbackWanted) return
        if (!screenInteractive()) {
            // 屏幕还黑着：再续一轮，不释放任何东西
            playbackHandler.postDelayed(stopPlayback, KEEPALIVE_GRACE_MS)
            return
        }
        // 后台接管中：网页是**故意暂停**的（声音交给原生播放器了），
        // 这时候收服务等于把正在播的东西关掉。再续一轮，等真正结束。
        if (MediaPlaybackService.isHandingOff()) {
            playbackHandler.postDelayed(stopPlayback, KEEPALIVE_GRACE_MS)
            return
        }
        // 原生引擎（VLC）正在播：页面此时**没有** <audio>，它报的"不播了"
        // 只代表"网页里没有元素在播"，收服务等于把 wma 直接掐断。
        if (MediaPlaybackService.isNativePlaying()) {
            playbackHandler.postDelayed(stopPlayback, KEEPALIVE_GRACE_MS)
            return
        }
        playbackWanted = false
        Diagnostics.add("保活", "播放结束 → 停前台服务并释放唤醒锁")
        MediaPlaybackService.stop(this)
    }

    private fun screenInteractive(): Boolean = try {
        (getSystemService(POWER_SERVICE) as? PowerManager)?.isInteractive ?: true
    } catch (e: Exception) {
        true // 拿不到就按"亮着"处理：宁可多留一会儿服务，也别在息屏时误停
    }
}
