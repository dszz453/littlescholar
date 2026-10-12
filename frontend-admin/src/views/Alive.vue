<template>
  <div class="page">
    <!-- 概览 -->
    <el-card shadow="never" class="status-card">
      <div class="status-row">
        <div class="status-left">
          <el-tag :type="status.enabled ? 'success' : 'info'" size="large" effect="dark">
            {{ status.enabled ? '巡检可用' : '未配置检测引擎' }}
          </el-tag>
          <span class="status-hint">
            连续 {{ status.service?.deadThreshold ?? '-' }} 次判失效才标记 ·
            超过 {{ status.service?.staleHours ?? '-' }} 小时未检查则重检
          </span>
        </div>
        <div class="status-right">
          <el-dropdown @command="startCheck">
            <el-button type="primary" :loading="starting" :disabled="!status.enabled">
              开始巡检
              <el-icon class="el-icon--right"><ArrowDown /></el-icon>
            </el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="stale">检查过期未检的（推荐）</el-dropdown-item>
                <el-dropdown-item command="all">全量重检（慢）</el-dropdown-item>
                <el-dropdown-item command="dead">只重查已判失效的</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
        </div>
      </div>

      <!-- 状态分布 -->
      <div v-if="status.enabled" class="dist-row">
        <div class="dist-item">
          <span class="dist-num c-alive">{{ status.distribution?.alive ?? 0 }}</span>
          <span class="dist-label">正常</span>
        </div>
        <div class="dist-item">
          <span class="dist-num c-dead">{{ status.distribution?.dead ?? 0 }}</span>
          <span class="dist-label">已失效</span>
        </div>
        <div class="dist-item">
          <span class="dist-num c-unknown">{{ status.distribution?.unknown ?? 0 }}</span>
          <span class="dist-label">未确认</span>
        </div>
        <div class="dist-item">
          <span class="dist-num">{{ status.total ?? 0 }}</span>
          <span class="dist-label">资源总数</span>
        </div>
      </div>

      <el-alert
        v-if="!status.enabled"
        type="warning"
        :closable="false"
        show-icon
        title="未配置检测引擎"
        description="巡检需要 PanSou（支持批量检测）或 PanCheck（逐条检测）至少一个可用。请在「系统设置」中配置 external.pansou_url 或 external.pancheck_url。"
        class="status-alert"
      />
    </el-card>

    <el-row :gutter="16">
      <!-- 巡检任务 -->
      <el-col :span="14">
        <el-card shadow="never">
          <template #header>
            <span class="card-title">巡检历史</span>
          </template>

          <el-table :data="tasks" v-loading="loading" stripe size="small">
            <el-table-column prop="id" label="ID" width="64" />
            <el-table-column label="触发" width="80">
              <template #default="{ row }">
                <el-tag size="small" effect="plain" :type="row.trigger === 'schedule' ? 'info' : 'primary'">
                  {{ row.trigger === 'schedule' ? '定时' : '手动' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="进度" width="150">
              <template #default="{ row }">
                <el-progress
                  :percentage="row.total ? Math.round((row.checked / row.total) * 100) : 0"
                  :stroke-width="9"
                  :status="row.status === 'running' ? undefined : 'success'"
                />
                <span class="prog-text">{{ row.checked }}/{{ row.total }}</span>
              </template>
            </el-table-column>
            <el-table-column label="结果" width="150">
              <template #default="{ row }">
                <span class="c-alive">存 {{ row.aliveCount }}</span>
                <span class="sep">/</span>
                <span class="c-dead">失 {{ row.deadCount }}</span>
                <span class="sep">/</span>
                <span class="c-unknown">未 {{ row.unknownCount }}</span>
              </template>
            </el-table-column>
            <el-table-column label="异常" width="70">
              <template #default="{ row }">
                <el-tooltip
                  v-if="row.failedBatches > 0"
                  :content="`${row.failedBatches} 个批次调用检测服务失败，说明检测服务本身可能有问题，此轮结果仅供参考`"
                  placement="top"
                >
                  <el-tag size="small" type="danger" effect="plain">{{ row.failedBatches }}</el-tag>
                </el-tooltip>
                <span v-else class="muted">—</span>
              </template>
            </el-table-column>
            <el-table-column label="时间" width="150">
              <template #default="{ row }">
                <span class="muted">{{ fmtTime(row.startedAt || row.createdAt) }}</span>
              </template>
            </el-table-column>
          </el-table>

          <el-pagination
            class="pager"
            layout="total, prev, next"
            :total="total"
            :current-page="query.page"
            :page-size="query.pageSize"
            @current-change="onPageChange"
          />
        </el-card>
      </el-col>

      <!-- 失效清单 -->
      <el-col :span="10">
        <el-card shadow="never">
          <template #header>
            <div class="card-head">
              <span class="card-title">失效资源</span>
              <span class="card-sub">需要人工处理</span>
            </div>
          </template>

          <el-empty v-if="!deadList.length" description="没有失效资源" :image-size="80" />
          <div v-else class="dead-list">
            <div v-for="r in deadList" :key="r.id" class="dead-item">
              <div class="dead-main">
                <span class="dead-title" :title="r.title">{{ r.title }}</span>
                <span class="dead-meta">
                  {{ r.diskType || '未知网盘' }} ·
                  连续失效 {{ r.consecutiveFailures }} 次 ·
                  {{ fmtTime(r.lastCheckedAt) }}
                </span>
              </div>
              <el-tooltip content="去资源管理处理（替换链接或删除）" placement="left">
                <el-button link type="primary" size="small" @click="gotoResource(r)">
                  处理
                </el-button>
              </el-tooltip>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, Refresh } from '@element-plus/icons-vue'
import { aliveApi } from '@/api'

const router = useRouter()

const status = ref({ enabled: false, service: {}, distribution: {}, total: 0 })
const tasks = ref([])
const total = ref(0)
const loading = ref(false)
const starting = ref(false)
const deadList = ref([])

const query = reactive({ page: 1, pageSize: 10 })

let pollTimer = null

async function loadStatus() {
  try {
    status.value = await aliveApi.status()
  } catch {
    status.value = { enabled: false, service: {}, distribution: {}, total: 0 }
  }
}

async function loadTasks() {
  loading.value = true
  try {
    const res = await aliveApi.tasks({ page: query.page, pageSize: query.pageSize })
    tasks.value = res?.list || []
    total.value = res?.total || 0
  } catch (e) {
    ElMessage.error(e?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

async function loadDead() {
  try {
    deadList.value = (await aliveApi.dead(50)) || []
  } catch {
    deadList.value = []
  }
}

function loadAll() {
  loadStatus()
  loadTasks()
  loadDead()
}

function onPageChange(p) {
  query.page = p
  loadTasks()
}

const MODE_LABEL = { stale: '检查过期未检的资源', all: '全量重检', dead: '重查已判失效的资源' }

async function startCheck(mode) {
  try {
    await ElMessageBox.confirm(
      `${MODE_LABEL[mode] || mode}。巡检会在后台运行，期间可以离开本页面。是否开始？`,
      '开始巡检',
      { type: 'info', confirmButtonText: '开始' },
    )
  } catch {
    return
  }
  starting.value = true
  try {
    const res = await aliveApi.start(mode)
    if (!res?.taskId) {
      ElMessage.info('没有需要巡检的资源')
      return
    }
    ElMessage.success('巡检已启动，结果会实时更新')
    query.page = 1
    loadAll()
  } catch (e) {
    ElMessage.error(e?.message || '启动失败')
  } finally {
    starting.value = false
  }
}

function gotoResource(r) {
  router.push({ path: '/resources', query: { keyword: r.title } })
}

function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n) => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

onMounted(() => {
  loadAll()
  // 有巡检在跑时自动刷新进度
  pollTimer = setInterval(() => {
    const running = tasks.value.some((t) => t.status === 'running')
    if (running) {
      loadTasks()
      loadDead()
    }
  }, 4000)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
.page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.status-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.status-left {
  display: flex;
  align-items: center;
  gap: 14px;
}

.status-hint {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.status-right {
  display: flex;
  gap: 8px;
}

/* 状态分布 */
.dist-row {
  display: flex;
  gap: 40px;
  margin-top: 18px;
  padding-top: 16px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.dist-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.dist-num {
  font-size: 22px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}

.dist-label {
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}

/* 语义色：中国习惯下「失效」用红、「正常」用绿（此处是状态而非涨跌） */
.c-alive {
  color: #22a06b;
}
.c-dead {
  color: #e34d59;
}
.c-unknown {
  color: var(--el-text-color-secondary);
}

.status-alert {
  margin-top: 16px;
}

.card-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
}

.card-title {
  font-weight: 600;
}

.card-sub {
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}

.prog-text {
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
}

.sep {
  color: var(--el-text-color-placeholder);
  margin: 0 3px;
}

.muted {
  color: var(--el-text-color-secondary);
  font-size: 12.5px;
}

.pager {
  margin-top: 14px;
  justify-content: flex-end;
}

/* 失效清单 */
.dead-list {
  display: flex;
  flex-direction: column;
  max-height: 480px;
  overflow-y: auto;
}

.dead-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.dead-item:last-child {
  border-bottom: none;
}

.dead-main {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.dead-title {
  font-size: 13.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dead-meta {
  font-size: 11.5px;
  color: var(--el-text-color-secondary);
}
</style>
