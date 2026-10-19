<template>
  <div class="play-page">
    <div class="play-header">
      <el-button :icon="ArrowLeft" text @click="$router.back()">返回</el-button>
      <h2 class="play-title">{{ info.title || '加载中...' }}</h2>
      <div class="play-meta text-secondary">
        <span v-if="info.categoryName">{{ info.categoryName }}</span>
        <span v-if="info.mediaType"> · {{ mediaText(info.mediaType) }}</span>
      </div>
    </div>

    <div class="play-body" v-loading="loading">
      <!-- 视频播放 -->
      <div v-if="info.mediaType === 'video'" class="player-wrap">
        <video
          ref="videoEl"
          class="player"
          controls
          playsinline
          :poster="info.poster"
          @timeupdate="onTimeUpdate"
          @loadedmetadata="onLoaded"
        >
          <source :src="playUrl" />
          您的浏览器不支持视频播放
        </video>
      </div>

      <!-- 音频播放 -->
      <div v-else-if="info.mediaType === 'audio'" class="audio-wrap">
        <div class="audio-cover">
          <img v-if="info.poster" :src="info.poster" alt="cover" />
          <div v-else class="audio-icon">🎵</div>
        </div>
        <audio ref="audioEl" class="audio-player" controls :src="playUrl" @timeupdate="onTimeUpdate">
          您的浏览器不支持音频播放
        </audio>
      </div>

      <!-- PDF 预览 -->
      <div v-else-if="info.mediaType === 'pdf'" class="pdf-wrap">
        <iframe v-if="playUrl" :src="playUrl" class="pdf-frame"></iframe>
      </div>

      <!-- 其他 -->
      <div v-else class="other-wrap">
        <el-empty description="该资源暂不支持在线预览">
          <el-button type="primary" @click="openExternal">在网盘中打开</el-button>
        </el-empty>
      </div>

      <!-- 外部播放提示 -->
      <el-alert
        v-if="info.playMode === 'external'"
        type="warning"
        :closable="false"
        class="mt-16"
        title="该资源未转存到网盘账号，无法直接播放。请先通过「转存」功能保存到网盘后再播放，或点击在原网盘页面打开。"
      >
        <el-button size="small" type="primary" @click="openExternal">在原网盘打开</el-button>
      </el-alert>
    </div>

    <div class="play-footer text-secondary">
      <span v-if="info.lastPosition > 0">上次播放到 {{ formatDuration(info.lastPosition) }}</span>
      <span v-if="info.playMode">播放模式：{{ modeText(info.playMode) }}</span>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import {  ElMessage  } from 'element-plus'
import { ArrowLeft } from '@element-plus/icons-vue'
import { playApi } from '@/api'

const route = useRoute()
const info = ref({})
const loading = ref(true)
const videoEl = ref()
const audioEl = ref()
let reportTimer = null

const playUrl = computed(() => info.value.url || '')

function mediaText(t) {
  return { video: '视频', audio: '音频', pdf: 'PDF 文档', image: '图片', ebook: '电子书' }[t] || t
}

function modeText(m) {
  return {
    direct: '直链播放',
    proxy: '服务端中转',
    hls: 'HLS 转码',
    audioformat: '音频转码（AAC）',
    ebook: '电子书转换（EPUB）',
    external: '外部打开',
  }[m] || m
}

function formatDuration(sec) {
  if (!sec) return '00:00'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.floor(sec % 60)
  const pad = (n) => String(n).padStart(2, '0')
  return h > 0 ? `${pad(h)}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

function onLoaded() {
  if (videoEl.value && info.value.lastPosition > 0) {
    videoEl.value.currentTime = info.value.lastPosition
  }
}

function onTimeUpdate(e) {
  const el = e.target
  if (!el.duration) return
  // 每 15 秒上报一次进度
  const now = Date.now()
  if (now - lastReport < 15000) return
  lastReport = now
  reportProgress(el.currentTime, el.duration)
}

let lastReport = 0

async function reportProgress(position, duration) {
  try {
    await playApi.progress(route.params.id, {
      position: Math.floor(position),
      duration: Math.floor(duration),
      playedSeconds: 15,
    })
  } catch (err) {
    // 静默失败，不打扰播放
  }
}

function openExternal() {
  if (info.value.url) window.open(info.value.url, '_blank')
}

onMounted(async () => {
  try {
    info.value = await playApi.info(route.params.id)
  } catch (e) {
    ElMessage.error('加载播放信息失败')
  } finally {
    loading.value = false
  }
})

onUnmounted(() => {
  // 离开时上报最终进度
  const el = videoEl.value || audioEl.value
  if (el && el.duration) {
    reportProgress(el.currentTime, el.duration)
  }
  if (reportTimer) clearInterval(reportTimer)
})
</script>

<style scoped>
.play-page {
  min-height: 100vh;
  background: #14161a;
  color: #fff;
  padding: 0 0 40px;
}

.play-header {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px 24px;
  border-bottom: 1px solid #26292f;
}

.play-title {
  font-size: 17px;
  font-weight: 500;
  margin: 0;
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.play-body {
  max-width: 1000px;
  margin: 0 auto;
  padding: 24px;
}

.player-wrap {
  background: #000;
  border-radius: 10px;
  overflow: hidden;
}

.player {
  width: 100%;
  max-height: 70vh;
  display: block;
}

.audio-wrap {
  text-align: center;
  padding: 40px 0;
}

.audio-cover {
  width: 220px;
  height: 220px;
  margin: 0 auto 24px;
  border-radius: 16px;
  overflow: hidden;
  background: #26292f;
  display: flex;
  align-items: center;
  justify-content: center;
}

.audio-cover img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.audio-icon {
  font-size: 80px;
}

.audio-player {
  width: 100%;
  max-width: 600px;
}

.pdf-wrap {
  background: #fff;
  border-radius: 10px;
  overflow: hidden;
  height: 75vh;
}

.pdf-frame {
  width: 100%;
  height: 100%;
  border: none;
}

.other-wrap {
  padding: 60px 0;
}

.play-footer {
  max-width: 1000px;
  margin: 0 auto;
  padding: 0 24px;
  display: flex;
  gap: 20px;
  font-size: 12px;
}
</style>
