<template>
  <div class="mapping-panel">
    <div class="mp-bar">
      <span class="text-secondary">
        映射把「接入源的一个远端目录」绑定到「系统的一个目录」，
        点一次同步即可把里面的媒体文件批量登记成资源（可反复执行）。
      </span>
      <div>
        <el-button :icon="Plus" type="primary" size="small" @click="openCreate">新增映射</el-button>
        <el-button
          :icon="RefreshRight"
          type="success"
          size="small"
          :disabled="!mappings.length"
          @click="syncAll"
        >
          全部同步
        </el-button>
        <el-button :icon="Refresh" size="small" @click="loadAll" />
      </div>
    </div>

    <el-table :data="mappings" v-loading="loading" empty-text="还没有映射规则">
      <el-table-column label="名称" min-width="180">
        <template #default="{ row }">
          <div class="mp-name">{{ row.name }}</div>
          <div class="text-secondary">
            {{ row.account?.nickname || '账号#' + row.accountId }}
            <el-tag v-if="row.account" size="small" effect="plain" class="ml-4">
              {{ diskName(row.account.diskType) }}
            </el-tag>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="远端目录" min-width="160">
        <template #default="{ row }">
          <span :title="row.remoteDirId">{{ row.remoteDirName || '根目录' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="系统目录" min-width="140">
        <template #default="{ row }">
          <el-tag size="small" type="info" effect="plain">
            {{ row.category?.name || '目录#' + row.categoryId }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="递归" width="70">
        <template #default="{ row }">
          <el-tag :type="row.recursive ? 'success' : 'info'" size="small" effect="plain">
            {{ row.recursive ? '开' : '关' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="70">
        <template #default="{ row }">
          <el-switch :model-value="row.enabled" @change="(v) => toggleEnabled(row, v)" />
        </template>
      </el-table-column>
      <el-table-column label="上次同步" min-width="200">
        <template #default="{ row }">
          <template v-if="row.lastSyncAt">
            <div class="text-secondary">{{ formatTime(row.lastSyncAt) }}</div>
            <div>
              <span class="text-ok">+{{ row.lastSyncCreated }}</span>
              <span class="text-secondary"> 跳过 {{ row.lastSyncSkipped }}</span>
            </div>
            <div v-if="row.lastSyncError" class="text-err">{{ row.lastSyncError }}</div>
          </template>
          <span v-else class="text-secondary">未同步</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="200" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" size="small" :loading="syncingId === row.id" @click="sync(row)">
            同步
          </el-button>
          <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 新增/编辑 -->
    <el-dialog v-model="dialog" :title="form.id ? '编辑映射' : '新增映射'" width="620px">
      <el-form :model="form" label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="留空自动生成" />
        </el-form-item>
        <el-form-item label="接入源" required>
          <el-select v-model="form.accountId" style="width: 100%" @change="onAccountChange">
            <el-option
              v-for="a in accounts"
              :key="a.id"
              :label="`${a.nickname}（${diskName(a.diskType)}）`"
              :value="a.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="远端目录">
          <el-input :model-value="form.remoteDirName || '根目录'" readonly style="width: 100%">
            <template #append>
              <el-button :loading="browsing" @click="openBrowse">浏览</el-button>
            </template>
          </el-input>
        </el-form-item>
        <el-form-item label="系统目录" required>
          <el-tree-select
            v-model="form.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            placeholder="选择目标系统目录"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="递归">
          <el-switch v-model="form.recursive" />
          <span class="form-tip ml-8">开启会把子目录里的文件一并登记</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 远端目录浏览 -->
    <el-dialog v-model="browseDialog" title="选择远端目录" width="560px">
      <div class="dir-nav mb-8">
        <el-button size="small" :icon="ArrowLeft" :disabled="!stack.length" @click="goUp">上级</el-button>
        <span class="text-secondary ml-8">{{ stack.map((s) => s.name).join(' / ') || '根目录' }}</span>
      </div>
      <div class="dir-list" v-loading="browsing">
        <div v-for="d in dirs" :key="d.id" class="dir-item" @click="enterDir(d)">
          <el-icon><Folder /></el-icon>
          <span>{{ d.name }}</span>
        </div>
        <el-empty v-if="!dirs.length && !browsing" description="该目录下没有子目录" :image-size="60" />
      </div>
      <template #footer>
        <el-button @click="browseDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmBrowse">选择当前目录</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { h, onMounted, ref } from 'vue'
import { ElCheckbox, ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, RefreshRight, ArrowLeft, Folder } from '@element-plus/icons-vue'
import { diskApi, mappingApi, categoryApi } from '@/api'

const mappings = ref([])
const accounts = ref([])
const categoryTree = ref([])
const loading = ref(false)
const saving = ref(false)
const syncingId = ref(null)

const dialog = ref(false)
const form = ref({})

const browseDialog = ref(false)
const browsing = ref(false)
const dirs = ref([])
const stack = ref([])
const browseAccountId = ref(null)

function diskName(t) {
  return (
    { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}
function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function loadAll() {
  loading.value = true
  try {
    const [m, a, c] = await Promise.all([
      mappingApi.list(),
      diskApi.list(),
      categoryApi.tree(false),
    ])
    mappings.value = m || []
    accounts.value = a || []
    categoryTree.value = c || []
  } catch (e) {
    /* 忽略 */
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.value = {
    id: 0,
    name: '',
    accountId: null,
    remoteDirId: '',
    remoteDirName: '',
    categoryId: null,
    recursive: true,
    enabled: true,
  }
  dialog.value = true
}

function openEdit(row) {
  form.value = {
    id: row.id,
    name: row.name,
    accountId: row.accountId,
    remoteDirId: row.remoteDirId,
    remoteDirName: row.remoteDirName,
    categoryId: row.categoryId,
    recursive: row.recursive,
    enabled: row.enabled,
  }
  dialog.value = true
}

function onAccountChange() {
  form.value.remoteDirId = ''
  form.value.remoteDirName = ''
}

async function save() {
  const f = form.value
  if (!f.accountId || !f.categoryId) {
    ElMessage.warning('请选择接入源和系统目录')
    return
  }
  saving.value = true
  try {
    const payload = {
      name: f.name,
      accountId: f.accountId,
      remoteDirId: f.remoteDirId,
      remoteDirName: f.remoteDirName,
      categoryId: f.categoryId,
      recursive: f.recursive,
      enabled: f.enabled,
    }
    if (f.id) await mappingApi.update(f.id, payload)
    else await mappingApi.create(payload)
    ElMessage.success('已保存')
    dialog.value = false
    loadAll()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(row, v) {
  try {
    await mappingApi.update(row.id, { enabled: v })
    row.enabled = v
  } catch (e) {
    /* 忽略 */
  }
}

async function remove(row) {
  // 删除映射默认**不动**已导入的资源：删资源不可逆，不该被"取消配置"顺带触发。
  //
  // 但只删映射会留下一批用户已经不想要的孤儿资源 —— 线上真实发生过：
  // 用户删掉夸克那条映射后，孩子端依然列着那 26 集，看起来就像"删除没生效"。
  // 所以这里把选择摆到明面上，并且默认勾选（用户的直觉就是要一起清掉）。
  const withResources = ref(true)
  try {
    await ElMessageBox({
      title: '删除映射',
      type: 'warning',
      showCancelButton: true,
      confirmButtonText: '确定删除',
      cancelButtonText: '取消',
      message: h('div', [
        h('p', { style: 'margin:0 0 12px' }, `确定删除映射「${row.name}」吗？`),
        h(ElCheckbox, {
          modelValue: withResources.value,
          'onUpdate:modelValue': (v) => {
            withResources.value = v
          },
        }, () => '同时删除已导入的资源'),
        h(
          'p',
          { style: 'margin:8px 0 0;color:#909399;font-size:12px;line-height:1.6' },
          '勾选后，这条映射同步进来的资源会从资源列表和孩子端一并移除（不可撤销）；不勾选则只删配置、资源保留。'
        ),
      ]),
    })
    const res = await mappingApi.remove(row.id, withResources.value)
    const n = Number(res?.removedResources) || 0
    const legacy = Number(res?.removedLegacy) || 0
    if (n === 0) {
      ElMessage.success('已删除映射')
    } else if (legacy > 0) {
      // 区分开"这条映射导入的"和"加 mapping_id 之前的历史资源"，
      // 免得用户看到比自己印象中更多的条数时以为是删错了。
      ElMessage.success(`已删除映射，并清理 ${n} 条资源（含 ${legacy} 条历史资源）`)
    } else {
      ElMessage.success(`已删除映射，并清理 ${n} 条资源`)
    }
    loadAll()
  } catch (e) {
    /* 取消 */
  }
}

async function sync(row) {
  syncingId.value = row.id
  try {
    const res = await mappingApi.sync(row.id)
    if (res.error) ElMessage.warning(`同步完成但有异常：${res.error}`)
    else ElMessage.success(`同步完成：新增 ${res.created}，跳过 ${res.skipped}`)
    loadAll()
  } catch (e) {
    /* 忽略 */
  } finally {
    syncingId.value = null
  }
}

async function syncAll() {
  try {
    const res = await mappingApi.syncAll()
    ElMessage.success(`全部同步完成：新增 ${res.created}，跳过 ${res.skipped}，失败 ${res.failed}`)
    loadAll()
  } catch (e) {
    /* 忽略 */
  }
}

async function openBrowse() {
  if (!form.value.accountId) {
    ElMessage.warning('请先选择接入源')
    return
  }
  browseAccountId.value = form.value.accountId
  stack.value = []
  dirs.value = []
  browseDialog.value = true
  await loadBrowse('')
}

async function loadBrowse(dirId) {
  browsing.value = true
  try {
    const res = await diskApi.browse(browseAccountId.value, dirId)
    dirs.value = (res.items || []).filter((i) => i.isDir)
  } catch (e) {
    dirs.value = []
  } finally {
    browsing.value = false
  }
}

async function enterDir(d) {
  stack.value.push({ id: d.id, name: d.name })
  await loadBrowse(d.id)
}

async function goUp() {
  stack.value.pop()
  await loadBrowse(stack.value.length ? stack.value[stack.value.length - 1].id : '')
}

function confirmBrowse() {
  const cur = stack.value.length ? stack.value[stack.value.length - 1] : null
  form.value.remoteDirId = cur ? cur.id : ''
  form.value.remoteDirName = cur ? cur.name : ''
  browseDialog.value = false
}

onMounted(loadAll)
</script>

<style scoped>
.mp-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.mp-name {
  font-weight: 500;
}
.form-tip {
  font-size: 12px;
  color: var(--ls-text-secondary);
}
.dir-nav {
  display: flex;
  align-items: center;
}
.dir-list {
  max-height: 320px;
  overflow-y: auto;
}
.dir-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 6px;
  cursor: pointer;
}
.dir-item:hover {
  background: var(--ls-bg);
}
.text-ok {
  color: var(--el-color-success);
  margin-right: 8px;
}
.text-err {
  color: var(--el-color-danger);
  font-size: 12px;
}
</style>
