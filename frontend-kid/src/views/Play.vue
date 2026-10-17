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
    <!-- 说明文案不是装饰：PRO 端第一次播放要先把这个文件转存进自己的网盘，
         网盘那边 3~30 秒都正常。只给一个转圈，用户会以为卡死了。 -->
    <div v-if="loading" class="loading">
      <div class="spinner"></div>
      <p class="loading-text">正在打开…</p>
      <p class="loading-sub">第一次播放要先存到你自己的网盘，可能要十几秒</p>
    </div>

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
          preload="auto"
          autoplay
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

      <!-- PDF：用 pdf.js 在页面内渲染。
           以前是 <iframe src="blob:...pdf"> —— 桌面 Chrome 自带 PDF 阅读器，
           所以看着没问题；但**安卓 WebView 没有 PDF 插件**，装成 App 之后
           点开绘本就是一片空白。这正是"孩子端要支持 PDF 查看"的由来。 -->
      <section v-else-if="isPDF" class="media-area pdf-area">
        <div class="pdf-viewer">
          <canvas
            ref="pdfCanvas"
            class="pdf-canvas"
            :class="{ 'is-loading': pdfLoading }"
            @touchstart.passive="onPdfTouchStart"
            @touchend.passive="onPdfTouchEnd"
          ></canvas>
          <div v-if="pdfLoading" class="loading pdf-wait">
            <div class="spinner"></div>
            <p class="pdf-wait-text">{{ pdfLoadingText }}</p>
          </div>
          <div v-if="pdfError" class="pdf-error">
            <div class="emoji">📖</div>
            <p>{{ pdfError }}</p>
            <p v-if="pdfErrorDetail" class="pdf-error-detail">{{ pdfErrorDetail }}</p>
            <button class="pdf-reload pressable" @click="hardReload">重新加载</button>
          </div>
        </div>

        <!-- 页内翻页。和外面的「上一首 / 下一首」不是一回事：
             那两个换的是"资源"，这里翻的是"同一本绘本的页"。 -->
        <div v-if="pdfPages > 1 && !pdfError" class="pdf-pager">
          <button class="pdf-nav pressable" :disabled="pdfPage <= 1" @click="pdfGo(-1)">
            ◀ 上一页
          </button>
          <span class="pdf-pageinfo">{{ pdfPage }} / {{ pdfPages }}</span>
          <button class="pdf-nav pressable" :disabled="pdfPage >= pdfPages" @click="pdfGo(1)">
            下一页 ▶
          </button>
        </div>

        <!-- 只有真的打不开时才给这个兜底，正常情况不占地方 -->
        <a
          v-if="pdfError"
          class="pdf-open pressable"
          :href="info.url"
          target="_blank"
          rel="noopener"
        >
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

      <!-- ===== 缓存（下载到本机） =====
           标准版与 PRO 用的是同一份孩子端前端，所以这一块两边都有；
           下载的是本站的代理地址（同源），不是网盘直链 —— 见 utils/offline.js。 -->
      <section v-if="canCache && !error" class="quality-bar dl-bar">
        <span class="qb-label">缓存</span>
        <button
          class="qb pressable"
          :class="{ on: offlineState === 'cached', busy: offlineState === 'downloading' }"
          @click="toggleOffline"
        >
          {{ offlineLabel }}
        </button>
        <span class="qb-hint">{{ offlineHintText }}</span>
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

      <!-- ===== 上一首 / 下一首 =====
           以前只能在锁屏 / 耳机上用系统媒体控件换集，页面上没有入口 ——
           手机上想换一首还得去翻播放列表。
           这两个按钮与锁屏、通知栏走的是同一套 prevTrackId/nextTrackId，
           顺序 / 随机 / 单曲三种模式下的行为完全一致，不会各走各的。 -->
      <section v-if="canSwitch" class="track-nav">
        <button
          class="track-btn pressable"
          :disabled="!hasPrev"
          aria-label="上一首"
          @click="goPrevTrack"
        >
          ⏮ 上一首
        </button>
        <button
          class="track-btn pressable"
          :disabled="!hasNext"
          aria-label="下一首"
          @click="goNextTrack"
        >
          下一首 ⏭
        </button>
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
import { showToast } from 'vant'
import { playApi } from '@/api'
import { tint } from '@/assets/icons'
import {
  isCached as offlineIsCached,
  getMeta as offlineGetMeta,
  cachedURL as offlineCachedURL,
  download as offlineDownload,
  removeCache as offlineRemove,
  releaseUrls as offlineReleaseUrls,
  fmtSize as offlineFmtSize,
  touchCache as offlineTouch,
} from '@/utils/offline'

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

