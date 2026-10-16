// 服务端「构建信息」的展示工具。
//
// 只有两个函数，但**两个页面都要用**（概览页、设置页的「系统状态」卡），
// 所以抽出来。两份各写一遍的结果是某天只改了一处，两个页面显示得不一样，
// 而"版本/构建"恰恰是出问题时最需要相信的那一行。
//
// 数据来自 `GET /api/admin/system/status`（后端 internal/version 注入）：
//   version    服务端版本号，唯一来源是 internal/version/version.go
//   commit     构建时的 git 短哈希（CI 注入；本地 go run 为空）
//   buildTime  构建时刻（UTC RFC3339；同上）

/**
 * 版本号后面那段构建信息，形如 `(a1b2c3d · 10-16 14:41)`。
 *
 * 取不到时返回**空串**而不是「未知」「—」：本地 `go run` 与部署较早的
 * 镜像里 commit/buildTime 本来就是空的，那不是错误状态，
 * 页面直接整段不渲染即可 —— 显示成"未知"反而会让人以为构建坏了。
 */
export function buildLabel(status) {
  if (!status) return ''
  const parts = []
  if (status.commit) parts.push(status.commit)
  const t = formatBuildTime(status.buildTime)
  if (t) parts.push(t)
  return parts.length ? `(${parts.join(' · ')})` : ''
}

/**
 * RFC3339 构建时刻 → `10-16 14:41`（本地时区）。
 *
 * 只到分钟：构建时间用于"这是不是我刚推的那次构建"，
 * 精确到秒没有意义，反而把这一行撑长。不补年份同理 ——
 * 没人会拿一年前的镜像排查当天的问题，真需要年份时页面上还有别的线索。
 */
export function formatBuildTime(t) {
  if (!t) return ''
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return String(t)
  return d.toLocaleString('zh-CN', {
    hour12: false,
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}
