import { ElMessage } from 'element-plus'

/**
 * 复制文本到剪贴板。
 *
 * ⚠️ 必须带 execCommand 兜底，不能只用 navigator.clipboard：
 * 后台很可能跑在 **http**（非 https）下，而那种来源里 navigator.clipboard
 * 是 undefined（不是"抛错"，是压根没有这个属性）。直接 await 它会抛
 * TypeError，用户看到的现象是"点了复制、什么都没发生、也没有报错" ——
 * 而复制分享链接恰恰是这个界面最高频的操作。
 *
 * 两条路都不行时明确说出来，不假装成功：分享链接是要贴到手机上用的，
 * 让用户以为复制成功了、粘出来是空的，比直接告诉他"请手动选中"更糟。
 */
export async function copyText(text) {
  const s = String(text || '')
  if (!s) return false

  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(s)
      return true
    }
  } catch (e) {
    /* 落到下面的兜底 */
  }

  try {
    const ta = document.createElement('textarea')
    ta.value = s
    // 不能用 display:none —— 隐藏元素无法被 select()，复制出来是空的。
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    ta.setSelectionRange(0, s.length)
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) return true
  } catch (e) {
    /* 落到下面的提示 */
  }

  ElMessage.warning('复制失败，请手动选中后复制')
  return false
}

/** 复制并提示（成功才提示，失败由 copyText 自己提示）。 */
export async function copyWithToast(text, label = '已复制') {
  if (await copyText(text)) ElMessage.success(label)
}
