<template>
  <div class="mapping-panel">
    <div class="mp-bar">
      <span class="text-secondary">
        左边是系统目录，右边是接入源。勾选两边的目录，用中间的箭头搬运：
        <b>→</b> 把网盘目录挂到系统目录下，<b>←</b> 把挂好的放回去。
      </span>
      <div>
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

    <!-- ============ 三栏操作区 ============ -->
    <div class="mp-cols">
      <!-- 左：系统目录 -->
      <div class="mp-col">
        <div class="mp-col-head">
          <span class="mp-col-title">系统目录</span>
          <span class="text-secondary">{{ catCheckedIds.length }} 个已选</span>
        </div>
        <div class="mp-col-body">
          <el-tree
            ref="catTreeRef"
            :data="categoryTree"
            node-key="id"
            show-checkbox
            check-strictly
            :props="{ label: 'name', children: 'children' }"
            :expand-on-click-node="false"
            empty-text="还没有系统目录"
            @check="onCatCheck"
          >
            <template #default="{ data }">
              <span class="mp-node">
                <span class="mp-node-name">{{ data.name }}</span>
                <el-tag
                  v-if="mapCountOf(data.id)"
                  size="small"
                  type="success"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ mapCountOf(data.id) }} 条
                </el-tag>
              </span>
            </template>
          </el-tree>
        </div>
      </div>

      <!-- 中：箭头 -->
      <div class="mp-arrows">
        <el-tooltip
          :content="mapTip"
          placement="top"
          :disabled="canMap"
        >
          <span class="mp-arrow-wrap">
            <el-button
              class="mp-arrow"
              type="primary"
              :disabled="!canMap"
              @click="doMap"
            >
              →
            </el-button>
          </span>
        </el-tooltip>
        <el-tooltip :content="unmapTip" placement="bottom" :disabled="canUnmap">
          <span class="mp-arrow-wrap">
            <el-button class="mp-arrow" :disabled="!canUnmap" @click="doUnmap">
              ←
            </el-button>
          </span>
        </el-tooltip>
        <div class="mp-opt">
          <el-checkbox v-model="recursive" size="small">递归</el-checkbox>
        </div>
      </div>

      <!-- 右：接入源 -->
      <div class="mp-col">
        <div class="mp-col-head">
          <span class="mp-col-title">接入源目录</span>
          <span class="text-secondary">{{ remoteChecked.length }} 个已选</span>
        </div>
        <div class="mp-col-body">
          <RemoteDirTree
            ref="remoteTreeRef"
            :accounts="accounts"
            :mapped-keys="mappedKeys"
            :show-toolbar="false"
            multiple
            @check="onRemoteCheck"
          />
        </div>
      </div>
    </div>

    <!-- ============ 已有映射 ============ -->
    <div class="mp-list">
      <div class="mp-list-head">
        <span class="mp-col-title">已有映射（{{ mappings.length }}）</span>
        <span class="text-secondary">
          同步会把远端目录里的媒体文件批量登记成资源，可反复执行
        </span>
      </div>

      <el-table :data="mappings" v-loading="loading" size="small" empty-text="还没有映射">
        <el-table-column label="名称" min-width="170">
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
        <el-table-column label="远端目录" min-width="150">
          <template #default="{ row }">
            <span :title="row.remoteDirId">{{ row.remoteDirName || '根目录' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="系统目录" min-width="130">
          <template #default="{ row }">
            <el-tag size="small" type="info" effect="plain">
              {{ row.category?.name || '目录#' + row.categoryId }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="递归" width="64">
          <template #default="{ row }">
            <el-tag :type="row.recursive ? 'success' : 'info'" size="small" effect="plain">
              {{ row.recursive ? '开' : '关' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="启用" width="64">
          <template #default="{ row }">
            <el-switch :model-value="row.enabled" @change="(v) => toggleEnabled(row, v)" />
          </template>
        </el-table-column>
        <el-table-column label="上次同步" min-width="170">
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
        <el-table-column label="操作" width="150" fixed="right">
          <template #default="{ row }">
            <el-button
              link
              type="primary"
              size="small"
              :loading="syncingId === row.id"
              @click="sync(row)"
            >
              同步
            </el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- ============ 编辑映射 ============ -->
    <el-dialog v-model="dialog" title="编辑映射" width="560px">
      <el-form :model="form" label-width="90px">
        <el-form-item label="名称">
          <el-input v-model="form.name" placeholder="留空自动生成" />
        </el-form-item>
        <el-form-item label="接入源">
          <span>{{ form.accountLabel || '-' }}</span>
        </el-form-item>
        <el-form-item label="远端目录">
          <span>{{ form.remoteDirName || '根目录' }}</span>
        </el-form-item>
        <el-form-item label="系统目录">
          <span>{{ form.categoryLabel || '-' }}</span>
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
  </div>
</template>

<script setup>
import { computed, h, onMounted, ref } from 'vue'
import { ElCheckbox, ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, RefreshRight } from '@element-plus/icons-vue'
import { diskApi, mappingApi, categoryApi } from '@/api'
import RemoteDirTree from '@/components/RemoteDirTree.vue'

const mappings = ref([])
const accounts = ref([])
const categoryTree = ref([])
const loading = ref(false)
const saving = ref(false)
const syncingId = ref(null)

// 三栏选中状态
const catTreeRef = ref(null)
const remoteTreeRef = ref(null)
const catCheckedIds = ref([])
const remoteChecked = ref([])
// 递归开关：影响下一次 → 建出来的映射
const recursive = ref(true)

const dialog = ref(false)
const form = ref({})

/** categoryId → 这条目录上直接挂了几个映射（不含子目录） */
const mapCountByCat = computed(() => {
  const m = {}
  for (const x of mappings.value) m[x.categoryId] = (m[x.categoryId] || 0) + 1
  return m
})

/** 右侧树上打「已映射」标的 key 集合 */
const mappedKeys = computed(() =>
  mappings.value.map((m) => `${m.accountId}::${m.remoteDirId || ''}`)
)

// 「映射」左边必须**恰好一个**目录：一个网盘目录只能挂到一个系统目录下，
// 挂两个的话第二个同步过去是 0 条（后端按 file_id 去重，同一份文件不会重复登记）。
const canMap = computed(() => catCheckedIds.value.length === 1 && remoteChecked.value.length > 0)
const canUnmap = computed(() => catCheckedIds.value.length > 0)

const mapTip = computed(() => {
  if (!remoteChecked.value.length) return '先在右边勾选要映射的网盘目录'
  if (!catCheckedIds.value.length) return '先在左边勾选一个系统目录'
  if (catCheckedIds.value.length > 1) return '映射时左边只能选一个目录（解绑可以多选）'
  return '映射过去'
})
const unmapTip = computed(() =>
  canUnmap.value ? '解绑并放回右边' : '先在左边勾选要解绑的目录'
)

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

function mapCountOf(id) {
  return mapCountByCat.value[id] || 0
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
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

function onCatCheck(_data, info) {
  catCheckedIds.value = (info && info.checkedKeys) || []
}

function onRemoteCheck(items) {
  remoteChecked.value = items || []
}

/** 把选中的目录补全成"自己 + 所有子孙"。解绑按块清理时才符合直觉。 */
function withDescendants(ids) {
  const flat = []
  const walk = (nodes) => {
    for (const n of nodes || []) {
      flat.push(n)
      walk(n.children)
    }
  }
  walk(categoryTree.value)
  const keep = new Set(ids)
  let changed = true
  while (changed) {
    changed = false
    for (const n of flat) {
      if (!keep.has(n.id) && n.parentId && keep.has(n.parentId)) {
        keep.add(n.id)
        changed = true
      }
    }
  }
  return keep
}

/* ============ → 映射 ============ */
async function doMap() {
  const catId = catCheckedIds.value[0]
  const targets = remoteChecked.value
  if (!catId || !targets.length) return

  const created = []
  const failed = []
  for (const t of targets) {
    try {
      const res = await mappingApi.create({
        name: t.dirName || t.path,
        accountId: t.accountId,
        remoteDirId: t.dirId || '',
        remoteDirName: t.dirName || '',
        categoryId: catId,
        recursive: recursive.value,
        enabled: true,
      })
      if (res && res.id) created.push(res.id)
    } catch (e) {
      failed.push(`${t.path}（${(e && e.message) || '失败'}）`)
    }
  }

  if (failed.length) {
    ElMessage.warning(`有 ${failed.length} 条没建上：${failed.join('；')}`)
  }
  if (!created.length) return

  await loadAll()
  remoteTreeRef.value?.clearChecked()

  // 建完直接同步 —— 只建映射不导入的话，孩子端还是空的，
  // 用户会以为"映射没生效"。这里多花几秒，换掉一次困惑。
  const syncMsg = await syncIds(created)
  ElMessage.success(`已建立 ${created.length} 条映射${syncMsg}`)
}

/** 同步一批映射，返回一句可拼在提示后面的话。 */
async function syncIds(ids) {
  let add = 0
  let skip = 0
  const bad = []
  for (const id of ids) {
    try {
      const r = await mappingApi.sync(id)
      add += Number(r && r.created) || 0
      skip += Number(r && r.skipped) || 0
      if (r && r.error) bad.push(r.error)
    } catch (e) {
      bad.push((e && e.message) || '同步失败')
    }
  }
  await loadAll()
  if (bad.length) return `，同步时 ${bad.length} 条出问题：${bad[0]}`
  return `，新增 ${add} 个资源（跳过 ${skip} 个已存在的）`
}

/* ============ ← 解绑 ============ */
async function doUnmap() {
  const ids = withDescendants(catCheckedIds.value)
  const hits = mappings.value.filter((m) => ids.has(m.categoryId))
  if (!hits.length) {
    ElMessage.info('选中的目录下没有映射')
    return
  }

  // 删资源不可逆，所以摆到明面上让用户选，而不是替他决定。
  // 默认勾上：用户的直觉就是"把这个资源拿回去"，留着孤儿资源反而会被当成没生效。
  const withResources = ref(true)
  const names = hits.slice(0, 5).map((m) => m.name).join('、')
  const more = hits.length > 5 ? ` 等 ${hits.length} 条` : ''
  try {
    await ElMessageBox({
      title: '解绑映射',
      type: 'warning',
      showCancelButton: true,
      confirmButtonText: '确定解绑',
      cancelButtonText: '取消',
      message: h('div', [
        h('p', { style: 'margin:0 0 12px' }, `将解绑 ${hits.length} 条映射：${names}${more}`),
        h(
          ElCheckbox,
          {
            modelValue: withResources.value,
            'onUpdate:modelValue': (v) => {
              withResources.value = v
            },
          },
          () => '同时移除已导入的资源'
        ),
        h(
          'p',
          { style: 'margin:8px 0 0;color:#909399;font-size:12px;line-height:1.6' },
          '勾选后，这些映射同步进来的资源会从资源列表和孩子端一并移除（不可撤销）；不勾选则只解绑配置、资源保留。'
        ),
      ]),
    })
  } catch (e) {
    return // 取消
  }

  let done = 0
  let removed = 0
  const failed = []
  for (const m of hits) {
    try {
      const res = await mappingApi.remove(m.id, withResources.value)
      done++
      removed += Number(res && res.removedResources) || 0
    } catch (e) {
      failed.push((e && e.message) || '失败')
    }
  }
  await loadAll()
  catTreeRef.value?.setCheckedKeys([])
  catCheckedIds.value = []

  if (failed.length) ElMessage.warning(`解绑了 ${done} 条，${failed.length} 条失败：${failed[0]}`)
  else ElMessage.success(`已解绑 ${done} 条映射${removed ? `，清理 ${removed} 个资源` : ''}`)
}

/* ============ 表格上的操作 ============ */
async function toggleEnabled(row, v) {
  try {
    await mappingApi.update(row.id, { enabled: v })
    row.enabled = v
  } catch (e) {
    /* 忽略 */
  }
}

function openEdit(row) {
  form.value = {
    id: row.id,
    name: row.name,
    accountLabel: `${row.account?.nickname || '账号#' + row.accountId}（${diskName(
      row.account?.diskType
    )}）`,
    remoteDirName: row.remoteDirName,
    categoryLabel: row.category?.name || '目录#' + row.categoryId,
    recursive: row.recursive,
    enabled: row.enabled,
    // 远端与目标目录不允许改：改这两项等于换一条映射，
    // 应该删掉重建，否则已导入的资源会挂在一个说不清的来源上。
    accountId: row.accountId,
    remoteDirId: row.remoteDirId,
    categoryId: row.categoryId,
  }
  dialog.value = true
}

async function save() {
  saving.value = true
  try {
    await mappingApi.update(form.value.id, {
      name: form.value.name,
      accountId: form.value.accountId,
      remoteDirId: form.value.remoteDirId,
      remoteDirName: form.value.remoteDirName,
      categoryId: form.value.categoryId,
      recursive: form.value.recursive,
      enabled: form.value.enabled,
    })
    ElMessage.success('已保存')
    dialog.value = false
    loadAll()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function remove(row) {
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
        h(
          ElCheckbox,
          {
            modelValue: withResources.value,
            'onUpdate:modelValue': (v) => {
              withResources.value = v
            },
          },
          () => '同时删除已导入的资源'
        ),
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
    if (n === 0) ElMessage.success('已删除映射')
    else if (legacy > 0) ElMessage.success(`已删除映射，并清理 ${n} 条资源（含 ${legacy} 条历史资源）`)
    else ElMessage.success(`已删除映射，并清理 ${n} 条资源`)
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

/* ============ 三栏 ============ */
.mp-cols {
  display: flex;
  align-items: stretch;
  gap: 10px;
  margin-bottom: 18px;
}
.mp-col {
  flex: 1 1 0;
  min-width: 0;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--ls-border, #e5e7eb);
  border-radius: 10px;
  background: #fff;
  overflow: hidden;
}
.mp-col-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 9px 12px;
  background: var(--ls-bg, #f7f9fc);
  border-bottom: 1px solid var(--ls-border, #e5e7eb);
  font-size: 13px;
}
.mp-col-title {
  font-weight: 600;
}
.mp-col-body {
  height: 420px;
  overflow: auto;
  padding: 6px 4px;
}
.mp-node {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.mp-node-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mp-node-tag {
  flex: 0 0 auto;
  transform: scale(0.88);
}

/* ============ 中间箭头 ============ */
.mp-arrows {
  flex: 0 0 76px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 14px;
}
.mp-arrow-wrap {
  display: inline-flex;
}
.mp-arrow {
  width: 52px;
  height: 52px;
  font-size: 22px;
  line-height: 1;
  border-radius: 50%;
  padding: 0;
}
.mp-opt {
  margin-top: 4px;
}

/* ============ 映射列表 ============ */
.mp-list {
  border-top: 1px solid var(--ls-border, #e5e7eb);
  padding-top: 12px;
}
.mp-list-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.mp-name {
  font-weight: 500;
}
.form-tip {
  font-size: 12px;
  color: var(--ls-text-secondary);
}
.text-ok {
  color: var(--el-color-success);
  margin-right: 8px;
}
.text-err {
  color: var(--el-color-danger);
  font-size: 12px;
}

/* 窄屏（后台也可能在平板上开）：三栏改成上下堆叠，箭头横过来 */
@media (max-width: 1100px) {
  .mp-cols {
    flex-direction: column;
  }
  .mp-col-body {
    height: 260px;
  }
  .mp-arrows {
    flex: 0 0 auto;
    flex-direction: row;
    gap: 18px;
    padding: 6px 0;
  }
}
</style>
