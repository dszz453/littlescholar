<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源列表</h2>
      <div>
        <el-button :icon="Refresh" @click="reload">刷新</el-button>
      </div>
    </div>

    <el-row :gutter="16">
      <!-- 左：接入源 -->
      <el-col :span="5">
        <div class="card-box src-list">
          <div class="src-list-title">
            接入源
            <el-button link type="primary" size="small" :icon="Refresh" @click="loadAccounts" />
          </div>
          <el-scrollbar height="600px">
            <div
              v-for="a in accounts"
              :key="a.id"
              class="src-item"
              :class="{ active: a.id === accountId }"
              @click="selectAccount(a)"
            >
              <el-icon class="src-ico"><Folder /></el-icon>
              <div class="src-meta">
                <div class="src-name" :title="a.nickname">{{ a.nickname }}</div>
                <div class="src-type">
                  <el-tag size="small" :type="a.isStorage ? 'info' : 'primary'" effect="plain">
                    {{ diskName(a.diskType) }}
                  </el-tag>
                </div>
              </div>
              <!-- 状态标：
                   只有「确定失效」才标红。未检测过的账号不该被当成坏的 ——
                   最早的版本只要 status !== valid 就显示"失效"，
                   于是老数据（状态字段为空）全都挂着红标，
                   实际却能正常浏览。 -->
              <el-tag
                v-if="a.status === 'invalid'"
                size="small"
                type="danger"
                class="src-state"
                @click.stop="recheck(a)"
              >
                失效 · 重检
              </el-tag>
              <el-tag
                v-else-if="a.status !== 'valid'"
                size="small"
                type="info"
                class="src-state"
                @click.stop="recheck(a)"
              >
                待检测
              </el-tag>
            </div>
            <el-empty
              v-if="!accounts.length"
              description="还没有接入源，请先到「资源接入」添加"
              :image-size="60"
            />
          </el-scrollbar>
        </div>
      </el-col>

      <!-- 右：平铺文件夹 -->
      <el-col :span="19">
        <div class="card-box">
          <div class="browse-bar">
            <div class="crumbs">
              <el-button size="small" :icon="ArrowLeft" :disabled="!stack.length" @click="goUp">
                上级
              </el-button>
              <el-breadcrumb separator="/" class="ml-8">
                <el-breadcrumb-item>
                  <a @click="loadDir('')">根目录</a>
                </el-breadcrumb-item>
                <el-breadcrumb-item v-for="(s, i) in stack" :key="s.id">
                  <a @click="jumpTo(i)">{{ s.name }}</a>
                </el-breadcrumb-item>
              </el-breadcrumb>
            </div>
            <div class="browse-ops">
              <el-button size="small" :icon="FolderAdd" :disabled="!accountId" @click="openMkdir">
                新建文件夹
              </el-button>
              <el-button
                size="small"
                type="primary"
                plain
                :disabled="!picked.length"
                @click="openImport"
              >
                导入为资源
              </el-button>
              <el-button
                size="small"
                type="success"
                plain
                :disabled="!accountId"
                @click="openMapping"
              >
                建立映射
              </el-button>
              <el-button size="small" :icon="Files" @click="openLib">已收录资源</el-button>
            </div>
          </div>

          <!-- 平铺网格：文件夹在前、文件在后 -->
          <div v-loading="loading" class="tiles">
            <div
              v-for="row in sortedItems"
              :key="row.id"
              class="tile"
              :class="{ dir: row.isDir, on: pickedIds.includes(row.id) }"
              @click="onTileClick(row)"
            >
              <div class="tile-check" @click.stop="togglePick(row)">
                <el-icon v-if="pickedIds.includes(row.id)"><Select /></el-icon>
              </div>

              <div class="tile-ico" :class="row.isDir ? 'is-dir' : 'is-file'">
                <el-icon><component :is="row.isDir ? Folder : Document" /></el-icon>
              </div>

              <div class="tile-name" :title="row.name">{{ row.name }}</div>
              <div class="tile-meta">
                {{ row.isDir ? '文件夹' : formatSize(row.size) }}
              </div>

              <div class="tile-ops" @click.stop>
                <el-tooltip content="重命名" placement="top">
                  <el-button link size="small" :icon="EditPen" @click="rename(row)" />
                </el-tooltip>
                <el-tooltip content="导入为资源" placement="top">
                  <el-button link size="small" :icon="Download" @click="importOne(row)" />
                </el-tooltip>
                <el-tooltip content="删除" placement="top">
                  <el-button link size="small" type="danger" :icon="Delete" @click="removeItem(row)" />
                </el-tooltip>
              </div>
            </div>

            <el-empty
              v-if="!loading && !items.length"
              :description="accountId ? '这个文件夹是空的' : '请先在左侧选择一个接入源'"
            />
          </div>

          <div class="mt-12 text-secondary">
            共 {{ items.length }} 项
            <span v-if="picked.length">，已选 {{ picked.length }} 项</span>
            <span v-if="accountId" class="ml-8">· 当前路径：{{ currentDirLabel }}</span>
          </div>
        </div>
      </el-col>
    </el-row>

    <!-- 已收录资源：从"目录视图"切到"库视图"看系统里到底登记了什么 -->
    <el-drawer v-model="libDrawer" title="已收录的资源" size="660px">
      <div class="lib-bar">
        <el-input
          v-model="libKeyword"
          placeholder="搜索标题"
          clearable
          style="width: 220px"
          @keyup.enter="loadLib"
        />
        <el-button :icon="Refresh" @click="loadLib">刷新</el-button>
      </div>
      <el-table
        :data="libItems"
        v-loading="libLoading"
        size="small"
        empty-text="还没有收录任何资源"
      >
        <el-table-column prop="title" label="标题" min-width="240" show-overflow-tooltip />
        <el-table-column label="类型" width="90">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ row.type || '-' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="danger" size="small" @click="removeLib(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="mt-12 text-secondary">共 {{ libTotal }} 个</div>
    </el-drawer>

    <!-- 新建文件夹 -->
    <el-dialog v-model="mkdirDialog" title="新建文件夹" width="440px">
      <el-form label-width="80px">
        <el-form-item label="名称">
          <el-input v-model="mkdirName" placeholder="例如：汪汪队立大功" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="mkdirDialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doMkdir">创建</el-button>
      </template>
    </el-dialog>

    <!-- 导入为资源 -->
    <el-dialog v-model="importDialog" title="导入为系统资源" width="520px">
      <el-alert type="info" :closable="false" class="mb-12">
        把接入源里<b>已有</b>的文件登记成孩子端可播的资源（不复制文件）。
      </el-alert>
      <el-form label-width="90px">
        <el-form-item label="系统目录" required>
          <el-tree-select
            v-model="importCategoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            placeholder="选择要归入的系统目录"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="递归子目录">
          <el-switch v-model="importRecursive" />
        </el-form-item>
        <el-form-item label="导入对象">
          <span class="text-secondary">{{ importTargets.length }} 项</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importDialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="doImport">开始导入</el-button>
      </template>
    </el-dialog>

    <!-- 建立映射 -->
    <el-dialog v-model="mappingDialog" title="建立资源映射" width="560px">
      <el-alert type="info" :closable="false" class="mb-12">
        把「当前接入源文件夹」绑定到系统目录，之后一次同步即可批量登记（可反复执行）。
      </el-alert>
      <el-form label-width="90px">
        <el-form-item label="映射名称">
          <el-input v-model="mappingName" placeholder="留空自动生成" />
        </el-form-item>
        <el-form-item label="远端目录">
          <el-input :model-value="currentDirLabel" readonly />
        </el-form-item>
        <el-form-item label="系统目录" required>
          <el-tree-select
            v-model="mappingCategoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            placeholder="选择目标系统目录"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="递归">
          <el-switch v-model="mappingRecursive" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="mappingDialog = false">取消</el-button>
        <el-button type="success" :loading="saving" @click="doMapping">创建并同步</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Refresh,
  Folder,
  Document,
  FolderAdd,
  ArrowLeft,
  EditPen,
  Download,
  Delete,
  Select,
  Files,
} from '@element-plus/icons-vue'
import { diskApi, mappingApi, categoryApi, resourceApi } from '@/api'
const accounts = ref([])
const accountId = ref(null)
const items = ref([])
const loading = ref(false)
const saving = ref(false)
const picked = ref([])
const stack = ref([]) // [{id, name}]
const categoryTree = ref([])