// PDF 阅读器状态（实现见下方「PDF：用 pdf.js 画到 canvas」）
const pdfCanvas = ref(null)
const pdfPage = ref(0)
const pdfPages = ref(0)
const pdfLoading = ref(false)
const pdfLoadingText = ref('正在打开绘本…')
const pdfError = ref('')
// 原始报错原文。对孩子不展示（看不懂），但家长截图反馈时它才是关键信息。
const pdfErrorDetail = ref('')

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

/* ---------- 下载 / 离线缓存 ---------- */
//
// 为什么下载的是"播放地址"而不是网盘直链：直链要带 Cookie/Referer，
// 浏览器 fetch 带不上，而且还有跨域；播放地址是我们**自己的代理**
// （/api/stream/:id 或 PRO 的 /edge/stream/:ticket），同源、无需额外头，
// 顺带还能吃到服务端的多连接加速。详见 utils/offline.js 的注释。
const offlineState = ref('none') // none | downloading | cached
const offlinePercent = ref(0)
const offlinePlay = ref(false) // 本次播放用的是本地文件
let offlineAbort = null
// 下载中要显示的"多少 MB / 多少 MB"，光有百分比看不出还有多久。
const offlineLoaded = ref(0)
const offlineTotal = ref(0)

const canCache = computed(() => {
  if (!info.value.url) return false
  // 流畅模式是 HLS（一堆切片 + m3u8），整包缓存要先合并，先不做。
  if (isSmooth.value && !offlinePlay.value) return false
  return ['video', 'audio', 'pdf', 'image'].includes(info.value.mediaType)
})

const offlineLabel = computed(() => {
  switch (offlineState.value) {
    case 'cached':
      return '已缓存'
    case 'downloading':
      return offlinePercent.value > 0 ? `${offlinePercent.value}%` : '准备中'
    default:
      return '下载'
  }
})

const offlineHintText = computed(() => {
  switch (offlineState.value) {
    case 'cached':
      return '没有网也能看，点一下可以删掉'
    case 'downloading':
      const total = offlineTotal.value ? ` / ${offlineFmtSize(offlineTotal.value)}` : ''
      return `正在缓存 ${offlineFmtSize(offlineLoaded.value)}${total}`
    default:
      return offlinePlay.value ? '正在用本地文件播放' : '存到本机，没有网也能看'
  }
})

// localURLFor 取本地缓存的播放地址；没有就返回空串（不抛错）。
async function localURLFor(id) {
  try {
    if (!(await offlineIsCached(id))) return ''
    // 顺手记一笔"刚看过"：自动清理按这个时间淘汰最久没看的，
    // 不刷新的话，一部片子反复看也会因为"下载时间久"被删掉。
    offlineTouch(id).catch(() => {})
    return await offlineCachedURL(id)
  } catch (e) {
    return ''
  }
}

async function refreshOfflineState() {
  try {
    offlineState.value = (await offlineIsCached(resourceId.value)) ? 'cached' : 'none'
  } catch (e) {
    offlineState.value = 'none'
  }
}

