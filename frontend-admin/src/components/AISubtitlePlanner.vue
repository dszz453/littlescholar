<template>
  <div class="ai-sub">
    <!-- 状态：一眼看清"现在能不能跑、跑得怎么样" -->
    <div class="card-box">
      <div class="row-between">
        <div>
          <div class="section-title">批量生成状态</div>
          <div class="text-secondary small">
            {{ statText }}
          </div>
        </div>
        <div class="actions">
          <el-button :icon="Refresh" :loading="loading" @click="refreshAll">刷新</el-button>
          <el-button type="primary" :icon="VideoPlay" :loading="running" @click="runNow">
            立即执行一批
          </el-button>
        </div>
      </div>

      <el-alert
        v-if="!summary.aiConfigured"
        type="warning"
        :closable="false"
        class="mt-12"
        show-icon
        title="还没有可用的 AI 配置"
        description="先在「接口配置」里添加一个接口并设为默认，否则批量任务会全部失败。"
      />

      <div class="stats mt-16">
        <div class="stat">
          <div class="stat-num">{{ counts.pending || 0 }}</div>
          <div class="stat-label">排队中</div>
        </div>
        <div class="stat">
          <div class="stat-num running-num">{{ counts.running || 0 }}</div>
          <div class="stat-label">生成中</div>
        </div>
        <div class="stat">
          <div class="stat-num ok">{{ counts.done || 0 }}</div>
          <div class="stat-label">已完成</div>
        </div>
        <div class="stat">
          <div class="stat-num warn">{{ counts.skipped || 0 }}</div>
          <div class="stat-label">已跳过</div>
        </div>
        <div class="stat">
          <div class="stat-num err">{{ counts.failed || 0 }}</div>
          <div class="stat-label">失败</div>
        </div>
      </div>
    </div>

    <!-- 计划 -->
    <div class="card-box mt-16">
      <div class="section-title">生成计划</div>
      <div class="text-secondary small mb-12">
        只在设定时段内自动生成，睡觉时间跑，白天不占带宽、不抢模型额度。
        时段可以跨零点（22:00 - 06:00）。
      </div>
      <el-form :model="form" label-width="110px" class="plan-form">
        <el-form-item label="自动生成">
          <el-switch v-model="form.enabled" />
          <span class="hint">
            {{ form.enabled ? '到时间会自动消费队列' : '关着，只能点「立即执行一批」' }}
          </span>
        </el-form-item>
        <el-form-item label="每天时段">
          <el-time-select
            v-model="form.start"
            start="00:00"
            step="00:30"
            end="23:30"
            placeholder="开始"
            style="width: 130px"
          />
          <span class="sep">至</span>
          <el-time-select
            v-model="form.end"
            start="00:00"
            step="00:30"
            end="23:30"
            placeholder="结束"
            style="width: 130px"
          />
          <span class="hint">结束时刻不含；当前{{ summary.inWindow ? '在' : '不在' }}时段内</span>
        </el-form-item>
        <el-form-item label="每轮条数">
          <el-input-number v-model="form.batchPerRound" :min="1" :max="50" />
          <span class="hint">一轮最多生成几条（一条 = 一次模型调用）</span>
        </el-form-item>
        <el-form-item label="间隔（秒）">
          <el-input-number v-model="form.gapSeconds" :min="0" :max="300" :step="5" />
          <span class="hint">两条之间歇多久，留余量给接口限流</span>
        </el-form-item>
        <el-form-item label="覆盖已有字幕">
          <el-switch v-model="form.overwrite" />
          <span class="hint warn-text">
            默认关：已有字幕（同目录/内嵌/字幕站）时间轴是准的，不要用模型复原的版本盖掉
          </span>
        </el-form-item>
        <el-form-item label="补充说明">
          <el-input
            v-model="form.note"
            type="textarea"
            :rows="2"
            maxlength="400"
            show-word-limit
            placeholder="可选。例如：这是 Collins Big Cat 三级读物，朗读版；或「牛津树 5 阶」"
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="savePlan">保存计划</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 选文件 -->
    <div class="card-box mt-16">
      <div class="row-between">
        <div>
          <div class="section-title">选择文件</div>
          <div class="text-secondary small">
            标题和目录名会一起交给模型；目录里写着套系和级别，标题只有一册的名字。
          </div>
        </div>
        <div class="actions">
          <el-input
            v-model="query.keyword"
            placeholder="搜标题"
            clearable
            style="width: 180px"
            @keyup.enter="loadCandidates(1)"
            @clear="loadCandidates(1)"
          />
          <el-checkbox v-model="query.onlyMissing" @change="loadCandidates(1)">只看没字幕的</el-checkbox>
          <el-button @click="loadCandidates(1)">搜索</el-button>
        </div>
      </div>

      <el-table
        :data="candidates"
        v-loading="loadingCandidates"
        row-key="id"
        class="mt-12"
        height="360"
        @selection-change="onSelect"
      >
        <el-table-column type="selection" width="44" reserve-selection />
        <el-table-column prop="title" label="文件" min-width="220" show-overflow-tooltip />
        <el-table-column prop="categoryName" label="目录" width="180" show-overflow-tooltip />
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.mediaType === 'audio' ? 'success' : 'primary'">
              {{ (row.ext || row.mediaType || '').toUpperCase() }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="时长" width="80">
          <template #default="{ row }">{{ clock(row.duration) }}</template>
        </el-table-column>
        <el-table-column label="字幕" width="150">
          <template #default="{ row }">
            <el-tag v-if="row.hasSubtitle" size="small" type="info" :title="row.subtitleSource">
              已有{{ originText(row.subtitleOrigin) }}
            </el-tag>
            <span v-else class="text-secondary small">无</span>
            <el-tag v-if="row.taskStatus" size="small" :type="taskTagType(row.taskStatus)" class="ml-4">
              {{ taskText(row.taskStatus) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="preview(row)">预览</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="row-between mt-12">
        <div class="text-secondary small">
          已选 {{ selected.length }} 个文件
        </div>
        <div class="actions">
          <el-pagination
            v-model:current-page="query.page"
            :page-size="query.pageSize"
            :total="candidateTotal"
            layout="total, prev, pager, next"
            @current-change="loadCandidates"
          />
          <el-button type="primary" :disabled="!selected.length" :loading="enqueueing" @click="enqueue">
            加入队列
          </el-button>
        </div>
      </div>
    </div>

    <!-- 队列 -->
    <div class="card-box mt-16">
      <div class="row-between">
        <div>
          <div class="section-title">生成队列</div>
          <div class="text-secondary small">
            “已跳过”= 模型明说不确定（或已有更好的字幕），重试也不会有变化。
          </div>
        </div>
        <div class="actions">
          <el-select v-model="taskQuery.status" style="width: 130px" @change="loadTasks(1)">
            <el-option label="全部状态" value="" />
            <el-option label="排队中" value="pending" />
            <el-option label="生成中" value="running" />
            <el-option label="已完成" value="done" />
            <el-option label="已跳过" value="skipped" />
            <el-option label="失败" value="failed" />
          </el-select>
          <el-button @click="clearTasks('done')">清理已完成</el-button>
          <el-button @click="clearTasks('skipped')">清理已跳过</el-button>
        </div>
      </div>

      <el-table :data="tasks" v-loading="loadingTasks" class="mt-12" height="360">
        <el-table-column prop="title" label="文件" min-width="220" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="taskTagType(row.status)">{{ taskText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="cues" label="条数" width="70" />
        <el-table-column prop="note" label="说明" min-width="220" show-overflow-tooltip />
        <el-table-column label="更新时间" width="160">
          <template #default="{ row }">{{ timeText(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              size="small"
              :disabled="row.status === 'running'"
              @click="previewTask(row)"
            >
              预览
            </el-button>
            <el-button
              link
              type="primary"
              size="small"
              :disabled="row.status === 'running'"
              @click="retry(row)"
            >
              重试
            </el-button>
            <el-button link type="danger" size="small" @click="removeTask(row)">移除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager mt-12">
        <el-pagination
          v-model:current-page="taskQuery.page"
          :page-size="taskQuery.pageSize"
          :total="taskTotal"
          layout="total, prev, pager, next"
          @current-change="loadTasks"
        />
      </div>
    </div>

    <!-- 预览：先看一眼，再决定要不要跑整批 -->
    <el-dialog v-model="previewVisible" title="生成预览（不保存）" width="760px">
      <div v-if="previewData">
        <el-alert
          v-if="!previewData.ok"
          type="warning"
          :closable="false"
          show-icon
          :title="previewData.message || '模型没有给出内容'"
        />
        <template v-else>
          <div class="text-secondary small mb-12">
            模型 {{ previewData.model }} · {{ previewData.alignText }} · 共
            {{ previewData.cues }} 条 · 时长 {{ clock(previewData.duration) }}
            <span v-if="previewData.truncated">（下面只显示前 40 条）</span>
          </div>
          <el-table :data="previewData.lines" height="420" size="small">
            <el-table-column width="110" label="时间">
              <template #default="{ row }">{{ clock(row.start) }}</template>
            </el-table-column>
            <el-table-column prop="text" label="原文" min-width="260" />
            <el-table-column prop="trans" label="中文对照" min-width="220" />
          </el-table>
        </template>
      </div>
      <template #footer>
        <el-button @click="previewVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, VideoPlay } from '@element-plus/icons-vue'
import { aiSubtitleApi } from '@/api'

const loading = ref(false)
const saving = ref(false)
const running = ref(false)
const enqueueing = ref(false)
const summary = ref({ plan: null, stat: {}, counts: {}, inWindow: false, aiConfigured: false })
const counts = computed(() => summary.value.counts || {})

const form = reactive({
  enabled: false,
  start: '22:00',
  end: '06:00',
  batchPerRound: 5,
  gapSeconds: 20,
  overwrite: false,
  note: '',
})

const query = reactive({ keyword: '', onlyMissing: true, page: 1, pageSize: 20 })
const candidates = ref([])
const candidateTotal = ref(0)
const loadingCandidates = ref(false)
const selected = ref([])

const taskQuery = reactive({ status: '', page: 1, pageSize: 20 })
const tasks = ref([])
const taskTotal = ref(0)
const loadingTasks = ref(false)

const previewVisible = ref(false)
const previewData = ref(null)

const statText = computed(() => {
  const st = summary.value.stat || {}
  const stateMap = {
    idle: '空闲',
    running: '正在生成…',
    disabled: '自动生成未启用',
    outside: '不在时段内',
    error: '出错了',
  }
  const parts = [stateMap[st.state] || '待运行']
  if (st.lastNote) parts.push(st.lastNote)
  if (st.lastRunAt) parts.push('上次 ' + timeText(st.lastRunAt))
  return parts.join(' · ')
})

function pad(n) {
  return String(n).padStart(2, '0')
}

// 分钟数 ↔ "HH:MM"
function minuteText(m) {
  const v = ((Number(m) || 0) % 1440 + 1440) % 1440
  return `${pad(Math.floor(v / 60))}:${pad(v % 60)}`
}

function minuteOf(text) {
  const m = /^(\d{1,2}):(\d{2})$/.exec(String(text || '').trim())
  if (!m) return 0
  return (Number(m[1]) % 24) * 60 + Number(m[2])
}

function clock(sec) {
  const s = Math.max(0, Math.round(Number(sec) || 0))
  return `${pad(Math.floor(s / 60))}:${pad(s % 60)}`
}

function timeText(v) {
  if (!v) return ''
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return String(v)
  return `${d.getMonth() + 1}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function taskText(s) {
  return {
    pending: '排队中',
    running: '生成中',
    done: '已完成',
    failed: '失败',
    skipped: '已跳过',
  }[s] || s || ''
}

function taskTagType(s) {
  return {
    pending: 'info',
    running: 'warning',
    done: 'success',
    failed: 'danger',
    skipped: 'info',
  }[s] || 'info'
}

function originText(o) {
  return {
    sibling: '同目录',
    embedded: '内嵌',
    online: '联网',
    manual: '人工',
    ai: 'AI',
  }[o] || '字幕'
}

async function loadPlan() {
  const data = await aiSubtitleApi.plan()
  summary.value = data || {}
  const plan = data?.plan
  if (plan) {
    Object.assign(form, {
      enabled: !!plan.enabled,
      start: minuteText(plan.startMinute),
      end: minuteText(plan.endMinute),
      batchPerRound: plan.batchPerRound || 5,
      gapSeconds: plan.gapSeconds ?? 20,
      overwrite: !!plan.overwrite,
      note: plan.note || '',
    })
  }
}

async function loadCandidates(page) {
  if (page) query.page = page
  loadingCandidates.value = true
  try {
    const data = await aiSubtitleApi.candidates({
      keyword: query.keyword,
      onlyMissing: query.onlyMissing,
      page: query.page,
      pageSize: query.pageSize,
    })
    candidates.value = data?.list || []
    candidateTotal.value = data?.total || 0
  } finally {
    loadingCandidates.value = false
  }
}

async function loadTasks(page) {
  if (page) taskQuery.page = page
  loadingTasks.value = true
  try {
    const data = await aiSubtitleApi.tasks({
      status: taskQuery.status,
      page: taskQuery.page,
      pageSize: taskQuery.pageSize,
    })
    tasks.value = data?.list || []
    taskTotal.value = data?.total || 0
  } finally {
    loadingTasks.value = false
  }
}

async function refreshAll() {
  loading.value = true
  try {
    await loadPlan()
    await Promise.all([loadCandidates(), loadTasks()])
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

async function savePlan() {
  if (minuteOf(form.start) === minuteOf(form.end)) {
    ElMessage.warning('开始和结束时间不能相同')
    return
  }
  saving.value = true
  try {
    await aiSubtitleApi.savePlan({
      enabled: form.enabled,
      startMinute: minuteOf(form.start),
      endMinute: minuteOf(form.end),
      batchPerRound: form.batchPerRound,
      gapSeconds: form.gapSeconds,
      overwrite: form.overwrite,
      note: form.note,
    })
    ElMessage.success('计划已保存')
    await loadPlan()
  } catch (e) {
    /* 已提示 */
  } finally {
    saving.value = false
  }
}

function onSelect(rows) {
  selected.value = rows
}

async function enqueue() {
  const ids = selected.value.map((r) => r.id)
  if (!ids.length) return
  enqueueing.value = true
  try {
    const res = await aiSubtitleApi.enqueue(ids)
    ElMessage.success(`已排队 ${res.added} 条，重置 ${res.reset} 条，跳过 ${res.skipped} 条`)
    await Promise.all([loadPlan(), loadTasks(), loadCandidates()])
  } catch (e) {
    /* 已提示 */
  } finally {
    enqueueing.value = false
  }
}

async function runNow() {
  running.value = true
  try {
    const res = await aiSubtitleApi.run()
    if (res && res.started === false) {
      ElMessage.info(res.message || '上一批还在跑')
    } else {
      ElMessage.success('已开始，进度会在这里刷新')
    }
    await loadPlan()
  } catch (e) {
    /* 已提示 */
  } finally {
    running.value = false
  }
}

async function preview(row) {
  previewData.value = null
  previewVisible.value = true
  const loadingMsg = ElMessage({ message: '正在生成预览，首次可能要几十秒…', duration: 0 })
  try {
    previewData.value = await aiSubtitleApi.preview(row.resourceId || row.id)
  } catch (e) {
    previewData.value = { ok: false, message: '预览失败，看控制台或稍后重试' }
  } finally {
    loadingMsg.close()
  }
}

function previewTask(row) {
  if (!row.resourceId) return
  preview({ id: row.resourceId })
}

async function retry(row) {
  try {
    await aiSubtitleApi.retry(row.id)
    ElMessage.success('已重新排队')
    loadTasks()
  } catch (e) {
    /* 已提示 */
  }
}

async function removeTask(row) {
  try {
    await ElMessageBox.confirm(`从队列里移除「${row.title || row.resourceId}」？`, '确认', {
      type: 'warning',
    })
    await aiSubtitleApi.remove(row.id)
    ElMessage.success('已移除')
    loadTasks()
  } catch (e) {
    /* 取消 */
  }
}

async function clearTasks(status) {
  try {
    await ElMessageBox.confirm(
      `清理所有「${taskText(status)}」的记录？已生成的字幕不受影响。`,
      '确认',
      { type: 'warning' }
    )
    const res = await aiSubtitleApi.clear(status)
    ElMessage.success(`已清理 ${res.removed} 条`)
    await Promise.all([loadPlan(), loadTasks()])
  } catch (e) {
    /* 取消 */
  }
}

let timer = null

onMounted(async () => {
  await refreshAll()
  // 一轮要跑几分钟：定时刷状态，管理员不用手动点
  timer = setInterval(() => {
    loadPlan().catch(() => {})
    if (tasks.value.some((t) => t.status === 'running' || t.status === 'pending')) {
      loadTasks().catch(() => {})
    }
  }, 15000)
})

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>

<style scoped>
.row-between {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.section-title {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 4px;
}

.small {
  font-size: 12px;
}

.actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.stats {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}

.stat {
  min-width: 96px;
  padding: 10px 14px;
  border-radius: 8px;
  background: var(--el-fill-color-light, #f5f7fa);
  text-align: center;
}

.stat-num {
  font-size: 22px;
  font-weight: 700;
}

.stat-num.ok {
  color: #67c23a;
}

.stat-num.warn {
  color: #e6a23c;
}

.stat-num.err {
  color: #f56c6c;
}

.stat-num.running-num {
  color: #409eff;
}

.stat-label {
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}

.plan-form {
  max-width: 760px;
}

.hint {
  margin-left: 10px;
  font-size: 12px;
  color: var(--el-text-color-secondary, #909399);
}

.warn-text {
  color: #e6a23c;
}

.sep {
  margin: 0 8px;
  color: var(--el-text-color-secondary, #909399);
}

.ml-4 {
  margin-left: 4px;
}

.pager {
  display: flex;
  justify-content: flex-end;
}
</style>
