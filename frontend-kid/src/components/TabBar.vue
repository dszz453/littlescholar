<template>
  <nav class="tabbar">
    <div
      v-for="item in items"
      :key="item.name"
      class="tab"
      :class="{ active: isActive(item) }"
      @click="go(item)"
    >
      <span class="ico">{{ item.emoji }}</span>
      <span class="label">{{ item.label }}</span>
    </div>
  </nav>
</template>

<script setup>
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()

const items = [
  { name: 'home', path: '/', label: '首页', emoji: '🏠' },
  { name: 'search', path: '/search', label: '找一找', emoji: '🔍' },
  { name: 'tutor', path: '/tutor', label: '小书童', emoji: '🤖' },
  { name: 'me', path: '/me', label: '我的', emoji: '🐻' },
]

// 高亮：首页 tab 在「目录页」也保持点亮，让层级感连续。
function isActive(item) {
  if (item.name === 'home') {
    return route.name === 'home' || route.name === 'category'
  }
  return route.name === item.name
}

// 跳转：**不能**复用 isActive —— 高亮把 category 也算作首页，
// 一旦复用，「在目录页点首页」会被判为"已激活"而直接 return，
// 表现就是点了没反应（历史 bug）。这里只认精确的路由名。
function go(item) {
  if (route.name === item.name) return
  router.push(item.path)
}
</script>

<style scoped>
.tabbar {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 100;
  display: flex;
  background: rgba(255, 255, 255, 0.96);
  backdrop-filter: blur(12px);
  border-top: 1px solid var(--c-border);
  padding-bottom: var(--safe-bottom);
}

.tab {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 8px 0 6px;
  color: var(--c-text-sub);
  transition: color 0.18s ease;
  user-select: none;
}

.tab .ico {
  font-size: 23px;
  line-height: 1.1;
  transition: transform 0.2s cubic-bezier(0.34, 1.56, 0.64, 1);
}

.tab .label {
  font-size: 11px;
  font-weight: 500;
}

.tab.active {
  color: var(--c-primary);
}
.tab.active .ico {
  transform: scale(1.14) translateY(-1px);
}
.tab.active .label {
  font-weight: 700;
}
</style>