async function toggleOffline() {
  if (offlineState.value === 'downloading') {
    // 再点一次 = 取消下载
    if (offlineAbort) offlineAbort.abort()
    return
  }
  if (offlineState.value === 'cached') {
    try {
      await offlineRemove(resourceId.value)
      offlineState.value = 'none'
      offlinePlay.value = false
      offlinePercent.value = 0
      showToast('已删除缓存')
    } catch (e) {
      showToast(e?.message || '删除失败')
    }
    return
  }

  if (!info.value.url) {
    showToast('还没拿到可下载的地址')
    return
  }
  // 下载的是**原始地址**：如果当前正在用本地文件播，
  // info.url 已经是 blob:，再下它没有意义（也已经下载过了）。
  if (offlinePlay.value) return

  offlineAbort = new AbortController()
  offlineState.value = 'downloading'
  offlinePercent.value = 0
  offlineLoaded.value = 0
  offlineTotal.value = 0
  try {
    await offlineDownload(
      resourceId.value,
      info.value.url,
      {
        title: info.value.title,
        mediaType: info.value.mediaType,
        cover: info.value.poster,
        categoryName: info.value.categoryName,
        duration: duration.value,
      },
      (p) => {
        offlinePercent.value = p.percent
        offlineLoaded.value = p.loaded
        offlineTotal.value = p.total || offlineTotal.value
      },
      offlineAbort.signal,
    )
    offlineState.value = 'cached'
    offlinePercent.value = 100
    showToast('已存到本机')
  } catch (e) {
    if (e && (e.name === 'AbortError' || e.code === 20)) {
      showToast('已取消下载')
    } else {
      showToast(e?.message || '下载失败')
    }
    offlineState.value = 'none'
    offlinePercent.value = 0
  } finally {
    offlineAbort = null
  }
}

/* ---------- 加载播放信息 ---------- */
// resumeAt > 0 时用它作为续播点（切换画质时用得上：那会儿还没上报，
// 库里的 lastPosition 可能是十几秒前的旧值）。
async function load(resumeAt = 0) {
  // 记住 PDF 地址，等 loading 关掉再渲染（原因见下面的注释）
  let pdfURL = ''
  loading.value = true
  error.value = ''
  errorRetried = false
  cleanupMedia()
  stopTranscodePolling()

  try {
    let data = await playApi.info(resourceId.value, quality.value)

    // 本地已有这份文件 → 直接播本地。既省流量，**没有网也能看**。
    //
    // 放在 external / transcoding 判断之前：缓存里是完整原片，
    // 既不需要跳网盘，也不需要等转码 —— 那两条路都是"只能在线"的方案。
    const localURL = await localURLFor(resourceId.value)
    if (localURL) {
      offlinePlay.value = true
      data = {
        ...(data || {}),
        url: localURL,
        playMode: 'direct',
        transcodeTaskId: 0,
        transcodeProgress: 0,
        quality: QUALITY_ORIGINAL,
      }
    } else {
      offlinePlay.value = false
    }

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
      // 这里**只记地址**，真正渲染放到 finally 之后。
      //
      // 原因：canvas 在 v-else-if="isPDF" 分支里，而它上面还有一个
      // v-if="loading" 挡着。此刻 loading 仍是 true，整个媒体区都没挂载，
      // 去取 pdfCanvas 只会拿到 null —— renderPdfPage 随即静默返回，
      // 既不报错也不进错误态，表现就是"页码出来了、画布一片空白"。
      // （这个 bug 只有真去读 canvas 像素才抓得到：接口、DOM 都存在。）
      pdfURL = data.url
    }
    if (isImage.value) {
      // 图片无需额外处理
    }
    setupMediaSession()

    loadSiblings()
    loadPlaylist()
  } catch (e) {
    // 网络不通 / 接口挂了，但本地有缓存 → 照常看。这正是"下载"的意义：
    // 以前离线时播放页只能给一句"资源加载失败"，孩子什么都干不了。
    const local = await localURLFor(resourceId.value)
    if (local) {
      const meta = (await offlineGetMeta(resourceId.value)) || {}
      offlinePlay.value = true
      info.value = {
        url: local,
        title: meta.title || '',
        mediaType: meta.mediaType || '',
        poster: meta.cover || '',
        categoryName: meta.categoryName || '',
        playMode: 'direct',
        quality: QUALITY_ORIGINAL,
        duration: Number(meta.duration) || 0,
      }
      duration.value = Number(meta.duration) || 0
      error.value = ''
      pendingSeek = resumeAt > 0 ? resumeAt : 0
      if (meta.mediaType === 'pdf') pdfURL = local
      setupMediaSession()
    } else {
      error.value = e?.message || '资源加载失败'
    }
  } finally {
    loading.value = false
  }
  // 下载按钮的状态要按"这条资源有没有缓存"刷新
  refreshOfflineState()
  // loading 关掉之后媒体区才挂载，PDF 的 canvas 这时候才存在
  if (pdfURL) await openPDF(pdfURL)
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

