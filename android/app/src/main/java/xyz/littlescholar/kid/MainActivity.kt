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
 *  2. 服务器地址通过 BuildConfig.SITE_URL 注入，不在代码里写死，
 *     便于同一份代码打不同环境的包（家庭内网 / 公网域名）。
 *  3. 不做离线缓存 —— 学习内容强依赖服务端鉴权与直链，离线包意义有限，
 *     且 PWA 自身的 Service Worker 已覆盖壳静态资源。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private lateinit var swipe: SwipeRefreshLayout

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
        // 全屏视频播放的占位容器
        val root = FrameLayout(this).apply {
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
                if (webView.canGoBack()) {
                    webView.goBack()
                } else {
                    isEnabled = false
                    onBackPressedDispatcher.onBackPressed()
                }
            }
        })

        webView.loadUrl(BuildConfig.SITE_URL)
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
                // 站内导航留在 WebView，外部链接交给系统浏览器
                return if (url.startsWith(BuildConfig.SITE_URL)) {
                    false
                } else {
                    startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                    true
                }
            }

            override fun onPageFinished(view: WebView, url: String) {
                swipe.isRefreshing = false
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
