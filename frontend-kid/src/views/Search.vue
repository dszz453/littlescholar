<template>
  <div class="page search-page">
    <header class="bar" :style="barStyle">
      <SearchBar
        ref="sb"
        v-model="keyword"
        placeholder="搜成语故事、英语儿歌…"
        @search="doSearch"
      />
    </header>

    <div class="page-pad">
      <!-- 未搜索：推荐分类（快捷入口） -->
      <template v-if="!searched">
        <div class="section-title"><h2>试试这些</h2></div>
        <div class="hot">
          <span
            v-for="t in hotWords"
            :key="t"
            class="hot-tag pressable"
            @click="quickSearch(t)"
          >{{ t }}</span>
        </div>

        <div v-if="allResources.length" class="section-title">
          <h2>随便看看</h2>
        </div>
        <ResourceCard
          v-for="r in allResources"
          :key="r.id"
          :node="r"
          @click="goPlay(r)"
        />
      </template>

      <!-- 搜索中 -->
      <div v-else-if="loading" class="loading"><div class="spinner"></div></div>

      <!-- 有结果 -->
      <template v-else-if="results.length">
        <div class="section-title">
          <h2>找到 {{ total }} 个</h2>
        </div>
        <ResourceCard
          v-for="(r, i) in results"
          :key="r.id"
          :node="r"
          class="pop-in"
          :style="{ animationDelay: i * 35 + 'ms' }"
          @click="goPlay(r)"
        />
      </template>

      <!-- 无结果 -->
      <div v-else class="empty">
        <span class="emoji">🔍</span>
        <p>没有找到「{{ keyword }}」</p>
        <p class="text-sub">换个词试试看吧</p>
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

const hotWords = ['英语儿歌', '成语故事', '唐诗', '睡前故事', '奥数']

const barStyle = ref({ paddingTop: 'calc(12px + var(--safe-top))' })

async function doSearch(v = keyword.value) {
  keyword.value = v
  const q = (v || '').trim()
  if (!q) {
    searched.value = false
    results.value = []
    return
  }
  searched.value = true
  loading.value = true
  try {
    const data = await resourceApi.list({ keyword: q, page: 1, pageSize: 30 })
    const list = data?.list || []
    results.value = list
    total.value = data?.total ?? list.length
  } catch {
    results.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
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
    // 没带词则聚焦输入框 + 加载推荐
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