const currentDirId = computed(() =>
  stack.value.length ? stack.value[stack.value.length - 1].id : ''
)
const currentDirLabel = computed(() =>
  stack.value.length ? stack.value.map((s) => s.name).join(' / ') : '根目录'
)
const pickedIds = computed(() => picked.value.map((r) => r.id))

// sortedItems 文件夹排在文件前面，同类按名称升序。
// 平铺视图里如果文件夹和文件混着排，找文件夹要来回扫，很费眼。
const sortedItems = computed(() =>
  [...items.value].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return String(a.name).localeCompare(String(b.name), 'zh-CN')
  })
)

const mkdirDialog = ref(false)
const mkdirName = ref('')
const importDialog = ref(false)
const importCategoryId = ref(null)
const importRecursive = ref(true)
const importTargets = ref([])
const mappingDialog = ref(false)
const mappingName = ref('')
const mappingCategoryId = ref(null)
const mappingRecursive = ref(true)

// —— 已收录资源抽屉 ——
const libDrawer = ref(false)
const libItems = ref([])
const libTotal = ref(0)
const libLoading = ref(false)
const libKeyword = ref('')

function diskName(t) {
  return (
    {
      quark: '夸克',
      baidu: '百度',
      uc: 'UC',
      tianyi: '天翼',
      aliyun: '阿里云盘',
      webdav: 'WebDAV',
      local: '本地硬盘',
    }[t] || t
  )
}

