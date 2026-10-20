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
      <!-- 字幕开关：只有真的配到字幕时才出现。
           没有字幕时留一个灰按钮只会让孩子反复点它。 -->
      <button
        v-if="subtitleState === 'ok'"
        class="more pressable cc"
        :class="{ on: subtitleOn }"
        @click="toggleSubtitle"
        aria-label="字幕"
      >
        CC
      </button>
      <!-- 学习资料的字幕是 AI 现听写的，第一次播放要等几十秒。
           不提示的话，家长只会看到"点了一遍没字幕"就再也不点了。 -->
      <span v-else-if="subtitleState === 'pending'" class="sub-pending">字幕生成中…</span>
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

        <!-- 音频也有字幕：很多"听故事"其实是英语启蒙，字幕是刚需 -->
        <p v-if="subtitleOn && subtitleHTML" class="sub-audio-line" v-html="subtitleHTML"></p>

        <audio
          v-if="info.url"
          ref="audioEl"
          :src="info.url"
          preload="metadata"
          playsinline
          @timeupdate="onTimeUpdate"
          @loadedmetadata="onLoadedMetadata"
          @play="onMediaPlay"
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
          @play="onMediaPlay"
          @pause="isPlaying = false"
          @ended="onEnded"
          @error="onMediaError"
        />
        <div v-else class="video-wait">
          <div class="spinner big"></div>
          <p class="prep-title">正在把这集转成能播的版本…</p>
        </div>
        <!-- 字幕层：自己渲染，不用 <track>。
             ① <track> 挂在 <audio> 上根本不显示，"听故事"就没有字幕了；
             ② 原生 cue 的字号/描边/位置几乎不可调。
             一套渲染逻辑两处共用，行为才一致。 -->
        <div v-if="subtitleOn && subtitleHTML" class="sub-overlay" v-html="subtitleHTML"></div>
        <p class="tip-lock">
          <span class="lock-emoji">🔒</span>
          播放中直接锁屏也不会停，把手机翻过来听也行
        </p>
        <div v-if="transcoding" class="transcode-tip">
          <span class="transcode-dot"></span>
          正在为这台设备准备更流畅的版本（{{ transcodeProgress }}%），转好后会自动切换
        </div>
      </section>

      <!-- ===== 电子书：EPUB / TXT / CBZ =====
           三个渲染器共用这一块外壳（标题条、字号、目录、翻页），
           真正的正文交给各自的动态 import —— epub.js 有 400KB+，
           不为一本 txt 把它拉下来。 -->
      <section v-else-if="isEbook" class="media-area ebook-area">
        <div class="ebook-bar">
          <span class="ebook-badge">{{ ebookBadge }}</span>
          <div class="ebook-tools">
            <button class="eb-btn pressable" @click="ebookFontStep(-1)" aria-label="缩小字号">A-</button>
            <button class="eb-btn pressable" @click="ebookFontStep(1)" aria-label="放大字号">A+</button>
            <button
              v-if="ebookTocItems.length"
              class="eb-btn pressable"
              :class="{ on: ebookTocOpen }"
              @click="ebookTocOpen = !ebookTocOpen"
            >
              目录
            </button>
          </div>
        </div>

        <div v-if="ebookLoading" class="ebook-wait">
          <div class="spinner"></div>
          <p class="ebook-wait-text">{{ ebookLoadingText }}</p>
        </div>

        <div v-else-if="ebookError" class="empty">
          <div class="emoji">📖</div>
          <p>{{ ebookError }}</p>
          <button class="retry pressable" @click="reloadEbook">再试一次</button>
        </div>

        <template v-else>
          <!-- EPUB：epub.js 渲染在受控 iframe 里，自带分页与滑动 -->
          <div v-if="ebookMode === 'epub'" ref="epubHost" class="epub-host"></div>

          <!-- TXT：纯文本，滚动阅读 -->
          <div
            v-if="ebookMode === 'txt'"
            ref="txtHost"
            class="txt-host"
            :style="{ fontSize: ebookFont + 'px' }"
          ></div>

          <!-- CBZ / 图片书：竖向滚动，到底部自动解压下一批 -->
          <div v-if="ebookMode === 'cbz'" class="cbz-host" @scroll.passive="onCbzScroll">
            <img
              v-for="(u, i) in cbzShown"
              :key="u + i"
              :src="u"
              class="cbz-page"
              alt=""
            />
            <p v-if="cbzLoadingMore" class="cbz-more">正在加载后面的页面…</p>
            <p v-else-if="cbzShown.length >= cbzTotal" class="cbz-more">已经是最后一页了</p>
          </div>

          <!-- 目录抽屉 -->
          <div v-if="ebookTocOpen && ebookTocItems.length" class="ebook-toc">
            <div
              v-for="(it, i) in ebookTocItems"
              :key="i"
              class="toc-item pressable"
              :style="{ paddingLeft: 12 + Math.min(it.depth || 0, 3) * 14 + 'px' }"
              @click="goToc(it)"
            >
              {{ it.label }}
            </div>
          </div>
        </template>

        <!-- EPUB 翻页条。epub.js 自己支持左右滑动，但孩子端的小手
             不一定划得动 iframe —— 页面上给一对明确的按钮更保险。 -->
        <div v-if="ebookMode === 'epub' && !ebookError" class="ebook-pager">
          <button class="pdf-nav pressable" @click="epubPrev">◀ 上一页</button>
          <span class="pdf-pageinfo">{{ ebookProgressText }}</span>
          <button class="pdf-nav pressable" @click="epubNext">下一页 ▶</button>
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
        <!-- 字幕来自哪里：家长最常问的就是"这字幕是片子自带的还是网上找的" -->
        <div v-if="subtitleState === 'ok' && subtitleSource" class="detail-row">
          <span class="k">字幕</span>
          <span class="v ellipsis">{{ subtitleSource }}</span>
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
// autoFallbackTried 记录"已经自动帮用户切过一次转码"。
// 只切一次：转码也可能失败，无限重试会变成一个看不见的死循环。
let autoFallbackTried = false

