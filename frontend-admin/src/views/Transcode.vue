<template>
  <div class="page">
    <!-- 引擎状态 -->
    <el-card shadow="never" class="engine-card">
      <div class="engine-row">
        <div class="engine-left">
          <el-tag :type="status.enabled ? 'success' : 'info'" size="large" effect="dark">
            {{ status.enabled ? '转码引擎就绪' : '转码未启用' }}
          </el-tag>
          <span class="engine-hint">
            同时进行 {{ status.service?.concurrency ?? 0 }} 路 · 当前运行
            {{ status.service?.running ?? 0 }} 个任务
          </span>
        </div>
        <div class="engine-right">
          <el-button :icon="Delete" :loading="gcing" @click="runGC">立即清理</el-button>
          <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
        </div>
      </div>

      <!-- 缓存占用 -->
      <div v-if="status.enabled" class="cache-row">
        <div class="cache-meta">
          <span class="cache-label">产物缓存</span>
          <span class="cache-value">{{ fmtSize(status.service?.diskUsedBytes) }}</span>
          <span class="cache-sep">/</span>
          <span class="cache-limit">{{ cacheLimitText }}</span>
        </div>
        <el-progress
          :percentage="cachePercent"
          :status="cachePercent >= 90 ? 'exception' : cachePercent >= 70 ? 'warning' : 'success'"
          :stroke-width="12"
          :show-text="false"
          class="cache-bar"
        />
        <div class="cache-tip">{{ cachePolicyText }}</div>
      </div>

      <el-alert
        v-if="!status.enabled"
        type="warning"
        :closable="false"
        show-icon
        title="转码功能未启用"
        description="请确认容器内已安装 ffmpeg，且配置项 transcode.enabled = true。未启用时，客户端仍可播放网盘直链，但无法处理 MKV/AVI 等不兼容格式。"
        class="engine-alert"
      />
    </el-card>

    <!-- 任务列表 -->
    <el-card shadow="never" class="table-card">
      <template #header>
        <div class="card-head">
          <span class="card-title">转码任务</span>
          <div class="filters">
            <el-select
              v-model="query.status"
              placeholder="全部状态"
              clearable
              style="width: 140px"
              @change="loadTasks"
            >
              <el-option label="待处理" value="pending" />
              <el-option label="进行中" value="running" />
              <el-option label="已完成" value="success" />
              <el-option label="失败" value="failed" />
              <el-option label="已取消" value="canceled" />
              <el-option label="已回收" value="evicted" />
            </el-select>
            <el-input
              v-model.number="query.resourceId"
              placeholder="按资源 ID 筛选"
              clearable
              style="width: 170px"
              @keyup.enter="loadTasks"
              @clear="loadTasks"
            />
            <el-button type="primary" @click="loadTasks">查询</el-button>
          </div>
        </div>
      </template>

      <el-table :data="tasks" v-loading="loading" stripe>
        <el-table-column prop="id" label="ID" width="80" />
        <el-table-column label="资源" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="res-title">{{ row.resourceTitle || `资源 #${row.resourceId}` }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small" effect="plain">
              {{ statusText(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="进度" width="180">
          <template #default="{ row }">
            <el-progress
              :percentage="row.progress || 0"
              :status="row.status === 'failed' ? 'exception' : undefined"
              :stroke-width="10"
            />
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="msg">{{ row.message || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="170">
          <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'success' && row.masterUrl"
              link
              type="primary"
              @click="preview(row)"
            >
              预览
            </el-button>
            <el-button
              v-if="row.status === 'running' || row.status === 'pending'"
              link
              type="warning"
              @click="cancel(row)"
            >
              取消
            </el-button>
            <el-button
              v-if="row.status !== 'running' && row.status !== 'pending'"
              link
              type="danger"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        class="pager"
        layout="total, prev, pager, next"
        :total="total"
        :current-page="query.page"
        :page-size="query.pageSize"
        @current-change="onPageChange"
      />
    </el-card>

    <!-- 预览抽屉 -->
    <el-drawer v-model="previewVisible" title="转码产物预览" size="640px">
      <video
        v-if="previewUrl"
        :src="previewUrl"
        controls
        autoplay
        playsinline
        class="preview-video"
      ></video>
      <p class="preview-url">
        <el-input v-model="previewUrl" readonly>
          <template #append>
            <el-button @click="copyUrl">复制</el-button>
          </template>
        </el-input>
      </p>
      <p class="preview-hint">
        该地址为 HLS master playlist，播放器会自动选择合适清晰度。
        直接拷贝到支持 HLS 的播放器（如 VLC / Safari）也可播放。
      </p>
    </el-drawer>
  </div>
</template>

<script setup>
import { computed, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Refresh } from '@element-plus/icons-vue'
import { transcodeApi } from '@/api'

const status = ref({ enabled: false, service: {} })
const tasks = ref([])
const total = ref(0)
const loading = ref(false)
const gcing = ref(false)

const query = reactive({
  page: 1,
  pageSize: 20,
  status: '',
  resourceId: undefined,
})

const previewVisible = ref(false)
const previewUrl = ref('')

let pollTimer = null

/* ---------- 缓存占用 ---------- */
const cacheMaxGB = computed(() => Number(status.value.service?.cacheMaxGb) || 0)

const cachePercent = computed(() => {
  const used = Number(status.value.service?.diskUsedBytes) || 0
  const limit = cacheMaxGB.value * 1024 * 1024 * 1024
  if (!limit) return 0
  return Math.min(100, Math.round((used / limit) * 100))
})

const cacheLimitText = computed(() =>
  cacheMaxGB.value > 0 ? `${cacheMaxGB.value} GB` : '未设上限',
)

const cachePolicyText = computed(() => {
  const gb = cacheMaxGB.value
  const days = Number(status.value.service?.cacheTtlDays) || 0
  const parts = []
  if (gb > 0) parts.push(`超过 ${gb} GB 时按最久未播优先淘汰`)
  if (days > 0) parts.push(`闲置超过 ${days} 天自动清理`)
  if (!parts.length) return '未配置清理策略，产物将永久驻留，建议至少设置一项上限'
  parts.push('每 6 小时自动检查一次')
  return parts.join('；')
})

function fmtSize(bytes) {
  const n = Number(bytes) || 0
  if (n <= 0) return '0 MB'
  const mb = n / (1024 * 1024)
  if (mb < 1024) return `${mb.toFixed(0)} MB`
  return `${(mb / 1024).toFixed(2)} GB`
}

async function runGC() {
  try {
    await ElMessageBox.confirm(
      '将清理闲置过久或超出容量上限的转码产物。被清理的资源不会丢失，下次播放时会自动重新转码（需等待几分钟）。是否继续？',
      '立即清理',
      { type: 'warning', confirmButtonText: '开始清理' },
    )
  } catch {
    return
  }
  gcing.value = true
  try {
    const res = await transcodeApi.gc()
    const freed = res?.freedMb ?? 0
    if (freed > 0) {
      ElMessage.success(`清理完成，释放 ${freed} MB（共 ${(res.removedByTtl || 0) + (res.removedBySize || 0)} 个产物）`)
    } else {
      ElMessage.info('当前无需清理')
    }
    if (res?.errors?.length) {
      ElMessage.warning(`有 ${res.errors.length} 项未能清理，详见后端日志`)
    }
    loadAll()
  } catch (e) {
    ElMessage.error(e?.message || '清理失败')
  } finally {
    gcing.value = false
  }
}

async function loadStatus() {
  try {
    status.value = await transcodeApi.status()
  } catch {
    status.value = { enabled: false, service: {} }
  }
}

async function loadTasks() {
  loading.value = true
  try {
    const params = { page: query.page, pageSize: query.pageSize }
    if (query.status) params.status = query.status
    if (query.resourceId) params.resourceId = query.resourceId
    const res = await transcodeApi.tasks(params)
    tasks.value = res?.list || res?.items || []
    total.value = res?.total || 0
  } catch (e) {
    ElMessage.error(e?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

function loadAll() {
  loadStatus()
  loadTasks()
}

function onPageChange(p) {
  query.page = p
  loadTasks()
}

async function cancel(row) {
  try {
    await ElMessageBox.confirm(`确定取消任务 #${row.id}？`, '取消转码', { type: 'warning' })
  } catch {
    return
  }
  try {
    await transcodeApi.cancel(row.id)
    ElMessage.success('已取消')
    loadTasks()
  } catch (e) {
    ElMessage.error(e?.message || '取消失败')
  }
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(
      `确定删除任务 #${row.id} 的记录？磁盘上的转码产物不会被删除。`,
      '删除记录',
      { type: 'warning' },
    )
  } catch {
    return
  }
  try {
    await transcodeApi.remove(row.id)
    ElMessage.success('已删除')
    loadTasks()
  } catch (e) {
    ElMessage.error(e?.message || '删除失败')
  }
}

function preview(row) {
  previewUrl.value = row.masterUrl
  previewVisible.value = true
}

async function copyUrl() {
  try {
    await navigator.clipboard.writeText(previewUrl.value)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选中')
  }
}

function statusText(s) {
  return (
    {
      pending: '待处理',
      running: '进行中',
      success: '已完成',
      failed: '失败',
      canceled: '已取消',
      evicted: '已回收',
    }[s] || s
  )
}

function statusType(s) {
  return (
    {
      pending: 'info',
      running: 'warning',
      success: 'success',
      failed: 'danger',
      canceled: 'info',
      evicted: 'info',
    }[s] || 'info'
  )
}

function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

onMounted(() => {
  loadAll()
  // 有进行中的任务时自动轮询，避免管理员盯着页面手动刷新
  pollTimer = setInterval(() => {
    const active = tasks.value.some((t) => t.status === 'running' || t.status === 'pending')
    if (active) loadTasks()
  }, 5000)
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

.engine-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.engine-left {
  display: flex;
  align-items: center;
  gap: 14px;
}

.engine-hint {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.engine-right {
  display: flex;
  gap: 8px;
}

/* 缓存占用 */
.cache-row {
  margin-top: 16px;
}

.cache-meta {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
}

.cache-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.cache-value {
  font-size: 17px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.cache-sep {
  color: var(--el-text-color-placeholder);
}

.cache-limit {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.cache-bar {
  max-width: 100%;
}

.cache-tip {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}

.engine-alert {
  margin-top: 14px;
}

.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.card-title {
  font-weight: 600;
}

.filters {
  display: flex;
  gap: 10px;
}

.res-title {
  font-weight: 500;
}

.msg {
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
}

.pager {
  margin-top: 16px;
  justify-content: flex-end;
}

.preview-video {
  width: 100%;
  border-radius: 8px;
  background: #000;
}

.preview-url {
  margin-top: 16px;
}

.preview-hint {
  font-size: 12.5px;
  color: var(--el-text-color-secondary);
  line-height: 1.7;
}
</style>
