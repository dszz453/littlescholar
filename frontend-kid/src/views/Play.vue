<template>
  <div class="play-page">
    <!-- ===== 顶栏 ===== -->
    <header class="topbar">
      <button class="back pressable" @click="goBack" aria-label="返回">←</button>
      <div class="head-info">
        <div class="head-title ellipsis">{{ info.title || '加载中…' }}</div>
        <div class="head-sub ellipsis">{{ info.categoryName || '' }}</div>
      </div>
      <button class="more pressable" @click="showPlaylist = true" aria-label="播放列表">
        ☰
      </button>
    </header>

    <!-- ===== 加载 / 错误 ===== -->
    <div v-if="loading" class="loading"><div class="spinner"></div></div>

    <div v-else-if="error" class="empty">
      <div class="emoji">😢</div>
      <p>{{ error }}</p>
      <button class="retry pressable" @click="load">再试一次</button>
    </div>

    <!-- ===== 播放区 ===== -->
    <template v-else>
      <!-- 音频：封面 + 波形装饰 -->
      <section v-if="isAudio" class="media-area audio-area">
        <div class="cover-blob" :style="coverStyle">
          <img v-if="info.poster" :src="info.poster" alt="" class="cover-img" />
          <span v-else class="cover-emoji">🎧</span>
        </div>
        <div class="audio-title ellipsis">{{ info.title }}</div>
        <div class="audio-sub ellipsis">{{ info.categoryName }}</div>

        <!-- 简易律动条，仅在播放时动 -->
        <div class="wave" :class="{ playing: isPlaying }">
          <span v-for="n in 5" :key="n" :style="{ animationDelay: n * 90 + 'ms' }"></span>
        </div>

        <audio
          ref="audioEl"
          :src="info.url"
          preload="metadata"
          playsinline
          @timeupdate="onTimeUpdate"
          @loadedmetadata="onLoadedMetadata"
          @play="isPlaying = true"
          @pause="isPlaying = false"
          @ended="onEnded"
          @error="onMediaError"
        />
      </section>

      <!-- 视频：原生 video，支持全屏与画中画 -->
      <section v-else-if="isVideo" class="media-area video-area">
        <video
          ref="videoEl"
          class="video"
          :src="info.url"
          :poster="info.poster"
          preload="metadata"
          playsinline
          webkit-playsinline
          controls
          x5-video-player-type="h5-page"
          @timeupdate="onTimeUpdate"
          @loadedmetadata="onLoadedMetadata"
          @play="isPlaying = true"
          @pause="isPlaying = false"
          @ended="onEnded"
          @error="onMediaError"
        />
        <p class="tip-lock">
          <span class="lock-emoji">🔒</span>
          播放中直接锁屏也不会停，把手机翻过来听也行
        </p>
        <div v-if="transcoding" class="transcode-tip">
          <span class="transcode-dot"></span>
          正在为这台设备准备更流畅的版本（{{ transcodeProgress }}%），转好后会自动切换
        </div>
      </section>

      <!-- PDF：内嵌 + 新窗口打开兜底 -->
      <section v-else-if="isPDF" class="media-area pdf-area">
        <iframe
          v-if="pdfBlobUrl"
          :src="pdfBlobUrl"
          class="pdf-frame"
          title="绘本阅读"
        ></iframe>
        <div v-else class="loading pdf-wait">
          <div class="spinner"></div>
          <p class="pdf-wait-text">正在打开绘本…</p>
        </div>
        <a class="pdf-open pressable" :href="info.url" target="_blank" rel="noopener">
          在新窗口打开 📖
        </a>
      </section>

      <!-- 图片：支持双指缩放 -->
      <section v-else-if="isImage" class="media-area image-area">
        <img :src="info.url" alt="" class="image" @click="toggleZoom" :class="{ zoomed: zoomed }" />
      </section>

      <!-- 外部跳转（无直链时） -->
      <section v-else class="media-area ext-area">
        <div class="empty">
          <div class="emoji">🔗</div>
          <p>这个资源要去网盘里播放</p>
          <a class="retry pressable" :href="info.url" target="_blank" rel="noopener">
            打开网盘
          </a>
        </div>
      </section>

      <!-- ===== 播放控制条（音频 / 图片 / PDF 用，视频用原生控件） ===== -->
      <section v-if="isAudio && !error" class="controls">
        <button class="skip pressable" @click="seekBy(-15)" aria-label="后退15秒">↺15</button>
        <button class="play-btn pressable" @click="togglePlay" aria-label="播放暂停">
          {{ isPlaying ? '⏸' : '▶' }}
        </button>
        <button class="skip pressable" @click="seekBy(15)" aria-label="快进15秒">15↻</button>
      </section>

      <!-- ===== 进度条 ===== -->
      <section v-if="(isAudio || isVideo) && duration > 0" class="progress-wrap">
        <span class="t">{{ fmtClock(current) }}</span>
        <input
          class="range"
          type="range"
          min="0"
          :max="duration"
          step="1"
          :value="current"
          :style="rangeStyle"
          @input="onSeekInput"
          @change="onSeekCommit"
        />
        <span class="t">{{ fmtClock(duration) }}</span>
      </section>

      <!-- ===== 倍速 / 循环（音频） ===== -->
      <section v-if="isAudio" class="extras">
        <div class="rate-group">
          <button
            v-for="r in RATES"
            :key="r"
            class="rate pressable"
            :class="{ on: rate === r }"
            @click="setRate(r)"
          >
            {{ r }}x
          </button>
        </div>
        <button class="loop pressable" :class="{ on: loop }" @click="toggleLoop">
          🔁 单曲循环
        </button>
      </section>

      <!-- ===== 详情 ===== -->
      <section class="detail card">
        <div class="detail-row">
          <span class="k">类型</span>
          <span class="v">{{ typeLabel }}</span>
        </div>
        <div v-if="info.categoryName" class="detail-row">
          <span class="k">分类</span>
          <span class="v ellipsis">{{ info.categoryName }}</span>
        </div>
        <div v-if="duration > 0" class="detail-row">
          <span class="k">时长</span>
          <span class="v">{{ fmtClock(duration) }}</span>
        </div>
        <div class="detail-row">
          <span class="k">学习进度</span>
          <span class="v">{{ Math.round(percent) }}%</span>
        </div>
      </section>

      <!-- ===== 同一目录下其他内容 ===== -->
      <section v-if="siblings.length" class="siblings">
        <h3 class="sib-title">这个文件夹里还有</h3>
        <div
          v-for="(s, i) in siblings"
          :key="s.id"
          class="sib-item pressable pop-in"
          :style="{ animationDelay: i * 40 + 'ms' }"
          @click="switchTo(s.id)"
        >
          <span class="sib-emoji">🎵</span>
          <span class="sib-name ellipsis">{{ s.title }}</span>
          <span class="sib-go">›</span>
        </div>
      </section>
    </template>

    <!-- ===== 播放列表面板 ===== -->
    <transition name="fade">
      <div v-if="showPlaylist" class="sheet-mask" @click.self="showPlaylist = false">
        <div class="sheet">
          <div class="sheet-bar"></div>
          <h3 class="sheet-title">播放列表</h3>
          <div class="sheet-list">
            <div
              class="sheet-item pressable"
              :class="{ on: it.id === resourceId }"
              v-for="it in playlist"
              :key="it.id"
              @click="switchTo(it.id)"
            >
              <span class="si-emoji">{{ it.id === resourceId ? '🔊' : '🎵' }}</span>
              <span class="si-name ellipsis">{{ it.title }}</span>
              <span class="si-time">{{ fmtClock(it.duration) }}</span>
            </div>
          </div>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { playApi } from '@/api'
