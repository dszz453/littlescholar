<template>
  <div class="source-browser">
    <el-row :gutter="16">
      <!-- 左：接入源 -->
      <el-col :span="6">
        <div class="card-box src-list">
          <div class="src-list-title">
            接入源
            <el-button link type="primary" size="small" :icon="Refresh" @click="loadAccounts" />
          </div>
          <el-scrollbar height="520px">
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
              <el-tag v-if="a.status !== 'valid'" size="small" type="danger">失效</el-tag>
            </div>
            <el-empty
              v-if="!accounts.length"
              description="还没有接入源，请先到「资源接入」添加"
              :image-size="60"
            />
          </el-scrollbar>
        </div>
      </el-col>

      <!-- 右：目录内容 -->
      <el-col :span="18">
        <div class="card-box">
          <div class="browse-bar">
            <div class="crumbs">
              <el-button
                size="small"
                :icon="ArrowLeft"
                :disabled="!stack.length"
                @click="goUp"
              >
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
                新建目录
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
              <el-button size="small" :icon="Refresh" @click="reload" />
            </div>
          </div>

          <el-table
            :data="items"
            v-loading="loading"
            size="small"
            empty-text="这个目录是空的"
            @selection-change="(r) => (picked = r)"
          >
            <el-table-column type="selection" width="42" />
            <el-table-column label="名称" min-width="240">
              <template #default="{ row }">
                <span class="file-name" :class="{ dir: row.isDir }" @click="row.isDir && enterDir(row)">
                  <el-icon><component :is="row.isDir ? Folder : Document" /></el-icon>
                  <span>{{ row.name }}</span>
                </span>
              </template>
            </el-table-column>
            <el-table-column label="类型" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="row.isDir ? 'warning' : 'info'" effect="plain">
                  {{ row.isDir ? '文件夹' : extText(row) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="大小" width="100">
              <template #default="{ row }">{{ row.isDir ? '-' : formatSize(row.size) }}</template>
            </el-table-column>
            <el-table-column label="修改时间" width="170">
              <template #default="{ row }">{{ formatTime(row.updatedAt) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="230" fixed="right">
              <template #default="{ row }">
                <el-button v-if="row.isDir" link type="primary" size="small" @click="enterDir(row)">
                  进入
                </el-button>
                <el-button link type="primary" size="small" @click="rename(row)">重命名</el-button>
                <el-button link type="primary" size="small" @click="importOne(row)">导入</el-button>
                <el-button link type="danger" size="small" @click="removeItem(row)">删除</el-button>
              </template>
            </el-table-column>
          </el-table>

          <div class="mt-12 text-secondary">
            共 {{ items.length }} 项
            <span v-if="picked.length">，已选 {{ picked.length }} 项</span>
            <span v-if="accountId" class="ml-8">
              · 目标目录：{{ currentDirId || '根目录' }}
            </span>
          </div>
        </div>
      </el-col>
    </el-row>

    <!-- 新建目录 -->
    <el-dialog v-model="mkdirDialog" title="新建目录" width="440px">
      <el-form label-width="80px">
        <el-form-item label="目录名">
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
        把「当前接入源目录」绑定到系统目录，之后一次同步即可批量登记（可反复执行）。
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

const currentDirId = computed(() => (stack.value.length ? stack.value[stack.value.length - 1].id : ''))
const currentDirLabel = computed(() =>
  stack.value.length ? stack.value.map((s) => s.name).join(' / ') : '根目录'
)

// 对话框
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

function diskName(t) {
  return (
    { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}
function extText(row) {
  return row.ext ? row.ext.toUpperCase() : '文件'
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
function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

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
  loadDir('')
}

async function loadDir(dirId) {
  if (!accountId.value) return
  loading.value = true
  try {
    const res = await diskApi.browse(accountId.value, dirId)
    items.value = res.items || []
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

function openMkdir() {
  mkdirName.value = ''
  mkdirDialog.value = true
}

async function doMkdir() {
  if (!mkdirName.value.trim()) {
    ElMessage.warning('请输入目录名')
    return
  }
  saving.value = true
  try {
    await diskApi.mkdir(accountId.value, currentDirId.value, mkdirName.value.trim())
    ElMessage.success('目录已创建')
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
      `确定删除「${row.name}」吗？这会**同时删除接入源上的真实文件/目录**，不可恢复。`,
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
</script>

<style scoped>
.src-list {
  min-height: 520px;
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
.file-name {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.file-name.dir {
  cursor: pointer;
  color: var(--el-color-primary);
}
.file-name.dir:hover {
  text-decoration: underline;
}
</style>
