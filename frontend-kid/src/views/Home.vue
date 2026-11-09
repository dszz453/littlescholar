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
      <!-- 接着上次听：一进首页就能续上，不用自己去翻目录找那一集。
           只在「真的播到一半」时出现（听完的、刚点开没播的不算）。 -->
      <div v-if="resumeCard" class="resume-strip pressable" @click="goResume">
        <span class="rs-ico">{{ resumeCard.emoji }}</span>
        <div class="rs-body">
          <div class="rs-title ellipsis">{{ resumeCard.title }}</div>
          <div class="rs-sub ellipsis">
            <template v-if="resumeCard.dir">{{ resumeCard.dir }} · </template>
            已听 {{ resumeCard.percent }}%
          </div>
          <div class="rs-bar"><i :style="{ width: resumeCard.percent + '%' }" /></div>
        </div>
        <span class="rs-go">继续 ›</span>
      </div>

      <!-- 今日任务：有学习计划时置顶，孩子一进首页就知道今天要做什么 -->
      <div v-if="planTask" class="plan-strip pressable" :class="{ done: planTask.done }" @click="goPlan">
        <span class="p-ico">{{ planTask.done ? '✅' : '🎯' }}</span>
        <div class="p-body">
          <div class="p-title">{{ planTask.title }}</div>
          <div class="p-sub">{{ planTask.sub }}</div>
        </div>
        <span class="p-go">{{ planTask.done ? '看看' : '去打卡' }} ›</span>
      </div>

      <!-- 小书童说：把这个目录读成人话，点进去就能看 -->
      <div v-if="digestCard" class="say-card pressable" @click="goDigestCategory">
        <div class="s-head">
          <span class="s-ava">🤖</span>
          <span class="s-name">小书童说</span>
          <span class="s-cat ellipsis">{{ digestCard.categoryName }}</span>
        </div>
        <p class="s-intro">{{ digestCard.intro }}</p>
      </div>

      <!-- 主导航：一级图标网格 -->
      <div class="section-title">
        <h2>我的学习乐园</h2>
      </div>

      <!-- 加载：用「和自己长得一样」的骨架屏，避免先转圈再突然铺满一屏 -->
      <div v-if="loading" class="grid">
        <div v-for="i in 4" :key="'sk' + i" class="tile sk-tile">
          <div class="tile-icon sk-anim"></div>
          <div class="sk-line sk-anim" style="width: 72%; margin: 0 auto"></div>
          <div class="sk-line sm sk-anim" style="width: 44%; margin: 9px auto 0"></div>
        </div>
      </div>

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
import { planApi, digestApi, playApi } from '@/api'
import { resolveIcon, tint } from '@/assets/icons'

const router = useRouter()
const userStore = useUserStore()

const keyword = ref('')
const loading = ref(true)
// 今日任务（null = 没有计划，不显示这条）
const planTask = ref(null)
// 小书童说（null = 还没有沉淀，不显示这张卡）
const digestCard = ref(null)
// 接着上次听（null = 没有"播到一半"的内容）
const resumeCard = ref(null)

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

/** 显示子分类数与内容数 */
function countOf(node) {
  const childCount = node.children?.length || 0
  const resCount = node.resourceCount || 0
  // resourceCount 是"含子孙"的内容总数，所以两者可以同时有值 ——
  // 只显示「N 个分类」会让人以为里面没内容（一级目录常常分类和内容都有）。
  if (childCount > 0 && resCount > 0) return `${childCount} 个分类 · ${resCount} 个内容`
  if (childCount > 0) return `${childCount} 个分类`
  if (resCount > 0) return `${resCount} 个内容`
  return ''
}

function onSearch(v) {
  if (!v) return
  router.push({ name: 'search', query: { q: v } })
}

function goCategory(node) {
  // 有子级 → 进目录页；无子级但有资源 → 进目录页看资源列表
  router.push({ name: 'category', params: { id: node.id } })
}

function goPlan() {
  router.push({ name: 'plan' })
}

function goDigestCategory() {
  if (!digestCard.value) return
  router.push({ name: 'category', params: { id: digestCard.value.categoryId } })
}

// loadPlanTask 今日任务条。没有计划就不显示 —— 首页不该出现空壳卡片。
async function loadPlanTask() {
  try {
    const d = await planApi.today()
    if (!d?.hasPlan) {
      planTask.value = null
      return
    }
    const pending = (d.plans || []).filter((p) => !p.todayCompleted)
    planTask.value = d.completed
      ? { done: true, title: '今天的任务都完成啦', sub: '真棒！明天继续加油' }
      : {
          done: false,
          title: `今天还有 ${pending.length} 个计划要完成`,
          sub: pending[0]?.title || '点开看看今天要学什么',
        }
  } catch {
    planTask.value = null
  }
}

// loadDigestCard 挑一条沉淀放在首页。
//
// 优先选「一级目录」的沉淀：孩子从首页看到的应该是对整个板块的概括，
// 而不是某个很深的子目录。没有一级目录的沉淀时退而取资源最多的那条。
async function loadDigestCard() {
  try {
    const list = await digestApi.list({ limit: 12 })
    if (!Array.isArray(list) || !list.length) {
      digestCard.value = null
      return
    }
    const rootIds = new Set((userStore.tree || []).map((n) => String(n.id)))
    digestCard.value =
      list.find((d) => rootIds.has(String(d.categoryId))) || list[0]
  } catch {
    digestCard.value = null
  }
}