import { tint } from '@/assets/icons'

const route = useRoute()
const router = useRouter()

const RATES = [0.75, 1, 1.25, 1.5]
const REPORT_MS = 15000

const resourceId = computed(() => Number(route.params.id))

const info = ref({})
const loading = ref(true)
const error = ref('')

const audioEl = ref(null)
const videoEl = ref(null)

const isPlaying = ref(false)
const current = ref(0)
const duration = ref(0)
const rate = ref(1)
const loop = ref(false)
const zoomed = ref(false)

const siblings = ref([])
const playlist = ref([])
const showPlaylist = ref(false)
const pdfBlobUrl = ref('')

// 转码状态：后台在为首播转码时置位，用于给家长/孩子一个"正在优化播放"的提示
const transcoding = ref(false)
const transcodeProgress = ref(0)

let reportTimer = null
let seekReported = 0

/* ---------- 媒体类型 ---------- */
const isAudio = computed(() => info.value.mediaType === 'audio')
const isVideo = computed(() => info.value.mediaType === 'video')
const isPDF = computed(() => info.value.mediaType === 'pdf')
const isImage = computed(() => info.value.mediaType === 'image')

const TYPE_LABEL = { audio: '音频', video: '视频', pdf: '绘本', image: '图片' }
const typeLabel = computed(() => TYPE_LABEL[info.value.mediaType] || '资源')

