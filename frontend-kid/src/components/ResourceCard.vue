<template>
  <div class="res-card pressable" @click="$emit('click')">
    <!-- 封面：有图用图，无图用类型色块 + 图标 -->
    <div class="cover" :style="coverStyle">
      <img v-if="node.cover" :src="node.cover" alt="" class="cover-img" loading="lazy" decoding="async" />
      <span v-else class="cover-emoji">{{ typeEmoji }}</span>

      <!-- 类型角标 -->
      <span class="badge" :style="{ background: typeColor }">
        {{ typeLabel }}
      </span>
    </div>

    <div class="body">
      <div class="title ellipsis-2">{{ node.title }}</div>
      <div class="meta">
        <!-- 所属目录：只在搜索页打开。孩子从"找一找"看到内容时，
             最需要知道的是"它在哪个文件夹里" —— 否则只能点进去才知道。 -->
        <span v-if="showCategory && categoryName" class="chip cat">📁 {{ categoryName }}</span>
        <span v-if="durationText" class="chip">⏱ {{ durationText }}</span>
        <span v-if="node.fileCount > 1" class="chip">📄 {{ node.fileCount }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { tint } from '@/assets/icons'

const props = defineProps({
  node: { type: Object, required: true },
  showCategory: { type: Boolean, default: false },
})

defineEmits(['click'])

const categoryName = computed(() => props.node?.category?.name || '')

const TYPE_META = {
  audio: { emoji: '🎧', label: '音频', color: '#269A99' },
  video: { emoji: '🎬', label: '视频', color: '#FF9D4D' },
  pdf: { emoji: '📄', label: '绘本', color: '#7F8C9A' },
  image: { emoji: '🖼️', label: '图片', color: '#F08BB4' },
}

const meta = computed(() => TYPE_META[props.node.mediaType] || TYPE_META.audio)
const typeEmoji = computed(() => meta.value.emoji)
const typeLabel = computed(() => meta.value.label)
const typeColor = computed(() => meta.value.color)

const coverStyle = computed(() => ({
  background: props.node.cover ? '#eef2f7' : tint(meta.value.color, 0.16),
}))

const durationText = computed(() => {
  const d = props.node.duration
  if (!d || d <= 0) return ''
  const m = Math.floor(d / 60)
  const s = d % 60
  if (m >= 60) {
    const h = Math.floor(m / 60)
    return `${h}小时${m % 60}分`
  }
  return m > 0 ? `${m}分${s}秒` : `${s}秒`
})
</script>

<style scoped>
.res-card {
  display: flex;
  gap: 13px;
  padding: 12px;
  background: var(--c-card);
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  margin-bottom: 10px;
}

.cover {
  position: relative;
  flex: 0 0 auto;
  width: 76px;
  height: 76px;
  border-radius: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.cover-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.cover-emoji {
  font-size: 34px;
  line-height: 1;
}

.badge {
  position: absolute;
  left: 0;
  bottom: 0;
  padding: 2px 7px;
  font-size: 10px;
  font-weight: 600;
  color: #fff;
  border-top-right-radius: 9px;
  border-bottom-left-radius: 13px;
}

.body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  justify-content: center;
}

.title {
  font-size: 15.5px;
  font-weight: 600;
  color: var(--c-text);
  line-height: 1.4;
}

.meta {
  display: flex;
  gap: 7px;
  margin-top: 6px;
  flex-wrap: wrap;
}
.chip {
  font-size: 11.5px;
  color: var(--c-text-sub);
  background: #f3f6fa;
  padding: 2px 8px;
  border-radius: 999px;
}

/* 所属目录：蓝色系与"时长/文件数"的灰区分开，且超长时省略 */
.chip.cat {
  color: #4a7ef0;
  background: rgba(91, 143, 249, 0.12);
  max-width: 52%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