/* ---------- PDF：用 pdf.js 画到 canvas ---------- */
//
// 为什么不用 <iframe src="blob:...pdf"> 或 <embed>：
// 那条路依赖"宿主环境自带 PDF 阅读器"。桌面 Chrome 有，所以以前看着是好的；
// 而**安卓 WebView 没有 PDF 插件** —— App 里点开绘本就是一片空白。
// pdf.js 是纯 JS 渲染，不依赖插件，任何 WebView 都能画出来。
//
// 顺带解决另一件事：不必再把整个 PDF 拉成 blob。
// 直接指向后端的 /api/stream/:id（同源、支持 Range），
// pdf.js 按需取页 —— 一本几十 MB 的绘本不用先全量下载。

let pdfDoc = null
let pdfRenderTask = null
let pdfjsMod = null
let pdfOpenSeq = 0

async function loadPdfJs() {
  if (pdfjsMod) return pdfjsMod
  // 动态 import：只有真的打开绘本才加载这一坨（gzip 后约 400KB），
  // 不影响首页和其它页面。用 legacy 构建是为了兼容较老的 WebView
  // （部分学习平板的 WebView 相当旧，新构建会直接语法报错）。
  const [lib, worker] = await Promise.all([
    import('pdfjs-dist/legacy/build/pdf'),
    import('pdfjs-dist/legacy/build/pdf.worker.min.js?url'),
  ])
  lib.GlobalWorkerOptions.workerSrc = worker.default
  pdfjsMod = lib
  return lib
}

/** 停掉在途的打开/渲染，清空状态。切资源、离开页面时都走它。 */
function resetPdf() {
  pdfOpenSeq++ // 让在途结果失效，避免"慢的那次"覆盖"新的一次"
  if (pdfRenderTask) {
    try {
      pdfRenderTask.cancel()
    } catch (e) {
      /* 忽略 */
    }
    pdfRenderTask = null
  }
  if (pdfDoc) {
    try {
      pdfDoc.destroy()
    } catch (e) {
      /* 忽略 */
    }
    pdfDoc = null
  }
  pdfPages.value = 0
  pdfPage.value = 0
  pdfLoading.value = false
  pdfError.value = ''
}

