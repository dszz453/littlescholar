package xyz.littlescholar.kid

import android.annotation.SuppressLint
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.webkit.PermissionRequest
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.swiperefreshlayout.widget.SwipeRefreshLayout

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

    private lateinit var webView: WebView
    private lateinit var swipe: SwipeRefreshLayout
    private lateinit var root: FrameLayout

    /** 当前生效的服务器地址（来自本地存储，家长可改）。 */
    private var siteUrl: String = ""

    /** 设置页；为 null 表示未显示。 */
    private var setupView: ServerSetupView? = null

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
        val view = ServerSetupView(this) { url ->
            siteUrl = ServerConfig.set(this, url)
            hideSetup()
            loadSite()
        }
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

    private fun hideSetup() {
        val v = setupView ?: return
        root.removeView(v)
        setupView = null
    }

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
            }

            /**
             * 主文档加载失败（地址填错 / 服务器没起 / 不在同一网络）时回到设置页。
             *
             * 没有这条兜底，孩子看到的是一片白屏，家长也不知道该改什么 ——
             * "填错了能自己走回来"是这个页面的核心价值。
             * 只对**主文档**且**当前配置地址下的**失败生效，
             * 免得页面内某个子资源失败（比如某张封面图）也弹设置页。
             */
            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: android.webkit.WebResourceError
            ) {
                if (!request.isForMainFrame) return
                val failed = request.url.toString()
                if (siteUrl.isEmpty() || !failed.startsWith(siteUrl)) return
                showSetup("连不上 $siteUrl\n请检查：设备是否和服务器在同一网络、地址和端口是否写对、服务是否在运行。")
            }
        }

        webView.webChromeClient = object : WebChromeClient() {
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
     * "息屏听故事"直接失效。所以这里 **不调用** webView.onPause()，
     * 仅在前台服务里持唤醒锁防止 CPU 休眠。
     *
     * 注意：这是"后台播放"的必要条件，不是充分条件。
     * 部分国产 ROM（MIUI / EMUI / ColorOS）还会额外杀后台，
     * 需要在系统设置里把小书童的电池策略设为"无限制"，或引导家长加白名单。
     */
    override fun onPause() {
        super.onPause()
        // 刻意留空：见上方说明，不暂停 WebView 以维持媒体播放
    }

    override fun onResume() {
        super.onResume()
        webView.onResume()
    }

    override fun onDestroy() {
        webView.destroy()
        super.onDestroy()
    }
}
