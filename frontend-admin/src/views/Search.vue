<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源搜索</h2>
      <div class="flex items-center">
        <el-tag :type="health.pansouOnline ? 'success' : 'danger'" size="small" class="mr-8">
          PanSou {{ health.pansouOnline ? '在线' : '离线' }}
          <template v-if="health.plugins"> · {{ health.plugins }} 个插件</template>
        </el-tag>
        <el-button :icon="Refresh" size="small" @click="checkHealth">检测</el-button>
      </div>
    </div>

    <!-- 搜索区 -->
    <div class="card-box">
      <div class="search-row">
        <el-input
          v-model="keyword"
          size="large"
          placeholder="输入关键词搜索网盘资源，如：新概念英语"
          clearable
          @keyup.enter="doSearch"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button type="primary" size="large" :loading="loading" @click="doSearch">
          搜 索
        </el-button>
      </div>

      <div class="filter-row">
        <span class="filter-label">网盘类型：</span>
        <el-checkbox-group v-model="diskTypes" size="small">
          <el-checkbox-button v-for="d in diskOptions" :key="d.value" :value="d.value">
            {{ d.label }}
          </el-checkbox-button>
        </el-checkbox-group>

        <el-divider direction="vertical" />

        <el-checkbox v-model="checkAlive" size="small">搜索时检测存活</el-checkbox>

        <el-divider direction="vertical" />

        <span class="filter-label">结果数：</span>
        <el-select v-model="pageSize" size="small" style="width: 100px">
          <el-option :value="20" label="20 条" />
          <el-option :value="50" label="50 条" />
          <el-option :value="100" label="100 条" />
        </el-select>
      </div>
    </div>

    <!-- 结果区 -->
    <div class="card-box mt-16" v-if="searched">
      <div class="flex justify-between items-center mb-16">
        <div>
          <span class="result-count">
            共找到 <b>{{ total }}</b> 条结果
            <span class="text-secondary">（耗时 {{ costMs }}ms）</span>
          </span>
        </div>
        <div>
          <el-button
            size="small"
            :disabled="!selected.length"
            @click="openImportDialog"
          >
            导入选中资源（{{ selected.length }}）
          </el-button>
          <el-button
            size="small"
            type="primary"
            :disabled="!selected.length"
            @click="openTransferDialog"
          >
            转存选中资源（{{ selected.length }}）
          </el-button>
        </div>
      </div>

      <el-table
        :data="results"
        size="small"
        v-loading="loading"
        @selection-change="onSelectionChange"
        empty-text="没有搜索到资源"
      >
        <el-table-column type="selection" width="45" />
        <el-table-column label="资源名称" min-width="280">
          <template #default="{ row }">
            <div class="res-title" @click="openLink(row.url)">
              {{ row.note || '（无标题）' }}
            </div>
            <div class="res-url">{{ row.url }}</div>
          </template>
        </el-table-column>
        <el-table-column label="网盘" width="90">
          <template #default="{ row }">
            <span :class="`disk-${row.diskType}`">{{ diskName(row.diskType) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="提取码" width="80">
          <template #default="{ row }">
            <el-tag v-if="row.password" size="small" type="info">{{ row.password }}</el-tag>
            <span v-else class="text-secondary">-</span>
          </template>
        </el-table-column>
        <el-table-column label="存活" width="90">
          <template #default="{ row }">
            <el-tag v-if="row.alive" :type="aliveTagType(row.alive)" size="small">
              {{ aliveText(row.alive) }}
            </el-tag>
            <span v-else class="text-secondary">未检测</span>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="130" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="text-secondary">{{ row.source }}</span>
          </template>
        </el-table-column>
        <el-table-column label="更新时间" width="110">
          <template #default="{ row }">
            <span class="text-secondary">{{ formatDate(row.datetime) }}</span>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-16 flex justify-between items-center">
        <el-button
          size="small"
          :disabled="page === 1"
          @click="changePage(page - 1)"
        >
          上一页
        </el-button>
        <span class="text-secondary">第 {{ page }} 页</span>
        <el-button
          size="small"
          :disabled="results.length < pageSize"
          @click="changePage(page + 1)"
        >
          下一页
        </el-button>
      </div>
    </div>

    <!-- 导入对话框 -->
    <el-dialog v-model="importDialog" title="导入资源到目录" width="560px">
      <el-form label-width="90px">
        <el-form-item label="目标目录" required>
          <el-tree-select
            v-model="importForm.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            placeholder="请选择目录"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="资源数量">
          <span>{{ selected.length }} 个资源将被导入</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importDialog = false">取消</el-button>
        <el-button type="primary" :loading="importing" @click="confirmImport">
          确认导入
        </el-button>
      </template>
    </el-dialog>

    <!-- 转存对话框 -->
    <el-dialog v-model="transferDialog" title="转存到网盘" width="620px">
      <el-form label-width="100px">
        <el-form-item label="网盘账号" required>
          <el-select v-model="transferForm.diskAccountId" placeholder="选择网盘账号" style="width: 100%">
            <el-option
              v-for="a in accounts"
              :key="a.id"
              :label="`${a.nickname}（${diskName(a.diskType)}）`"
              :value="a.id"
            />
          </el-select>
          <div class="text-secondary mt-8" v-if="!accounts.length">
            暂无网盘账号，请先到「网盘账号」页面绑定
          </div>
        </el-form-item>
        <el-form-item label="网盘目录">
          <div class="disk-browser">
            <el-input v-model="transferForm.targetDirId" placeholder="默认为根目录，可留空">
              <template #append>
                <el-button @click="openBrowser">浏览</el-button>
              </template>
            </el-input>
          </div>
        </el-form-item>
        <el-form-item label="资源归属目录">
          <el-tree-select
            v-model="transferForm.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            placeholder="转存成功后资源归入哪个目录（可选）"
            clearable
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="待转存">
          <span>{{ selected.length }} 个资源</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="transferDialog = false">取消</el-button>
        <el-button type="primary" :loading="transferring" @click="confirmTransfer">
          提交转存
        </el-button>
      </template>
    </el-dialog>

    <!-- 网盘目录浏览对话框 -->
    <el-dialog v-model="browserDialog" title="选择网盘目录" width="560px">
      <div class="browser-path">
        <el-button size="small" :disabled="!browserStack.length" @click="browserBack">
          返回上级
        </el-button>
        <span class="text-secondary">{{ browserPath }}</span>
      </div>
      <el-table :data="browserItems" size="small" height="320" @row-click="enterDir">
        <el-table-column label="名称" min-width="200">
          <template #default="{ row }">
            <span>
              <el-icon v-if="row.isDir"><Folder /></el-icon>
              <el-icon v-else><Document /></el-icon>
              {{ row.name }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">{{ row.isDir ? '文件夹' : '文件' }}</template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="browserDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmBrowserDir">选择当前目录</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import {  ElMessage  } from 'element-plus'
import { Search, Refresh, Folder, Document } from '@element-plus/icons-vue'
import { searchApi, categoryApi, diskApi, resourceApi, transferApi } from '@/api'

const keyword = ref('')
const results = ref([])
const total = ref(0)
const costMs = ref(0)
const page = ref(1)
const pageSize = ref(20)
const searched = ref(false)
const loading = ref(false)
const checkAlive = ref(false)
const health = ref({})

const diskOptions = [
  { label: '夸克', value: 'quark' },
  { label: '百度', value: 'baidu' },
  { label: 'UC', value: 'uc' },
  { label: '天翼', value: 'tianyi' },
  { label: '阿里', value: 'aliyun' },
  { label: '迅雷', value: 'xunlei' },
  { label: '123', value: '123' },
  { label: '移动', value: 'mobile' },
  { label: '115', value: '115' },
]
const diskTypes = ref(['quark', 'baidu', 'uc', 'tianyi'])

const selected = ref([])
const categoryTree = ref([])
const accounts = ref([])

const importDialog = ref(false)
const importForm = ref({ categoryId: null })
const importing = ref(false)

const transferDialog = ref(false)
const transferForm = ref({ diskAccountId: null, targetDirId: '', categoryId: null })
const transferring = ref(false)

const browserDialog = ref(false)
const browserItems = ref([])
const browserStack = ref([])
const browserPath = ref('根目录')

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里', xunlei: '迅雷', 123: '123', mobile: '移动', 115: '115' }[t] || t
}

function aliveText(s) {
  return { ok: '有效', bad: '失效', locked: '需密码', unsupported: '不支持', uncertain: '不确定' }[s] || s
}

function aliveTagType(s) {
  return { ok: 'success', bad: 'danger', locked: 'warning', unsupported: 'info', uncertain: 'info' }[s] || 'info'
}

function formatDate(d) {
  if (!d) return '-'
  try {
    return new Date(d).toLocaleDateString('zh-CN')
  } catch (e) {
    return '-'
  }
}

function openLink(url) {
  window.open(url, '_blank')
}

async function doSearch(resetPage = true) {
  if (!keyword.value.trim()) {
    ElMessage.warning('请输入搜索关键词')
    return
  }
  if (resetPage) page.value = 1
  loading.value = true
  try {
    const res = await searchApi.search({
      keyword: keyword.value.trim(),
      diskTypes: diskTypes.value,
      checkAlive: checkAlive.value,
      page: page.value,
      pageSize: pageSize.value,
    })
    results.value = res.list || []
    total.value = res.total || 0
    costMs.value = res.costMs || 0
    searched.value = true
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function changePage(p) {
  page.value = p
  doSearch(false)
}

function onSelectionChange(rows) {
  selected.value = rows
}

async function checkHealth() {
  try {
    health.value = await searchApi.health()
    ElMessage.success(health.value.pansouOnline ? 'PanSou 在线' : 'PanSou 离线')
  } catch (e) {
    /* 忽略 */
  }
}

async function loadCategories() {
  try {
    categoryTree.value = await categoryApi.tree(false)
  } catch (e) {
    /* 忽略 */
  }
}

async function loadAccounts() {
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
}

function openImportDialog() {
  if (!categoryTree.value.length) {
    ElMessage.warning('请先创建目录')
    return
  }
  importForm.value.categoryId = null
  importDialog.value = true
}

async function confirmImport() {
  if (!importForm.value.categoryId) {
    ElMessage.warning('请选择目标目录')
    return
  }
  importing.value = true
  try {
    const items = selected.value.map((r) => ({
      title: r.note,
      diskType: r.diskType,
      shareUrl: r.url,
      passcode: r.password,
      note: r.note,
    }))
    const res = await resourceApi.import({
      categoryId: importForm.value.categoryId,
      items,
    })
    ElMessage.success(`导入完成：成功 ${res.created} 个，跳过 ${res.skipped} 个`)
    importDialog.value = false
  } catch (e) {
    /* 忽略 */
  } finally {
    importing.value = false
  }
}

async function openTransferDialog() {
  await loadAccounts()
  if (!accounts.value.length) {
    ElMessage.warning('请先到「网盘账号」页面绑定网盘账号')
    return
  }
  transferForm.value = { diskAccountId: null, targetDirId: '', categoryId: null }
  transferDialog.value = true
}

async function confirmTransfer() {
  if (!transferForm.value.diskAccountId) {
    ElMessage.warning('请选择网盘账号')
    return
  }
  transferring.value = true
  try {
    const items = selected.value.map((r) => ({
      shareUrl: r.url,
      passcode: r.password,
      diskType: r.diskType,
      title: r.note,
    }))
    const res = await transferApi.batch({
      diskAccountId: transferForm.value.diskAccountId,
      targetDirId: transferForm.value.targetDirId,
      categoryId: transferForm.value.categoryId || 0,
      items,
    })
    ElMessage.success(`已提交 ${res.count} 个转存任务，可在「转存任务」页面查看进度`)
    transferDialog.value = false
  } catch (e) {
    /* 忽略 */
  } finally {
    transferring.value = false
  }
}

async function openBrowser() {
  if (!transferForm.value.diskAccountId) {
    ElMessage.warning('请先选择网盘账号')
    return
  }
  browserStack.value = []
  browserPath.value = '根目录'
  await loadBrowserDir('')
  browserDialog.value = true
}

async function loadBrowserDir(dirId) {
  try {
    const res = await diskApi.browse(transferForm.value.diskAccountId, dirId)
    browserItems.value = (res.items || []).filter((i) => i.isDir)
  } catch (e) {
    browserItems.value = []
  }
}

function enterDir(row) {
  if (!row.isDir) return
  browserStack.value.push({ id: currentDirId(), name: row.name })
  browserPath.value = browserStack.value.map((s) => s.name).join(' / ')
  loadBrowserDir(row.id)
}

function browserBack() {
  browserStack.value.pop()
  browserPath.value = browserStack.value.length
    ? browserStack.value.map((s) => s.name).join(' / ')
    : '根目录'
  loadBrowserDir(currentDirId())
}

function currentDirId() {
  return browserStack.value.length ? browserStack.value[browserStack.value.length - 1].id : ''
}

function confirmBrowserDir() {
  transferForm.value.targetDirId = currentDirId()
  browserDialog.value = false
}

onMounted(() => {
  checkHealth()
  loadCategories()
})
</script>

<style scoped>
.search-row {
  display: flex;
  gap: 12px;
}

.filter-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 14px;
}

.filter-label {
  font-size: 13px;
  color: var(--ls-text-secondary);
}

.result-count {
  font-size: 14px;
}

.res-title {
  font-weight: 500;
  cursor: pointer;
  color: var(--ls-primary);
}

.res-title:hover {
  text-decoration: underline;
}

.res-url {
  font-size: 12px;
  color: var(--ls-text-secondary);
  word-break: break-all;
  margin-top: 2px;
}

.browser-path {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
}
</style>
