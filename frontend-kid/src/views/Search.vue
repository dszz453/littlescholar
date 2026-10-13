<template>
  <div class="page search-page">
    <header class="bar" :style="barStyle">
      <SearchBar
        ref="sb"
        v-model="keyword"
        :placeholder="scope === 'system' ? '搜成语故事、英语儿歌…' : '搜全网动画、电影、纪录片…'"
        @search="doSearch"
      />

      <!-- 搜索范围：搜系统 / 搜全网 -->
      <div class="scope">
        <button
          class="scope-btn"
          :class="{ on: scope === 'system' }"
          @click="switchScope('system')"
        >
          搜系统
        </button>
        <button
          class="scope-btn"
          :class="{ on: scope === 'global' }"
          @click="switchScope('global')"
        >
          搜全网
        </button>
      </div>

      <!-- 网盘筛选：只在「搜系统」出现。
           只列出结果里真实存在的网盘，点了就按它过滤 —— 与后台
           「资源搜索」的按网盘筛选是同一套语义。 -->
      <div v-if="scope === 'system' && diskFacets.length > 1" class="disk-filter">
        <span
          class="df-chip pressable"
          :class="{ on: activeDisk === '' }"
          @click="pickDisk('')"
        >
          全部
        </span>
        <span
          v-for="f in diskFacets"
          :key="f.diskType"
          class="df-chip pressable"
          :class="{ on: activeDisk === f.diskType }"
          @click="pickDisk(f.diskType)"
        >
          {{ f.name }} {{ f.total }}
        </span>
      </div>
    </header>

    <div class="page-pad">
      <!-- ============ 搜系统 ============ -->
      <template v-if="scope === 'system'">
        <!-- 未搜索：推荐 + 热门词 -->
        <template v-if="!searched">
          <div class="section-title"><h2>试试这些</h2></div>
          <div class="hot">
            <span v-for="t in hotWords" :key="t" class="hot-tag pressable" @click="quickSearch(t)">
              {{ t }}
            </span>
          </div>

          <div v-if="allResources.length" class="section-title"><h2>随便看看</h2></div>
          <ResourceCard v-for="r in allResources" :key="r.id" :node="r" @click="goPlay(r)" />
        </template>

        <div v-else-if="loading" class="loading"><div class="spinner"></div></div>

        <template v-else-if="results.length">
          <div class="section-title"><h2>找到 {{ total }} 个</h2></div>
          <ResourceCard
            v-for="(r, i) in results"
            :key="r.id"
            :node="r"
            class="pop-in"
            :style="{ animationDelay: i * 35 + 'ms' }"
            @click="goPlay(r)"
          />
        </template>

        <div v-else class="empty">
          <span class="emoji">🔍</span>
          <p>没有找到「{{ keyword }}」</p>
          <p class="text-sub">
            {{ activeDisk ? '换个网盘或点「全部」试试看吧' : '换个词试试看吧' }}
          </p>
        </div>
      </template>

      <!-- ============ 搜全网 ============ -->
      <template v-else>
        <!-- 未配置转存网盘时的提示 -->
        <div v-if="!globalReady && globalSearched" class="notice">
          <span class="emoji">🔌</span>
          <p>全网搜索已就绪，但还没配置默认转存网盘</p>
          <p class="text-sub">请让家长在后台「系统设置 → 搜一搜 · 搜全网」里指定网盘账号和目录</p>
        </div>

        <template v-if="!globalSearched">
          <div class="section-title"><h2>全网找一找</h2></div>
          <div class="hot">
            <span v-for="t in globalHotWords" :key="t" class="hot-tag pressable" @click="quickSearch(t)">
              {{ t }}
            </span>
          </div>
          <p class="hint">
            点开结果会<b>自动转存</b>到你家的网盘并立刻播放，边下边播、可以拖进度。
          </p>
        </template>

        <div v-else-if="globalLoading" class="loading"><div class="spinner"></div></div>

        <template v-else-if="globalResults.length">
          <div class="section-title"><h2>全网找到 {{ globalTotal }} 个</h2></div>
          <div
            v-for="(r, i) in globalResults"
            :key="i"
            class="g-card pressable pop-in"
            :class="{ off: !isSupported(r) }"
            :style="{ animationDelay: i * 35 + 'ms' }"
            @click="playGlobal(r)"
          >
            <div class="g-cover" :style="{ background: tintOf(r.diskType) }">
              {{ diskEmoji(r.diskType) }}
            </div>
            <div class="g-body">
              <div class="g-title ellipsis-2">{{ titleOf(r) }}</div>
              <div class="g-meta">
                <span class="g-chip" :style="{ color: tintOf(r.diskType), background: tintBg(r.diskType) }">
                  {{ diskName(r.diskType) }}
                </span>
                <span v-if="r.alive" class="g-chip" :class="aliveClass(r.alive)">
                  {{ aliveText(r.alive) }}
                </span>
                <span v-if="r.source" class="g-chip plain">{{ r.source }}</span>
                <span v-if="r.datetime" class="g-chip plain">{{ shortDate(r.datetime) }}</span>
                <span v-if="!isSupported(r)" class="g-chip plain">未绑定该网盘</span>
              </div>
            </div>
            <div class="g-go">{{ isSupported(r) ? '▶' : '✕' }}</div>
          </div>
        </template>

        <div v-else class="empty">
          <span class="emoji">🌐</span>
          <p>全网没有找到「{{ keyword }}」</p>
          <p class="text-sub">换个词试试看吧</p>
        </div>
      </template>
    </div>

    <!-- 转存中遮罩 -->
    <div v-if="preparing" class="prep-mask">
      <div class="prep-box">
        <div class="spinner big"></div>
        <p class="prep-title">正在转存并准备播放…</p>
        <p class="prep-sub">{{ preparingText }}</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { showToast } from 'vant'
