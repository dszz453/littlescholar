<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">WebDAV 与资源映射</h2>
      <div>
        <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
        <el-button :icon="Plus" type="primary" @click="openStorageDialog">
          添加 WebDAV
        </el-button>
        <el-button :icon="Link" type="primary" @click="openMappingDialog">
          添加映射
        </el-button>
        <el-button
          :icon="RefreshRight"
          type="success"
          :disabled="!mappings.length"
          @click="syncAll"
        >
          全部同步
        </el-button>
      </div>
    </div>

    <el-alert type="info" :closable="false" class="mb-16">
      <template #title>
        把 NAS / Alist / 群晖等 WebDAV 挂到系统里，再用「映射」把它的某个目录绑定到系统目录，
        一次同步即可批量登记成孩子端可播的资源。
      </template>
    </el-alert>

    <!-- 存储源 -->
    <div class="section-title">存储源（WebDAV）</div>
    <div class="card-box mb-16">
      <el-table :data="storages" empty-text="还没有添加 WebDAV 存储">
        <el-table-column label="名称" min-width="160">
          <template #default="{ row }">
            <div class="res-title">{{ row.nickname }}</div>
            <div class="text-secondary">{{ hostOf(row) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.status === 'valid' ? 'success' : 'danger'" size="small">
              {{ row.status === 'valid' ? '正常' : '失效' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近校验" width="160">
          <template #default="{ row }">{{ formatTime(row.lastCheckAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="validate(row)">校验</el-button>
            <el-button link type="danger" size="small" @click="removeStorage(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 映射 -->
    <div class="section-title">资源映射</div>
    <div class="card-box">
      <el-table :data="mappings" empty-text="还没有映射规则，点右上角「添加映射」创建">
        <el-table-column label="名称" min-width="160">
          <template #default="{ row }">
            <div class="res-title">{{ row.name }}</div>
            <div class="text-secondary">
              {{ row.account?.nickname || ('账号 #' + row.accountId) }}
              → {{ row.category?.name || ('目录 #' + row.categoryId) }}
            </div>
          </template>
        </el-table-column>
        <el-table-column label="递归" width="70">
          <template #default="{ row }">
            <el-tag :type="row.recursive ? 'success' : 'info'" size="small">
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
                <span v-if="row.lastSyncError" class="text-err">{{ row.lastSyncError }}</span>
              </div>
            </template>
            <span v-else class="text-secondary">未同步</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" :loading="syncingId === row.id" @click="sync(row)">
              同步
            </el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="removeMapping(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 添加 WebDAV -->
    <el-dialog v-model="storageDialog" title="添加 WebDAV" width="560px">
      <el-form :model="storageForm" label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="storageForm.nickname" placeholder="如：家里 NAS" />
        </el-form-item>
        <el-form-item label="地址" required>
          <el-input v-model="storageForm.url" placeholder="http://192.168.1.10:5006/dav 或 https://nas.example.com/dav" />
          <div class="form-tip">WebDAV 根地址，以 /dav 结尾的那一类（Alist / 群晖 / rclone 的地址各不相同，照抄即可）</div>
        </el-form-item>
        <el-form-item label="用户名" required>
          <el-input v-model="storageForm.username" placeholder="WebDAV 用户名" />
        </el-form-item>
        <el-form-item label="密码" required>
          <el-input v-model="storageForm.password" type="password" show-password placeholder="WebDAV 密码" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="storageDialog = false">取消</el-button>
        <el-button type="primary" :loading="storageSaving" @click="saveStorage">校验并保存</el-button>
      </template>
    </el-dialog>

    <!-- 添加/编辑映射 -->
    <el-dialog v-model="mappingDialog" :title="mappingForm.id ? '编辑映射' : '添加映射'" width="640px">
      <el-form :model="mappingForm" label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="mappingForm.name" placeholder="留空自动生成" />
        </el-form-item>
        <el-form-item label="存储源" required>
          <el-select v-model="mappingForm.accountId" style="width: 100%" @change="onAccountChange">
            <el-option v-for="s in storages" :key="s.id" :label="s.nickname || hostOf(s)" :value="s.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="远端目录">
          <div class="dir-picker">
            <el-input
              :model-value="mappingForm.remoteDirId || '根目录'"
              readonly
              style="width: 100%"
            >
              <template #append>
                <el-button :loading="browsing" @click="openBrowse">浏览</el-button>
              </template>
            </el-input>
          </div>
        </el-form-item>
        <el-form-item label="系统目录" required>
          <el-tree-select
            v-model="mappingForm.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="递归">
          <el-switch v-model="mappingForm.recursive" />
          <span class="form-tip ml-8">开启会把子目录里的文件一并登记</span>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="mappingForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="mappingDialog = false">取消</el-button>
        <el-button type="primary" :loading="mappingSaving" @click="saveMapping">保存</el-button>
      </template>
    </el-dialog>

    <!-- 目录浏览 -->
    <el-dialog v-model="browseDialog" title="选择远端目录" width="560px">
      <div class="dir-nav mb-8">
        <el-button size="small" :icon="ArrowLeft" :disabled="!browseStack.length" @click="goUp">
          上级
        </el-button>
        <span class="text-secondary ml-8">{{ browsePath || '根目录' }}</span>
      </div>
      <div class="dir-list" v-loading="browsing">
        <div
          v-for="d in browseDirs"
          :key="d.id"
          class="dir-item"
          @click="enterDir(d)"
        >
          <el-icon><Folder /></el-icon>
          <span>{{ d.name }}</span>
        </div>
        <el-empty v-if="!browseDirs.length && !browsing" description="该目录下没有子目录" :image-size="60" />
      </div>
      <template #footer>
        <el-button @click="browseDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmBrowse">选择当前目录</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, RefreshRight, Link, ArrowLeft, Folder } from '@element-plus/icons-vue'
import { diskApi, mappingApi, categoryApi } from '@/api'

const storages = ref([])
const mappings = ref([])
const categoryTree = ref([])
const syncingId = ref(null)

// 添加存储
const storageDialog = ref(false)
const storageSaving = ref(false)
const storageForm = ref({ nickname: '', url: '', username: '', password: '' })

// 映射
const mappingDialog = ref(false)
const mappingSaving = ref(false)
const mappingForm = ref({})

// 目录浏览
const browseDialog = ref(false)
const browsing = ref(false)
const browseDirs = ref([])
const browseStack = ref([]) // 用于「上级」返回
const browseAccountId = ref(null)
const browsePath = computed(() => browseStack.value.map((s) => s.name).join(' / '))

function hostOf(row) {
  if (!row.extra || !row.extra.url) return '-'
  try {
    return new URL(row.extra.url).host
  } catch (e) {
    return row.extra.url
  }
}

function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function loadStorages() {
  try {
    storages.value = await diskApi.list('webdav')
  } catch (e) {
    /* 忽略 */
  }
}

async function loadMappings() {
  try {
    mappings.value = await mappingApi.list()
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

function loadAll() {
  loadStorages()
  loadMappings()
  loadCategories()
}

// ===== 存储 =====
function openStorageDialog() {
  storageForm.value = { nickname: '', url: '', username: '', password: '' }
  storageDialog.value = true
}

async function saveStorage() {
  const f = storageForm.value
  if (!f.url.trim() || !f.username.trim() || !f.password) {
    ElMessage.warning('请填写地址、用户名和密码')
    return
  }
  storageSaving.value = true
  try {
    await diskApi.addManual({
      diskType: 'webdav',
      cookie: `${f.username.trim()}:${f.password}`,
      nickname: f.nickname.trim(),
      extra: { url: f.url.trim() },
    })
    ElMessage.success('WebDAV 已添加')
    storageDialog.value = false
    loadStorages()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    storageSaving.value = false
  }
}

async function validate(row) {
  try {
    const res = await diskApi.validate(row.id)
    if (res.valid) ElMessage.success('连接正常')
    else ElMessage.error(res.message || '连接失败')
    loadStorages()
  } catch (e) {
    /* 忽略 */
  }
}

async function removeStorage(row) {
  try {
    await ElMessageBox.confirm(`确定删除 WebDAV「${row.nickname}」吗？`, '删除确认', { type: 'warning' })
    await diskApi.remove(row.id)
    ElMessage.success('已删除')
    loadStorages()
  } catch (e) {
    /* 取消 */
  }
}

// ===== 映射 =====
function openMappingDialog() {
  mappingForm.value = { id: 0, name: '', accountId: null, remoteDirId: '', remoteDirName: '', categoryId: null, recursive: true, enabled: true }
  mappingDialog.value = true
}

function openEdit(row) {
  mappingForm.value = {
    id: row.id,
    name: row.name,
    accountId: row.accountId,
    remoteDirId: row.remoteDirId,
    remoteDirName: row.remoteDirName,
    categoryId: row.categoryId,
    recursive: row.recursive,
    enabled: row.enabled,
  }
  mappingDialog.value = true
}

function onAccountChange() {
  mappingForm.value.remoteDirId = ''
  mappingForm.value.remoteDirName = ''
}

async function saveMapping() {
  const f = mappingForm.value
  if (!f.accountId || !f.categoryId) {
    ElMessage.warning('请选择存储源和系统目录')
    return
  }
  mappingSaving.value = true
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
    mappingDialog.value = false
    loadMappings()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    mappingSaving.value = false
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

async function removeMapping(row) {
  try {
    await ElMessageBox.confirm(
      `确定删除映射「${row.name}」吗？已导入的资源会保留。`,
      '删除确认',
      { type: 'warning' }
    )
    await mappingApi.remove(row.id)
    ElMessage.success('已删除')
    loadMappings()
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
    loadMappings()
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
    loadMappings()
  } catch (e) {
    /* 忽略 */
  }
}

// ===== 目录浏览 =====
async function openBrowse() {
  if (!mappingForm.value.accountId) {
    ElMessage.warning('请先选择存储源')
    return
  }
  browseAccountId.value = mappingForm.value.accountId
  browseStack.value = []
  browseDirs.value = []
  browseDialog.value = true
  await loadBrowse('')
}

async function loadBrowse(dirId) {
  browsing.value = true
  try {
    const res = await diskApi.browse(browseAccountId.value, dirId)
    browseDirs.value = (res.items || []).filter((i) => i.isDir)
  } catch (e) {
    browseDirs.value = []
  } finally {
    browsing.value = false
  }
}

async function enterDir(d) {
  browseStack.value.push({ id: d.id, name: d.name })
  await loadBrowse(d.id)
}

async function goUp() {
  browseStack.value.pop()
  const cur = browseStack.value.length ? browseStack.value[browseStack.value.length - 1].id : ''
  await loadBrowse(cur)
}

function confirmBrowse() {
  const cur = browseStack.value.length ? browseStack.value[browseStack.value.length - 1] : null
  mappingForm.value.remoteDirId = cur ? cur.id : ''
  mappingForm.value.remoteDirName = cur ? cur.name : ''
  browseDialog.value = false
}

onMounted(loadAll)
</script>

<style scoped>
.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 8px 0 12px;
}

.form-tip {
  font-size: 12px;
  color: var(--ls-text-secondary);
  line-height: 1.6;
}

.dir-picker {
  width: 100%;
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
  display: block;
  font-size: 12px;
}
</style>