async function openPDF(url) {
  resetPdf()
  const seq = pdfOpenSeq
  if (!url) {
    pdfError.value = '这个绘本没有可用的地址'
    return
  }
  pdfLoading.value = true
  pdfLoadingText.value = '正在打开绘本…'
  try {
    const lib = await loadPdfJs()
    if (seq !== pdfOpenSeq) return
    const task = lib.getDocument({
      url,
      // ⚠️ CMap 与标准字体是**中日韩字体必需**的，别省。
      // 不配的话 pdf.js 会刷 "The CMap baseUrl parameter must be specified"，
      // 表现是**中文绘本"图在、字没了"** —— 中文用 CID 编码，
      // 少了 .bcmap 就映射不出字形。纯英文绘本看不出来，所以很容易漏。
      // 这两个目录由 vite.config.js 在构建时从 pdfjs-dist 拷进 public/。
      cMapUrl: `${import.meta.env.BASE_URL}cmaps/`,
      cMapPacked: true,
      standardFontDataUrl: `${import.meta.env.BASE_URL}standard_fonts/`,
      // 关掉 pdf.js 的 eval 优化：收益只是渲染快一点，
      // 代价是"PDF 内容能动用代码执行"。给孩子的设备上不划算。
      isEvalSupported: false,
    })
    task.onProgress = ({ loaded, total }) => {
      if (seq !== pdfOpenSeq || !total) return
      pdfLoadingText.value = `正在打开绘本… ${Math.round((loaded / total) * 100)}%`
    }
    const doc = await task.promise
    if (seq !== pdfOpenSeq) {
      try {
        doc.destroy()
      } catch (e) {
        /* 忽略 */
      }
      return
    }
    pdfDoc = doc
    pdfPages.value = doc.numPages
    pdfPage.value = 1
    pdfLoading.value = false
    await renderPdfPage(1)
  } catch (e) {
    if (seq !== pdfOpenSeq) return
    pdfLoading.value = false
    // 原文留给家长反馈用（截图里带上它，我一眼能定位）
    pdfErrorDetail.value = String((e && e.message) || e || '').slice(0, 180)
    pdfError.value = pdfErrorText(e)
  }
}

async function renderPdfPage(n) {
  // canvas 可能还没挂载（媒体区受 v-if="loading" 控制，时序上也许差一帧）。
  // 等一小会儿再取，取不到就当这页没画 —— 上面的调用方已经保证过时机，
  // 这里是兜底，避免以后有人在别处调用时又踩同一个坑。
  let canvas = pdfCanvas.value
  for (let i = 0; i < 20 && !canvas; i++) {
    await new Promise((r) => setTimeout(r, 50))
    canvas = pdfCanvas.value
  }
  if (!pdfDoc || !canvas) return
  // 同一张 canvas 上并发 render 会直接抛错，上一个任务必须先取消
  if (pdfRenderTask) {
    try {
      pdfRenderTask.cancel()
    } catch (e) {
      /* 忽略 */
    }
    pdfRenderTask = null
  }
  try {
    const page = await pdfDoc.getPage(n)
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    const base = page.getViewport({ scale: 1 })
    // 按容器宽度铺满：绘本在竖屏手机上也能看清。
    // 用父元素宽度而不是 canvas 自身 —— canvas 此刻还是上一页的尺寸。
    const box = canvas.parentElement
    const avail = Math.max((box ? box.clientWidth : window.innerWidth) - 4, 120)
    const scale = (avail / base.width) * dpr
    const vp = page.getViewport({ scale })
    const ctx = canvas.getContext('2d', { alpha: false })
    canvas.width = Math.floor(vp.width)
    canvas.height = Math.floor(vp.height)
    // CSS 尺寸按逻辑像素写，canvas 像素按 dpr 放大 —— 高分屏才不糊
    canvas.style.width = Math.floor(vp.width / dpr) + 'px'
    canvas.style.height = Math.floor(vp.height / dpr) + 'px'
    pdfRenderTask = page.render({ canvasContext: ctx, viewport: vp })
    await pdfRenderTask.promise
  } catch (e) {
    // "取消"是快速翻页的正常流程，不是错误
    if (e && e.name === 'RenderingCancelledException') return
    pdfError.value = '这一页没能画出来'
  } finally {
    pdfRenderTask = null
  }
}

function pdfGo(delta) {
  const n = pdfPage.value + delta
  if (n < 1 || n > pdfPages.value) return
  pdfPage.value = n
  renderPdfPage(n)
}

// 左右滑动翻页（触屏）
let pdfTouchX = 0
let pdfTouchY = 0

function onPdfTouchStart(e) {
  const t = e.changedTouches && e.changedTouches[0]
  if (!t) return
  pdfTouchX = t.clientX
  pdfTouchY = t.clientY
}

