<template>
  <div class="cat-card pressable" :style="cardStyle" @click="$emit('click')">
    <!-- 图标：彩色圆形底 + emoji -->
    <div class="icon-wrap" :style="{ background: tint(icon.color, 0.18) }">
      <span class="emoji">{{ icon.emoji }}</span>
    </div>

    <div class="body">
      <div class="name ellipsis-2">{{ node.name }}</div>
      <div v-if="subtitle" class="sub ellipsis">{{ subtitle }}</div>
    </div>

    <!-- 有子级时显示小箭头，暗示可点进去 -->
    <span v-if="hasChildren" class="arrow">›</span>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { resolveIcon, tint } from '@/assets/icons'

const props = defineProps({
  node: { type: Object, required: true },
})

defineEmits(['click'])

const icon = computed(() => resolveIcon(props.node.icon, props.node.color))

const hasChildren = computed(() => (props.node.children?.length || 0) > 0)

// 副标题：优先显示资源数，其次描述
const subtitle = computed(() => {
  const n = props.node.resourceCount
  if (n > 0) return `${n} 个内容`
  if (hasChildren.value) return `${props.node.children.length} 个分类`
  return props.node.description || ''
})

const cardStyle = computed(() => ({
  borderLeft: `4px solid ${icon.value.color}`,
}))
</script>

<style scoped>
.cat-card {
  position: relative;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 14px 16px;
  background: var(--c-card);
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  margin-bottom: 10px;
}

.icon-wrap {
  flex: 0 0 auto;
  width: 52px;
  height: 52px;
  border-radius: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.emoji {
  font-size: 28px;
  line-height: 1;
}

.body {
  flex: 1;
  min-width: 0;
}

.name {
  font-size: 16px;
  font-weight: 600;
  color: var(--c-text);
  line-height: 1.35;
}

.sub {
  margin-top: 3px;
  font-size: 12.5px;
  color: var(--c-text-sub);
}

.arrow {
  flex: 0 0 auto;
  font-size: 24px;
  color: #c8d3e0;
  line-height: 1;
  margin-left: -4px;
}
</style>
