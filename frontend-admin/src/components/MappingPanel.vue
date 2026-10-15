<template>
  <div class="mapping-panel">
    <div class="mp-bar">
      <span class="text-secondary">
        左边是系统目录，右边是接入源。勾选两边的目录，用中间的箭头搬运：
        <b>→</b> 把网盘目录挂到系统目录下，<b>←</b> 把挂好的放回去。
        系统目录展开后能看到它下面<b>已经挂了哪些网盘目录</b>，再展开就是网盘里的内容。
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
      <!-- 左：系统目录（已映射的网盘目录直接长在它下面） -->
      <div class="mp-col">
        <div class="mp-col-head">
          <span class="mp-col-title">系统目录</span>
          <span class="text-secondary">{{ catCheckedIds.length }} 个已选</span>
        </div>
        <div class="mp-col-body" v-loading="leftLoading">
          <el-tree
            ref="catTreeRef"
            :key="leftKey"
            :props="leftProps"
            :load="loadLeftNode"
            lazy
            node-key="id"
            show-checkbox
            check-strictly
            :expand-on-click-node="false"
            empty-text="还没有系统目录"
            @check="onCatCheck"
          >
            <template #default="{ data }">
              <span class="mp-node">
                <span v-if="data.kind === 'mapping'" class="mp-icon">☁️</span>
                <span
                  class="mp-node-name"
                  :class="{ 'is-mapping': data.kind === 'mapping', 'is-off': data.kind === 'mapping' && !data.enabled }"
                >{{ data.name }}</span>

                <el-tag
                  v-if="data.kind === 'mapping'"
                  size="small"
                  :type="data.enabled ? 'success' : 'info'"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ diskName(data.diskType) }}{{ data.enabled ? '' : ' · 已停用' }}
                </el-tag>
                <el-tag
                  v-else-if="data.kind === 'remote'"
                  size="small"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ data.isDir ? '目录' : fmtSize(data.size) }}
                </el-tag>
                <el-tag
                  v-else-if="data.mapCount"
                  size="small"
                  type="success"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ data.mapCount }} 条映射
                </el-tag>
              </span>
            </template>
          </el-tree>
        </div>
      </div>

      <!-- 中：箭头 -->
      <div class="mp-arrows">
        <el-tooltip :content="mapTip" placement="top" :disabled="canMap">
          <span class="mp-arrow-wrap">
            <el-button class="mp-arrow" type="primary" :disabled="!canMap" @click="doMap">
              →
            </el-button>
          </span>
        </el-tooltip>
        <el-tooltip :content="unmapTip" placement="bottom" :disabled="canUnmap">
          <span class="mp-arrow-wrap">
            <el-button class="mp-arrow" :disabled="!canUnmap" @click="doUnmap"> ← </el-button>
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

    <!-- ============ 映射明细（收起，只作同步/编辑的入口） ============ -->
    <el-collapse v-model="openDetail" class="mp-detail">
      <el-collapse-item name="detail">
        <template #title>
          <span class="mp-col-title">映射明细（{{ mappings.length }} 条）</span>
          <span class="text-secondary mp-detail-hint">
            同步、改名、启用开关在这里；日常增删用上面的箭头就够了
          </span>
        </template>

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
          <el-table-column label="操作" width="130" fixed="right">
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
            </template>
          </el-table-column>
        </el-table>
      </el-collapse-item>
    </el-collapse>

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

// 左树：lazy 模式。因为它要长三种节点 —— 系统目录 / 该目录下已挂的映射 /
// 映射展开后的远端内容 —— 后两种都依赖异步数据，用静态 data 拼不出来。
const catTreeRef = ref(null)
const leftKey = ref(0)
const leftLoading = ref(false)
const catCheckedIds = ref([])
// lazy 树的 isLeaf / disabled 由节点字段控制
const leftProps = { label: 'name', isLeaf: 'isLeaf', disabled: 'disabled' }

const remoteTreeRef = ref(null)
const remoteChecked = ref([])
const recursive = ref(true)

const openDetail = ref([]) // 明细默认收起
const dialog = ref(false)
const form = ref({})

/** categoryId → 直接挂在这一层上的映射条数 */
const mapCountByCat = computed(() => {
  const m = {}
  for (const x of mappings.value) m[x.categoryId] = (m[x.categoryId] || 0) + 1
  return m
})

/** 右侧树上打「已映射」标的 key 集合 */
const mappedKeys = computed(() =>
  mappings.value.map((m) => `${m.accountId}::${m.remoteDirId || ''}`)
)

// 「映射」左边必须**恰好一个系统目录**：一个网盘目录挂到两个系统目录没有意义
// （后端按 file_id 去重，第二处同步过去是 0 条）。
const canMap = computed(() => {
  const keys = catCheckedIds.value.map(String)
  const catOnly = keys.filter((k) => !k.startsWith('map-'))
  return catOnly.length === 1 && keys.length === 1 && remoteChecked.value.length > 0
})
const canUnmap = computed(() => catCheckedIds.value.length > 0)

const mapTip = computed(() => {
  if (!remoteChecked.value.length) return '先在右边勾选要映射的网盘目录'
  const keys = catCheckedIds.value.map(String)
  if (!keys.length) return '先在左边勾选一个系统目录'
  if (keys.length > 1) return '映射时左边只能选一个目录（解绑可以多选）'
  if (keys[0].startsWith('map-')) return '左边要选「系统目录」，不是已挂的映射'
  return '挂过去'
})
const unmapTip = computed(() =>
  canUnmap.value ? '解绑并放回右边' : '先在左边勾选目录或已挂的映射'
)

