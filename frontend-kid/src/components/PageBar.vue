<template>
  <header class="page-bar" :class="{ sticky }">
    <div class="pb-row">
      <button
        v-if="showBack"
        class="pb-back pressable"
        type="button"
        aria-label="返回"
        @click="onBack"
      >‹</button>

      <div class="pb-main">
        <!-- 默认渲染标题；需要面包屑之类的复杂内容时用默认插槽覆盖 -->
        <slot>
          <div class="pb-title ellipsis">{{ title }}</div>
        </slot>
      </div>

      <div class="pb-extra">
        <slot name="right" />
      </div>
    </div>
  </header>
</template>

<script setup>
import { useRouter } from 'vue-router'

const props = defineProps({
  title: { type: String, default: '' },
  showBack: { type: Boolean, default: true },
  // 没有历史可退时（直接打开链接进来）的落点
  fallback: { type: String, default: 'home' },
  sticky: { type: Boolean, default: true },
})

const emit = defineEmits(['back'])
const router = useRouter()

// 返回一律走 router.back()，但在"直接打开深链"的场景下 history 里
// 没有上一页，back() 会把人送出站外 —— 这时退回 fallback。
function onBack() {
  emit('back')
  if (window.history.length > 1) router.back()
  else router.push({ name: props.fallback })
}
</script>

<style scoped>
.page-bar {
  padding: 0 16px 10px;
  padding-top: calc(8px + var(--safe-top));
  background: linear-gradient(180deg, #e8f0ff 0%, var(--c-bg) 100%);
}
.page-bar.sticky {
  position: sticky;
  top: 0;
  z-index: 50;
}

.pb-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 40px;
}

.pb-back {
  flex: 0 0 auto;
  width: 40px;
  height: 40px;
  border: 0;
  border-radius: 50%;
  background: #fff;
  box-shadow: var(--shadow-sm);
  font-size: 24px;
  line-height: 1;
  color: var(--c-text);
  display: flex;
  align-items: center;
  justify-content: center;
  padding-bottom: 3px;
  font-family: inherit;
}

.pb-main {
  flex: 1;
  min-width: 0;
}

.pb-title {
  font-size: 18px;
  font-weight: 700;
  color: var(--c-text);
}

.pb-extra {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