const audioEl = ref(null)
const videoEl = ref(null)

const isPlaying = ref(false)
// 用户自己按的暂停。切后台时原生要判断"该不该接着播" ——
// 分不清"用户暂停"和"系统把网页压下去导致的暂停"，就会出现
// "明明我按了暂停，切后台它自己又响起来"。
const userPaused = ref(false)
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

const TYPE_LABEL = { audio: '音频', video: '视频', pdf: '绘本', image: '图片', ebook: '电子书' }
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
  // 记住 PDF / 电子书地址，等 loading 关掉再渲染（原因见下面的注释）
  let pdfURL = ''
  let ebookURL = ''
  loading.value = true
  error.value = ''
  errorRetried = false
  autoFallbackTried = false
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
    if (isEbook.value) {
      // 电子书的宿主同样在 v-if="loading" 里面，只能等 finally 之后再挂载
      ebookURL = data.url
    }
    if (isImage.value) {
      // 图片无需额外处理
    }
    setupMediaSession()
    // 系统媒体面板要的是"标题/封面/时长"，这些此刻才拿到
    lastNativePush = Date.now()
    pushNativeMedia()
    // 不 await：字幕要打网盘/ffprobe/联网匹配，慢的时候十几秒，
    // 但播放本身一秒都不该等它。
    loadSubtitle()
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
      if (meta.mediaType === 'ebook') ebookURL = local
      setupMediaSession()
      lastNativePush = Date.now()
      pushNativeMedia()
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
  if (ebookURL) await openEbook(ebookURL)
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

/* ---------- 电子书：EPUB / TXT / CBZ ---------- */
//
// 为什么是这三个渲染器：
//   - EPUB：epub.js（纯前端、分页/目录/字号齐全）。mobi/azw3/cbr/fb2
//     由服务端先转成 EPUB，到达前端时统一是 epub；
//   - TXT：没有排版可言，直接滚动显示，字号可调；
//   - CBZ：本质是「一堆图片打进 zip」，解压后按自然序竖向排列。
//
// 三者共同点：都是**动态 import**。epub.js 有 400KB+，jszip 也有 100KB，
// 不能因为它们让首页多等一秒 —— 只有真的打开一本电子书才加载。
const FONT_KEY = 'kid_ebook_font'
const FONT_MIN = 14
const FONT_MAX = 30

const epubHost = ref(null)
const txtHost = ref(null)
const ebookLoading = ref(false)
const ebookLoadingText = ref('正在打开…')
const ebookError = ref('')
const ebookTocOpen = ref(false)
const ebookTocItems = ref([])
const ebookProgressText = ref('')
const ebookFont = ref(readEbookFont())

const cbzPages = ref([]) // 全部页的 blob: 地址
const cbzShownCount = ref(0)
const cbzTotal = ref(0)
const cbzLoadingMore = ref(false)
const cbzShown = computed(() => cbzPages.value.slice(0, cbzShownCount.value))

let epubBook = null
let epubRendition = null
let ebookSeq = 0 // 让"慢的那次"不能覆盖"新的一次"
let ebookPos = '' // 当前 CFI

const isEbook = computed(() => info.value.mediaType === 'ebook')

const ebookFormat = computed(() => String(info.value.ebookFormat || '').toLowerCase())

// ebookMode 决定用哪个渲染器。认不出来时退回滚动文本 ——
// 至少让孩子看到内容，而不是一个空白页。
const ebookMode = computed(() => {
  const f = ebookFormat.value
  if (f === 'epub') return 'epub'
  if (f === 'cbz') return 'cbz'
  if (f === 'txt' || f === '') return 'txt'
  return 'txt'
})

const ebookBadge = computed(() => {
  const f = ebookFormat.value || 'book'
  return f.toUpperCase()
})

function readEbookFont() {
  try {
    const v = Number(localStorage.getItem(FONT_KEY))
    if (v >= FONT_MIN && v <= FONT_MAX) return v
  } catch {
    /* 隐私模式：用默认值 */
  }
  return 18
}

