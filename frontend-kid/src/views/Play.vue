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
      <div class="emoji">{{ needSmooth ? '🎬' : '😢' }}</div>
      <p>{{ error }}</p>
      <!-- 原画放不出来时唯一的正解就是切到流畅，按钮直接给出来，
           别让孩子/家长去猜"哪里能改"。 -->
      <button v-if="needSmooth" class="retry pressable" @click="setQuality('smooth')">
        用「流畅」播放
      </button>
      <button v-else class="retry pressable" @click="load()">再试一次</button>
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
          v-if="info.url"
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
        <div v-else class="video-wait">
          <div class="spinner big"></div>
          <p class="prep-title">正在把这段音频转成能播的版本…</p>
        </div>
      </section>

      <!-- 视频：原生 video，支持全屏与画中画 -->
      <section v-else-if="isVideo" class="media-area video-area">
        <!-- 转码中且容器浏览器不支持时，后端不会给预览地址（给了也是黑屏）。
             这里必须用 v-if 挡住 —— 渲染出 src="" 的 <video> 会被浏览器
             当成"加载当前页面"，反而立刻触发一次 error。 -->
        <video
          v-if="info.url"
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
        <div v-else class="video-wait">
          <div class="spinner big"></div>
          <p class="prep-title">正在把这集转成能播的版本…</p>
        </div>
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

      <!-- ===== 画质切换（原画 / 流畅） =====
           默认「原画」：直接播源文件，**不转码**，点开就有画面。
           选「流畅」才转码成 HLS，弱网更稳、更省流量，代价是首次要等一会儿。 -->
      <section v-if="(isVideo || isAudio) && !error" class="quality-bar">
        <span class="qb-label">画质</span>
        <div class="qb-group">
          <button
            class="qb pressable"
            :class="{ on: !isSmooth }"
            @click="setQuality('original')"
          >
            原画
          </button>
          <button
            class="qb pressable"
            :class="{ on: isSmooth }"
            :disabled="!transcodeAvailable"
            @click="setQuality('smooth')"
          >
            流畅
          </button>
        </div>
        <span class="qb-hint">{{ isSmooth ? '转码后更省流量' : '不转码，点开就播' }}</span>
        <p v-if="smoothUnavailable" class="qb-warn">转码服务暂时不可用，「流畅」用不了</p>
      </section>

      <!-- ===== 播放模式（顺序 / 随机 / 单曲） =====
           列表已按标题升序，"顺序"就是列表里从上往下的那个顺序，
           播完最后一集停下 —— 不回头循环，睡前不会一直播下去。
           样式复用画质那套分段控件（.qb-*），两处手感保持一致。 -->
      <section v-if="(isVideo || isAudio) && !error" class="quality-bar play-mode-bar">
        <span class="qb-label">播放</span>
        <div class="qb-group">
          <button
            v-for="m in PLAY_MODES"
            :key="m.key"
            class="qb pressable"
            :class="{ on: playMode === m.key }"
            @click="setPlayMode(m.key)"
          >
            {{ m.label }}
          </button>
        </div>
        <span class="qb-hint">{{ modeHint }}</span>
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

      <!-- ===== 倍速（音频） =====
           单曲循环原来也在这里，现在归到上面的「播放模式」——
           视频也需要单曲循环，放在只对音频可见的地方是摆错了位置。 -->
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
          <h3 class="sheet-title">
            播放列表<span v-if="playlist.length" class="sheet-count">
              共 {{ playlist.length }} 集 · {{ modeLabel }}</span
            >
          </h3>
          <div class="sheet-list">
            <div
              class="sheet-item pressable"
              :class="{ on: it.id === resourceId }"
              v-for="(it, i) in playlist"
              :key="it.id"
              @click="switchTo(it.id)"
            >
              <span class="si-idx">{{ i + 1 }}</span>
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
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
// errorRetried 防止"媒体报错 → 重取直链 → 又报错"打成死循环。
// 一次播放生命周期内只自动重取一次，之后交给用户手动重试。
let errorRetried = false

const audioEl = ref(null)
const videoEl = ref(null)

const isPlaying = ref(false)
const current = ref(0)
const duration = ref(0)
const rate = ref(1)
const zoomed = ref(false)

const siblings = ref([])
const playlist = ref([])
const showPlaylist = ref(false)
const pdfBlobUrl = ref('')