function onPdfTouchEnd(e) {
  const t = e.changedTouches && e.changedTouches[0]
  if (!t) return
  const dx = t.clientX - pdfTouchX
  const dy = t.clientY - pdfTouchY
  // 只在"明显是横向滑动"时才翻页：纵向滑动是页面滚动，
  // 误判会让孩子想往下看的时候突然跳页。
  if (Math.abs(dx) < 45 || Math.abs(dx) < Math.abs(dy) * 1.2) return
  pdfGo(dx < 0 ? 1 : -1)
}

/**
 * 彻底重载：连 Service Worker 的缓存一起清掉再刷新。
 *
 * 为什么需要这么一个按钮：孩子端是 PWA，SW 会**离线优先**地喂缓存。
 * 服务端发了新版，App 里完全可能还跑着旧的那份 JS —— 表现就是
 * "我这边明明更新了，客户端还是老样子"。这种情况下普通刷新治不了，
 * 因为请求还没到网络就被 SW 截住了，必须先把 SW 和 Cache Storage 清掉。
 */
async function hardReload() {
  try {
    if ('serviceWorker' in navigator) {
      const regs = await navigator.serviceWorker.getRegistrations()
      await Promise.all(regs.map((r) => r.unregister()))
    }
    if (window.caches) {
      const keys = await caches.keys()
      await Promise.all(keys.map((k) => caches.delete(k)))
    }
  } catch (e) {
    // 清不动也照样 reload —— 至少能拿到网络上的那一份
  }
  location.reload()
}

/** 把 pdf.js 的各种失败翻译成家长能看懂的话。 */
function pdfErrorText(e) {
  const msg = String((e && e.message) || e || '')
  if (/Missing PDF|Invalid PDF|Unexpected server response/i.test(msg)) {
    return '这个绘本打不开，可能是文件本身有问题'
  }
  if (/fetch|network|Failed to load|NetworkError/i.test(msg)) {
    return '绘本加载失败，检查一下网络再试试'
  }
  return '绘本打开失败'
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
  // 点开就播 —— 视频、音频都一样。
  //
  // 以前这里只给音频自动播放，视频要用户再点一次播放键：孩子端看到的是
  // 一个转圈的播放器，点一下才动，像是"卡住了"。安卓壳（标准版与 PRO）
  // 都关掉了 mediaPlaybackRequiresUserGesture，所以这里能直接播；
  // 万一被浏览器策略拦下，catch 掉即可，手动点依然有效。
  el.play().catch(() => {
    // 拦截 / 解码未就绪都会走到这里，等用户点按钮
    isPlaying.value = false
  })
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
    // load() 会把播放器打回暂停态，这里补一次播放：
    // 走到这条路说明刚才正在播（直链过期 / 网络抖了一下），换好源就该继续。
    el?.play().catch(() => {})
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

/* ---------- 上一首 / 下一首（页面按钮） ---------- */
// 只有一集时不显示这一栏：摆两个必然点不动的按钮只会让人困惑。
const canSwitch = computed(() => !error.value && playlist.value.length > 1)

// 顺序播放到两头时把按钮置灰 —— 比"点了没反应"更清楚
const hasPrev = computed(() => prevTrackId() !== 0)
const hasNext = computed(() => nextTrackId() !== 0)

function goPrevTrack() {
  const id = prevTrackId()
  if (id) goTo(id)
}

function goNextTrack() {
  const id = nextTrackId()
  if (id) goTo(id)
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
  resetPdf()
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
  // 给安卓壳（以及任何宿主）用的最小桥：通知栏 / 锁屏 / 车机的
  // 上一首、下一首、播放暂停都调这里，与页面按钮、系统媒体面板
  // 共用同一套实现 —— 换集逻辑只存在一份。
  window.__kidMedia = {
    prev: goPrevTrack,
    next: goNextTrack,
    toggle: () => togglePlay(),
    isPlaying: () => isPlaying.value,
  }
  load()
  startReportTimer()
  document.addEventListener('visibilitychange', onVisibilityChange)
  window.addEventListener('pagehide', onPageHide)
})

