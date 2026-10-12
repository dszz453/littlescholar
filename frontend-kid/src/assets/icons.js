/**
 * 儿童化图标库
 *
 * 设计思路：
 * - 用 emoji 而非图标字体：无需加载资源、跨平台一致、孩子天然熟悉
 * - 每个图标配一个背景色，形成彩色卡片，视觉上更有吸引力
 * - 与后台的 IconLibrary 保持 key 对齐，后台选了图标孩子端就能正确显示
 */

// key -> { emoji, 默认色 }
export const ICON_MAP = {
  // 学科
  english:   { emoji: '🔤', color: '#4A90D9' },
  chinese:   { emoji: '📖', color: '#D9554A' },
  math:      { emoji: '🔢', color: '#5AD8A6' },
  science:   { emoji: '🔬', color: '#6DC8EC' },
  music:     { emoji: '🎵', color: '#F6BD16' },
  art:       { emoji: '🎨', color: '#F08BB4' },
  pe:        { emoji: '⚽', color: '#5B8FF9' },

  // 内容形态
  book:      { emoji: '📚', color: '#9270CA' },
  video:     { emoji: '🎬', color: '#FF9D4D' },
  audio:     { emoji: '🎧', color: '#269A99' },
  pdf:       { emoji: '📄', color: '#7F8C9A' },
  story:     { emoji: '📗', color: '#61DDAA' },
  poem:      { emoji: '🖋️', color: '#C99A4A' },

  // 童趣主题
  star:      { emoji: '⭐', color: '#F6BD16' },
  moon:      { emoji: '🌙', color: '#7262FD' },
  sun:       { emoji: '☀️', color: '#FF9D4D' },
  rocket:    { emoji: '🚀', color: '#5B8FF9' },
  robot:     { emoji: '🤖', color: '#6DC8EC' },
  dinosaur:  { emoji: '🦕', color: '#5AD8A6' },
  panda:     { emoji: '🐼', color: '#7F8C9A' },
  cat:       { emoji: '🐱', color: '#F6BD16' },
  rabbit:    { emoji: '🐰', color: '#F08BB4' },
  flower:    { emoji: '🌸', color: '#F08BB4' },
  apple:     { emoji: '🍎', color: '#D9554A' },
  cake:      { emoji: '🎂', color: '#FF9D4D' },
  gift:      { emoji: '🎁', color: '#D9554A' },
  balloon:   { emoji: '🎈', color: '#F08BB4' },
  puzzle:    { emoji: '🧩', color: '#5AD8A6' },
  crayon:    { emoji: '🖍️', color: '#5B8FF9' },
  globe:     { emoji: '🌍', color: '#269A99' },
  clock:     { emoji: '⏰', color: '#7262FD' },
  trophy:    { emoji: '🏆', color: '#F6BD16' },
  lightbulb: { emoji: '💡', color: '#F6BD16' },
  seedling:  { emoji: '🌱', color: '#61DDAA' },
  umbrella:  { emoji: '☂️', color: '#5B8FF9' },
  train:     { emoji: '🚂', color: '#FF9D4D' },
  plane:     { emoji: '✈️', color: '#6DC8EC' },
  car:       { emoji: '🚗', color: '#D9554A' },
}

// 默认图标（未设置或未知 key 时使用）
const DEFAULT_ICON = { emoji: '📁', color: '#5B8FF9' }

/**
 * 根据 key 取图标
 * @param {string} key  图标标识
 * @param {string} [customColor] 自定义色（后台可为单个目录配色）
 */
export function resolveIcon(key, customColor) {
  const found = ICON_MAP[key] || DEFAULT_ICON
  return {
    emoji: found.emoji,
    color: customColor || found.color,
  }
}

/**
 * 取图标对应的浅色背景（用于卡片底）
 * 把主色转成极浅的同色系，保证彩卡协调
 */
export function tint(color, alpha = 0.12) {
  const hex = (color || '#5B8FF9').replace('#', '')
  const r = parseInt(hex.slice(0, 2), 16)
  const g = parseInt(hex.slice(2, 4), 16)
  const b = parseInt(hex.slice(4, 6), 16)
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}