import SearchBar from '@/components/SearchBar.vue'
import ResourceCard from '@/components/ResourceCard.vue'
import { resourceApi, searchApi, instantApi } from '@/api'

const route = useRoute()
const router = useRouter()

const sb = ref(null)
const keyword = ref('')
const scope = ref('system') // system | global

// —— 搜系统 ——
const searched = ref(false)
const loading = ref(false)
const results = ref([])
const total = ref(0)
const allResources = ref([])
// 网盘筛选：activeDisk 为空 = 全部；diskFacets 是"当前结果里真实存在的网盘"
const activeDisk = ref('')
const diskFacets = ref([])

// —— 搜全网 ——
const globalSearched = ref(false)
const globalLoading = ref(false)
const globalResults = ref([])
const globalTotal = ref(0)
const globalReady = ref(true)
// 已绑定账号的网盘类型；结果里不在其中的，点了必然失败，提前标灰
const availableTypes = ref([])

// —— 转存中 ——
const preparing = ref(false)
const preparingText = ref('')

const hotWords = ['英语儿歌', '成语故事', '唐诗', '睡前故事', '奥数']
const globalHotWords = ['小猪佩奇', '海底小纵队', '汪汪队', '西游记动画', '恐龙纪录片']

const barStyle = ref({ paddingTop: 'calc(12px + var(--safe-top))' })

const DISK_META = {
  quark: { name: '夸克', emoji: '⚡', color: '#5B8FF9' },
  aliyun: { name: '阿里云盘', emoji: '☁️', color: '#FF9D4D' },
  uc: { name: 'UC', emoji: '🅤', color: '#269A99' },
  tianyi: { name: '天翼', emoji: '📡', color: '#7F8C9A' },
  baidu: { name: '百度', emoji: '🐾', color: '#F08BB4' },
  webdav: { name: 'WebDAV', emoji: '🗄️', color: '#7F8C9A' },
  local: { name: '本地硬盘', emoji: '💾', color: '#7F8C9A' },
}

function diskName(t) {
  return (DISK_META[t] || {}).name || t || '网盘'
}
function diskEmoji(t) {
  return (DISK_META[t] || {}).emoji || '📦'
}
function tintOf(t) {
  return (DISK_META[t] || {}).color || '#5B8FF9'
}
function tintBg(t) {
  const c = tintOf(t)
  return c + '22'
}
function titleOf(r) {
  const t = (r.note || '').trim()
  if (t) return t.replace(/<[^>]+>/g, '').slice(0, 60)
  return `${diskName(r.diskType)}分享`
}

