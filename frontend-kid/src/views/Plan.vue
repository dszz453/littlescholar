<template>
  <div class="page plan-page">
    <!-- 顶部：返回 + 标题 + 新建（返回与安全区统一由 PageBar 处理） -->
    <PageBar title="学习计划">
      <template #right>
        <button class="add pressable" type="button" aria-label="新建计划" @click="openCreate">＋</button>
      </template>
    </PageBar>

    <SkeletonList v-if="loading" :count="3" />

    <div v-else class="page-pad">
      <!-- ===== 今日打卡 ===== -->
      <div class="today-card" :class="{ done: today.completed, idle: !today.hasPlan }">
        <template v-if="today.hasPlan">
          <div class="tc-head">
            <span class="tc-emoji">{{ today.completed ? '🎉' : '⏰' }}</span>
            <div class="tc-body">
              <div class="tc-title">
                {{ today.completed ? '今天已经完成啦！' : '今天还没打卡哦' }}
              </div>
              <div class="tc-sub">
                {{ today.completed ? '真棒，明天继续保持～' : '去学一会儿，完成今天的小目标吧' }}
              </div>
            </div>
          </div>

          <div class="tc-list">
            <div v-for="p in today.plans" :key="p.id" class="tc-item">
              <div class="tci-top">
                <span class="tci-name ellipsis">{{ p.title }}</span>
                <span class="tci-num" :class="{ ok: p.todayCompleted }">
                  {{ p.todayMinutes }} / {{ p.minMinutes }} 分
                </span>
              </div>
              <div class="tci-bar">
                <i :style="{ width: pct(p.todayMinutes, p.minMinutes) + '%' }"></i>
              </div>
            </div>
          </div>

          <button
            v-if="!today.completed"
            class="tc-go pressable"
            @click="goPlanCategory(today.plans[0])"
          >开始学习</button>
        </template>

        <template v-else>
          <div class="tc-head">
            <span class="tc-emoji">📅</span>
            <div class="tc-body">
              <div class="tc-title">还没有进行中的计划</div>
              <div class="tc-sub">点右上角「＋」给自己定个小目标吧</div>
            </div>
          </div>
        </template>
      </div>

      <!-- ===== 本周坚持 ===== -->
      <div class="week-card">
        <div class="wk-head">
          <h3>本周坚持</h3>
          <span class="wk-range">{{ shortDate(today.weekStart) }} - {{ shortDate(today.weekEnd) }}</span>
        </div>
        <div class="wk-body">
          <div class="wk-ring">
            <svg viewBox="0 0 44 44">
              <circle cx="22" cy="22" r="18" class="ring-bg" />
              <circle
                cx="22" cy="22" r="18"
                class="ring-fg"
                :stroke-dasharray="circumference"
                :stroke-dashoffset="ringOffset"
              />
            </svg>
            <span class="wk-days">{{ today.weekDays }}<i>/{{ today.awardDays }}</i></span>
          </div>
          <div class="wk-info">
            <div class="wk-line" v-if="today.awarded">
              🏆 这周的奖状已经拿到啦！
            </div>
            <div class="wk-line" v-else-if="today.weekDays >= today.awardDays">
              达标啦，奖状马上就来～
            </div>
            <div class="wk-line" v-else>
              再坚持 <b>{{ today.awardDays - today.weekDays }}</b> 天就能拿到奖状啦
            </div>
            <div class="wk-sub">本周累计学习 {{ today.weekMinutes }} 分钟</div>
            <button class="wk-btn pressable" @click="tryAward">看看能不能拿奖状</button>
          </div>
        </div>
      </div>

      <!-- ===== 我的计划 ===== -->
      <div class="section-title"><h2>我的计划</h2></div>

      <template v-if="plans.length">
        <div
          v-for="p in plans"
          :key="p.id"
          class="plan-card"
          :class="{ expired: p.expired }"
        >
          <div class="pc-top">
            <span class="pc-ico">{{ p.expired ? '📦' : '🎯' }}</span>
            <div class="pc-body">
              <div class="pc-name ellipsis">{{ p.title }}</div>
              <div class="pc-cat ellipsis">📁 {{ p.categoryName || '未指定目录' }}</div>
            </div>
            <span v-if="p.expired" class="pc-tag off">已结束</span>
            <span v-else-if="p.activeToday" class="pc-tag on">进行中</span>
            <span v-else class="pc-tag soon">未开始</span>
          </div>

          <div class="pc-meta">
            <span>📅 {{ shortDate(p.startDate) }} - {{ shortDate(p.endDate) }}</span>
            <span>⏱ 每天 ≥ {{ p.minMinutes }} 分钟</span>
          </div>

          <div class="pc-progress">
            <div class="pcp-bar"><i :style="{ width: progressOf(p) + '%' }"></i></div>
            <span class="pcp-text">已坚持 {{ p.daysChecked }} / {{ p.daysElapsed || p.daysTotal }} 天</span>
          </div>

          <div class="pc-actions">
            <button class="pc-btn pressable" @click="goPlanCategory(p)">去学习</button>
            <button class="pc-btn ghost pressable" @click="openEdit(p)">修改</button>
            <button class="pc-btn danger pressable" @click="removePlan(p)">删除</button>
          </div>
        </div>
      </template>

      <div v-else class="empty">
        <span class="emoji">🎯</span>
        <p>还没有学习计划</p>
        <p class="text-sub">定个每天要完成的小目标吧</p>
      </div>

      <!-- ===== 奖状墙 ===== -->
      <template v-if="awards.length">
        <div class="section-title"><h2>我的奖状</h2></div>
        <div class="award-wall">
          <div
            v-for="a in awards"
            :key="a.id"
            class="award-mini pressable"
            :style="{ '--ac': a.color || '#F6BD16' }"
            @click="openedAward = a"
          >
            <span class="am-emoji">{{ a.emoji || '🏆' }}</span>
            <div class="am-title ellipsis">{{ a.title }}</div>
            <div class="am-week">{{ shortDate(a.weekStart) }} 那周</div>
          </div>
        </div>
      </template>
    </div>

    <!-- ===== 新建 / 修改计划 ===== -->
    <div v-if="editorOpen" class="mask" @click.self="closeEditor">
      <div class="sheet">
        <h3>{{ editing ? '修改学习计划' : '新建学习计划' }}</h3>

        <div class="field">
          <label>学习哪个目录</label>
          <select v-model="form.categoryId" class="input">
            <option value="" disabled>请选择目录</option>
            <option v-for="c in flatCats" :key="c.id" :value="c.id">
              {{ '　'.repeat(c.depth) }}{{ c.depth ? '└ ' : '' }}{{ c.name }}
            </option>
          </select>
        </div>

        <div class="field two">
          <div>
            <label>从哪天开始</label>
            <input v-model="form.startDate" type="date" class="input" />
          </div>
          <div>
            <label>到哪天结束</label>
            <input v-model="form.endDate" type="date" class="input" />
          </div>
        </div>

        <div class="field">
          <label>每天至少学习多少分钟</label>
          <div class="min-row">
            <button
              v-for="m in minuteOptions"
              :key="m"
              class="min-chip pressable"
              :class="{ on: Number(form.minMinutes) === m }"
              @click="form.minMinutes = m"
            >{{ m }}</button>
            <input v-model.number="form.minMinutes" type="number" min="1" max="600" class="min-input" />
          </div>
        </div>

        <div class="sheet-actions">
          <button class="pc-btn ghost pressable" @click="closeEditor">取消</button>
          <button class="pc-btn primary pressable" :disabled="saving" @click="save">
            {{ saving ? '保存中…' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- ===== 恭喜弹窗 ===== -->
    <div v-if="congrats" class="mask center" @click.self="congrats = false">
      <div class="congrats">
        <div class="cg-emoji">🎉</div>
        <h3>太棒啦，今天的任务完成！</h3>
        <p>你完成了今天的学习计划，明天继续保持哦～</p>
        <button class="pc-btn primary pressable" @click="congrats = false">好的</button>
      </div>
    </div>

    <!-- ===== 奖状详情 ===== -->
    <div v-if="openedAward" class="mask center" @click.self="openedAward = null">
      <div class="award-card" :style="{ '--ac': openedAward.color || '#F6BD16' }">
        <div class="aw-ribbon">奖 状</div>
        <div class="aw-emoji">{{ openedAward.emoji || '🏆' }}</div>
        <div class="aw-name">{{ openedAward.title }}</div>
        <p class="aw-content">{{ openedAward.content }}</p>
        <div class="aw-foot">
          <span>{{ shortDate(openedAward.weekStart) }} - {{ shortDate(openedAward.weekEnd) }}</span>
          <span>坚持 {{ openedAward.daysChecked }} 天 · {{ openedAward.totalMinutes }} 分钟</span>
        </div>
        <button class="pc-btn primary pressable" @click="openedAward = null">收起</button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import PageBar from '@/components/PageBar.vue'
import SkeletonList from '@/components/SkeletonList.vue'
import { useUserStore } from '@/stores/user'
import { planApi } from '@/api'
import { showToast } from 'vant'

const router = useRouter()
const userStore = useUserStore()

const loading = ref(true)
const saving = ref(false)
const today = ref({})
const plans = ref([])
const awards = ref([])

const editorOpen = ref(false)
const editing = ref(null)
const congrats = ref(false)
const openedAward = ref(null)

const minuteOptions = [10, 20, 30, 45, 60]

const form = ref({ categoryId: '', startDate: '', endDate: '', minMinutes: 20 })

// 环形进度（本周打卡天数 / 目标天数）
const circumference = 2 * Math.PI * 18
const ringOffset = computed(() => {
  const target = today.value.awardDays || 5
  const done = Math.min(today.value.weekDays || 0, target)
  return circumference * (1 - done / target)
})

// 目录树拍平：原生 select 不认嵌套，用全角空格做缩进
const flatCats = computed(() => {
  const out = []
  const walk = (nodes, depth) => {
    for (const n of nodes || []) {
      out.push({ id: String(n.id), name: n.name, depth })
      if (n.children?.length) walk(n.children, depth + 1)
    }
  }
  walk(userStore.tree, 0)
  return out
})

function pct(cur, target) {
  if (!target) return 0
  return Math.min(100, Math.round(((cur || 0) / target) * 100))
}

function progressOf(p) {
  const denom = p.daysElapsed || p.daysTotal
  if (!denom) return 0
  return Math.min(100, Math.round((p.daysChecked / denom) * 100))
}

function shortDate(d) {
  return d ? String(d).slice(5).replace('-', '/') : ''
}

function goPlanCategory(p) {
  if (p?.categoryId) router.push({ name: 'category', params: { id: p.categoryId } })
}

// ---- 恭喜弹窗去重：同一天只弹一次，否则每次刷新/回页面都会弹 ----
function congratsKey() {
  return `plan_congrats_${today.value.date || ''}`
}
function congratsShown() {
  try {
    return localStorage.getItem(congratsKey()) === '1'
  } catch {
    return false
  }
}
function markCongratsShown() {
  try {
    localStorage.setItem(congratsKey(), '1')
  } catch {
    /* 隐私模式下写不了，忽略即可 */
  }
}

// lastCompleted 记住上一次的完成状态。
// 只有「本次会话里从没完成变成完成」才弹恭喜 —— 进页面时本来就已完成
// 不该再弹（否则孩子每次打开计划页都被庆祝一次，很快就没感觉了）。
let lastCompleted = null

async function loadToday() {
  try {
    const data = await planApi.today()
    const prev = lastCompleted
    today.value = data || {}
    lastCompleted = !!data?.completed
    if (data?.hasPlan && prev === false && lastCompleted && !congratsShown()) {
      markCongratsShown()
      congrats.value = true
    }
  } catch {
    /* 静默：今日状态拿不到不该让整页白掉 */
  }
}

async function loadPlans() {
  try {
    const data = await planApi.list()
    plans.value = Array.isArray(data) ? data : data?.list || []
  } catch {
    plans.value = []
  }
}

async function loadAwards() {
  try {
    const data = await planApi.awards({ page: 1, pageSize: 30 })
    awards.value = data?.list || []
  } catch {
    awards.value = []
  }
}

async function loadAll() {
  loading.value = true
  await Promise.all([userStore.loadTree().catch(() => {}), loadToday(), loadPlans(), loadAwards()])
  loading.value = false
}

// ---- 新建 / 修改 ----
function todayStr(offsetDays = 0) {
  const d = new Date()
  d.setDate(d.getDate() + offsetDays)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function openCreate() {
  editing.value = null
  form.value = {
    categoryId: '',
    startDate: todayStr(),
    endDate: todayStr(29),
    minMinutes: 20,
  }
  editorOpen.value = true
}

function openEdit(p) {
  editing.value = p
  form.value = {
    categoryId: String(p.categoryId || ''),
    startDate: p.startDate,
    endDate: p.endDate,
    minMinutes: p.minMinutes,
  }
  editorOpen.value = true
}

function closeEditor() {
  editorOpen.value = false
}

async function save() {
  if (!form.value.categoryId) {
    showToast('请选择学习目录')
    return
  }
  if (!form.value.startDate || !form.value.endDate) {
    showToast('请选择起止日期')
    return
  }
  if (form.value.endDate < form.value.startDate) {
    showToast('结束日期不能早于开始日期')
    return
  }
  const payload = {
    categoryId: Number(form.value.categoryId),
    startDate: form.value.startDate,
    endDate: form.value.endDate,
    minMinutes: Number(form.value.minMinutes) || 20,
  }
  saving.value = true
  try {
    if (editing.value) {
      await planApi.update(editing.value.id, payload)
      showToast('已更新')
    } else {
      await planApi.create(payload)
      showToast('计划已创建')
    }
    editorOpen.value = false
    await Promise.all([loadPlans(), loadToday()])
  } catch {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function removePlan(p) {
  // 用原生 confirm 而不是花哨弹窗：删除是"要慎重"的操作，
  // 系统级的确认框反而更能让孩子意识到这一步不好撤销。
  if (!window.confirm(`确定要删除「${p.title}」吗？打卡记录也会一起清掉。`)) return
  try {
    await planApi.remove(p.id)
    showToast('已删除')
    await Promise.all([loadPlans(), loadToday(), loadAwards()])
  } catch {
    /* 拦截器已提示 */
  }
}

async function tryAward() {
  try {
    const data = await planApi.refreshAward()
    if (data?.award) {
      showToast('恭喜！你拿到了新奖状 🎉')
      openedAward.value = data.award
      await loadAwards()
      await loadToday()
    } else {
      showToast(data?.message || '再坚持几天就能拿到啦')
    }
  } catch {
    /* 拦截器已提示 */
  }
}

// ---- 回到前台就刷一次今日状态 ----
// 孩子大多是从播放页切回来，这时时长已经落库，必须重新拉才能看到"已完成"。
function onVisible() {
  if (document.visibilityState === 'visible') loadToday()
}

let timer = null

onMounted(() => {
  loadAll()
  document.addEventListener('visibilitychange', onVisible)
  // 兜底轮询：同屏挂着不动时（比如后台播放）也能在达标后弹恭喜
  timer = setInterval(loadToday, 60000)
})

onUnmounted(() => {
  document.removeEventListener('visibilitychange', onVisible)
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.plan-page {
  /* 底部要留出 TabBar 的高度，否则最后一张卡片被挡住 */
  padding-bottom: calc(96px + var(--safe-bottom));
}

.add {
  width: 40px;
  height: 40px;
  flex: 0 0 auto;
  border: 0;
  border-radius: 50%;
  background: #fff;
  box-shadow: var(--shadow-sm);
  font-size: 22px;
  line-height: 1;
  color: var(--c-primary);
  font-weight: 700;
  font-family: inherit;
}

/* ===== 今日打卡 ===== */
.today-card {
  margin-top: 14px;
  padding: 16px;
  border-radius: var(--r-lg);
  background: linear-gradient(135deg, #eaf1ff 0%, #f7faff 100%);
  box-shadow: var(--shadow-sm);
}
.today-card.done {
  background: linear-gradient(135deg, #e6fbf0 0%, #f6fffa 100%);
}
.today-card.idle {
  background: #fff;
}
.tc-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.tc-emoji {
  font-size: 32px;
  line-height: 1;
}
.tc-body {
  flex: 1;
  min-width: 0;
}
.tc-title {
  font-size: 16.5px;
  font-weight: 700;
}
.tc-sub {
  margin-top: 2px;
  font-size: 13px;
  color: var(--c-text-sub);
}

.tc-list {
  margin-top: 12px;
}
.tc-item + .tc-item {
  margin-top: 10px;
}
.tci-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 13.5px;
}
.tci-name {
  color: var(--c-text);
  font-weight: 600;
}
.tci-num {
  flex: 0 0 auto;
  color: var(--c-text-sub);
}
.tci-num.ok {
  color: #25a06a;
  font-weight: 700;
}
.tci-bar {
  margin-top: 5px;
  height: 7px;
  border-radius: 999px;
  background: rgba(91, 143, 249, 0.14);
  overflow: hidden;
}
.tci-bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--c-primary);
  transition: width 0.4s ease;
}
.today-card.done .tci-bar i {
  background: #25a06a;
}

.tc-go {
  width: 100%;
  margin-top: 14px;
  padding: 12px;
  border: 0;
  border-radius: var(--r-md);
  background: var(--c-primary);
  color: #fff;
  font-size: 15.5px;
  font-weight: 700;
  font-family: inherit;
}

/* ===== 本周坚持 ===== */
.week-card {
  margin-top: 14px;
  padding: 16px;
  border-radius: var(--r-lg);
  background: #fff;
  box-shadow: var(--shadow-sm);
}
.wk-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
}
.wk-head h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 700;
}
.wk-range {
  font-size: 12px;
  color: var(--c-text-sub);
}
.wk-body {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-top: 12px;
}
.wk-ring {
  position: relative;
  width: 76px;
  height: 76px;
  flex: 0 0 auto;
}
.wk-ring svg {
  width: 100%;
  height: 100%;
  transform: rotate(-90deg);
}
.ring-bg {
  fill: none;
  stroke: #eef2f7;
  stroke-width: 5;
}
.ring-fg {
  fill: none;
  stroke: var(--c-primary);
  stroke-width: 5;
  stroke-linecap: round;
  transition: stroke-dashoffset 0.5s ease;
}
.wk-days {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 20px;
  font-weight: 800;
  color: var(--c-primary);
}
.wk-days i {
  font-size: 12px;
  font-style: normal;
  color: var(--c-text-sub);
  font-weight: 600;
}
.wk-info {
  flex: 1;
  min-width: 0;
}
.wk-line {
  font-size: 14px;
  font-weight: 600;
}
.wk-line b {
  color: var(--c-primary);
}
.wk-sub {
  margin-top: 3px;
  font-size: 12.5px;
  color: var(--c-text-sub);
}
.wk-btn {
  margin-top: 8px;
  padding: 7px 14px;
  border: 0;
  border-radius: 999px;
  background: rgba(91, 143, 249, 0.12);
  color: var(--c-primary);
  font-size: 13px;
  font-weight: 700;
  font-family: inherit;
}

/* ===== 计划卡片 ===== */
.plan-card {
  padding: 15px;
  border-radius: var(--r-lg);
  background: #fff;
  box-shadow: var(--shadow-sm);
  margin-bottom: 12px;
}
.plan-card.expired {
  opacity: 0.72;
}
.pc-top {
  display: flex;
  align-items: center;
  gap: 11px;
}
.pc-ico {
  font-size: 26px;
  line-height: 1;
}
.pc-body {
  flex: 1;
  min-width: 0;
}
.pc-name {
  font-size: 16px;
  font-weight: 700;
}
.pc-cat {
  margin-top: 2px;
  font-size: 12.5px;
  color: var(--c-text-sub);
}
.pc-tag {
  flex: 0 0 auto;
  font-size: 11.5px;
  font-weight: 700;
  padding: 3px 9px;
  border-radius: 999px;
}
.pc-tag.on {
  background: rgba(37, 160, 106, 0.12);
  color: #25a06a;
}
.pc-tag.soon {
  background: rgba(91, 143, 249, 0.12);
  color: var(--c-primary);
}
.pc-tag.off {
  background: #f0f2f5;
  color: var(--c-text-sub);
}

.pc-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 11px;
  font-size: 12.5px;
  color: var(--c-text-sub);
}

.pc-progress {
  margin-top: 11px;
}
.pcp-bar {
  height: 8px;
  border-radius: 999px;
  background: #eef2f7;
  overflow: hidden;
}
.pcp-bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: linear-gradient(90deg, #7aa7ff, #5b8ff9);
  transition: width 0.4s ease;
}
.pcp-text {
  display: inline-block;
  margin-top: 5px;
  font-size: 12px;
  color: var(--c-text-sub);
}

.pc-actions {
  display: flex;
  gap: 8px;
  margin-top: 13px;
}
.pc-btn {
  flex: 1;
  padding: 10px;
  border: 0;
  border-radius: var(--r-sm);
  background: rgba(91, 143, 249, 0.12);
  color: var(--c-primary);
  font-size: 14px;
  font-weight: 700;
  font-family: inherit;
}
.pc-btn.primary {
  background: var(--c-primary);
  color: #fff;
}
.pc-btn.ghost {
  background: #f3f6fa;
  color: var(--c-text-sub);
}
.pc-btn.danger {
  background: rgba(217, 85, 74, 0.1);
  color: #d9554a;
}

/* ===== 奖状墙 ===== */
.award-wall {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
}
.award-mini {
  padding: 12px 8px;
  border-radius: var(--r-md);
  background: #fff;
  box-shadow: var(--shadow-sm);
  border-top: 3px solid var(--ac);
  text-align: center;
}
.am-emoji {
  font-size: 26px;
  line-height: 1;
}
.am-title {
  margin-top: 6px;
  font-size: 12.5px;
  font-weight: 700;
}
.am-week {
  margin-top: 2px;
  font-size: 11px;
  color: var(--c-text-sub);
}

/* ===== 底部弹层（新建/修改） ===== */
.mask {
  position: fixed;
  inset: 0;
  z-index: 200;
  background: rgba(20, 32, 48, 0.42);
  display: flex;
  align-items: flex-end;
}
.mask.center {
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.sheet {
  width: 100%;
  background: #fff;
  border-radius: var(--r-xl) var(--r-xl) 0 0;
  padding: 20px 18px calc(20px + var(--safe-bottom));
  max-height: 88vh;
  overflow-y: auto;
}
.sheet h3 {
  margin: 0 0 16px;
  font-size: 17px;
  font-weight: 700;
}
.field {
  margin-bottom: 14px;
}
.field.two {
  display: flex;
  gap: 10px;
}
.field.two > div {
  flex: 1;
  min-width: 0;
}
.field label {
  display: block;
  margin-bottom: 6px;
  font-size: 13px;
  color: var(--c-text-sub);
}
.input {
  width: 100%;
  padding: 11px 12px;
  border: 1px solid var(--c-border);
  border-radius: var(--r-sm);
  background: #f9fbfd;
  font-size: 15px;
  font-family: inherit;
  color: var(--c-text);
}
.min-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.min-chip {
  padding: 8px 15px;
  border: 0;
  border-radius: 999px;
  background: #f3f6fa;
  color: var(--c-text-sub);
  font-size: 14px;
  font-weight: 700;
  font-family: inherit;
}
.min-chip.on {
  background: var(--c-primary);
  color: #fff;
}
.min-input {
  width: 74px;
  padding: 8px 10px;
  border: 1px solid var(--c-border);
  border-radius: 999px;
  text-align: center;
  font-size: 14px;
  font-family: inherit;
}
.sheet-actions {
  display: flex;
  gap: 10px;
  margin-top: 20px;
}

/* ===== 恭喜弹窗 ===== */
.congrats {
  width: 100%;
  max-width: 320px;
  padding: 26px 22px 20px;
  border-radius: var(--r-xl);
  background: #fff;
  text-align: center;
  animation: pop 0.32s cubic-bezier(0.2, 1.3, 0.5, 1);
}
@keyframes pop {
  from {
    transform: scale(0.7);
    opacity: 0;
  }
}
.cg-emoji {
  font-size: 58px;
  line-height: 1;
}
.congrats h3 {
  margin: 14px 0 6px;
  font-size: 18px;
  font-weight: 800;
}
.congrats p {
  margin: 0 0 20px;
  font-size: 14px;
  color: var(--c-text-sub);
}

/* ===== 奖状详情 ===== */
.award-card {
  width: 100%;
  max-width: 330px;
  padding: 22px 20px 18px;
  border-radius: var(--r-xl);
  background: #fff;
  border: 3px solid var(--ac);
  text-align: center;
  animation: pop 0.32s cubic-bezier(0.2, 1.3, 0.5, 1);
}
.aw-ribbon {
  display: inline-block;
  padding: 3px 16px;
  border-radius: 999px;
  background: var(--ac);
  color: #fff;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 3px;
}
.aw-emoji {
  margin-top: 14px;
  font-size: 54px;
  line-height: 1;
}
.aw-name {
  margin-top: 8px;
  font-size: 21px;
  font-weight: 800;
  color: var(--ac);
}
.aw-content {
  margin: 12px 0 0;
  font-size: 14px;
  line-height: 1.7;
  color: var(--c-text);
  text-align: left;
}
.aw-foot {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 16px 0 16px;
  font-size: 12px;
  color: var(--c-text-sub);
}
</style>
