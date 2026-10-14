<template>
  <transition name="bt-fade">
    <button
      v-if="visible"
      class="back-top pressable"
      type="button"
      aria-label="回到顶部"
      @click="toTop"
    >↑</button>
  </transition>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue'

const props = defineProps({
  // 滚过多少像素才出现。400 大约是"一屏半"，短页面不会闪出来。
  threshold: { type: Number, default: 400 },
})

const visible = ref(false)

function onScroll() {
  visible.value = window.scrollY > props.threshold
}

function toTop() {
  // 'smooth' 在部分旧内核上会被忽略，那样也只是直接跳顶，不影响可用性
  try {
    window.scrollTo({ top: 0, behavior: 'smooth' })
  } catch {
    window.scrollTo(0, 0)
  }
}

onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
  onScroll()
})
onUnmounted(() => window.removeEventListener('scroll', onScroll))
</script>

<style scoped>
.back-top {
  position: fixed;
  right: 16px;
  /* 抬到 TabBar 之上：压在导航条上会点不准 */
  bottom: calc(76px + var(--safe-bottom));
  z-index: 90;
  width: 42px;
  height: 42px;
  border: 0;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.94);
  backdrop-filter: blur(8px);
  box-shadow: var(--shadow-md);
  color: var(--c-primary);
  font-size: 20px;
  line-height: 1;
  font-family: inherit;
}

.bt-fade-enter-active,
.bt-fade-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}
.bt-fade-enter-from,
.bt-fade-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
</style>