// loadResumeCard 「接着上次听」卡片。
//
// 数据就是播放历史（按 played_at 倒序），但**不能直接用第一条**：
// 孩子刚看完一集的话，最近一条是"已完成"的，点"继续"却从头播，
// 比不显示更让人困惑。所以要挑最近一条真的停在中间的。
//
// 门槛：播过 10 秒以上（少于这个数多半只是点开看了一眼）、
// 完成度 95% 以下（后端 95% 就标 finished 了，这里再兜一次）。
async function loadResumeCard() {
  try {
    const data = await playApi.history({ page: 1, pageSize: 8 })
    const list = data?.list || data || []
    if (!Array.isArray(list) || !list.length) {
      resumeCard.value = null
      return
    }
    const hit = list.find((r) => {
      if (r.finished) return false
      const pos = Number(r.position) || 0
      const pct = Number(r.percentage) || 0
      return pos > 10 && pct < 95
    })
    if (!hit) {
      resumeCard.value = null
      return
    }
    const dur = Number(hit.duration) || 0
    const pos = Number(hit.position) || 0
    const pct = dur > 0 ? Math.round((pos / dur) * 100) : Math.round(Number(hit.percentage) || 0)
    const emoji = { audio: '🎧', video: '🎬', pdf: '📄', image: '🖼️', ebook: '📖' }
    resumeCard.value = {
      resourceId: hit.resourceId || hit.id,
      title: hit.title || '上次的内容',
      dir: hit.categoryPath || hit.categoryName || '',
      percent: Math.max(1, Math.min(99, pct)),
      emoji: emoji[hit.mediaType] || '📚',
    }
  } catch {
    resumeCard.value = null
  }
}

function goResume() {
  const c = resumeCard.value
  if (!c?.resourceId) return
  // 播放页会自己按 playInfo.lastPosition 定位，这里不用带进度参数
  router.push({ name: 'play', params: { id: c.resourceId } })
}

onMounted(async () => {
  try {
    await userStore.loadTree()
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }

  // 下面几个都是"有就显示、没有就不显示"，彼此独立，谁失败都不影响别人
  loadResumeCard()
  loadPlanTask()
  loadDigestCard()
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

/* ===== 接着上次听 ===== */

.resume-strip {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 13px 15px;
  margin-bottom: 10px;
  border-radius: var(--r-lg);
  background: linear-gradient(135deg, #fff3e0, #ffe9d1);
  box-shadow: 0 6px 18px rgba(240, 160, 60, 0.18);
}
.rs-ico {
  font-size: 26px;
  line-height: 1;
}
.rs-body {
  flex: 1;
  min-width: 0;
}
.rs-title {
  font-size: 15.5px;
  font-weight: 700;
  color: #6b4423;
}
.rs-sub {
  margin-top: 2px;
  font-size: 12.5px;
  color: #9c7248;
}
.rs-bar {
  margin-top: 7px;
  height: 4px;
  border-radius: 2px;
  background: rgba(240, 160, 60, 0.22);
  overflow: hidden;
}
.rs-bar i {
  display: block;
  height: 100%;
  border-radius: 2px;
  background: linear-gradient(90deg, #f6a623, #ffc46b);
}
.rs-go {
  font-size: 13.5px;
  font-weight: 600;
  color: #d98218;
  white-space: nowrap;
}

/* ===== 今日任务 ===== */

.plan-strip {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 15px;
  border-radius: var(--r-lg);
  background: linear-gradient(135deg, #5b8ff9, #7ea6f5);
  box-shadow: 0 6px 18px rgba(91, 143, 249, 0.28);
  color: #fff;
}
.plan-strip.done {
  background: linear-gradient(135deg, #48b881, #6fd39d);
  box-shadow: 0 6px 18px rgba(72, 184, 129, 0.26);
}
.p-ico {
  font-size: 24px;
  line-height: 1;
  flex: 0 0 auto;
}
.p-body {
  flex: 1;
  min-width: 0;
}
.p-title {
  font-size: 15.5px;
  font-weight: 700;
}
.p-sub {
  margin-top: 2px;
  font-size: 12.5px;
  opacity: 0.9;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.p-go {
  flex: 0 0 auto;
  font-size: 12.5px;
  font-weight: 600;
  background: rgba(255, 255, 255, 0.22);
  padding: 5px 11px;
  border-radius: 999px;
}

/* ===== 小书童说 ===== */

.say-card {
  margin-top: 12px;
  padding: 14px 15px;
  border-radius: var(--r-lg);
  background: linear-gradient(150deg, #eef4ff 0%, #f7fbff 100%);
  border: 1px solid #dbe7ff;
  box-shadow: var(--shadow-sm);
}
.s-head {
  display: flex;
  align-items: center;
  gap: 7px;
}
.s-ava {
  font-size: 16px;
  line-height: 1;
}
.s-name {
  font-size: 13.5px;
  font-weight: 700;
  color: #3d6fe0;
  flex: 0 0 auto;
}
.s-cat {
  min-width: 0;
  font-size: 12px;
  color: #7f93b8;
}
.s-intro {
  margin: 8px 0 0;
  font-size: 13.5px;
  line-height: 1.6;
  color: #33465e;
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
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

/* 骨架屏版卡片：只有白底和圆角，不含边框与渐变（内容还没有，别装成有） */
.sk-tile {
  background: var(--c-card);
  border-color: transparent;
}
.sk-tile .tile-icon {
  background: #eef2f7;
}
</style>