function ebookFontStep(d) {
  const next = Math.max(FONT_MIN, Math.min(FONT_MAX, ebookFont.value + d * 2))
  ebookFont.value = next
  try {
    localStorage.setItem(FONT_KEY, String(next))
  } catch {
    /* 存不下不影响本次阅读 */
  }
  if (epubRendition) {
    try {
      epubRendition.themes.fontSize(next + 'px')
    } catch {
      /* 老版本 epub.js 不支持运行时改字号，忽略 */
    }
  }
}

function resetEbook() {
  ebookSeq++
  if (epubRendition) {
    try {
      epubRendition.destroy()
    } catch {
      /* noop */
    }
    epubRendition = null
  }
  if (epubBook) {
    try {
      epubBook.destroy()
    } catch {
      /* noop */
    }
    epubBook = null
  }
  // blob: 地址必须显式释放，否则翻十几本漫画就把内存吃光了
  cbzPages.value.forEach((u) => {
    try {
      URL.revokeObjectURL(u)
    } catch {
      /* noop */
    }
  })
  cbzPages.value = []
  cbzShownCount.value = 0
  cbzTotal.value = 0
  cbzLoadingMore.value = false
  cbzZip = null
  cbzNames = []
  pendingTxt = null
  ebookTocItems.value = []
  ebookTocOpen.value = false
  ebookProgressText.value = ''
  ebookLoading.value = false
  ebookError.value = ''
}

// fetchBook 取整本书的字节。
//
// 不用封装好的 axios 实例：它的 baseURL 是 /api，而这些地址本身
// 已经是 /api/ebook/... 了，拼起来会变成 /api/api/...；
// 另外票据鉴权不依赖 Authorization 头，用裸 fetch 最省事。
async function fetchBook(url) {
  const res = await fetch(url, { credentials: 'same-origin' })
  if (!res.ok) {
    let msg = ''
    try {
      const j = await res.json()
      msg = j && (j.message || j.msg)
    } catch {
      /* 不是 JSON（比如网关的 HTML 错误页），走下面的兜底文案 */
    }
    throw new Error(msg || '这本书暂时打不开')
  }
  return res.arrayBuffer()
}

async function openEbook(url) {
  resetEbook()
  const seq = ebookSeq
  if (!url) {
    ebookError.value = '这本书没有可读的地址'
    return
  }
  ebookLoading.value = true
  ebookLoadingText.value = ebookMode.value === 'epub' ? '正在打开这本书…' : '正在准备…'
  // EPUB 的"解析"和"渲染"必须分两步：渲染宿主在 loading 关掉之后才挂载。
  let mount = null
  try {
    const buf = await fetchBook(url)
    if (seq !== ebookSeq) return
    if (ebookMode.value === 'epub') {
      mount = await prepareEpub(buf, seq)
    } else if (ebookMode.value === 'cbz') {
      await openCbz(buf)
    } else {
      pendingTxt = decodeBookText(buf)
    }
  } catch (e) {
    if (seq !== ebookSeq) return
    ebookError.value = ebookErrorText(e)
  } finally {
    if (seq === ebookSeq) ebookLoading.value = false
  }
  if (seq !== ebookSeq) return
  await nextTick()
  if (seq !== ebookSeq) return
  if (mount) {
    await mount()
  } else if (pendingTxt !== null) {
    openTxt(pendingTxt)
    pendingTxt = null
  }
}

let pendingTxt = null

// prepareEpub 只解析、不渲染，返回一个"DOM 就绪后再调"的函数。
async function prepareEpub(buf, seq) {
  // epub.js 对"不是 zip"的文件会抛一个很抽象的错误，先自己判一下
  const head = new Uint8Array(buf.slice(0, 4))
  if (!(head[0] === 0x50 && head[1] === 0x4b)) {
    throw new Error('这个文件不是标准的 EPUB')
  }
  const mod = await import('epubjs')
  const ePub = mod.default || mod
  if (seq !== ebookSeq) return null

  const book = ePub(buf)
  epubBook = book
  await book.ready
  if (seq !== ebookSeq) return null

  ebookTocItems.value = flattenToc(book.navigation && book.navigation.toc)
  return () => mountEpub(book, seq)
}

async function mountEpub(book, seq) {
  const host = epubHost.value
  if (!host || seq !== ebookSeq) return
  const rendition = book.renderTo(host, {
    width: '100%',
    height: '100%',
    flow: 'paginated',
    spread: 'none',
  })
  epubRendition = rendition
  try {
    rendition.themes.fontSize(ebookFont.value + 'px')
    rendition.themes.override('line-height', '1.7')
  } catch {
    /* 字号主题失败不影响翻页 */
  }

  // 从上次读到的地方继续
  const saved = readBookPos()
  await rendition.display(saved || undefined)

  rendition.on('relocated', (loc) => {
    if (!loc || !loc.start) return
    ebookPos = loc.start.cfi
    saveBookPos(ebookPos)
    if (loc.start.displayed && loc.start.displayed.page) {
      const { page, total } = loc.start.displayed
      ebookProgressText.value = `第 ${page} / ${total} 页`
    } else {
      ebookProgressText.value = `${Math.round((loc.start.percentage || 0) * 100)}%`
    }
  })
}

