<template>
  <div class="page">
    <!-- 顶部：返回 + 面包屑 -->
    <header class="bar" :style="barStyle">
      <div class="bar-row">
        <button class="back pressable" @click="goBack">‹</button>
        <div class="crumb">
          <span class="crumb-item" @click="goRoot">首页</span>
          <template v-for="c in crumbs" :key="c.id">
            <span class="sep">›</span>
            <span
              class="crumb-item"
              :class="{ current: c.id === node?.id }"
              @click="goCrumb(c)"
            >{{ c.name }}</span>
          </template>
        </div>
        <span class="ico-lg">{{ icon.emoji }}</span>
      </div>
    </header>

    <div class="page-pad">
      <!-- 子目录 -->
      <template v-if="children.length">
        <div class="section-title"><h2>分类</h2></div>
        <CategoryCard
          v-for="(c, i) in children"
          :key="c.id"
          :node="c"
          class="pop-in"
          :style="{ animationDelay: i * 40 + 'ms' }"
          @click="goCategory(c)"
        />
      </template>

      <!-- 资源列表 -->
      <template v-if="resources.length">
        <div class="section-title">
          <h2>{{ children.length ? '内容' : '全部内容' }}</h2>
          <span v-if="total > resources.length" class="more">
            共 {{ total }} 个
          </span>
        </div>

        <ResourceCard
          v-for="(r, i) in resources"
          :key="r.id"
          :node="r"
          class="pop-in"
          :style="{ animationDelay: i * 40 + 'ms' }"
          @click="goPlay(r)"
        />

        <button v-if="hasMore" class="more-btn pressable" @click="loadMore">
          {{ loadingMore ? '加载中…' : '看更多' }}
        </button>
      </template>

      <!-- 加载 / 空态 -->
      <div v-if="loading" class="loading"><div class="spinner"></div></div>

      <div v-else-if="!children.length && !resources.length" class="empty">
        <span class="emoji">🍃</span>
        <p>这个分类还是空的</p>
        <p class="text-sub">等爸爸妈妈放点内容进来吧</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import CategoryCard from '@/components/CategoryCard.vue'
import ResourceCard from '@/components/ResourceCard.vue'
import { useUserStore } from '@/stores/user'
import { resourceApi } from '@/api'
import { resolveIcon } from '@/assets/icons'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const loading = ref(true)
const loadingMore = ref(false)
const resources = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = 20

const nodeId = computed(() => route.params.id)

const node = computed(() => userStore.findNode(nodeId.value))

const children = computed(() => node.value?.children || [])

const crumbs = computed(() => userStore.pathOf(nodeId.value) || [])

const icon = computed(() => resolveIcon(node.value?.icon, node.value?.color))

const hasMore = computed(() => resources.value.length < total.value)

const barStyle = computed(() => ({
  paddingTop: 'calc(8px + var(--safe-top))',
}))

async function fetchResources(reset = true) {
  if (reset) {
    page.value = 1
    resources.value = []
  }
  try {
    const data = await resourceApi.list({
      categoryId: nodeId.value,
      page: page.value,
      pageSize,
      // 直接列出该目录（含子目录）的资源
      includeChildren: true,
    })
    const list = data?.list || []
    total.value = data?.total ?? list.length
    resources.value = reset ? list : [...resources.value, ...list]
  } catch {
    if (reset) resources.value = []
  }
}

async function load() {
  loading.value = true
  try {
    await userStore.loadTree()
    await fetchResources(true)
  } finally {
    loading.value = false
  }
}

async function loadMore() {
  if (loadingMore.value) return
  loadingMore.value = true
  page.value += 1
  try {
    await fetchResources(false)
  } finally {
    loadingMore.value = false
  }
}

function goBack() {
  if (window.history.length > 1) router.back()
  else router.push({ name: 'home' })
}

function goRoot() {
  router.push({ name: 'home' })
}

function goCrumb(c) {
  if (c.id === node.value?.id) return
  router.push({ name: 'category', params: { id: c.id } })
}

function goCategory(c) {
  router.push({ name: 'category', params: { id: c.id } })
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

onMounted(load)
watch(nodeId, load)
</script>

<style scoped>
.bar {
  position: sticky;
  top: 0;
  z-index: 50;
  background: rgba(247, 249, 252, 0.94);
  backdrop-filter: blur(12px);
  padding-bottom: 8px;
}

.bar-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 16px;
}

.back {
  flex: 0 0 auto;
  width: 36px;
  height: 36px;
  border: 0;
  border-radius: 50%;
  background: #fff;
  box-shadow: var(--shadow-sm);
  font-size: 24px;
  line-height: 1;
  color: var(--c-text);
  display: flex;
  align-items: center;
  justify-content: center;
  padding-bottom: 3px;
}

.crumb {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 6px;
  overflow-x: auto;
  scrollbar-width: none;
  font-size: 14.5px;
}
.crumb::-webkit-scrollbar {
  display: none;
}
.crumb-item {
  flex: 0 0 auto;
  color: var(--c-text-sub);
  white-space: nowrap;
}
.crumb-item.current {
  color: var(--c-text);
  font-weight: 700;
}
.sep {
  flex: 0 0 auto;
  color: #c8d3e0;
}

.ico-lg {
  flex: 0 0 auto;
  font-size: 22px;
  line-height: 1;
}

.more-btn {
  width: 100%;
  margin: 6px 0 12px;
  padding: 13px;
  border: 0;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
  color: var(--c-primary);
  font-size: 15px;
  font-weight: 600;
  font-family: inherit;
}
</style>