// 转码状态：后台在为首播转码时置位，用于给家长/孩子一个"正在优化播放"的提示
const transcoding = ref(false)
const transcodeProgress = ref(0)

/* ---------- 画质（原画 / 流畅） ---------- */
//
// 为什么要把这个选择权交给用户：
//   - 原画：直接把源文件播出去，**不转码**，点开就有画面；
//   - 流畅：转成 HLS，等一会儿，但码率更低、弱网更稳、能扛 RMVB 这类
//     浏览器根本放不出来的容器。
//
// 默认必须是原画。以前是后端按容器自动决定，用户没有任何选择权，
// 结果一个 RMVB 目录每次点开都是"正在转码"，等十几分钟是常态。
const QUALITY_KEY = 'kid_play_quality'
const QUALITY_ORIGINAL = 'original'
const QUALITY_SMOOTH = 'smooth'

function readQuality() {
  try {
    return localStorage.getItem(QUALITY_KEY) === QUALITY_SMOOTH ? QUALITY_SMOOTH : QUALITY_ORIGINAL
  } catch {
    // 隐私模式 / 禁用存储：读不到就用默认值，不影响播放
    return QUALITY_ORIGINAL
  }
}

const quality = ref(readQuality())
// 转码引擎不可用（未装 ffmpeg / 后台关掉了转码）时，前端把「流畅」置灰，
// 而不是让孩子点了以后干等一个永远不会来的产物。
const transcodeAvailable = ref(true)
const smoothUnavailable = ref(false)

const isSmooth = computed(() => quality.value === QUALITY_SMOOTH)

// containerUnsupported 与"还没选流畅"同时成立时，界面上要给一条明确的出路：
// 一个「用流畅播放」的按钮。否则孩子看到的就只是一句"放不出来"。
const needSmooth = computed(
  () => !!info.value?.containerUnsupported && !isSmooth.value
)

// containerHint 容器浏览器放不出来时的引导文案。
//
// 关键区别在于"有没有出路"：
//   - 原画模式下要给出「切到流畅」这条明路（用户自己就能解决）；
//   - 已经是流畅了还放不出来，那就是转码真的没成功，只能请家长处理。
function containerHint(ext) {
  const label = ext ? `「${String(ext).toUpperCase()}」格式` : '这个格式'
  if (isSmooth.value) {
    return `${label}需要转码才能播，但转码没有成功。请让家长检查后台的转码设置`
  }
  return `${label}手机浏览器直接放不出来，点下面用「流畅」播放（首次要转一会儿）`
}

// setQuality 切换画质并重新拉取播放信息。
//
// 切换等于换源重放，所以先把当前进度落库/带过去，别让孩子每次换画质都从头看。
async function setQuality(q) {
  const pos = currentTime()
  flushProgress(false)
  // 先落存储再判断"是否变化"：后端可能刚把画质纠正过（比如请求了流畅但
  // 转码不可用），此时 quality 已经是 q，但存储里还是旧值 —— 直接 return
  // 会让下次进来又按旧值请求一遍。
  try {
    localStorage.setItem(QUALITY_KEY, q)
  } catch {
    /* 存不下不影响本次切换 */
  }
  if (q === quality.value) return
  quality.value = q
  await load(pos)
}

/* ---------- 播放模式（顺序 / 随机 / 单曲） ---------- */
//
// 播放列表由后端按标题升序给出，所以三种模式就是：
//   顺序——顺着列表从上往下，播完最后一集**停下**（不回头循环，
//        睡前不会一直播下去）；
//   随机——每集放完随机挑一集，尽量不连着放同一集；
//   单曲——同一集反复放，适合精听跟读。
//
// 一个有意的取舍：单曲循环**不用** <audio loop> 原生属性实现。
// 原生 loop 会让 ended 事件永远不触发，三种模式就得走两条路；
// 统一在 onEnded 里处理，换源、切模式都不会因为媒体元素被重建而丢状态。
const MODE_KEY = 'kid_play_mode'
const MODE_ORDER = 'order'
const MODE_SHUFFLE = 'shuffle'
const MODE_SINGLE = 'single'
const PLAY_MODES = [
  { key: MODE_ORDER, label: '顺序' },
  { key: MODE_SHUFFLE, label: '随机' },
  { key: MODE_SINGLE, label: '单曲' },
]

