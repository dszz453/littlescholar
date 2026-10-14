<template>
  <div class="page search-page">
    <header class="bar" :style="barStyle">
      <SearchBar
        ref="sb"
        v-model="keyword"
        placeholder="搜成语故事、英语儿歌…"
        @search="doSearch"
      />

      <!-- 网盘筛选：只列出结果里真实存在的网盘 -->
      <div v-if="diskFacets.length > 1" class="disk-filter">
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
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import SearchBar from '@/components/SearchBar.vue'
import ResourceCard from '@/components/ResourceCard.vue'
import { resourceApi } from '@/api'

const route = useRoute()
const router = useRouter()

const sb = ref(null)
const keyword = ref('')

const searched = ref(false)
const loading = ref(false)
const results = ref([])
const total = ref(0)
const allResources = ref([])
// 网盘筛选：activeDisk 为空 = 全部；diskFacets 是"当前结果里真实存在的网盘"
const activeDisk = ref('')
const diskFacets = ref([])

const hotWords = ['英语儿歌', '成语故事', '唐诗', '睡前故事', '奥数']

const barStyle = ref({ paddingTop: 'calc(12px + var(--safe-top))' })

async function doSearch(v = keyword.value) {
  keyword.value = v
  await doSystemSearch((v || '').trim())
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
</style>
