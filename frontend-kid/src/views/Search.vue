<template>
  <div class="page search-page">
    <header class="bar" :style="barStyle">
      <SearchBar
        ref="sb"
        v-model="keyword"
        placeholder="搜文件夹、成语故事、英语儿歌…"
        @search="doSearch"
      />

      <!-- 网盘筛选：只列出结果里真实存在的网盘 -->
      <div v-if="searched && diskFacets.length > 1" class="disk-filter">
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
      <!-- 未搜索：热门词 + 随便看看 -->
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

      <SkeletonList v-else-if="loading" :count="5" />

      <div v-else-if="failed" class="error-box">
        <span class="emoji">😵</span>
        <p>搜索没能完成，可能是网络不太顺</p>
        <button class="retry pressable" @click="doSearch()">再试一次</button>
      </div>

      <template v-else-if="categories.length || results.length">
        <!-- 命中的「文件夹」——可以点进去，这是这次搜索最大的变化：
             以前只能搜到具体的文件，孩子想"看看这个专题里都有啥"是做不到的。 -->
        <template v-if="categories.length">
          <div class="section-title">
            <h2>文件夹 · {{ categories.length }}</h2>
          </div>
          <div
            v-for="(c, i) in categories"
            :key="'c' + c.id"
            class="dir-card pressable pop-in"
            :style="{ animationDelay: i * 30 + 'ms' }"
            @click="goCategory(c)"
          >
            <span class="dir-ico" :style="{ background: tint(dirColor(c), 0.18) }">
              {{ dirEmoji(c) }}
            </span>
            <div class="dir-body">
              <div class="dir-name ellipsis">{{ c.name }}</div>
              <div v-if="c.breadcrumb" class="dir-crumb ellipsis">{{ c.breadcrumb }}</div>
            </div>
            <span class="dir-count">{{ c.resourceCount }} 个</span>
            <span class="dir-arrow">›</span>
          </div>
        </template>

        <!-- 命中的「内容」 -->
        <template v-if="results.length">
          <div class="section-title">
            <h2>内容 · {{ total }}</h2>
          </div>
          <ResourceCard
            v-for="(r, i) in results"
            :key="r.id"
            :node="r"
            show-category
            class="pop-in"
            :style="{ animationDelay: i * 30 + 'ms' }"
            @click="goPlay(r)"
          />
          <div v-if="results.length < total" class="more">
            <span class="more-btn pressable" :class="{ dim: loadingMore }" @click="loadMore">
              {{ loadingMore ? '正在加载…' : '加载更多' }}
            </span>
          </div>
        </template>
      </template>

      <div v-else class="empty">
        <span class="emoji">🔍</span>
        <p>没有找到「{{ keyword }}」</p>
        <p class="text-sub">
          {{ activeDisk ? '换个网盘或点「全部」试试看吧' : '换个词试试看吧' }}
        </p>
      </div>
    </div>

    <BackTop />
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import SearchBar from '@/components/SearchBar.vue'
import ResourceCard from '@/components/ResourceCard.vue'
import SkeletonList from '@/components/SkeletonList.vue'
import BackTop from '@/components/BackTop.vue'
import { resourceApi, searchApi } from '@/api'
import { resolveIcon, tint } from '@/assets/icons'

// 一页资源条数。目录不限页（后端给 50 上限），孩子要的是"往里走"的入口。
const PAGE_SIZE = 20

const route = useRoute()
const router = useRouter()

const sb = ref(null)
const keyword = ref('')

const searched = ref(false)
const loading = ref(false)
const failed = ref(false)
const loadingMore = ref(false)
const categories = ref([])
const results = ref([])
const total = ref(0)
const page = ref(1)
const allResources = ref([])

// 网盘筛选：activeDisk 为空 = 全部；diskFacets 是"当前结果里真实存在的网盘"
const activeDisk = ref('')
const diskFacets = ref([])

const hotWords = ['英语儿歌', '成语故事', '唐诗', '睡前故事', '奥数']

const barStyle = ref({ paddingTop: 'calc(12px + var(--safe-top))' })

function dirEmoji(c) {
  return resolveIcon(c.icon, c.color).emoji
}
function dirColor(c) {
  return c.color || resolveIcon(c.icon).color
}

async function doSearch(v = keyword.value) {
  keyword.value = v
  await doSystemSearch((v || '').trim())
}

async function doSystemSearch(q) {
  if (!q) {
    reset()
    return
  }
  searched.value = true
  loading.value = true
  failed.value = false
  page.value = 1
  try {
    const data = await searchApi.all(searchParams(q, 1))
    categories.value = data?.categories || []
    results.value = data?.resources?.list || []
    total.value = data?.resources?.total ?? results.value.length
    // 筛选标签跟结果同步刷新：没有结果的网盘不该继续占位置
    refreshFacets(q)
  } catch {
    // 失败与"搜不到"必须分开：都显示成"没有找到"会让人以为库里真的没有
    categories.value = []
    results.value = []
    total.value = 0
    failed.value = true
  } finally {
    loading.value = false
  }
}

function searchParams(q, p) {
  const params = { keyword: q, page: p, pageSize: PAGE_SIZE }
  if (activeDisk.value) params.diskType = activeDisk.value
  return params
}

// loadMore 只追加资源 —— 目录是一次性给全的（上限 50），翻页没意义。
async function loadMore() {
  const q = keyword.value.trim()
  if (!q || loadingMore.value) return
  if (results.value.length >= total.value) return
  loadingMore.value = true
  try {
    const next = page.value + 1
    const data = await searchApi.all(searchParams(q, next))
    const list = data?.resources?.list || []
    if (list.length) {
      results.value = results.value.concat(list)
      page.value = next
    }
    total.value = data?.resources?.total ?? total.value
  } catch {
    /* 加载更多失败不打断已有结果，保持页面不动 */
  } finally {
    loadingMore.value = false
  }
}

function reset() {
  searched.value = false
  failed.value = false
  categories.value = []
  results.value = []
  total.value = 0
  diskFacets.value = []
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
  if (!keyword.value.trim()) return
  doSystemSearch(keyword.value)
}

function quickSearch(t) {
  keyword.value = t
  doSearch(t)
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

function goCategory(c) {
  router.push({ name: 'category', params: { id: c.id } })
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

/* ---- 目录结果卡片 ---- */
.dir-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  margin-bottom: 10px;
  background: var(--c-card);
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
}
.dir-ico {
  flex: 0 0 auto;
  width: 46px;
  height: 46px;
  border-radius: 14px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 24px;
  line-height: 1;
}
.dir-body {
  flex: 1;
  min-width: 0;
}
.dir-name {
  font-size: 15.5px;
  font-weight: 600;
  color: var(--c-text);
}
.dir-crumb {
  margin-top: 3px;
  font-size: 12px;
  color: var(--c-text-sub);
}
.dir-count {
  flex: 0 0 auto;
  font-size: 12px;
  color: var(--c-text-sub);
  background: #f3f6fa;
  padding: 3px 9px;
  border-radius: 999px;
}
.dir-arrow {
  flex: 0 0 auto;
  color: #b9c4d2;
  font-size: 20px;
  line-height: 1;
}

/* ---- 加载更多 ---- */
.more {
  display: flex;
  justify-content: center;
  padding: 6px 0 18px;
}
.more-btn {
  padding: 9px 26px;
  border-radius: 999px;
  background: #fff;
  color: #4a7ef0;
  font-size: 14px;
  font-weight: 600;
  box-shadow: var(--shadow-sm);
}
.more-btn.dim {
  opacity: 0.6;
}
</style>
