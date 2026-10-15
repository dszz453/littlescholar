package xyz.littlescholar.kid

import android.content.Context
import android.graphics.Color
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView

/**
 * 「填写服务器地址」设置页。
 *
 * 出现的时机有四个：
 *  1. 首次打开（还没存过地址）；
 *  2. 长按屏幕（家长要换地址）；
 *  3. **页面加载失败**（地址填错 / 服务器没开 / 不在同一网络）——
 *     这条最重要：没有它，孩子只会看到一片白屏，家长也不知道该改什么。
 *  4. 从诊断面板回来（点「关闭」后不额外做处理，页面仍在栈上）。
 *
 * 也是**诊断信息的入口**：连不上、白屏、某个功能打不开时，家长点
 * 「查看诊断信息」即可拿到 WebView 内核版本 + 全部报错，复制发出来。
 * 这个入口是必需的 —— 之前几轮排查都卡在"拿不到任何报错文本"。
 *
 * 故意做得很朴素：这是个"家长用一次就再也不看"的页面，
 * 不需要为它引入布局 XML 或 Material 依赖，代码里搭出来就好。
 */
class ServerSetupView(
    context: Context,
    private val onSave: (String) -> Unit,
    private val onScan: () -> Unit,
    private val onDiagnostics: () -> Unit
) : LinearLayout(context) {

    private val input = EditText(context)
    private val errorBox = TextView(context)

    private val pad = { dp: Int -> (dp * context.resources.displayMetrics.density).toInt() }

    init {
        orientation = VERTICAL
        gravity = Gravity.CENTER
        setBackgroundColor(Color.WHITE)
        setPadding(pad(28), pad(28), pad(28), pad(28))
        isClickable = true // 吃掉点击，避免穿透到下面的 WebView

        addView(title("小书童"))
        addView(subtitle("孩子端 · 请先填写服务器地址"))

        input.apply {
            hint = ServerConfig.default.ifEmpty { "https://study.19860819.xyz:8616" }
            setText(ServerConfig.default)
            setSingleLine(true)
            inputType = InputType.TYPE_TEXT_VARIATION_URI
            textSize = 16f
            setPadding(pad(16), pad(14), pad(16), pad(14))
            background = android.graphics.drawable.GradientDrawable().apply {
                setColor(Color.parseColor("#F3F5F9"))
                cornerRadius = pad(12).toFloat()
                setStroke(pad(1), Color.parseColor("#DDE3EE"))
            }
            setSelectAllOnFocus(true)
        }
        addView(
            input,
            LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                .also { it.topMargin = pad(24) }
        )

        addView(
            tip(
                "在电脑或手机浏览器里打开孩子端，地址栏里那一串就是（要带端口号，如 :8093）。\n" +
                    "填错也没关系，连不上会自动回到这一页。"
            ).also { it.setTextAlignment(TEXT_ALIGNMENT_VIEW_START) }
        )

        val scan = Button(context).apply {
            text = "扫二维码"
            textSize = 15f
            setOnClickListener { onScan() }
        }
        addView(
            scan,
            LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                .also { it.topMargin = pad(20) }
        )

        val connect = Button(context).apply {
            text = "连接"
            textSize = 17f
            setOnClickListener {
                val url = ServerConfig.normalize(input.text?.toString().orEmpty())
                if (url.isEmpty()) {
                    showError("请先填写地址")
                    return@setOnClickListener
                }
                onSave(url)
            }
        }
        addView(
            connect,
            LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                .also { it.topMargin = pad(10) }
        )

        val reset = TextView(context).apply {
            text = "恢复默认地址"
            textSize = 13f
            setTextColor(Color.parseColor("#6B7280"))
            gravity = Gravity.CENTER
            setPadding(pad(8), pad(12), pad(8), pad(8))
            setOnClickListener {
                input.setText(ServerConfig.default)
                input.setSelection(input.text?.length ?: 0)
            }
        }
        addView(reset)

        // 诊断入口放在设置页里，而不是"长按后连点七下"之类的隐藏手势 ——
        // 家长遇到问题时要能自己找到它。文案直说连不上时该点这里。
        val diag = TextView(context).apply {
            text = "查看诊断信息（连不上 / 白屏 / 某个功能打不开时点这里）"
            textSize = 13f
            setTextColor(Color.parseColor("#2563EB"))
            gravity = Gravity.CENTER
            setPadding(pad(8), pad(4), pad(8), pad(10))
            setOnClickListener { onDiagnostics() }
        }
        addView(diag)

        errorBox.apply {
            textSize = 13.5f
            setTextColor(Color.parseColor("#B91C1C"))
            gravity = Gravity.CENTER
            visibility = GONE
        }
        addView(
            errorBox,
            LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
                .also { it.topMargin = pad(8) }
        )
    }

    /** 显示错误（连不上时由 MainActivity 调用）。 */
    fun showError(msg: String?) {
        if (msg.isNullOrBlank()) {
            errorBox.visibility = GONE
        } else {
            errorBox.text = msg
            errorBox.visibility = VISIBLE
        }
    }

    /**
     * 扫码之后把结果填进输入框（只填不提交）。
     *
     * 刻意不自动连接：扫到的可能是公网地址、也可能是内网地址，
     * 让家长看一眼再点「连接」，扫错了还能改。
     */
    fun setUrl(raw: String) {
        input.setText(ServerConfig.normalize(raw))
        input.setSelection(input.text?.length ?: 0)
        showError(null)
    }

    /** 聚焦到输入框并全选，方便家长直接覆盖重填。 */
    fun focusInput() {
        input.requestFocus()
        input.setSelection(0, input.text?.length ?: 0)
    }

    private fun title(t: String) = TextView(context).apply {
        text = t
        textSize = 26f
        gravity = Gravity.CENTER
        setTextColor(Color.parseColor("#1F2328"))
    }

    private fun subtitle(t: String) = TextView(context).apply {
        text = t
        textSize = 14f
        gravity = Gravity.CENTER
        setTextColor(Color.parseColor("#6B7280"))
        setPadding(0, pad(8), 0, 0)
    }

    private fun tip(t: String) = TextView(context).apply {
        text = t
        textSize = 12.5f
        setTextColor(Color.parseColor("#6B7280"))
        setLineSpacing(pad(3).toFloat(), 1f)
        setPadding(0, pad(12), 0, 0)
    }
}