onBeforeUnmount(() => {
  // 离开播放页要把桥摘掉，否则宿主会拿到一个已经卸载的组件上的旧闭包
  try {
    delete window.__kidMedia
  } catch (e) {
    window.__kidMedia = null
  }
  cleanupMedia()
  stopTranscodePolling()
  // 正在下载就中断，别让一个已卸载的页面继续吃流量
  if (offlineAbort) offlineAbort.abort()
  // 释放 blob: 地址（只释放地址，不删已下载的数据）
  offlineReleaseUrls()
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

/* 加载态：转圈 + 两行说明，竖排。
   main.css 里的 .loading 是横排（只有转圈），这里只在本页收紧。 */
.play-page .loading {
  flex-direction: column;
  gap: 10px;
  padding: 64px 32px;
}
.loading-text {
  margin: 4px 0 0;
  font-size: 15px;
  color: var(--c-text);
}
.loading-sub {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--c-text-2, #8a8a8a);
  text-align: center;
  max-width: 260px;
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

.dl-bar .qb {
  background: #fff;
  color: var(--c-primary);
  box-shadow: 0 1px 4px rgba(38, 154, 153, 0.14);
  min-width: 74px;
}
.dl-bar .qb.on {
  background: #e8f6f5;
  color: var(--c-primary);
}
.dl-bar .qb.busy {
  opacity: 0.72;
}

/* PDF */
.pdf-area {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.pdf-viewer {
  position: relative;
  width: 100%;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  min-height: 180px;
}
.pdf-canvas {
  display: block;
  max-width: 100%;
  height: auto;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
}
/* 加载中先把上一页藏起来，避免"旧页 + 转圈"同时出现 */
.pdf-canvas.is-loading {
  visibility: hidden;
}
.pdf-wait {
  position: absolute;
  inset: 0;
  min-height: 40vh;
  flex-direction: column;
  gap: 12px;
}
.pdf-wait-text {
  margin: 0;
  font-size: 14px;
  color: var(--c-text-sub);
}
.pdf-error {
  text-align: center;
  padding: 36px 16px;
  color: var(--c-text-sub);
}
.pdf-error .emoji {
  font-size: 40px;
  margin-bottom: 10px;
}
.pdf-error-detail {
  font-size: 11.5px;
  color: var(--c-text-sub);
  opacity: 0.75;
  word-break: break-all;
  margin: 6px auto 14px;
  max-width: 320px;
  line-height: 1.5;
}
.pdf-reload {
  border: none;
  background: var(--c-primary);
  color: #fff;
  font-size: 14.5px;
  font-weight: 600;
  padding: 11px 26px;
  border-radius: 999px;
  box-shadow: var(--shadow-sm);
}
.pdf-pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
}
.pdf-nav {
  border: none;
  background: #fff;
  color: var(--c-text);
  font-size: 14.5px;
  font-weight: 600;
  padding: 11px 18px;
  border-radius: 999px;
  box-shadow: var(--shadow-sm);
}
.pdf-nav:disabled {
  opacity: 0.42;
  box-shadow: none;
}
.pdf-pageinfo {
  font-size: 14px;
  color: var(--c-text-sub);
  font-variant-numeric: tabular-nums;
  min-width: 64px;
  text-align: center;
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
  padding: 12px 16px 10px;
}

/* ===== 上一首 / 下一首 ===== */
.track-nav {
  display: flex;
  gap: 12px;
  justify-content: center;
  padding: 18px 16px 0;
}
.track-btn {
  flex: 1 1 0;
  max-width: 168px;
  border: none;
  background: #fff;
  color: var(--c-text);
  font-size: 14.5px;
  font-weight: 600;
  padding: 13px 10px;
  border-radius: 999px;
  box-shadow: var(--shadow-sm);
}
.track-btn:disabled {
  opacity: 0.42;
  box-shadow: none;
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