function formatSize(bytes) {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let n = bytes
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(1)} ${units[i]}`
}

// ============ 选择与进入 ============

// onTileClick 单击卡片：文件夹直接进入，文件切换选中。
//
// 文件夹的"选中"只能通过左上角勾选框，否则想进目录反而被选上了。
function onTileClick(row) {
  if (row.isDir) {
    enterDir(row)
    return
  }
  togglePick(row)
}

function togglePick(row) {
  const i = picked.value.findIndex((r) => r.id === row.id)
  if (i >= 0) picked.value.splice(i, 1)
  else picked.value.push(row)
}

// ============ 数据加载 ============

async function loadAccounts() {
  try {
    accounts.value = await diskApi.list()
    if (accountId.value && !accounts.value.some((a) => a.id === accountId.value)) {
      accountId.value = null
    }
    if (!accountId.value && accounts.value.length) {
      selectAccount(accounts.value[0])
    }
  } catch (e) {
    /* 忽略 */
  }
}

// recheck 重新检测某个接入源。
//
// 后端在账号信息接口失败时会退回"能否列根目录"实测，
// 所以这里的结果比单纯看状态字段更可信。
async function recheck(a) {
  try {
    const res = await diskApi.validate(a.id)
    if (res?.valid) {
      ElMessage.success(`${a.nickname} 正常`)
    } else {
      ElMessage.warning(res?.message || `${a.nickname} 校验未通过`)
    }
    await loadAccounts()
  } catch (e) {
    /* 拦截器已提示 */
  }
}

async function loadCategories() {
  try {
    categoryTree.value = await categoryApi.tree(false)
  } catch (e) {
    /* 忽略 */
  }
}

function selectAccount(a) {
  accountId.value = a.id
  stack.value = []
  picked.value = []
  loadDir('')
}

async function loadDir(dirId) {
  if (!accountId.value) return
  loading.value = true
  try {
    const res = await diskApi.browse(accountId.value, dirId)
    items.value = res.items || []
    // 换目录后旧的选中项已经不在当前视图里了，必须清掉，
    // 否则"导入为资源"会把看不见的文件也一起导进去。
    picked.value = []
  } catch (e) {
    items.value = []
  } finally {
    loading.value = false
  }
}

function enterDir(row) {
  stack.value.push({ id: row.id, name: row.name })
  loadDir(row.id)
}

function goUp() {
  stack.value.pop()
  loadDir(currentDirId.value)
}

function jumpTo(index) {
  stack.value = stack.value.slice(0, index + 1)
  loadDir(currentDirId.value)
}

function reload() {
  loadDir(currentDirId.value)
}

// ============ 增删改 ============

function openMkdir() {
  mkdirName.value = ''
  mkdirDialog.value = true
}

async function doMkdir() {
  if (!mkdirName.value.trim()) {
    ElMessage.warning('请输入名称')
    return
  }
  saving.value = true
  try {
    await diskApi.mkdir(accountId.value, currentDirId.value, mkdirName.value.trim())
    ElMessage.success('文件夹已创建')
    mkdirDialog.value = false
    reload()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function rename(row) {
  try {
    const { value } = await ElMessageBox.prompt('输入新名称', '重命名', {
      inputValue: row.name,
      inputValidator: (v) => (v && v.trim() ? true : '名称不能为空'),
    })
    await diskApi.rename(accountId.value, row.id, value.trim())
    ElMessage.success('已重命名')
    reload()
  } catch (e) {
    /* 取消 */
  }
}

async function removeItem(row) {
  try {
    await ElMessageBox.confirm(
      `确定删除「${row.name}」吗？这会同时删除接入源上的真实文件/目录，不可恢复。`,
      '危险操作',
      { type: 'warning', confirmButtonText: '确认删除', confirmButtonClass: 'el-button--danger' }
    )
    await diskApi.deleteRemote(accountId.value, [row.id])
    ElMessage.success('已删除')
    reload()
  } catch (e) {
    /* 取消 */
  }
}

// ============ 导入 / 映射 ============

function openImport() {
  importTargets.value = picked.value.slice()
  importCategoryId.value = null
  importDialog.value = true
}

function importOne(row) {
  importTargets.value = [row]
  importCategoryId.value = null
  importDialog.value = true
}

async function doImport() {
  if (!importCategoryId.value) {
    ElMessage.warning('请选择系统目录')
    return
  }
  saving.value = true
  try {
    const res = await resourceApi.importFromDisk({
      categoryId: importCategoryId.value,
      accountId: accountId.value,
      recursive: importRecursive.value,
      items: importTargets.value.map((r) => ({
        fileId: r.id,
        title: r.name,
        size: r.size,
        isDir: r.isDir,
      })),
    })
    ElMessage.success(`导入完成：新增 ${res.created}，跳过 ${res.skipped}`)
    importDialog.value = false
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

function openMapping() {
  mappingName.value = ''
  mappingCategoryId.value = null
  mappingDialog.value = true
}

async function doMapping() {
  if (!mappingCategoryId.value) {
    ElMessage.warning('请选择系统目录')
    return
  }
  saving.value = true
  try {
    const created = await mappingApi.create({
      name: mappingName.value,
      accountId: accountId.value,
      remoteDirId: currentDirId.value,
      remoteDirName: currentDirLabel.value,
      categoryId: mappingCategoryId.value,
      recursive: mappingRecursive.value,
      enabled: true,
    })
    ElMessage.success('映射已创建，正在同步…')
    const res = await mappingApi.sync(created.id)
    if (res.error) ElMessage.warning(`同步有异常：${res.error}`)
    else ElMessage.success(`同步完成：新增 ${res.created}，跳过 ${res.skipped}`)
    mappingDialog.value = false
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  loadAccounts()
  loadCategories()
})

// ============ 已收录资源 ============

function openLib() {
  libDrawer.value = true
  loadLib()
}

async function loadLib() {
  libLoading.value = true
  try {
    const data = await resourceApi.list({
      keyword: libKeyword.value.trim(),
      page: 1,
      pageSize: 200,
    })
    libItems.value = data?.list || []
    libTotal.value = data?.total ?? libItems.value.length
  } catch (e) {
    libItems.value = []
  } finally {
    libLoading.value = false
  }
}

async function removeLib(row) {
  try {
    await ElMessageBox.confirm(`确定删除资源「${row.title}」吗？`, '提示', { type: 'warning' })
    await resourceApi.batchDelete([row.id])
    ElMessage.success('已删除')
    loadLib()
  } catch (e) {
    /* 取消 */
  }
}
</script>

<style scoped>
.src-list {
  min-height: 600px;
}
.src-list-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-weight: 600;
  margin-bottom: 8px;
}
.src-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 8px;
  cursor: pointer;
  margin-bottom: 4px;
}
.src-item:hover {
  background: var(--ls-bg);
}
.src-item.active {
  background: var(--el-color-primary-light-9);
}
.src-meta {
  flex: 1;
  min-width: 0;
}
.src-name {
  font-size: 14px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.src-type {
  margin-top: 2px;
}
/* 状态标可点：点一下就地重新校验，省得再去「资源接入」找 */
.src-state {
  cursor: pointer;
  flex: 0 0 auto;
}
.src-ico {
  color: #f7ba2a;
}

.browse-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
  gap: 12px;
}
.crumbs {
  display: flex;
  align-items: center;
  min-width: 0;
}
.crumbs a {
  cursor: pointer;
  color: var(--el-color-primary);
}
.browse-ops {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

/* ============ 平铺文件夹 ============ */
.tiles {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(148px, 1fr));
  gap: 12px;
  min-height: 260px;
  align-content: start;
}

.tile {
  position: relative;
  padding: 16px 10px 12px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 12px;
  background: #fff;
  text-align: center;
  cursor: pointer;
  transition: all 0.16s ease;
}
.tile:hover {
  border-color: var(--el-color-primary-light-5);
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.08);
  transform: translateY(-2px);
}
.tile.on {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
}
.tile.dir .tile-ico {
  color: #f7ba2a;
}
.tile .tile-ico {
  color: #9aa7b5;
}

.tile-check {
  position: absolute;
  top: 6px;
  left: 6px;
  width: 18px;
  height: 18px;
  border: 1px solid var(--el-border-color);
  border-radius: 4px;
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  color: #fff;
  opacity: 0;
  transition: opacity 0.15s ease;
}
.tile:hover .tile-check,
.tile.on .tile-check {
  opacity: 1;
}
.tile.on .tile-check {
  background: var(--el-color-primary);
  border-color: var(--el-color-primary);
}

.tile-ico {
  font-size: 34px;
  line-height: 1;
}

.tile-name {
  margin-top: 8px;
  font-size: 13px;
  font-weight: 500;
  color: var(--el-text-color-primary);
  line-height: 1.4;
  height: 36px;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  word-break: break-all;
}

.tile-meta {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.tile-ops {
  position: absolute;
  right: 4px;
  bottom: 2px;
  opacity: 0;
  transition: opacity 0.15s ease;
}
.tile:hover .tile-ops {
  opacity: 1;
}

.lib-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
</style>
