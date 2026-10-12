<template>
  <div class="page me-page">
    <!-- 个人卡片 -->
    <header class="profile" :style="headerStyle">
      <div class="p-card">
        <span class="p-avatar">{{ userStore.avatar || '🐻' }}</span>
        <div class="p-info">
          <div class="p-name">{{ userStore.nickname }}</div>
          <div v-if="userStore.grade" class="p-grade">{{ userStore.grade }}</div>
        </div>
      </div>

      <!-- 学习统计 -->
      <div class="stats">
        <div class="stat">
          <div class="num">{{ todayMinutes }}</div>
          <div class="lab">今日分钟</div>
        </div>
        <div class="divider"></div>
        <div class="stat">
          <div class="num">{{ weekMinutes }}</div>
          <div class="lab">本周分钟</div>
        </div>
        <div class="divider"></div>
        <div class="stat">
          <div class="num">{{ finishedCount }}</div>
          <div class="lab">已完成</div>
        </div>
      </div>
    </header>

    <div class="page-pad">
      <!-- 标签切换 -->
      <div class="tabs">
        <button
          v-for="t in tabs"
          :key="t.key"
          class="tab pressable"
          :class="{ on: tab === t.key }"
          @click="tab = t.key"
        >{{ t.label }}</button>
      </div>

      <div v-if="loading" class="loading"><div class="spinner"></div></div>

      <!-- 播放历史 -->
      <template v-else-if="tab === 'history'">
        <template v-if="history.length">
          <ResourceCard
            v-for="r in history"
            :key="r.id"
            :node="r"
            class="pop-in"
            @click="goPlay(r)"
          />
        </template>
        <div v-else class="empty">
          <span class="emoji">🎧</span>
          <p>还没有听过看过内容</p>
          <p class="text-sub">去乐园里挑一个吧</p>
        </div>
      </template>

      <!-- 学习记录 -->
      <template v-else>
        <template v-if="records.length">
          <div v-for="r in records" :key="r.id" class="rec-item">
            <span class="r-ico">{{ recEmoji(r) }}</span>
            <div class="r-body">
              <div class="r-title ellipsis">{{ r.title || r.resourceTitle }}</div>
              <div class="r-meta">
                <span>{{ fmtTime(r.happenedAt || r.createdAt) }}</span>
                <span v-if="r.duration">· {{ Math.round(r.duration / 60) }} 分钟</span>
              </div>
            </div>
          </div>
        </template>
        <div v-else class="empty">
          <span class="emoji">📖</span>
          <p>还没有学习记录</p>
        </div>
      </template>

      <!-- 退出 -->
      <button class="logout pressable" @click="doLogout">退出登录</button>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ResourceCard from '@/components/ResourceCard.vue'
import { useUserStore } from '@/stores/user'
import { playApi, studyApi } from '@/api'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const tab = ref('history')
const loading = ref(false)
const history = ref([])
const records = ref([])
const stats = ref(null)

const tabs = [
  { key: 'history', label: '最近听过' },
  { key: 'records', label: '学习记录' },
]

const headerStyle = computed(() => ({
  paddingTop: 'calc(24px + var(--safe-top))',
}))

const todayMinutes = computed(() => stats.value?.todayMinutes ?? 0)
const weekMinutes = computed(() => stats.value?.weekMinutes ?? 0)
const finishedCount = computed(() => stats.value?.finishedCount ?? 0)

function recEmoji(r) {
  const map = { audio: '🎧', video: '🎬', pdf: '📄', image: '🖼️' }
  return map[r.mediaType] || '📚'
}

function fmtTime(t) {
  if (!t) return ''
  const d = new Date(t)
  const now = new Date()
  const diff = (now - d) / 1000
  if (diff < 60) return '刚刚'
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 604800) return `${Math.floor(diff / 86400)} 天前`
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

async function load() {
  loading.value = true
  try {
    if (tab.value === 'history') {
      const data = await playApi.history({ page: 1, pageSize: 30 })
      history.value = data?.list || data || []
    } else {
      const data = await studyApi.overview({ page: 1, pageSize: 30 })
      records.value = data?.records || data?.list || []
    }
  } catch {
    if (tab.value === 'history') history.value = []
    else records.value = []
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  try {
    const data = await studyApi.overview({})
    stats.value = {
      todayMinutes: Math.round((data?.todayDuration || 0) / 60),
      weekMinutes: Math.round((data?.weekDuration || 0) / 60),
      finishedCount: data?.finishedCount || 0,
    }
  } catch {
    stats.value = null
  }
}

async function doLogout() {
  await userStore.logout()
  router.replace({ name: 'login' })
}

function goPlay(r) {
  router.push({ name: 'play', params: { id: r.id } })
}

onMounted(() => {
  // 支持从首页"全部"跳过来直接定位到历史 tab
  if (route.query.tab === 'history') tab.value = 'history'
  loadStats()
  load()
})

watch(tab, load)
</script>

<style scoped>
.profile {
  padding: 0 16px 20px;
  background: linear-gradient(180deg, #e8f0ff 0%, var(--c-bg) 100%);
}

.p-card {
  display: flex;
  align-items: center;
  gap: 13px;
  padding: 16px;
  background: #fff;
  border-radius: var(--r-lg);
  box-shadow: var(--shadow-sm);
  margin-bottom: 14px;
}
.p-avatar {
  width: 56px;
  height: 56px;
  border-radius: 50%;
  background: #f0f5ff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 30px;
}
.p-name {
  font-size: 18px;
  font-weight: 700;
}
.p-grade {
  margin-top: 3px;
  font-size: 13px;
  color: var(--c-text-sub);
}

.stats {
  display: flex;
  align-items: center;
  padding: 16px 8px;
  background: #fff;
  border-radius: var(--r-lg);
  box-shadow: var(--shadow-sm);
}
.stat {
  flex: 1;
  text-align: center;
}
.stat .num {
  font-size: 22px;
  font-weight: 800;
  color: var(--c-primary);
  line-height: 1.1;
}
.stat .lab {
  margin-top: 4px;
  font-size: 12px;
  color: var(--c-text-sub);
}
.divider {
  width: 1px;
  height: 28px;
  background: var(--c-border);
}

/* ===== 标签 ===== */

.tabs {
  display: flex;
  gap: 9px;
  padding: 20px 0 14px;
}
.tab {
  flex: 1;
  padding: 10px;
  border: 0;
  border-radius: var(--r-sm);
  background: #fff;
  color: var(--c-text-sub);
  font-size: 14.5px;
  font-weight: 600;
  font-family: inherit;
  box-shadow: var(--shadow-sm);
}
.tab.on {
  background: var(--c-primary);
  color: #fff;
}

/* ===== 学习记录项 ===== */

.rec-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 13px 15px;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  margin-bottom: 9px;
}
.r-ico {
  font-size: 22px;
  line-height: 1;
}
.r-body {
  flex: 1;
  min-width: 0;
}
.r-title {
  font-size: 15px;
  font-weight: 600;
}
.r-meta {
  margin-top: 3px;
  font-size: 12.5px;
  color: var(--c-text-sub);
}

/* ===== 退出 ===== */

.logout {
  width: 100%;
  margin: 28px 0 12px;
  padding: 14px;
  border: 0;
  border-radius: var(--r-md);
  background: #fff;
  color: #d9554a;
  font-size: 15.5px;
  font-weight: 600;
  font-family: inherit;
  box-shadow: var(--shadow-sm);
}
</style>
