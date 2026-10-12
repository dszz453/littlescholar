<template>
  <div class="page home">
    <!-- 顶部：问候 + 大搜索框 -->
    <header class="hero" :style="heroStyle">
      <div class="greet">
        <div class="hi">{{ greeting }}，{{ userStore.nickname }} 👋</div>
        <div v-if="userStore.grade" class="grade">
          {{ userStore.grade }} · 今天也要加油哦
        </div>
      </div>

      <SearchBar v-model="keyword" @search="onSearch" />
    </header>

    <div class="page-pad">
      <!-- 继续学习：有历史时显示 -->
      <section v-if="recent.length" class="recent-wrap">
        <div class="section-title">
          <h2>接着上次看</h2>
          <span class="more" @click="goHistory">全部 ›</span>
        </div>

        <div class="recent-scroll">
          <div
            v-for="r in recent"
            :key="r.id"
            class="recent-card pressable"
            @click="goPlay(r)"
          >
            <span class="r-emoji">{{ recentEmoji(r) }}</span>
            <div class="r-title ellipsis-2">{{ r.title }}</div>
            <div class="r-progress" v-if="r.percent > 0">
              <div class="bar" :style="{ width: r.percent + '%' }"></div>
            </div>
          </div>
        </div>
      </section>

      <!-- 主导航：一级图标网格 -->
      <div class="section-title">
        <h2>我的学习乐园</h2>
      </div>

      <div v-if="loading" class="loading"><div class="spinner"></div></div>

      <div v-else-if="!roots.length" class="empty">
        <span class="emoji">🌱</span>
        <p>还没有可以学习的内容</p>
        <p class="text-sub">请让爸爸妈妈在后台添加哦</p>
      </div>

      <!-- 一级图标：2 列大卡片 -->
      <div v-else class="grid">
        <div
          v-for="(node, i) in roots"
          :key="node.id"
          class="tile pressable"
          :style="{ ...tileStyle(node), animationDelay: i * 45 + 'ms' }"
          @click="goCategory(node)"
        >
          <div class="tile-icon" :style="{ background: tint(iconOf(node).color, 0.2) }">
            <span class="tile-emoji">{{ iconOf(node).emoji }}</span>
          </div>
          <div class="tile-name ellipsis-2">{{ node.name }}</div>
          <div v-if="countOf(node)" class="tile-count">{{ countOf(node) }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import SearchBar from '@/components/SearchBar.vue'
import { useUserStore } from '@/stores/user'
import { playApi } from '@/api'
import { resolveIcon, tint } from '@/assets/icons'

const router = useRouter()
const userStore = useUserStore()

const keyword = ref('')
const recent = ref([])
const loading = ref(true)

const roots = computed(() => userStore.tree || [])

const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 6) return '夜深了'
  if (h < 11) return '早上好'
  if (h < 14) return '中午好'
  if (h < 18) return '下午好'
  return '晚上好'
})

const heroStyle = computed(() => ({
  paddingTop: 'calc(20px + var(--safe-top))',
}))

function iconOf(node) {
  return resolveIcon(node.icon, node.color)
}

function tileStyle(node) {
  return {
    background: `linear-gradient(145deg, ${tint(iconOf(node).color, 0.14)}, #fff)`,
  }
}

/** 显示子分类数或资源数 */
function countOf(node) {
  const childCount = node.children?.length || 0
  const resCount = node.resourceCount || 0
  if (childCount > 0) return `${childCount} 个分类`
  if (resCount > 0) return `${resCount} 个内容`
  return ''
}

function recentEmoji(r) {
  const map = { audio: '🎧', video: '🎬', pdf: '📄', image: '🖼️' }
  return map[r.mediaType] || '📚'
}

function onSearch(v) {
  if (!v) return
  router.push({ name: 'search', query: { q: v } })
}

function goCategory(node) {
  // 有子级 → 进目录页；无子级但有资源 → 进目录页看资源列表
  router.push({ name: 'category', params: { id: node.id } })
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

function goHistory() {
  router.push({ name: 'me', query: { tab: 'history' } })
}

onMounted(async () => {
  try {
    await userStore.loadTree()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }

  // 最近播放，失败静默（新用户没有历史）
  try {
    const data = await playApi.history({ page: 1, pageSize: 6 })
    recent.value = (data?.list || data || []).slice(0, 6)
  } catch {
    recent.value = []
  }
})
</script>

<style scoped>
.hero {
  padding: 0 16px 20px;
  background: linear-gradient(180deg, #e8f0ff 0%, var(--c-bg) 100%);
}

.greet {
  margin-bottom: 16px;
}
.hi {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: 0.3px;
}
.grade {
  margin-top: 3px;
  font-size: 13.5px;
  color: var(--c-text-sub);
}

/* ===== 继续学习（横向滚动） ===== */

.recent-wrap {
  margin: 0 -16px;
}
.recent-scroll {
  display: flex;
  gap: 11px;
  overflow-x: auto;
  padding: 2px 16px 6px;
  scrollbar-width: none;
}
.recent-scroll::-webkit-scrollbar {
  display: none;
}

.recent-card {
  flex: 0 0 auto;
  width: 128px;
  padding: 13px;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
}
.r-emoji {
  font-size: 25px;
  line-height: 1;
}
.r-title {
  margin-top: 8px;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.35;
  min-height: 36px;
}
.r-progress {
  margin-top: 8px;
  height: 4px;
  background: #eef2f7;
  border-radius: 999px;
  overflow: hidden;
}
.bar {
  height: 100%;
  background: var(--c-primary);
  border-radius: 999px;
  transition: width 0.3s ease;
}

/* ===== 一级图标网格 ===== */

.grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}

.tile {
  position: relative;
  padding: 18px 14px 16px;
  border-radius: var(--r-lg);
  box-shadow: var(--shadow-sm);
  text-align: center;
  border: 1px solid rgba(255, 255, 255, 0.9);
}

.tile-icon {
  width: 62px;
  height: 62px;
  margin: 0 auto 11px;
  border-radius: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.tile-emoji {
  font-size: 33px;
  line-height: 1;
}

.tile-name {
  font-size: 15.5px;
  font-weight: 700;
  color: var(--c-text);
  line-height: 1.35;
  min-height: 42px;
}

.tile-count {
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--c-text-sub);
}
</style>