function flattenToc(items, depth = 0, out = []) {
  if (!Array.isArray(items)) return out
  for (const it of items) {
    if (!it) continue
    if (it.label) out.push({ label: String(it.label).trim(), href: it.href, depth })
    if (it.subitems && it.subitems.length) flattenToc(it.subitems, depth + 1, out)
  }
  return out.slice(0, 200)
}

function readBookPos() {
  try {
    return localStorage.getItem('kid_ebook_pos_' + resourceId.value) || ''
  } catch {
    return ''
  }
}

function saveBookPos(cfi) {
  try {
    localStorage.setItem('kid_ebook_pos_' + resourceId.value, cfi)
  } catch {
    /* 存不下不影响阅读 */
  }
}

function epubPrev() {
  epubRendition && epubRendition.prev()
}

function epubNext() {
  epubRendition && epubRendition.next()
}

function goToc(it) {
  ebookTocOpen.value = false
  if (epubRendition && it && it.href) epubRendition.display(it.href)
}

// decodeBookText 解 TXT。国内大部分 txt 是 GBK，用 UTF-8 硬解会满屏问号。
function decodeBookText(buf) {
  let text = ''
  try {
    text = new TextDecoder('utf-8').decode(buf)
  } catch {
    text = ''
  }
  const bad = (text.match(/\uFFFD/g) || []).length
  if (bad > 3 && bad / Math.max(text.length, 1) > 0.001) {
    try {
      text = new TextDecoder('gbk').decode(buf)
    } catch {
      /* 浏览器不支持 gbk 就保留 UTF-8 的结果 */
    }
  }
  return text
}

function openTxt(text) {
  const host = txtHost.value
  if (!host) return
  // textContent 而不是 innerHTML：书里的 "<" 就是字面量，
  // 用 innerHTML 会把它们当标签解析（内容被吃掉，还有注入风险）
  host.textContent = text || '（这本书是空的）'
  host.scrollTop = Number(readTxtPos()) || 0
  host.addEventListener(
    'scroll',
    () => {
      try {
        localStorage.setItem('kid_ebook_txtpos_' + resourceId.value, String(host.scrollTop))
      } catch {
        /* noop */
      }
    },
    { passive: true },
  )
}

function readTxtPos() {
  try {
    return localStorage.getItem('kid_ebook_txtpos_' + resourceId.value)
  } catch {
    return 0
  }
}

async function openCbz(buf) {
  const mod = await import('jszip')
  const JSZip = mod.default || mod
  const zip = await JSZip.loadAsync(buf)
  const names = Object.keys(zip.files)
    .filter((n) => !zip.files[n].dir && /\.(jpe?g|png|gif|webp|bmp)$/i.test(n))
    .sort(naturalCompare)
  if (!names.length) throw new Error('这个漫画包里没有图片')

  cbzTotal.value = names.length
  cbzPages.value = names.map(() => '') // 占位，按需填充
  // 句柄留着给"滚到底加载下一批"用，离开页面时由 resetEbook 释放
  cbzZip = zip
  cbzNames = names
  await loadCbzBatch(zip, names, 0, 6)
}

function naturalCompare(a, b) {
  return a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' })
}

async function loadCbzBatch(zip, names, from, count) {
  cbzLoadingMore.value = true
  try {
    const end = Math.min(names.length, from + count)
    for (let i = from; i < end; i++) {
      const blob = await zip.files[names[i]].async('blob')
      cbzPages.value[i] = URL.createObjectURL(blob)
    }
    cbzShownCount.value = end
  } finally {
    cbzLoadingMore.value = false
  }
}

// onCbzScroll 到底了再解压下一批。
//
// 一话漫画 30~60 张图，一次性全部解成 blob 在低端机上会 OOM；
// 分批加载让"打开第一页"和"内存占用"都保持在可接受范围。
async function onCbzScroll(e) {
  if (cbzLoadingMore.value || cbzShownCount.value >= cbzTotal.value) return
  const el = e.target
  if (el.scrollTop + el.clientHeight < el.scrollHeight - 600) return
  if (!cbzZip || !cbzNames.length) return
  await loadCbzBatch(cbzZip, cbzNames, cbzShownCount.value, 6)
}

let cbzZip = null
let cbzNames = []

function reloadEbook() {
  load()
}

