<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">转存任务</h2>
      <div>
        <el-select v-model="status" placeholder="全部状态" clearable size="default" style="width: 140px" @change="load">
          <el-option label="等待中" value="pending" />
          <el-option label="进行中" value="running" />
          <el-option label="成功" value="success" />
          <el-option label="失败" value="failed" />
          <el-option label="已取消" value="canceled" />
        </el-select>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="dialogVisible = true">
          新建转存
        </el-button>
      </div>
    </div>

    <el-alert type="info" :closable="false" class="mb-16">
      转存任务在后台异步执行。可在此查看进度、重试失败任务。
    </el-alert>

    <div class="card-box">
      <el-table :data="list" v-loading="loading" empty-text="暂无转存任务">
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="title" label="资源" min-width="200" show-overflow-tooltip />
        <el-table-column label="网盘" width="90">
          <template #default="{ row }">
            <span :class="`disk-${row.diskType}`">{{ diskName(row.diskType) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="目标目录" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="text-secondary">{{ row.targetDirName || row.targetDirId || '根目录' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="tagType(row.status)" size="small">{{ statusText(row.status) }}</el-tag>
            <el-progress
              v-if="row.status === 'running'"
              :percentage="row.progress"
              :stroke-width="4"
              :show-text="false"
              style="margin-top: 4px"
            />
          </template>
        </el-table-column>
        <el-table-column prop="message" label="信息" min-width="180" show-overflow-tooltip />
        <el-table-column label="创建时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'failed' || row.status === 'canceled'"
              link
              type="primary"
              size="small"
              @click="retry(row)"
            >
              重试
            </el-button>
            <el-button
              v-if="row.status === 'pending' || row.status === 'running'"
              link
              type="danger"
              size="small"
              @click="cancel(row)"
            >
              取消
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-16 flex justify-end">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="sizes, prev, pager, next"
          @current-change="load"
          @size-change="load"
        />
      </div>
    </div>

    <!-- 新建转存 -->
    <el-dialog v-model="dialogVisible" title="新建转存任务" width="600px">
      <el-form :model="form" label-width="100px">
        <el-form-item label="分享链接" required>
          <el-input v-model="form.shareUrl" placeholder="如：https://pan.quark.cn/s/xxxxx" />
        </el-form-item>
        <el-form-item label="提取码">
          <el-input v-model="form.passcode" placeholder="无则留空" style="width: 160px" />
        </el-form-item>
        <el-form-item label="网盘类型" required>
          <el-select v-model="form.diskType" style="width: 100%" @change="onDiskChange">
            <el-option label="夸克" value="quark" />
            <el-option label="百度" value="baidu" />
            <el-option label="UC" value="uc" />
            <el-option label="天翼" value="tianyi" />
          </el-select>
        </el-form-item>
        <el-form-item label="网盘账号">
          <el-select v-model="form.diskAccountId" placeholder="默认使用该网盘默认账号" clearable style="width: 100%">
            <el-option
              v-for="a in filteredAccounts"
              :key="a.id"
              :label="`${a.nickname}${a.isDefault ? '（默认）' : ''}`"
              :value="a.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="网盘目标目录">
          <el-input v-model="form.targetDirId" placeholder="目录 ID，留空为根目录" />
        </el-form-item>
        <el-form-item label="目录名称">
          <el-input v-model="form.targetDirName" placeholder="可选，只用于列表显示（否则列表里只有一串目录 ID）" />
        </el-form-item>
        <el-form-item label="标题">
          <el-input v-model="form.title" placeholder="可选，留空自动识别" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submit">提交</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import {  ElMessage  } from 'element-plus'
import { Refresh, Plus } from '@element-plus/icons-vue'
import { transferApi, diskApi } from '@/api'

const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const status = ref('')
const loading = ref(false)

const dialogVisible = ref(false)
const submitting = ref(false)
const accounts = ref([])

const form = reactive({
  shareUrl: '',
  passcode: '',
  diskType: 'quark',
  diskAccountId: null,
  targetDirId: '',
  // targetDirName 仅用于列表显示。这里**没有** categoryId：
  // 转存不负责资源归属，归属统一交给「资源与映射」。
  targetDirName: '',
  title: '',
})

const filteredAccounts = computed(() =>
  accounts.value.filter((a) => a.diskType === form.diskType)
)

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼' }[t] || t
}

function statusText(s) {
  return { pending: '等待中', running: '进行中', success: '成功', failed: '失败', canceled: '已取消' }[s] || s
}

function tagType(s) {
  return { success: 'success', failed: 'danger', running: 'warning', pending: 'info' }[s] || 'info'
}

function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  loading.value = true
  try {
    const res = await transferApi.tasks({
      page: page.value,
      pageSize: pageSize.value,
      status: status.value,
    })
    list.value = res.list || []
    total.value = res.total || 0
  } catch (e) {
    /* 忽略 */
  } finally {
    loading.value = false
  }
}

async function retry(row) {
  await transferApi.retry(row.id)
  ElMessage.success('已重新提交')
  load()
}

async function cancel(row) {
  await transferApi.cancel(row.id)
  ElMessage.success('已取消')
  load()
}

function onDiskChange() {
  form.diskAccountId = null
}

async function submit() {
  if (!form.shareUrl.trim()) {
    ElMessage.warning('请填写分享链接')
    return
  }
  submitting.value = true
  try {
    await transferApi.create({ ...form })
    ElMessage.success('已提交转存任务')
    dialogVisible.value = false
    form.shareUrl = ''
    form.passcode = ''
    form.title = ''
    load()
  } catch (e) {
    /* 忽略 */
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  load()
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
  setInterval(() => {
    const hasRunning = list.value.some((t) => t.status === 'running' || t.status === 'pending')
    if (hasRunning) load()
  }, 5000)
})
</script>
