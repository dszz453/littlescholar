<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">概览</h2>
      <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
    </div>

    <!-- 统计卡片 -->
    <el-row :gutter="16">
      <el-col :span="6" v-for="s in statCards" :key="s.label">
        <div class="card-box stat-card">
          <div class="stat-icon" :style="{ background: s.color }">{{ s.icon }}</div>
          <div class="stat-body">
            <div class="stat-value">{{ s.value }}</div>
            <div class="stat-label">{{ s.label }}</div>
          </div>
        </div>
      </el-col>
    </el-row>

    <el-row :gutter="16" class="mt-16">
      <!-- 系统状态 -->
      <el-col :span="12">
        <div class="card-box">
          <h3 class="section-title">系统状态</h3>
          <el-descriptions :column="2" border>
            <!-- 版本 / 构建放在同一项里，凑满 2 列布局，不留半行空格。
                 「构建」= 这一版服务端二进制是从哪次提交构建出来的：
                 版本号只能说明"哪一版"，改了却忘了升版本号时就对不上；
                 commit 是客观的，所以它是"线上跑的对不对"最硬的证据。
                 本地 go run 与旧镜像没有注入，只显示版本号，不是错误。 -->
            <el-descriptions-item label="版本">
              <span class="ver">v{{ status.version || '…' }}</span>
              <span v-if="status.commit || status.buildTime" class="build">{{ buildLabel(status) }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="运行时长">{{ formatUptime(status.uptime) }}</el-descriptions-item>
            <el-descriptions-item label="数据库">
              <el-tag :type="status.dbOnline ? 'success' : 'danger'" size="small">
                {{ status.dbOnline ? '正常' : '异常' }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="PanSou 搜索">
              <el-tag :type="status.pansouOnline ? 'success' : 'danger'" size="small">
                {{ status.pansouOnline ? '在线' : '离线' }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="转存队列">{{ status.transferQueue }}</el-descriptions-item>
            <el-descriptions-item label="待处理任务">{{ status.pendingTasks }}</el-descriptions-item>
            <el-descriptions-item label="内存占用">{{ status.memoryMB }} MB</el-descriptions-item>
            <el-descriptions-item label="协程数">{{ status.goroutines }}</el-descriptions-item>
          </el-descriptions>
        </div>
      </el-col>

      <!-- 快捷入口 -->
      <el-col :span="12">
        <div class="card-box">
          <h3 class="section-title">快捷入口</h3>
          <div class="quick-grid">
            <div
              v-for="q in quickLinks"
              :key="q.path"
              class="quick-item"
              @click="$router.push(q.path)"
            >
              <div class="quick-icon">{{ q.icon }}</div>
              <div class="quick-title">{{ q.title }}</div>
              <div class="quick-desc">{{ q.desc }}</div>
            </div>
          </div>
        </div>
      </el-col>
    </el-row>

    <!-- 最近转存任务 -->
    <div class="card-box mt-16">
      <div class="flex justify-between items-center mb-16">
        <h3 class="section-title" style="margin: 0">最近转存任务</h3>
        <el-button text type="primary" @click="$router.push('/transfer')">
          查看全部
        </el-button>
      </div>
      <el-table :data="recentTasks" size="small" empty-text="暂无转存任务">
        <el-table-column prop="title" label="资源" min-width="180" show-overflow-tooltip />
        <el-table-column prop="diskType" label="网盘" width="100">
          <template #default="{ row }">
            <span :class="`disk-${row.diskType}`">{{ diskName(row.diskType) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="status" label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="taskTagType(row.status)" size="small">
              {{ taskStatusText(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="message" label="信息" min-width="200" show-overflow-tooltip />
        <el-table-column prop="createdAt" label="创建时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { systemApi, transferApi } from '@/api'
import { buildLabel } from '@/utils/buildinfo'

const status = ref({})
const recentTasks = ref([])

const statCards = computed(() => [
  { label: '用户数', value: status.value.userCount ?? '-', icon: '👤', color: '#4a90d9' },
  { label: '资源数', value: status.value.resourceCount ?? '-', icon: '📦', color: '#5ad8a6' },
  { label: '目录数', value: status.value.categoryCount ?? '-', icon: '📁', color: '#f6bd16' },
  { label: '待处理任务', value: status.value.pendingTasks ?? '-', icon: '⚙️', color: '#d9554a' },
])

const quickLinks = [
  { path: '/search', icon: '🔍', title: '资源搜索', desc: '搜索各大网盘资源' },
  { path: '/disk-accounts', icon: '☁️', title: '网盘账号', desc: '扫码绑定网盘' },
  { path: '/categories', icon: '🗂️', title: '目录管理', desc: '维护资源目录树' },
  { path: '/study', icon: '📊', title: '学习分析', desc: 'AI 学情报告' },
]

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里' }[t] || t
}

function taskStatusText(s) {
  return {
    pending: '等待中',
    running: '进行中',
    success: '成功',
    failed: '失败',
    canceled: '已取消',
  }[s] || s
}

function taskTagType(s) {
  return { success: 'success', failed: 'danger', running: 'warning', pending: 'info' }[s] || 'info'
}

function formatUptime(sec) {
  if (!sec) return '-'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}天 ${h}小时`
  if (h > 0) return `${h}小时 ${m}分`
  return `${m}分钟`
}

function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function loadAll() {
  try {
    status.value = await systemApi.status()
  } catch (e) {
    /* 忽略 */
  }
  try {
    const res = await transferApi.tasks({ page: 1, pageSize: 5 })
    recentTasks.value = res.list || []
  } catch (e) {
    /* 忽略 */
  }
}

onMounted(loadAll)
</script>

<style scoped>
.stat-card {
  display: flex;
  align-items: center;
  gap: 14px;
}

.stat-icon {
  width: 52px;
  height: 52px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 24px;
  flex-shrink: 0;
}

.stat-value {
  font-size: 26px;
  font-weight: 600;
  line-height: 1.2;
}

.stat-label {
  font-size: 13px;
  color: var(--ls-text-secondary);
}

.ver {
  font-weight: 600;
}

/* 构建信息是次要信息，压成小字挂在版本号后面，不抢眼但随时可读 */
.build {
  margin-left: 6px;
  font-size: 12px;
  color: var(--ls-text-secondary);
}

.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px;
}

.quick-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}

.quick-item {
  border: 1px solid var(--ls-border);
  border-radius: 10px;
  padding: 14px;
  cursor: pointer;
  transition: all 0.2s;
}

.quick-item:hover {
  border-color: var(--ls-primary);
  background: #f0f7ff;
  transform: translateY(-2px);
}

.quick-icon {
  font-size: 22px;
}

.quick-title {
  font-weight: 600;
  margin: 6px 0 2px;
}

.quick-desc {
  font-size: 12px;
  color: var(--ls-text-secondary);
}
</style>
