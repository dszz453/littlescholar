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

        <el-checkbox v-model="checkAlive" size="small">搜索时检测存活（PanCheck 实时检测）</el-checkbox>

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
            <el-input
              v-model="transferForm.targetDirName"
              readonly
              placeholder="根目录"
            >
              <template #append>
                <el-button @click="openBrowser">浏览</el-button>
              </template>
            </el-input>
            <div class="text-secondary mt-8">
              <template v-if="transferForm.targetDirId">
                将转存到该目录
                <el-button link type="primary" size="small" @click="clearTargetDir">
                  改回根目录
                </el-button>
              </template>
              <template v-else>未选择目录，将转存到网盘根目录</template>
            </div>
          </div>
        </el-form-item>
        <el-form-item label="待转存">
          <span>{{ selected.length }} 个资源</span>
        </el-form-item>
        <el-form-item label=" ">
          <span class="text-secondary">
            转存只负责把文件放进网盘目录。要让孩子端看到，请到「资源与映射」
            把这个目录映射成本地目录。
          </span>
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
        <div class="browser-footer">
          <span class="text-secondary">
            当前目录：<b>{{ browserPath }}</b>
          </span>
          <div>
            <el-button @click="browserDialog = false">取消</el-button>
            <el-button type="primary" @click="confirmBrowserDir">转存到该目录</el-button>
          </div>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import {  ElMessage  } from 'element-plus'
import { Search, Refresh, Folder, Document } from '@element-plus/icons-vue'
import { searchApi, diskApi, transferApi } from '@/api'

const keyword = ref('')
const results = ref([])
const total = ref(0)
const costMs = ref(0)
const page = ref(1)
const pageSize = ref(20)
const searched = ref(false)
const loading = ref(false)
const checkAlive = ref(true)
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
const accounts = ref([])

const transferDialog = ref(false)
// targetDirId 是网盘侧的目录 ID（发给后端），targetDirName 只是给人看的名。
//
// 为什么要拆成两个字段：网盘目录 ID 是一串无意义的字符（夸克是形如
// 3f2a… 的 fid），直接把它显示在输入框里，用户根本无从确认"我到底选到哪了"，
// 也就没人能一眼看出"选了目录却转存到根目录"这个 bug。
//
// 这里**没有** categoryId：转存不负责"资源归属"。归属（账号 + 目录 → 本地分类）
// 统一由「资源与映射」负责，转存只把文件放进网盘目录。
const transferForm = ref({ diskAccountId: null, targetDirId: '', targetDirName: '' })
const transferring = ref(false)

const browserDialog = ref(false)
const browserItems = ref([])
// browserStack 存的是**已经进入过**的那一串目录，元素格式 {id, name}。
//
// 这里是本 bug 的根因所在：原实现 push 的是 `currentDirId()`（即进入前的
// 父目录 id）配上子目录的 name，于是栈里每一项的 id 都指向上一级，
// 最深一项的 id 恒为 '' —— 无论点进多少层，currentDirId() 都返回空串，
// 转存自然全部落到根目录。
const browserStack = ref([])

// 当前所在目录 ID（'' = 根目录）。与「资源与映射」「资源列表」里的
// 同名函数保持一致取法：栈顶就是当前目录。
function currentDirId() {
  return browserStack.value.length ? browserStack.value[browserStack.value.length - 1].id : ''
}

// 路径面包屑：根目录 / A / B
const browserPath = computed(() =>
  browserStack.value.length
    ? ['根目录', ...browserStack.value.map((s) => s.name)].join(' / ')
    : '根目录'
)

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里', xunlei: '迅雷', 123: '123', mobile: '移动', 115: '115' }[t] || t
}

function aliveText(s) {
  return { ok: '有效', bad: '失效', locked: '需密码', unsupported: '不支持', uncertain: '未检出' }[s] || s
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

async function loadAccounts() {
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
}

async function openTransferDialog() {
  await loadAccounts()
  if (!accounts.value.length) {
    ElMessage.warning('请先到「网盘账号」页面绑定网盘账号')
    return
  }
  transferForm.value = { diskAccountId: null, targetDirId: '', targetDirName: '' }
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
      targetDirName: transferForm.value.targetDirName,
      items,
    })
    // 提示里点明"下一步去哪"，因为转存完孩子端还看不到东西 —— 还得去建映射。
    ElMessage.success(
      `已提交 ${res.count} 个转存任务，可在「转存任务」页面查看进度；` +
        '转存完成后到「资源与映射」把这个目录映射成本地目录，孩子端即可看到'
    )
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
  // 入栈的是**被进入的目录自己**的 id，不是父目录的。
  browserStack.value.push({ id: row.id, name: row.name })
  loadBrowserDir(row.id)
}

function browserBack() {
  if (!browserStack.value.length) return
  browserStack.value.pop()
  loadBrowserDir(currentDirId())
}

function confirmBrowserDir() {
  transferForm.value.targetDirId = currentDirId()
  // 面包屑去掉最前面的「根目录」前缀，得到的正好是 根目录 / A / B 的
  // 「A / B」；在根目录时栈为空，显示「根目录」。
  transferForm.value.targetDirName = browserStack.value.length
    ? browserStack.value.map((s) => s.name).join(' / ')
    : '根目录'
  browserDialog.value = false
}

// 恢复到"不指定目录 → 转存到根目录"
function clearTargetDir() {
  transferForm.value.targetDirId = ''
  transferForm.value.targetDirName = ''
}

onMounted(() => {
  checkHealth()
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

/* 底部左边写清"当前目录是谁"，右边才是按钮 —— 转存到哪必须一眼可验，
   否则用户只能靠"转完了去看网盘"来发现问题。 */
.browser-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.browser-footer span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