// 有效性状态（由后端实时调用 PanCheck 得到）
function aliveText(s) {
  return { ok: '有效', bad: '已失效', locked: '需密码', unsupported: '不支持', uncertain: '未检出' }[s] || s
}
function aliveClass(s) {
  return { ok: 'alive-ok', bad: 'alive-bad', locked: 'alive-warn', uncertain: 'alive-unk' }[s] || 'alive-unk'
}
function shortDate(s) {
  if (!s) return ''
  const d = new Date(s)
  if (isNaN(d.getTime())) return String(s).slice(0, 10)
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

function switchScope(s) {
  if (scope.value === s) return
  scope.value = s
  if (keyword.value.trim()) doSearch(keyword.value)
}
// isSupported 该结果对应的网盘类型是否已绑定账号。
// availableTypes 为空（老后端 / 未返回）时一律视为可点，避免误伤。
function isSupported(r) {
  if (!availableTypes.value.length) return true
  return availableTypes.value.includes(r.diskType)
}

async function doSearch(v = keyword.value) {
  keyword.value = v
  const q = (v || '').trim()
  if (scope.value === 'global') return doGlobalSearch(q)
  return doSystemSearch(q)
}

async function doSystemSearch(q) {
  if (!q) {
    searched.value = false
    results.value = []
    diskFacets.value = []
    return
  }
  searched.value = true
  loading.value = true
  try {
    const params = { keyword: q, page: 1, pageSize: 30 }
    if (activeDisk.value) params.diskType = activeDisk.value
    const data = await resourceApi.list(params)
    const list = data?.list || []
    results.value = list
    total.value = data?.total ?? list.length
    // 筛选标签跟搜索结果同步刷新：没有结果的网盘不该继续占位置
    refreshFacets(q)
  } catch {
    results.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// refreshFacets 拉一次"结果里有哪些网盘"。
// 不传 diskType —— 标签要反映全部可选网盘，否则一旦筛了某一种，
// 其他网盘的入口就消失了，孩子再也切不回去。
async function refreshFacets(q) {
  try {
    const data = await resourceApi.facets({ keyword: q })
    diskFacets.value = data?.diskTypes || []
    // 当前筛选的网盘在新结果里不存在了 → 自动回到"全部"
    if (activeDisk.value && !diskFacets.value.some((f) => f.diskType === activeDisk.value)) {
      activeDisk.value = ''
    }
  } catch {
    diskFacets.value = []
  }
}

function pickDisk(t) {
  if (activeDisk.value === t) return
  activeDisk.value = t
  if (keyword.value.trim()) doSystemSearch(keyword.value)
}

async function doGlobalSearch(q) {
  if (!q) {
    globalSearched.value = false
    globalResults.value = []
    return
  }
  globalSearched.value = true
  globalLoading.value = true
  try {
    const data = await searchApi.global({ keyword: q, page: 1, pageSize: 30 })
    globalResults.value = data?.list || []
    globalTotal.value = data?.total ?? globalResults.value.length
    globalReady.value = data?.instantReady !== false
    availableTypes.value = data?.availableDiskTypes || []
  } catch {
    globalResults.value = []
    globalTotal.value = 0
  } finally {
    globalLoading.value = false
  }
}

function quickSearch(t) {
  keyword.value = t
  doSearch(t)
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

// playGlobal 点结果 → 即时转存 → 立刻播放
async function playGlobal(r) {
  if (!globalReady.value) {
    showToast('还没配置默认转存网盘，请让家长在后台设置')
    return
  }
  if (!isSupported(r)) {
    showToast(`还没绑定${diskName(r.diskType)}账号，请让家长在「资源接入」里添加`)
    return
  }
  if (preparing.value) return
  preparing.value = true
  preparingText.value = `${diskName(r.diskType)} · 正在转存…`
  try {
    const res = await instantApi.play({
      shareUrl: r.url,
      passcode: r.password || '',
      title: titleOf(r),
      diskType: r.diskType,
      note: r.note || '',
    })
    preparingText.value = res.fileCount > 1 ? `共 ${res.fileCount} 个文件，开始播放…` : '开始播放…'
    router.push({ name: 'play', params: { id: res.resourceId } })
  } catch (e) {
    // 拦截器已经 toast 过错误信息
  } finally {
    preparing.value = false
  }
}

onMounted(async () => {
  // 从首页搜索框带词进来
  const q = route.query.q
  if (typeof q === 'string' && q) {
    keyword.value = q
    doSearch(q)
  } else {
    nextTick(() => sb.value?.focus())
    try {
      const data = await resourceApi.list({ page: 1, pageSize: 8 })
      allResources.value = data?.list || []
    } catch {
      allResources.value = []
    }
  }
})
</script>

<style scoped>
.bar {
  position: sticky;
  top: 0;
  z-index: 50;
  padding: 0 16px 12px;
  background: linear-gradient(180deg, #e8f0ff 0%, var(--c-bg) 100%);
}

/* 搜索范围切换 */
.scope {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}
.scope-btn {
  flex: 1;
  height: 38px;
  border: 0;
  border-radius: 999px;
  background: #fff;
  color: var(--c-text-sub, #6b7c8f);
  font-size: 14.5px;
  font-weight: 600;
  font-family: inherit;
  box-shadow: var(--shadow-sm);
  transition: all 0.18s ease;
}
.scope-btn.on {
  background: linear-gradient(135deg, #5b8ff9, #4a7ef0);
  color: #fff;
  box-shadow: 0 4px 14px rgba(91, 143, 249, 0.35);
}

/* 网盘筛选：横向可滚动，避免网盘多时挤成两行把搜索栏推走 */
.disk-filter {
  display: flex;
  gap: 8px;
  margin-top: 10px;
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  scrollbar-width: none;
  padding-bottom: 2px;
}
.disk-filter::-webkit-scrollbar {
  display: none;
}
.df-chip {
  flex: 0 0 auto;
  padding: 5px 12px;
  border-radius: 999px;
  background: #fff;
  color: var(--c-text-sub, #6b7c8f);
  font-size: 13px;
  border: 1px solid rgba(15, 40, 80, 0.08);
  white-space: nowrap;
}
.df-chip.on {
  background: rgba(91, 143, 249, 0.12);
  color: #4a7ef0;
  border-color: rgba(91, 143, 249, 0.4);
  font-weight: 600;
}

/* 全网结果的有效性标（实时由 PanCheck 检出） */
.alive-ok {
  color: #12a15b;
  background: rgba(18, 161, 91, 0.12);
}
.alive-bad {
  color: #e04b4b;
  background: rgba(224, 75, 75, 0.12);
}
.alive-warn {
  color: #d68a1a;
  background: rgba(214, 138, 26, 0.14);
}
.alive-unk {
  color: #7f8c9a;
  background: rgba(127, 140, 154, 0.12);
}

.hot {
  display: flex;
  flex-wrap: wrap;
  gap: 9px;
  padding-bottom: 4px;
}
.hot-tag {
  padding: 8px 16px;
  background: #fff;
  border-radius: 999px;
  font-size: 14px;
  color: var(--c-text);
  box-shadow: var(--shadow-sm);
}

.hint {
  margin-top: 16px;
  font-size: 13px;
  line-height: 1.7;
  color: var(--c-text-sub, #6b7c8f);
}

/* 全网结果卡片 */
.g-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px;
  background: var(--c-card, #fff);
  border-radius: var(--r-md, 14px);
  box-shadow: var(--shadow-sm);
  margin-bottom: 10px;
}
.g-card.off {
  opacity: 0.55;
}
.g-card.off .g-go {
  background: #cfd8e3;
  box-shadow: none;
}
.g-cover {
  flex: 0 0 auto;
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
}
.g-body {
  flex: 1;
  min-width: 0;
}
.g-title {
  font-size: 14.5px;
  font-weight: 600;
  color: var(--c-text);
  line-height: 1.4;
}
.g-meta {
  display: flex;
  gap: 6px;
  margin-top: 6px;
  flex-wrap: wrap;
}
.g-chip {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 999px;
  font-weight: 600;
}
.g-chip.plain {
  background: #f3f6fa;
  color: var(--c-text-sub, #6b7c8f);
  font-weight: 400;
}
.g-go {
  flex: 0 0 auto;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  background: linear-gradient(135deg, #5b8ff9, #4a7ef0);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
}

.notice {
  text-align: center;
  padding: 32px 16px;
}
.notice .emoji {
  font-size: 44px;
}

/* 转存遮罩 */
.prep-mask {
  position: fixed;
  inset: 0;
  z-index: 999;
  background: rgba(20, 30, 45, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  backdrop-filter: blur(2px);
}
.prep-box {
  width: 240px;
  padding: 26px 20px;
  background: #fff;
  border-radius: 18px;
  text-align: center;
}
.prep-title {
  margin-top: 14px;
  font-size: 15px;
  font-weight: 600;
  color: var(--c-text);
}
.prep-sub {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--c-text-sub, #6b7c8f);
}
.spinner.big {
  width: 34px;
  height: 34px;
  margin: 0 auto;
  border-width: 3px;
}
</style>
