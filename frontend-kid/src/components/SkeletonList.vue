<template>
  <div class="sk-list" aria-hidden="true">
    <div v-for="i in count" :key="i" class="sk-row">
      <div class="sk-cover sk-anim"></div>
      <div class="sk-body">
        <div class="sk-line sk-anim" style="width: 78%"></div>
        <div class="sk-line sk-anim sm" style="width: 46%"></div>
      </div>
    </div>
  </div>
</template>

<script setup>
// 骨架屏：比转圈更接近"马上就出来"的感觉，也不会让页面高度先塌再跳。
defineProps({
  count: { type: Number, default: 5 },
})
</script>

<style scoped>
.sk-list {
  padding-top: 4px;
}

.sk-row {
  display: flex;
  gap: 13px;
  padding: 12px;
  margin-bottom: 10px;
  background: var(--c-card);
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
}

.sk-cover {
  flex: 0 0 auto;
  width: 76px;
  height: 76px;
  border-radius: 14px;
}

.sk-body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 9px;
}

.sk-line {
  /* 高度与圆角定义在全局样式表，这里只声明本组件内的默认宽度语义 */
  width: 100%;
}
</style>