const coverStyle = computed(() => {
  const base = isVideo.value ? '#FF9D4D' : '#269A99'
  return { background: tint(base, 0.16) }
})

const percent = computed(() => {
  if (!duration.value) return 0
  return Math.min(100, (current.value / duration.value) * 100)
})

const rangeStyle = computed(() => ({
  background: `linear-gradient(to right, var(--c-primary) ${percent.value}%, #dde6f0 ${percent.value}%)`,
}))

/* ---------- 当前媒体元素 ---------- */
function mediaEl() {
  return isVideo.value ? videoEl.value : audioEl.value
}

/* ---------- 时间格式化 ---------- */
function fmtClock(sec) {
  const s = Math.max(0, Math.floor(Number(sec) || 0))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = s % 60
  const pad = (n) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(ss)}` : `${m}:${pad(ss)}`
}

/* ---------- 加载播放信息 ---------- */
async function load() {
  loading.value = true
  error.value = ''
  cleanupMedia()
  stopTranscodePolling()

  try {
    const data = await playApi.info(resourceId.value)
    info.value = data || {}
    duration.value = Number(data?.duration) || 0

    // 外部播放（无直链）：直接跳网盘
    if (data?.playMode === 'external' && data?.url) {
      window.location.href = data.url
      return
    }

    // 转码中：先用直链兜底播着，同时轮询转码进度，转好后再切成 HLS
    //
    // 不直接阻塞等待的原因：转码可能十几分钟，让孩子盯着转圈不如先给能播的内容。
    // 后端在 transcoding 模式下也会尽量带一个直链回来（url 字段）。
    if (data?.playMode === 'transcoding') {
      transcoding.value = true
      transcodeProgress.value = Number(data?.transcodeProgress) || 0
      startTranscodePolling(data?.transcodeTaskId)
    } else {
      transcoding.value = false
    }

    // 断点续播位置（等元数据加载后再 seek）
    pendingSeek = Number(data?.lastPosition) || 0

    if (isPDF.value) {
      await preparePDF(data.url)
    }
    if (isImage.value) {
      // 图片无需额外处理
    }
    setupMediaSession()

    loadSiblings()
    loadPlaylist()
  } catch (e) {
    error.value = e?.message || '资源加载失败'
  } finally {
    loading.value = false
  }
}

/* ---------- 转码轮询 ---------- */
let transcodeTimer = null

function stopTranscodePolling() {
  if (transcodeTimer) {
    clearInterval(transcodeTimer)
    transcodeTimer = null
  }
}

function startTranscodePolling(taskId) {
  if (!taskId) return
  stopTranscodePolling()
  // 10 秒一次：转码本身是分钟级，没必要问太勤
  transcodeTimer = setInterval(async () => {
    try {
      const st = await playApi.info(resourceId.value)
      const mode = st?.playMode
      if (mode === 'hls' && st?.url) {
        // 转好了：换源，保留当前播放位置
        const pos = currentTime()
        stopTranscodePolling()
        transcoding.value = false
        info.value = { ...info.value, ...st }
        await switchToHls(st.url, pos)
      } else if (mode === 'transcoding') {
        transcodeProgress.value = Number(st?.transcodeProgress) || 0
      } else {
        // 回退到直链/外部：停止轮询，不再打扰
        stopTranscodePolling()
        transcoding.value = false
      }
    } catch {
      // 轮询失败不打断播放，下一轮再试
    }
  }, 10000)
}

/** 从当前播放状态切到 HLS 源 */
async function switchToHls(url, resumeAt) {
  const el = mediaEl()
  if (!el || !url) return
  el.src = url
  if (resumeAt > 0) {
    pendingSeek = resumeAt
  }
  try {
    await el.play()
  } catch {
    // 自动播放可能被浏览器拦截，用户手动点一下即可
  }
}

/** 读取当前播放位置（秒） */
function currentTime() {
  const el = mediaEl()
  return el && !Number.isNaN(el.currentTime) ? el.currentTime : 0
}

let pendingSeek = 0

/* ---------- PDF：用 blob + objectURL 内嵌，避免跨域被拒 ---------- */
async function preparePDF(url) {
  if (!url) return
  try {
    const res = await fetch(url, { credentials: 'omit' })
    if (!res.ok) throw new Error('fetch failed')
    const blob = await res.blob()
    // 强制 pdf mime，确保浏览器识别为 PDF 而非下载
    const typed = blob.type === 'application/pdf' ? blob : new Blob([blob], { type: 'application/pdf' })
    pdfBlobUrl.value = URL.createObjectURL(typed)
  } catch {
    // 失败就让 iframe 直连原地址兜底
    pdfBlobUrl.value = url
  }
}

/* ---------- 媒体事件 ---------- */
function onLoadedMetadata() {
  const el = mediaEl()
  if (!el) return
  duration.value = Number(el.duration) || duration.value || 0
  if (pendingSeek > 0 && pendingSeek < duration.value - 2) {
    try {
      el.currentTime = pendingSeek
      current.value = pendingSeek
    } catch {
      /* 某些格式 seek 会抛错，忽略 */
    }
  }
  pendingSeek = 0
  // 音频自动播放（视频不自动播，避免流量与自动出声）
  if (isAudio.value) {
    el.play().catch(() => {
      // 浏览器策略拦截自动播放时静默失败，等用户点按钮
      isPlaying.value = false
    })
  }
}

function onTimeUpdate() {
  const el = mediaEl()
  if (!el) return
  current.value = el.currentTime || 0
  if (Number.isFinite(el.duration) && el.duration > 0) {
    duration.value = el.duration
  }
}

function onEnded() {
  isPlaying.value = false
  if (loop.value) {
    const el = mediaEl()
    if (el) {
      el.currentTime = 0
      el.play().catch(() => {})
    }
    return
  }
  flushProgress(true)
  // 自动播放同目录下一首
  const idx = playlist.value.findIndex((p) => p.id === resourceId.value)
  if (idx >= 0 && idx < playlist.value.length - 1) {
    router.replace(`/play/${playlist.value[idx + 1].id}`)
  }
}

function onMediaError() {
  // 直链过期是很常见的情况，给出可操作的提示而不是空白
  error.value = '播放地址可能已过期，请返回重新进入'
}

/* ---------- 播放控制 ---------- */
function togglePlay() {
  const el = mediaEl()
  if (!el) return
  if (el.paused) {
    el.play().catch(() => {})
  } else {
    el.pause()
  }
}

function seekBy(delta) {
  const el = mediaEl()
  if (!el) return
  const d = Number.isFinite(el.duration) ? el.duration : duration.value
  let t = (el.currentTime || 0) + delta
  t = Math.max(0, Math.min(t, d || t))
  el.currentTime = t
  current.value = t
}

let seekCommitTimer = null
function onSeekInput(e) {
  current.value = Number(e.target.value)
  // 拖动中做节流式预览 seek，手感更跟手
  const el = mediaEl()
  if (!el) return
  clearTimeout(seekCommitTimer)
  seekCommitTimer = setTimeout(() => {
    el.currentTime = current.value
  }, 60)
}

function onSeekCommit(e) {
  const el = mediaEl()
  if (!el) return
  clearTimeout(seekCommitTimer)
  el.currentTime = Number(e.target.value)
  current.value = Number(e.target.value)
}

function setRate(r) {
  rate.value = r
  const el = mediaEl()
  if (el) el.playbackRate = r
}

function toggleLoop() {
  loop.value = !loop.value
  const el = mediaEl()
  if (el) el.loop = loop.value
}

function toggleZoom() {
  zoomed.value = !zoomed.value
}

/* ---------- 进度上报 ---------- */
function currentPayload(finished = false) {
  return {
    position: Math.floor(current.value || 0),
    duration: Math.floor(duration.value || 0),
    playedSeconds: REPORT_MS / 1000,
    finished,
  }
}

function reportProgress(finished = false) {
  if (!duration.value) return
  playApi.progress(resourceId.value, currentPayload(finished)).catch(() => {
    // 上报失败不打扰孩子
  })
}

function flushProgress(finished = false) {
  reportProgress(finished)
}

function startReportTimer() {
  stopReportTimer()
  reportTimer = setInterval(() => {
    if (isPlaying.value) reportProgress(false)
  }, REPORT_MS)
}

function stopReportTimer() {
  if (reportTimer) {
    clearInterval(reportTimer)
    reportTimer = null
  }
}

/* ---------- MediaSession：锁屏 / 后台播放控制 ---------- */
function setupMediaSession() {
  if (!('mediaSession' in navigator)) return
  const artwork = info.value.poster
    ? [{ src: info.value.poster, sizes: '512x512', type: 'image/jpeg' }]
    : []
  try {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: info.value.title || '小书童',
      artist: info.value.categoryName || '小书童',
      album: '小书童 · 学习空间',
      artwork,
    })
    navigator.mediaSession.setActionHandler('play', () => {
      const el = mediaEl()
      el?.play().catch(() => {})
    })
    navigator.mediaSession.setActionHandler('pause', () => {
      mediaEl()?.pause()
    })
    navigator.mediaSession.setActionHandler('seekbackward', () => seekBy(-15))
    navigator.mediaSession.setActionHandler('seekforward', () => seekBy(15))
    navigator.mediaSession.setActionHandler('seekto', (d) => {
      const el = mediaEl()
      if (el && typeof d.seekTime === 'number') {
        el.currentTime = d.seekTime
        current.value = d.seekTime
      }
    })
    navigator.mediaSession.setActionHandler('previoustrack', () => {
      const idx = playlist.value.findIndex((p) => p.id === resourceId.value)
      if (idx > 0) switchTo(playlist.value[idx - 1].id)
    })
    navigator.mediaSession.setActionHandler('nexttrack', () => {
      const idx = playlist.value.findIndex((p) => p.id === resourceId.value)
      if (idx >= 0 && idx < playlist.value.length - 1) switchTo(playlist.value[idx + 1].id)
    })
  } catch {
    /* 某些浏览器不支持全部 action，忽略 */
  }
}

/* ---------- 列表加载 ---------- */
async function loadSiblings() {
  try {
    const data = await playApi.playlist(resourceId.value)
    const list = Array.isArray(data) ? data : data?.list || []
    playlist.value = list
    siblings.value = list.filter((it) => it.id !== resourceId.value).slice(0, 12)
  } catch {
    playlist.value = []
    siblings.value = []
  }
}

function loadPlaylist() {
  // playlist 已在 loadSiblings 中一并获取
}

/* ---------- 切换资源 ---------- */
function switchTo(id) {
  if (id === resourceId.value) {
    showPlaylist.value = false
    return
  }
  flushProgress(false)
  showPlaylist.value = false
  router.replace(`/play/${id}`)
}

/* ---------- 返回 ---------- */
function goBack() {
  flushProgress(false)
  if (window.history.length > 1) {
    router.back()
  } else {
    router.replace('/')
  }
}

/* ---------- 生命周期 ---------- */
function cleanupMedia() {
  if (pdfBlobUrl.value) {
    URL.revokeObjectURL(pdfBlobUrl.value)
    pdfBlobUrl.value = ''
  }
  stopReportTimer()
  const el = mediaEl()
  if (el) {
    try {
      el.pause()
    } catch {
      /* noop */
    }
  }
  isPlaying.value = false
  current.value = 0
  duration.value = 0
  zoomed.value = false
  transcoding.value = false
  transcodeProgress.value = 0
}

// 页面隐藏时上报一次（切后台、锁屏）
function onVisibilityChange() {
  if (document.visibilityState === 'hidden') {
    flushProgress(false)
  } else {
    // 回到前台，同步一次真实状态
    const el = mediaEl()
    if (el) isPlaying.value = !el.paused
  }
}

// 页面卸载（真正离开）时同步上报
function onPageHide() {
  flushProgress(false)
}

onMounted(() => {
  load()
  startReportTimer()
  document.addEventListener('visibilitychange', onVisibilityChange)
  window.addEventListener('pagehide', onPageHide)
})

onBeforeUnmount(() => {
  cleanupMedia()
  stopTranscodePolling()
  document.removeEventListener('visibilitychange', onVisibilityChange)
  window.removeEventListener('pagehide', onPageHide)
})

// 同一路由参数变化（切换资源）时重新加载
watch(resourceId, () => {
  load()
})

// 播放状态变化时同步 MediaSession playState（部分浏览器需要）
watch(isPlaying, (v) => {
  if (!('mediaSession' in navigator)) return
  try {
    navigator.mediaSession.playbackState = v ? 'playing' : 'paused'
  } catch {
    /* noop */
  }
})
</script>

<style scoped>
.play-page {
  min-height: 100vh;
  background: var(--c-bg);
  padding-bottom: calc(32px + var(--safe-bottom));
}

/* ===== 顶栏 ===== */
.topbar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: calc(10px + var(--safe-top)) 12px 10px;
  background: rgba(247, 249, 252, 0.92);
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border-bottom: 1px solid var(--c-border);
}
.back,
.more {
  flex: 0 0 auto;
  width: 38px;
  height: 38px;
  border: none;
  border-radius: 12px;
  background: #fff;
  box-shadow: var(--shadow-sm);
  font-size: 17px;
  color: var(--c-text);
  display: flex;
  align-items: center;
  justify-content: center;
  line-height: 1;
}
.more {
  font-size: 15px;
}
.head-info {
  flex: 1;
  min-width: 0;
}
.head-title {
  font-size: 16px;
  font-weight: 700;
  color: var(--c-text);
}
.head-sub {
  font-size: 12px;
  color: var(--c-text-sub);
  margin-top: 1px;
}

/* ===== 媒体区通用 ===== */
.media-area {
  padding: 22px 16px 8px;
}

/* 音频 */
.audio-area {
  display: flex;
  flex-direction: column;
  align-items: center;
}
.cover-blob {
  width: 190px;
  height: 190px;
  border-radius: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  box-shadow: var(--shadow-md);
}
.cover-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.cover-emoji {
  font-size: 78px;
  line-height: 1;
}
.audio-title {
  margin-top: 20px;
  max-width: 100%;
  font-size: 18px;
  font-weight: 700;
  color: var(--c-text);
  text-align: center;
}
.audio-sub {
  margin-top: 4px;
  max-width: 100%;
  font-size: 13px;
  color: var(--c-text-sub);
}

/* 律动条 */
.wave {
  display: flex;
  align-items: flex-end;
  gap: 5px;
  height: 26px;
  margin-top: 18px;
}
.wave span {
  width: 5px;
  height: 8px;
  border-radius: 3px;
  background: var(--c-primary);
  opacity: 0.35;
  transition: opacity 0.2s;
}
.wave.playing span {
  opacity: 0.9;
  animation: bounce 0.9s ease-in-out infinite;
}
@keyframes bounce {
  0%,
  100% {
    height: 8px;
  }
  50% {
    height: 24px;
  }
}

/* 视频 */
.video-area {
  padding: 14px 0 0;
}
.video {
  width: 100%;
  max-height: 62vh;
  background: #000;
  display: block;
  border-radius: 0;
}
.tip-lock {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 10px 16px 0;
  font-size: 12.5px;
  color: var(--c-text-sub);
  background: #eef4ff;
  padding: 9px 12px;
  border-radius: var(--r-sm);
}
.lock-emoji {
  font-size: 14px;
}
/* 转码中提示：低调但让家长知道"系统在自适应这台设备" */
.transcode-tip {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 10px 16px 0;
  font-size: 12.5px;
  color: #8a5a12;
  background: #fff6e5;
  padding: 9px 12px;
  border-radius: var(--r-sm);
}
.transcode-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: #f0a020;
  animation: transcode-pulse 1.2s ease-in-out infinite;
}
@keyframes transcode-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.35; transform: scale(0.75); }
}

/* PDF */
.pdf-area {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.pdf-frame {
  width: 100%;
  height: 68vh;
  border: none;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
}
.pdf-wait {
  height: 68vh;
  flex-direction: column;
  gap: 12px;
}
.pdf-wait-text {
  margin: 0;
  font-size: 14px;
  color: var(--c-text-sub);
}
.pdf-open {
  align-self: center;
  padding: 10px 22px;
  border-radius: 999px;
  background: var(--c-primary-light);
  color: var(--c-primary);
  font-size: 14px;
  font-weight: 600;
  text-decoration: none;
}

/* 图片 */
.image-area {
  display: flex;
  justify-content: center;
}
.image {
  max-width: 100%;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-md);
  transition: transform 0.24s ease;
}
.image.zoomed {
  transform: scale(1.9);
}

/* 外部跳转 */
.ext-area .retry {
  margin-top: 14px;
  padding: 10px 24px;
  border-radius: 999px;
  background: var(--c-primary);
  color: #fff;
  font-weight: 600;
  font-size: 14px;
  text-decoration: none;
  border: none;
}

/* ===== 控制条 ===== */
.controls {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 34px;
  padding: 22px 16px 10px;
}
.play-btn {
  width: 66px;
  height: 66px;
  border: none;
  border-radius: 50%;
  background: linear-gradient(145deg, #6d9bff, var(--c-primary));
  color: #fff;
  font-size: 25px;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 6px 20px rgba(91, 143, 249, 0.4);
}
.skip {
  border: none;
  background: #fff;
  color: var(--c-text-sub);
  font-size: 13px;
  font-weight: 600;
  width: 52px;
  height: 52px;
  border-radius: 50%;
  box-shadow: var(--shadow-sm);
}

/* ===== 进度条 ===== */
.progress-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 18px 2px;
}
.t {
  flex: 0 0 auto;
  font-size: 11.5px;
  color: var(--c-text-sub);
  font-variant-numeric: tabular-nums;
  min-width: 38px;
  text-align: center;
}
.range {
  flex: 1;
  -webkit-appearance: none;
  appearance: none;
  height: 5px;
  border-radius: 999px;
  outline: none;
  background: #dde6f0;
}
.range::-webkit-slider-thumb {
  -webkit-appearance: none;
  appearance: none;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #fff;
  border: 3px solid var(--c-primary);
  box-shadow: 0 1px 6px rgba(91, 143, 249, 0.45);
}
.range::-moz-range-thumb {
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #fff;
  border: 3px solid var(--c-primary);
}

/* ===== 倍速 / 循环 ===== */
.extras {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 16px 16px 4px;
  flex-wrap: wrap;
}
.rate-group {
  display: flex;
  gap: 8px;
}
.rate {
  border: none;
  padding: 6px 13px;
  border-radius: 999px;
  background: #fff;
  color: var(--c-text-sub);
  font-size: 13px;
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}
.rate.on {
  background: var(--c-primary);
  color: #fff;
  box-shadow: 0 3px 12px rgba(91, 143, 249, 0.35);
}
.loop {
  border: none;
  padding: 7px 14px;
  border-radius: 999px;
  background: #fff;
  color: var(--c-text-sub);
  font-size: 13px;
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}
.loop.on {
  background: var(--c-primary-light);
  color: var(--c-primary);
}

/* ===== 详情 ===== */
.detail {
  margin: 18px 16px 0;
  padding: 6px 16px;
}
.detail-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 0;
  border-bottom: 1px solid var(--c-border);
}
.detail-row:last-child {
  border-bottom: none;
}
.k {
  font-size: 14px;
  color: var(--c-text-sub);
  flex: 0 0 auto;
}
.v {
  font-size: 14px;
  font-weight: 600;
  color: var(--c-text);
  min-width: 0;
  text-align: right;
}

/* ===== 同目录其他 ===== */
.siblings {
  margin-top: 24px;
}
.sib-title {
  margin: 0 0 10px;
  padding: 0 18px;
  font-size: 16px;
  font-weight: 700;
  color: var(--c-text);
}
.sib-item {
  display: flex;
  align-items: center;
  gap: 11px;
  margin: 0 16px 8px;
  padding: 13px 15px;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
}
.sib-emoji {
  font-size: 19px;
  flex: 0 0 auto;
}
.sib-name {
  flex: 1;
  min-width: 0;
  font-size: 14.5px;
  font-weight: 500;
  color: var(--c-text);
}
.sib-go {
  flex: 0 0 auto;
  color: #c3cdd8;
  font-size: 20px;
  line-height: 1;
}

/* ===== 播放列表弹层 ===== */
.sheet-mask {
  position: fixed;
  inset: 0;
  z-index: 50;
  background: rgba(20, 30, 45, 0.4);
  display: flex;
  align-items: flex-end;
}
.sheet {
  width: 100%;
  max-height: 68vh;
  background: var(--c-card);
  border-top-left-radius: 24px;
  border-top-right-radius: 24px;
  padding: 8px 0 calc(14px + var(--safe-bottom));
  display: flex;
  flex-direction: column;
  animation: sheetUp 0.24s ease;
}
@keyframes sheetUp {
  from {
    transform: translateY(100%);
  }
  to {
    transform: translateY(0);
  }
}
.sheet-bar {
  width: 40px;
  height: 4px;
  border-radius: 999px;
  background: #dfe6ee;
  margin: 6px auto 4px;
}
.sheet-title {
  margin: 6px 0 8px;
  padding: 0 20px;
  font-size: 16px;
  font-weight: 700;
  color: var(--c-text);
}
.sheet-list {
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  padding: 0 12px;
}
.sheet-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 10px;
  border-radius: var(--r-sm);
}
.sheet-item.on {
  background: var(--c-primary-light);
}
.si-emoji {
  font-size: 16px;
  flex: 0 0 auto;
}
.si-name {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  color: var(--c-text);
}
.sheet-item.on .si-name {
  color: var(--c-primary);
  font-weight: 600;
}
.si-time {
  flex: 0 0 auto;
  font-size: 12px;
  color: var(--c-text-sub);
  font-variant-numeric: tabular-nums;
}
</style>