function readPlayMode() {
  try {
    const v = localStorage.getItem(MODE_KEY)
    if (v === MODE_SHUFFLE || v === MODE_SINGLE) return v
  } catch {
    // 隐私模式 / 禁用存储：读不到就用默认，不影响播放
  }
  return MODE_ORDER
}

const playMode = ref(readPlayMode())

const modeLabel = computed(
  () => PLAY_MODES.find((m) => m.key === playMode.value)?.label || '顺序'
)

const modeHint = computed(() => {
  if (playMode.value === MODE_SHUFFLE) return '随机挑着播'
  if (playMode.value === MODE_SINGLE) return '这一集反复播'
  return '按列表顺序播'
})

function setPlayMode(m) {
  if (!PLAY_MODES.some((it) => it.key === m)) return
  playMode.value = m
  try {
    localStorage.setItem(MODE_KEY, m)
  } catch {
    /* 存不下不影响本次切换 */
  }
}

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
// resumeAt > 0 时用它作为续播点（切换画质时用得上：那会儿还没上报，
// 库里的 lastPosition 可能是十几秒前的旧值）。
async function load(resumeAt = 0) {
  loading.value = true
  error.value = ''
  errorRetried = false
  cleanupMedia()
  stopTranscodePolling()

  try {
    const data = await playApi.info(resourceId.value, quality.value)
    info.value = data || {}
    duration.value = Number(data?.duration) || 0
    // 后端回传本次实际生效的画质：以它为准，避免本地状态和真实播放不一致
    if (data?.quality) quality.value = data.quality
    transcodeAvailable.value = data?.transcodeAvailable !== false
    smoothUnavailable.value = !!data?.smoothUnavailable

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
    pendingSeek = resumeAt > 0 ? resumeAt : Number(data?.lastPosition) || 0

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
      const st = await playApi.info(resourceId.value, quality.value)
      const mode = st?.playMode
      if (mode === 'hls' && st?.url) {
        // 转好了：换源，保留当前播放位置
        const pos = currentTime()
        stopTranscodePolling()
        transcoding.value = false
        info.value = { ...info.value, ...st }
        // 等 <video> 渲染出来再换源：容器不支持格式时元素本来不存在
        // （被 v-if 挡住了），这一步不做的话 mediaEl() 会是 null。
        await nextTick()
        await switchToHls(st.url, pos)
      } else if (mode === 'transcoding') {
        transcodeProgress.value = Number(st?.transcodeProgress) || 0
      } else {
        // 转码没能继续（失败/被关）：合并最新状态再决定怎么收场。
        // 若依旧没有可播地址，说明格式浏览器放不出来且转码没成功，
        // 这时候必须明确告诉用户，而不是留一个转圈的空页面。
        stopTranscodePolling()
        transcoding.value = false
        info.value = { ...info.value, ...st }
        if (!st?.url) {
          error.value = containerHint(st?.ext || info.value.ext)
        }
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
  flushProgress(true)

  // 单曲循环：原地重播。不用 <audio loop>（见上面播放模式那段注释）
  if (playMode.value === MODE_SINGLE) {
    const el = mediaEl()
    if (el) {
      try {
        el.currentTime = 0
      } catch {
        /* 某些格式 seek 会抛错，接着 play 依然会从头开始 */
      }
      el.play().catch(() => {})
    }
    return
  }

  const next = nextTrackId()
  // nextTrackId 返回 0 表示没有下一集（顺序播放的最后一集）→ 停在这里
  if (next) goTo(next)
}

function onMediaError() {
  const el = mediaEl()
  const code = el && el.error ? el.error.code : 0

  // —— 情况一：容器格式浏览器根本不支持（rmvb / avi / mkv …）——
  //
  // 原画模式下这是**预期**结果：我们明知它浏览器放不出来，还是按用户
  // 选的"不转码"给了直链。所以这里不能重试（重试一百次也还是同一个容器），
  // 而要给出唯一的正解：切到「流畅」。已经是流畅了那就是转码真失败了。
  if (info.value?.containerUnsupported) {
    error.value = containerHint(info.value.ext)
    return
  }

  // —— 情况二：源不支持（网盘直链过期是真实且常见的情况）——
  //
  // 直链有效期通常只有几十分钟，孩子把页面挂在那儿过一会儿再点播放，
  // 拿到的大概率已经是个死链。这种错误重取一次直链就能自愈，
  // 所以先静默重试一次，别一上来就让用户去手动点。
  if (code === 4 /* MEDIA_ERR_SRC_NOT_SUPPORTED */ && !errorRetried) {
    errorRetried = true
    // 稍微延迟一下：立刻重入会和此刻正在进行的错误处理抢媒体元素
    setTimeout(() => reloadInfo(), 600)
    return
  }

  error.value = code === 4
    ? '这个视频暂时播不了，可能是地址过期或格式不支持，点下面重试一下'
    : '播放地址可能已过期，请返回重新进入'
}

// reloadInfo 只重取播放信息并换源，不重置断点/播放列表。
//
// 和 load() 的区别：load() 会把整页打回 loading 态、清掉续播位置，
// 对"直链过期"这种小故障来说太重了。
//
// ⚠️ 这里最重要的一条：**"后端说要转码" 不等于 "出错"**。
//
// 存量网盘资源入库时格式未知（Ext 为空），只有真正取一次流、让后端从
// 上游 Content-Type 认出是 rmvb/avi 之后，下一次 play/:id 才会返回
// playMode=transcoding 且 url 为空。
// 而这个"下一次"恰好就是本次重取 —— 所以 url 为空是**预期结果**，
// 必须切到"转码中"的等待界面继续轮询，而不是报错。
// 以前这里写的是 `if (!data?.url) throw`，直接把这种情况打成了
// "播放地址可能已过期"，误导用户反复刷新（问题根本不在地址上）。
async function reloadInfo() {
  let data
  try {
    data = await playApi.info(resourceId.value, quality.value)
  } catch {
    error.value = '网络好像不太稳定，点下面再试一次'
    return
  }

  if (data?.quality) quality.value = data.quality
  transcodeAvailable.value = data?.transcodeAvailable !== false
  smoothUnavailable.value = !!data?.smoothUnavailable

  // 只要是转码中，等待提示就该立起来 —— 但**别急着丢掉地址**：
  // 后端对"容器本身能播"的资源会在转码期间照给一条直链先播着
  // （见 PlayHandler.Info 里 playURL 那段），一看到 transcoding
  // 就去显示空等待界面反而是倒退。
  if (data?.playMode === 'transcoding') {
    transcoding.value = true
    transcodeProgress.value = Number(data?.transcodeProgress) || 0
    startTranscodePolling(data?.transcodeTaskId)
  }

  // —— 拿到了可播地址：换源重播 ——
  if (data?.url) {
    // 地址没变说明重取也没用，直接给提示，别让孩子对着黑屏等
    if (data.url === info.value.url) {
      error.value = '这个视频暂时播不了，可能是地址过期或格式不支持，点下面重试一下'
      return
    }
    info.value = { ...info.value, ...data }
    error.value = ''
    const pos = Number(data.lastPosition) || 0
    await nextTick()
    const el = mediaEl()
    if (el && pos > 0) {
      pendingSeek = pos
    }
    el?.load()
    return
  }

  // —— 没有地址且正在转码：这是**预期**结果，不是错误 ——
  if (data?.playMode === 'transcoding') {
    info.value = { ...info.value, ...data }
    error.value = ''
    return
  }

  // —— 既没地址也没排上转码：说清楚到底是什么问题 ——
  // containerUnsupported 语义就是"这个容器浏览器放不出来，且当前没有转码任务"。
  // 原画模式下这正是预期外的失败（用户在等他点「流畅」），
  // 已经是流畅了才说明转码真没成功。
  info.value = { ...info.value, ...data }
  error.value = containerHint(data?.ext || info.value.ext)
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
      const id = prevTrackId()
      if (id) goTo(id)
    })
    navigator.mediaSession.setActionHandler('nexttrack', () => {
      const id = nextTrackId()
      if (id) goTo(id)
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

/* ---------- 列表内的前后导航 ---------- */
// 列表按标题升序，索引往后走就是"下一集"。
function currentIndex() {
  return playlist.value.findIndex((p) => p.id === resourceId.value)
}

// randomOtherId 随机挑一集，尽量避开当前这集 ——
// 否则"随机播放"会时不时原地重播，看起来像卡住了。
function randomOtherId() {
  const list = playlist.value
  if (!list.length) return 0
  if (list.length === 1) return list[0].id
  for (let i = 0; i < 30; i++) {
    const pick = list[Math.floor(Math.random() * list.length)]
    if (pick.id !== resourceId.value) return pick.id
  }
  const fallback = list.find((p) => p.id !== resourceId.value)
  return fallback ? fallback.id : 0
}

// nextTrackId 自动续播和"下一集"按钮共用。返回 0 表示没有下一集。
function nextTrackId() {
  if (!playlist.value.length) return 0
  if (playMode.value === MODE_SHUFFLE) return randomOtherId()
  const idx = currentIndex()
  if (idx < 0) return 0
  return idx < playlist.value.length - 1 ? playlist.value[idx + 1].id : 0
}

// prevTrackId 供锁屏 / 耳机上的"上一集"。随机模式下退化为再随机一首。
function prevTrackId() {
  if (!playlist.value.length) return 0
  if (playMode.value === MODE_SHUFFLE) return randomOtherId()
  const idx = currentIndex()
  return idx > 0 ? playlist.value[idx - 1].id : 0
}

/* ---------- 切换资源 ---------- */
// switchTo 点击列表项：已经在播的那集只收弹层，不重新加载
function switchTo(id) {
  if (id === resourceId.value) {
    showPlaylist.value = false
    return
  }
  goTo(id)
}

// goTo 是通用实现 —— 自动续播、锁屏"下一集"都走它，
// 保证"离开前先落一次进度"这条规则不会被漏掉。
function goTo(id) {
  if (!id || id === resourceId.value) return
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

// 打开播放列表时把"正在播的这集"滚进视野。
// 一个目录几十集，不滚的话每次都要自己翻半天。
watch(showPlaylist, async (v) => {
  if (!v) return
  await nextTick()
  const on = document.querySelector('.sheet-item.on')
  if (on && typeof on.scrollIntoView === 'function') {
    on.scrollIntoView({ block: 'center' })
  }
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
/* 转码等待区：此时还没有可播地址，用一个安静的黑底占位替代 <video>，
   避免出现"黑屏 + 报错"这种既没信息也没进展的中间态。 */
.video-wait {
  width: 100%;
  min-height: 190px;
  max-height: 62vh;
  background: #000;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
}
.video-wait .prep-title {
  color: #fff;
  font-size: 14px;
  margin: 0;
}
/* 黑底上要把转圈的环改成亮色，否则默认的浅灰环几乎看不见 */
.video-wait .spinner {
  width: 40px;
  height: 40px;
  border-color: rgba(255, 255, 255, 0.22);
  border-top-color: #fff;
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

/* 画质切换：原画 / 流畅
   放在播放区正下方而不是塞进「设置」里 —— 放不出来的时候，
   孩子/家长一眼就要能看到这个开关，藏起来等于没有。 */
.quality-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin: 12px 16px 0;
  padding: 10px 12px;
  background: #f4f7fc;
  border-radius: var(--r-sm);
}
.qb-label {
  flex: 0 0 auto;
  font-size: 12.5px;
  color: var(--c-text-sub);
}
.qb-group {
  display: flex;
  flex: 0 0 auto;
  padding: 3px;
  background: #e3eaf4;
  border-radius: 999px;
}
.qb {
  border: none;
  background: transparent;
  color: var(--c-text-sub);
  font-size: 13px;
  font-weight: 600;
  padding: 6px 16px;
  border-radius: 999px;
  transition: background 0.18s, color 0.18s;
}
.qb.on {
  background: #fff;
  color: var(--c-primary);
  box-shadow: 0 1px 4px rgba(38, 154, 153, 0.18);
}
.qb:disabled {
  opacity: 0.42;
}
.qb-hint {
  flex: 1 1 auto;
  text-align: right;
  font-size: 12px;
  color: var(--c-text-sub);
}
.qb-warn {
  flex: 1 1 100%;
  margin: 2px 0 0;
  font-size: 12px;
  color: #c0392b;
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

/* ===== 倍速 =====
   单曲循环已归到「播放模式」那一条（视频也要用），这里只剩倍速，
   所以改成左对齐而不是两端撑开。 */
.extras {
  display: flex;
  align-items: center;
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
/* 标题右边挂一行小字：一共几集 + 当前播放模式。
   模式放在这里，是因为"列表怎么往下播"正是在看列表时要想的事。 */
.sheet-count {
  margin-left: 8px;
  font-size: 12px;
  font-weight: 500;
  color: var(--c-text-sub);
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
.si-idx {
  flex: 0 0 auto;
  min-width: 16px;
  font-size: 11.5px;
  color: #b6c2cf;
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.sheet-item.on .si-idx {
  color: var(--c-primary);
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