function ebookErrorText(e) {
  const msg = String((e && e.message) || e || '')
  if (/Failed to fetch|network|NetworkError/i.test(msg)) return '网络好像不太稳定，检查一下再试'
  if (/不是标准|zip/i.test(msg)) return '这本书的文件好像损坏了'
  if (/版权|DRM/i.test(msg)) return '这本书有版权保护，暂时打不开'
  return msg || '这本书暂时打不开'
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
  // 系统媒体面板的进度条：5 秒推一次就够（更频繁只会白耗电，
  // 系统自己会根据播放速度往上推）。
  const now = Date.now()
  if (now - lastNativePush > 5000) {
    lastNativePush = now
    pushNativeMedia()
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

  // —— 情况三：容器看着没问题，但里面的**编码**放不出来 ——
  //
  // mp4/mov 里封 HEVC、AC3、DTS 是最常见的坑：容器判定说"能播"，
  // 播放器却直接给 code 4（HEVC 在部分设备）或者能出画面没声音。
  // 这种只能等真的失败才知道，所以在这里自动切一次转码 ——
  // 用户不需要理解"编码"是什么，只需要最后能看。
  if (
    code === 4 &&
    errorRetried &&
    !autoFallbackTried &&
    !isSmooth.value &&
    transcodeAvailable.value &&
    isVideo.value
  ) {
    autoFallbackTried = true
    showToast('这个格式要转一下，正在准备…')
    setQuality(QUALITY_SMOOTH)
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
// 真正开始播放时清掉"用户按过暂停"的标记：
// 自动播放、切下一集、原生交还，都不该被上一次的暂停意图挡住。
function onMediaPlay() {
  isPlaying.value = true
  userPaused.value = false
}

function togglePlay() {
  const el = mediaEl()
  if (!el) return
  if (el.paused) {
    userPaused.value = false
    el.play().catch(() => {})
  } else {
    userPaused.value = true
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

/* ---------- 系统媒体控制中心（原生壳） ----------
 *
 * WebView 里的 `navigator.mediaSession` **不会**出现在 Android 的系统媒体区
 * （那是 Chrome 浏览器自己的实现）。所以真正的常驻控制必须由壳里的
 * 前台服务 + MediaSessionCompat 来做，这里只负责把状态推过去。
 *
 * 只在标准版/PRO 里有效：纯浏览器打开时没有 KidNative 桥，静默跳过。
 */
let lastNativePush = 0

function pushNativeMedia() {
  const bridge = nativeBridge()
  if (!bridge) return
  if (!isVideo.value && !isAudio.value) return
  try {
    bridge.mediaState(JSON.stringify(nativePayload()))
  } catch {
    /* 桥挂了不能影响播放 */
  }
}

/* ---------- 原生接管（后台播放） ----------
 *
 * 问题：App 切到后台后，Chromium 会把 WebView 的 JS 执行挂起。
 * 上一版把"播放"完全留在网页里，通知栏/媒体面板的每个按钮都要
 * `evaluateJavascript` 打回网页执行 —— 网页一挂起，按钮就点不动，
 * 正在播的东西也会停。孩子看到的就是"有图标、点不动、切后台就断"。
 *
 * 现在改成**所有权交接**：
 *   切后台 → 把 url + 位置交给原生（takeOver），原生用 MediaPlayer 接着播，
 *            然后 ack 回来让我们把自己静音并暂停（避免两边出声）；
 *   回前台 → 原生停下，把位置还回来（giveBack），我们 seek 过去继续播。
 *
 * ⚠️ 交接期间 `isPlaying` 会被置成 false —— 这是**故意的**：网页确实没在播了。
 * 壳里判断"要不要停前台服务"的逻辑要认得这个状态（见 maybeStopPlayback），
 * 否则宽限期一到就把服务收了，后台播放跟着断。
 */
let handoffActive = false

function nativeBridge() {
  // 标准版的桥叫 KidNative，PRO 的叫 EdgeNative —— 两个壳各自独立，
  // 这里都认（纯浏览器里两个都没有，静默跳过）。
  const b = window.KidNative || window.EdgeNative
  if (!b || typeof b.mediaState !== 'function') return null
  return b
}

/** 推给原生/系统面板的那一份状态。 */
function nativePayload() {
  return {
    active: true,
    playing: !!isPlaying.value,
    title: info.value.title || '',
    subtitle: info.value.categoryName || '',
    poster: info.value.poster || '',
    duration: Math.round(Number(duration.value) || 0),
    position: Math.round(Number(current.value) || 0),
    hasPrev: !!prevTrackId(),
    hasNext: !!nextTrackId(),
    // 后台接管要用（原生读不到 WebView 里的状态）
    //
    // ⚠️ 必须是**绝对地址**：后端给的是 `/api/stream/123?t=...` 这种相对路径，
    // 浏览器会按当前页面补全，而原生的 MediaPlayer 收到相对路径会把它
    // 当成一个本地文件名 —— 接管"成功"，但一点声音都没有。
    // 现象就是"控制中心在、点播放没反应、后台也没声"。
    url: absoluteURL(info.value.url || ''),
    video: !!isVideo.value,
    paused: !!userPaused.value,
  }
}

/** 相对地址补成绝对地址；blob:/data: 原样返回（它们本来就不能交给原生）。 */
function absoluteURL(u) {
  if (!u) return ''
  if (/^(https?|blob|data|file):/i.test(u)) return u
  try {
    return new URL(u, location.href).href
  } catch {
    return u
  }
}

/**
 * 能不能交给原生。
 *
 * `blob:` 坚决不行 —— 它只存在于 WebView 进程内，原生播放器读不到，
 * 接管"成功"了却一点声音都没有，比不接管更糟。离线缓存播放走的就是 blob。
 */
function canHandoff() {
  const bridge = nativeBridge()
  if (!bridge || typeof bridge.takeOver !== 'function') return false
  if (!isVideo.value && !isAudio.value) return false
  if (offlinePlay.value) return false
  if (!isPlaying.value) return false
  const u = info.value.url || ''
  if (!u) return false
  return !/^(blob|data):/i.test(u)
}

// 播放/暂停要立刻反映到系统面板（不能等下一个 5 秒节拍）
watch(isPlaying, () => {
  lastNativePush = Date.now()
  pushNativeMedia()
})

// 离开播放页：让系统面板收起。壳里会等 20 秒宽限再停服务 ——
// 切集时"清理→重新加载"必然会走一次 inactive，不能立刻停。
function pushNativeInactive() {
  const bridge = nativeBridge()
  if (!bridge) return
  try {
    bridge.mediaState(JSON.stringify({ active: false }))
  } catch {
    /* 同上 */
  }
}

/* ---------- 字幕 ----------
 *
 * 三条来源都在服务端做完了（同目录文件 → 容器内嵌轨 → 联网匹配），
 * 这里只负责"取回 VTT + 按时间显示"。前端不做匹配逻辑：
 * 标题清洗、集数识别、语言优先级这些规则放一份，两端才不会各说各话。
 */
const SUB_KEY = 'kid_subtitle_on'

const subtitleCues = ref([])
const subtitleState = ref('idle') // idle | loading | ok | none | pending
const subtitleSource = ref('')
const subtitleOn = ref(readSubtitlePref())

function readSubtitlePref() {
  try {
    return localStorage.getItem(SUB_KEY) !== '0'
  } catch {
    return true
  }
}

function toggleSubtitle() {
  subtitleOn.value = !subtitleOn.value
  try {
    localStorage.setItem(SUB_KEY, subtitleOn.value ? '1' : '0')
  } catch {
    /* 隐私模式下写不了，只影响本次会话 */
  }
}

// vttSeconds 把 `00:01:02.500` / `01:02.500` 转成秒。
function vttSeconds(s) {
  const parts = String(s || '').trim().split(':')
  if (parts.length < 2 || parts.length > 3) return NaN
  const sec = Number(parts[parts.length - 1].replace(',', '.'))
  const min = Number(parts[parts.length - 2])
  const hr = parts.length === 3 ? Number(parts[0]) : 0
  if (!isFinite(sec) || !isFinite(min) || !isFinite(hr)) return NaN
  return hr * 3600 + min * 60 + sec
}

// parseVTT 解析成 [{start, end, text}]。
//
// 只认 WebVTT 的核心结构：时间轴行 + 其后的文本块。
// STYLE/NOTE/REGION 这些块直接跳过 —— 我们不渲染它们，解析了也是死代码。
function parseVTT(raw) {
  const lines = String(raw || '').replace(/\r\n?/g, '\n').split('\n')
  const cues = []
  let i = 0
  while (i < lines.length) {
    const ln = lines[i]
    if (ln.includes('-->')) {
      const [a, b] = ln.split('-->')
      const start = vttSeconds(a)
      // 结束时间后面可能跟着位置设置（`00:00:03.000 align:start`），只取第一段
      const end = vttSeconds((b || '').trim().split(/\s+/)[0])
      i++
      const buf = []
      while (i < lines.length && lines[i].trim() !== '') {
        buf.push(lines[i])
        i++
      }
      if (isFinite(start) && isFinite(end) && end > start && buf.length) {
        cues.push({ start, end, text: buf.join('\n') })
      }
    } else {
      i++
    }
  }
  // 二分查找要求按开始时间有序，正常 VTT 本来就有序，这里兜一下
  cues.sort((x, y) => x.start - y.start)
  return cues
}

// cueHTML 把 cue 文本变成安全的 HTML。
//
// ⚠️ 字幕可能来自网上（OpenSubtitles），**绝不能直接 v-html**：
// 那等于把任意 HTML/脚本注入到孩子端页面里。
// 做法是先全量转义，再只把 <i>/<b>/<u>/<br> 这四种无害标签放回来。
function cueHTML(text) {
  return String(text || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/&lt;(\/?)(i|b|u)&gt;/gi, '<$1$2>')
    .replace(/\n/g, '<br>')
}

// subtitleHTML 当前时刻应该显示的那句。
//
// 用"最后一个 start <= t < end"而不是"第一个匹配"：
// 有的字幕条会重叠（歌词/多角色对白），显示最后开始的那条更符合直觉。
const subtitleHTML = computed(() => {
  const cues = subtitleCues.value
  if (!cues.length) return ''
  const t = current.value
  let lo = 0
  let hi = cues.length - 1
  let hit = -1
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    if (cues[mid].start <= t) {
      hit = mid
      lo = mid + 1
    } else {
      hi = mid - 1
    }
  }
  // 从命中的位置往回找一小段，兼容"后一条还没到结束、前一条还该显示"的重叠
  for (let i = hit; i >= 0 && i > hit - 8; i--) {
    if (t >= cues[i].start && t < cues[i].end) return cueHTML(cues[i].text)
  }
  return ''
})

function resetSubtitle() {
  subtitleCues.value = []
  subtitleState.value = 'idle'
  subtitleSource.value = ''
}

// ticketFrom 从播放地址里取票据（本地缓存播放时没有票据）。
function ticketFrom(url) {
  try {
    return new URL(url, location.origin).searchParams.get('t') || ''
  } catch {
    return ''
  }
}

// decodeSubtitleSource 解服务端编码过的字幕来源。
//
// HTTP 头只能是 ASCII，而来源里有中文（「同目录字幕 · xxx.srt」），
// 服务端用 url.QueryEscape 编过一道，这里解回来。
// 解不开就原样显示 —— 宁可难看一点，也不要因为一个展示字段让字幕整体失败。
function decodeSubtitleSource(v) {
  if (!v) return ''
  try {
    // QueryEscape 把空格编成 '+'，decodeURIComponent 不认，先换回来
    return decodeURIComponent(String(v).replace(/\+/g, ' '))
  } catch {
    return String(v)
  }
}

// 转写进行中时服务端会回 204 + 这个头。它和"真的没有字幕"是两回事：
// 后者是终局，前者值得再等一会儿 —— 学习资料的字幕是 AI 现听写的。
const SUB_POLL_DELAYS = [10000, 20000, 30000, 60000, 90000, 120000]

// 每轮请求的代号。切集/重播会让旧的那一轮作废，
// 否则上一集的轮询会在新一集上把字幕刷成旧的。
let subGen = 0

async function loadSubtitle() {
  const gen = ++subGen
  resetSubtitle()
  await fetchSubtitle(gen, 0)
}

async function fetchSubtitle(gen, attempt) {
  if (!isVideo.value && !isAudio.value) return
  // 离线缓存播放：没有网络也没有票据，直接跳过
  if (offlinePlay.value) return

  // 服务端签好的地址优先（PRO 会把 url 换成自己的加速流，票据就取不到了）
  let target = info.value.subtitleUrl || ''
  if (!target) {
    const t = ticketFrom(info.value.url)
    if (!t) {
      subtitleState.value = 'none'
      return
    }
    target = `/api/subtitle/${resourceId.value}?t=${encodeURIComponent(t)}`
  }

  if (attempt === 0) subtitleState.value = 'loading'
  try {
    const res = await fetch(target, { cache: 'no-store' })
    if (res.status === 204 || !res.ok) {
      // 204 = 服务端明确说"没有字幕"。
      // 但带 X-Subtitle-Pending 的 204 是"正在听写，等会儿再来"。
      if (res.headers.get('X-Subtitle-Pending') === '1' && attempt < SUB_POLL_DELAYS.length) {
        if (gen !== subGen) return
        subtitleState.value = 'pending'
        setTimeout(() => {
          if (gen === subGen) fetchSubtitle(gen, attempt + 1)
        }, SUB_POLL_DELAYS[attempt])
        return
      }
      // 204 = 服务端明确说"没有字幕"。这是常态，不提示。
      subtitleState.value = 'none'
      return
    }
    const text = await res.text()
    // 鉴权失败时后端会回 JSON（200 + code:401），别把它当字幕解析
    if (/^\s*\{/.test(text)) {
      subtitleState.value = 'none'
      return
    }
    const cues = parseVTT(text)
    if (!cues.length) {
      subtitleState.value = 'none'
      return
    }
    if (gen !== subGen) return // 请求期间已经换集了
    subtitleCues.value = cues
    subtitleSource.value = decodeSubtitleSource(res.headers.get('X-Subtitle-Source'))
    subtitleState.value = 'ok'
  } catch {
    subtitleState.value = 'none'
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
  resetEbook()
  resetSubtitle()
  pushNativeInactive()
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
    // 切后台：把播放交给原生接着播。
    //
    // ⚠️ 必须在 visibilitychange 里做，不能等原生的 onStop —— 那时 WebView
    // 可能已经被挂起，页面没机会把地址和位置送出去。
    if (canHandoff()) {
      try {
        nativeBridge().takeOver(JSON.stringify(nativePayload()))
      } catch {
        /* 交不出去就让网页自己撑着，不至于更糟 */
      }
    }
  } else {
    // 回前台：先要回播放位置，再同步真实状态
    if (handoffActive) {
      try {
        nativeBridge()?.giveBack?.()
      } catch {
        /* 同上 */
      }
    }
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

    // ⚠️ play / pause 必须分开且**幂等**。
    // 系统会重复发 onPlay（耳机重连、蓝牙切车机、媒体面板回连都会），
    // 用 toggle 顶着会把正在播的暂停掉 —— 表现就是"点播放反而停了"。
    play: () => {
      const el = mediaEl()
      if (!el || !el.paused) return
      el.play().catch(() => {})
    },
    pause: () => {
      const el = mediaEl()
      if (!el || el.paused) return
      el.pause()
    },

    isPlaying: () => isPlaying.value,

    // 系统媒体面板拖动进度条 → 原生 → 这里
    seekTo: (sec) => {
      const el = mediaEl()
      if (!el) return
      const t = Number(sec)
      if (!Number.isFinite(t) || t < 0) return
      try {
        el.currentTime = t
        current.value = t
      } catch {
        /* 元数据还没加载时设 currentTime 会抛，忽略即可 */
      }
    },

    /* ---------- 后台接管的两个回调（见 canHandoff 上方那段说明） ---------- */

    /** 原生已经接手在播了：把我们自己静音并暂停，否则两边一起出声。 */
    takeOverAck: () => {
      handoffActive = true
      const el = mediaEl()
      if (!el) return
      try {
        el.muted = true
        el.pause()
      } catch {
        /* noop */
      }
      isPlaying.value = false
      lastNativePush = Date.now()
    },

    /** 原生交还播放：seek 回去继续播。`ended` = 后台已经把这一集播完了。 */
    giveBack: (sec, ended) => {
      handoffActive = false
      if (ended) {
        // 后台播完由原生报回来，走同一套"自动下一集"
        onEnded()
        return
      }
      const el = mediaEl()
      if (!el) return
      const t = Number(sec)
      try {
        if (Number.isFinite(t) && t > 0 && Math.abs(t - (el.currentTime || 0)) > 1.5) {
          el.currentTime = t
          current.value = t
        }
        el.muted = false
      } catch {
        /* noop */
      }
      el.play().catch(() => {})
      isPlaying.value = !el.paused
    },
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
/* 字幕生成中的提示：不弹窗、不遮画面，只是让人知道"在做了" */
.sub-pending {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--c-text-sub);
  padding: 0 6px;
  white-space: nowrap;
  animation: sub-blink 1.6s ease-in-out infinite;
}
@keyframes sub-blink {
  0%,
  100% {
    opacity: 0.45;
  }
  50% {
    opacity: 1;
  }
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
  /* 字幕层用 absolute 定位压在上面，必须给一个定位上下文 */
  position: relative;
}

/* ---------- 字幕 ---------- */
.cc {
  font-size: 12.5px;
  font-weight: 700;
  letter-spacing: 0.3px;
  opacity: 0.5;
}
.cc.on {
  opacity: 1;
  color: #4a7ef0;
  background: #eef3ff;
}

/* 视频字幕：压在下缘之上一点点，避开原生控制条（约 44px 高） */
.sub-overlay {
  position: absolute;
  left: 14px;
  right: 14px;
  bottom: 62px;
  text-align: center;
  font-size: 16px;
  line-height: 1.42;
  font-weight: 600;
  color: #fff;
  /* 白字压在亮画面上会看不见，双层阴影是最省事也最稳的描边 */
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.95), 0 0 6px rgba(0, 0, 0, 0.85);
  pointer-events: none;
  white-space: pre-wrap;
  word-break: break-word;
  z-index: 3;
}

/* 音频字幕：页面流内，不遮挡封面与进度 */
.sub-audio-line {
  margin: 10px 18px 0;
  font-size: 15px;
  line-height: 1.5;
  color: var(--c-text);
  text-align: center;
  min-height: 23px;
  word-break: break-word;
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

/* ===== 电子书（EPUB / TXT / CBZ） ===== */
.ebook-area {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.ebook-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.ebook-badge {
  font-size: 11.5px;
  font-weight: 700;
  letter-spacing: 0.4px;
  color: var(--c-primary);
  background: #e8f6f5;
  padding: 4px 10px;
  border-radius: 999px;
}
.ebook-tools {
  display: flex;
  gap: 8px;
}
.eb-btn {
  border: none;
  background: #fff;
  color: var(--c-text);
  font-size: 13px;
  font-weight: 600;
  padding: 7px 12px;
  border-radius: 999px;
  box-shadow: var(--shadow-sm);
  min-width: 40px;
}
.eb-btn.on {
  background: var(--c-primary);
  color: #fff;
}
.ebook-wait {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 48px 20px;
}
.ebook-wait-text {
  margin: 0;
  font-size: 13.5px;
  color: var(--c-text-sub);
  text-align: center;
}

/* epub.js 会在这个容器里塞一个 iframe；必须给它一个明确的高度，
   否则 iframe 会缩成 0 —— 表现是"目录出来了、正文区域一片空白" */
.epub-host {
  width: 100%;
  height: 66vh;
  min-height: 320px;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  overflow: hidden;
}

/* 纯文本：保留换行与缩进，长段落自动折行 */
.txt-host {
  width: 100%;
  height: 62vh;
  min-height: 300px;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  white-space: pre-wrap;
  word-break: break-word;
  line-height: 1.85;
  color: var(--c-text);
  background: #fff;
  padding: 18px 16px;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  font-family: -apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif;
}

/* 漫画：竖向滚动，一页一张图 */
.cbz-host {
  width: 100%;
  height: 70vh;
  min-height: 320px;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  background: #111;
  border-radius: var(--r-md);
  padding: 6px 0;
}
.cbz-page {
  display: block;
  width: 100%;
  height: auto;
  margin: 0 0 6px;
  background: #111;
}
.cbz-more {
  margin: 12px 0 18px;
  text-align: center;
  font-size: 12.5px;
  color: #bbb;
}

.ebook-toc {
  max-height: 44vh;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  padding: 6px 0;
}
.toc-item {
  padding: 11px 14px;
  font-size: 14px;
  color: var(--c-text);
  border-bottom: 1px solid #f0f3f8;
}
.toc-item:last-child {
  border-bottom: none;
}
.ebook-pager {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 14px;
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