function diskName(t) {
  return (
    { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}

function fmtSize(n) {
  const v = Number(n) || 0
  if (v >= 1024 * 1024 * 1024) return (v / 1024 / 1024 / 1024).toFixed(1) + 'G'
  if (v >= 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + 'M'
  if (v >= 1024) return (v / 1024).toFixed(0) + 'K'
  return v ? v + 'B' : '文件'
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
    // 映射变了，左树上挂的那些"已映射"节点也得跟着变 —— 整棵重建
    leftKey.value += 1
    catCheckedIds.value = []
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}

/* ============ 左树的三种节点 ============ */

function catNode(c) {
  return {
    id: c.id,
    name: c.name,
    kind: 'category',
    parentId: c.parentId,
    children: c.children || [],
    mapCount: mapCountByCat.value[c.id] || 0,
  }
}

function mapNode(m) {
  return {
    id: `map-${m.id}`,
    name: m.remoteDirName || '根目录',
    kind: 'mapping',
    mappingId: m.id,
    accountId: m.accountId,
    remoteDirId: m.remoteDirId,
    diskType: m.account ? m.account.diskType : '',
    enabled: m.enabled,
  }
}

/** 列出某个远端目录的内容（映射节点、以及它下面的子目录都用它）。 */
async function remoteChildren(accountId, remoteDirId) {
  try {
    const res = await diskApi.browse(accountId, remoteDirId)
    return (res && res.items ? res.items : []).map((it) => ({
      // 远端 id 在不同账号下可能重名，加账号前缀保证 node-key 唯一
      id: `r-${accountId}-${it.id}`,
      name: it.name,
      kind: 'remote',
      isDir: !!it.isDir,
      size: it.size,
      accountId,
      remoteDirId: it.id,
      // 文件没有下级，别给它渲染展开箭头
      isLeaf: !it.isDir,
      // 远端内容只是"给你看里面有什么"，不是操作对象，不给勾选框
      disabled: true,
    }))
  } catch (e) {
    ElMessage.warning('远端目录打不开：' + ((e && e.message) || '账号可能失效或网络不通'))
    return []
  }
}

async function loadLeftNode(node, resolve) {
  // 根：只放系统目录的最上层（默认收起）
  if (node.level === 0) {
    resolve(categoryTree.value.map(catNode))
    return
  }
  const d = node.data || {}
  if (d.kind === 'mapping' || (d.kind === 'remote' && d.isDir)) {
    resolve(await remoteChildren(d.accountId, d.remoteDirId))
    return
  }
  if (d.kind === 'remote') {
    resolve([])
    return
  }
  // 系统目录：先放子目录，再放"已经挂在这个目录上的网盘目录"
  const kids = (d.children || []).map(catNode)
  const maps = mappings.value.filter((m) => m.categoryId === d.id).map(mapNode)
  resolve([...kids, ...maps])
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
  const catId = Number(catCheckedIds.value.map(String).filter((k) => !k.startsWith('map-'))[0])
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

  if (failed.length) ElMessage.warning(`有 ${failed.length} 条没挂上：${failed.join('；')}`)
  if (!created.length) return

  await loadAll()
  remoteTreeRef.value?.clearChecked()

  // 建完直接同步一次 —— 只挂映射不导资源的话孩子端还是空的，
  // 用户会以为"没生效"。这里多花几秒，换掉一次困惑。
  const syncMsg = await syncIds(created)
  ElMessage.success(`已挂上 ${created.length} 条映射${syncMsg}`)
}

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
  const keys = catCheckedIds.value.map(String)
  // 直接勾了"已挂的映射"节点 → 就解绑那几条
  const directIds = keys.filter((k) => k.startsWith('map-')).map((k) => Number(k.slice(4)))
  // 勾了系统目录 → 它（含子孙）下面的所有映射
  const catIds = keys.filter((k) => !k.startsWith('map-')).map(Number)
  const scope = withDescendants(catIds)

  const hits = mappings.value.filter(
    (m) => directIds.includes(m.id) || scope.has(m.categoryId)
  )
  if (!hits.length) {
    ElMessage.info('选中的目录下没有映射')
    return
  }

  // 删资源不可逆，所以摆到明面上让用户选，而不是替他决定。
  // 默认勾上：用户的直觉就是"把这个资源拿回去"，留孤儿资源反而会被当成没生效。
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
  if (failed.length) ElMessage.warning(`解绑了 ${done} 条，${failed.length} 条失败：${failed[0]}`)
  else ElMessage.success(`已解绑 ${done} 条映射${removed ? `，清理 ${removed} 个资源` : ''}`)
}

/* ============ 明细里的操作 ============ */
async function toggleEnabled(row, v) {
  try {
    await mappingApi.update(row.id, { enabled: v })
    row.enabled = v
    leftKey.value += 1 // 左树上的"已停用"标记要跟着变
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
    // 远端与目标目录不允许改：改这两项等于换一条映射，应该删掉重建，
    // 否则已导入的资源会挂在说不清的来源上。
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
  margin-bottom: 14px;
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
  height: 440px;
  overflow: auto;
  padding: 6px 4px;
}
.mp-node {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.mp-icon {
  flex: 0 0 auto;
}
.mp-node-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 已挂的映射：和系统目录在视觉上分得开，一眼能看出"这是挂进来的" */
.mp-node-name.is-mapping {
  color: var(--el-color-success);
  font-weight: 500;
}
.mp-node-name.is-off {
  color: var(--ls-text-secondary);
  text-decoration: line-through;
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

/* ============ 映射明细 ============ */
.mp-detail {
  border-top: 1px solid var(--ls-border, #e5e7eb);
  border-bottom: none;
}
.mp-detail :deep(.el-collapse-item__header) {
  font-size: 13px;
  gap: 12px;
}
.mp-detail-hint {
  font-size: 12px;
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
