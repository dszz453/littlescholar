<template>
  <div class="page-container">
    <div class="page-header">
      <div>
        <h2 class="page-title">知识沉淀</h2>
        <div class="page-sub">
          小书童每天把目录里的内容读成一段人话，孩子点进目录就能看到「这里有什么、先学什么」。
          内容没变化的目录会自动跳过，不消耗 AI 额度。
        </div>
      </div>
      <div class="head-actions">
        <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
        <el-button
          type="primary"
          :icon="MagicStick"
          :loading="refreshingAll"
          @click="refreshAll"
        >
          全部重新生成
        </el-button>
      </div>
    </div>

    <el-alert
      v-if="!loading && !list.length"
      type="info"
      show-icon
      :closable="false"
      title="还没有任何沉淀"
      description="沉淀由每天一次的定时任务生成，也可以点右上角「全部重新生成」立刻跑一轮。前提是需要先在「AI 配置」里配好可用的接口。"
      class="tip"
    />

    <el-table v-loading="loading" :data="list" stripe>
      <el-table-column label="目录" min-width="210">
        <template #default="{ row }">
          <div class="cat-name">
            {{ row.categoryId === 0 ? '🏠 整个学习乐园' : row.categoryName }}
          </div>
          <div class="cat-crumb">{{ row.breadcrumb || '—' }}</div>
        </template>
      </el-table-column>

      <el-table-column label="小书童说" min-width="420">
        <template #default="{ row }">
          <div v-if="row.intro" class="intro">{{ row.intro }}</div>
          <div v-else class="intro muted">（还没有内容）</div>
          <div v-if="row.points && row.points.length" class="points">
            <el-tag
              v-for="p in row.points"
              :key="p"
              size="small"
              effect="plain"
              class="point-tag"
            >{{ p }}</el-tag>
          </div>
          <div v-if="row.keywords && row.keywords.length" class="keywords">
            <span class="kw-label">关键词</span>
            <span v-for="k in row.keywords" :key="k" class="kw">{{ k }}</span>
          </div>
        </template>
      </el-table-column>

      <el-table-column prop="resourceCount" label="内容数" width="90" align="right" />

      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tooltip
            :disabled="!row.lastError"
            :content="row.lastError"
            placement="top"
          >
            <el-tag :type="row.status === 'ready' ? 'success' : 'danger'" size="small">
              {{ row.status === 'ready' ? '已生成' : '生成失败' }}
            </el-tag>
          </el-tooltip>
        </template>
      </el-table-column>

      <el-table-column label="更新时间" width="160">
        <template #default="{ row }">
          {{ fmtTime(row.generatedAt || row.updatedAt) }}
        </template>
      </el-table-column>

      <el-table-column label="操作" width="110" fixed="right">
        <template #default="{ row }">
          <el-button
            v-if="row.categoryId !== 0"
            link
            type="primary"
            :loading="busyId === row.categoryId"
            @click="refreshOne(row)"
          >重新生成</el-button>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { MagicStick, Refresh } from '@element-plus/icons-vue'
import { digestApi } from '@/api'

const list = ref([])
const loading = ref(false)
const refreshingAll = ref(false)
const busyId = ref(null)

async function load() {
  loading.value = true
  try {
    const data = await digestApi.list()
    list.value = data || []
  } catch {
    list.value = []
  } finally {
    loading.value = false
  }
}

async function refreshOne(row) {
  busyId.value = row.categoryId
  try {
    // force=true：用户点了「重新生成」就是明确要求重算，
    // 不该因为指纹没变而被跳过（否则点了没反应，像坏了）
    const res = await digestApi.refresh(row.categoryId, true)
    ElMessage.success(res?.generated ? '已重新生成' : 'AI 没有返回内容，请检查 AI 配置')
    await load()
  } catch {
    /* 拦截器已提示 */
  } finally {
    busyId.value = null
  }
}

async function refreshAll() {
  refreshingAll.value = true
  try {
    await digestApi.refresh(0, false)
    ElMessage.success('已开始全量刷新，几分钟后回来刷新本页查看')
  } catch {
    /* 拦截器已提示 */
  } finally {
    refreshingAll.value = false
  }
}

function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

onMounted(load)
</script>

<style scoped>
.page-sub {
  margin-top: 6px;
  max-width: 720px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--ls-text-secondary);
}

.head-actions {
  flex: 0 0 auto;
}

.tip {
  margin-bottom: 16px;
}

.cat-name {
  font-weight: 600;
}
.cat-crumb {
  margin-top: 2px;
  font-size: 12px;
  color: var(--ls-text-secondary);
}

.intro {
  font-size: 13px;
  line-height: 1.6;
}
.intro.muted,
.muted {
  color: var(--ls-text-secondary);
}

.points {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.point-tag {
  max-width: 100%;
}

.keywords {
  margin-top: 6px;
  font-size: 12px;
  color: var(--ls-text-secondary);
}
.kw-label {
  margin-right: 6px;
}
.kw {
  margin-right: 8px;
  color: #4a90d9;
}
</style>
