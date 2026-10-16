<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">目录管理</h2>
      <div>
        <el-button :icon="Plus" type="primary" @click="openCreate(null)">
          新建一级目录
        </el-button>
        <el-button :icon="Refresh" @click="loadTree">刷新</el-button>
      </div>
    </div>

    <el-alert
      type="info"
      :closable="false"
      class="mb-16"
      title="目录支持任意层级。前端会按这里的层级展示，一级为大图标入口，逐级进入。"
    />

    <div class="card-box">
      <el-tree
        :data="tree"
        node-key="id"
        :props="{ label: 'name', children: 'children' }"
        default-expand-all
        :expand-on-click-node="false"
        draggable
        @node-drop="onNodeDrop"
      >
        <template #default="{ node, data }">
          <div class="tree-node">
            <div class="node-left">
              <span class="node-icon" :style="{ background: data.color || '#4a90d9' }">
                {{ iconEmoji(data.icon) }}
              </span>
              <span class="node-name">{{ data.name }}</span>
              <el-tag v-if="data.isPublic" size="small" type="success">公开</el-tag>
              <el-tag v-if="data.hidden" size="small" type="info">孩子端隐藏</el-tag>
              <span class="node-count">{{ data.resourceCount || 0 }} 个资源</span>
            </div>
            <div class="node-actions">
              <el-button link type="primary" size="small" @click.stop="openCreate(data)">
                添加子目录
              </el-button>
              <el-button link type="primary" size="small" @click.stop="openEdit(data)">
                编辑
              </el-button>
              <el-button link type="danger" size="small" @click.stop="removeNode(data)">
                删除
              </el-button>
            </div>
          </div>
        </template>
      </el-tree>

      <el-empty v-if="!tree.length" description="还没有目录，点击右上角新建" />
    </div>

    <!-- 创建/编辑对话框 -->
    <el-dialog
      v-model="dialogVisible"
      :title="editing ? '编辑目录' : '新建目录'"
      width="560px"
    >
      <el-form :model="form" label-width="90px">
        <el-form-item label="上级目录" v-if="!editing">
          <span>{{ parentName || '（一级目录）' }}</span>
        </el-form-item>
        <el-form-item label="目录名称" required>
          <el-input v-model="form.name" placeholder="如：乐学英语" maxlength="50" />
        </el-form-item>
        <el-form-item label="图标">
          <div class="icon-picker">
            <div
              v-for="ic in icons"
              :key="ic.key"
              class="icon-item"
              :class="{ active: form.icon === ic.key }"
              :title="ic.name"
              @click="form.icon = ic.key"
            >
              {{ ic.emoji }}
            </div>
          </div>
        </el-form-item>
        <el-form-item label="主题色">
          <el-color-picker v-model="form.color" :predefine="presetColors" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input
            v-model="form.description"
            type="textarea"
            :rows="2"
            placeholder="可选"
          />
        </el-form-item>
        <el-form-item label="公开访问">
          <el-switch v-model="form.isPublic" />
          <span class="text-secondary" style="margin-left: 10px">
            公开目录无需授权即可被所有用户访问
          </span>
        </el-form-item>
        <el-form-item label="孩子端隐藏">
          <el-switch v-model="form.hidden" />
          <span class="text-secondary" style="margin-left: 10px">
            不在孩子端「我的学习乐园」里显示。用于「搜全网」这类自动收录的容器目录：
            内容照样能被搜到、能播，只是不作为一块学习板块露出来
          </span>
        </el-form-item>
        <el-form-item label="排序">
          <el-input-number v-model="form.sortOrder" :min="0" />
        </el-form-item>

        <!-- 从网盘目录创建：选一个远端目录，目录名自动填上，
             保存时顺手把映射也建好 —— 省掉「先建目录、再去资源映射加一条」两步。 -->
        <el-form-item v-if="!editing" label="网盘目录">
          <div class="remote-pick">
            <el-input
              :model-value="remote ? remote.path : ''"
              readonly
              placeholder="可选：选一个网盘目录，保存时一并建立映射"
            >
              <template #append>
                <el-button @click="openPicker">浏览</el-button>
              </template>
            </el-input>
            <el-button v-if="remote" link type="danger" @click="clearRemote">清除</el-button>
          </div>
          <div class="form-tip">
            <template v-if="remote">
              保存后会同时建立一条<b>递归映射</b>，把该目录里的媒体文件登记成资源。
              目录名已按网盘目录名填好，可以改。<br />
              想建完马上把资源拉下来，用下面的「<b>保存并同步</b>」。
            </template>
            <template v-else>
              选好网盘目录后，目录名会自动填上；不想建映射就留空。
            </template>
          </div>
        </el-form-item>

        <!-- 同步：编辑目录时就地同步，不再逼用户跑去「资源映射」页找那一条。
             ⚠️ 一个系统目录理论上可以挂多条映射（虽然少见），
             所以这里列全并逐条同步，而不是只同步第一条。 -->
        <el-form-item v-if="editing" label="映射同步">
          <div v-if="catMappings.length" class="sync-box">
            <div v-for="m in catMappings" :key="m.id" class="sync-row">
              <span class="sync-name">{{ m.name || ('映射 #' + m.id) }}</span>
              <span class="text-secondary">{{ syncHint(m) }}</span>
            </div>
            <el-button
              type="success"
              size="small"
              :icon="RefreshRight"
              :loading="syncing"
              @click="syncCat"
            >
              立即同步（{{ catMappings.length }} 条）
            </el-button>
          </div>
          <span v-else class="text-secondary">
            这个目录还没有网盘映射。新建目录时选一个「网盘目录」就会自动建一条。
          </span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button v-if="!editing && remote" :loading="saving" @click="save(true)">
          保存并同步
        </el-button>
        <el-button type="primary" :loading="saving" @click="save(false)">保存</el-button>
      </template>
    </el-dialog>

    <!-- 选网盘目录：复用 RemoteDirTree，与「资源映射」页是同一棵树 -->
    <el-dialog v-model="pickerVisible" title="选择网盘目录" width="660px" append-to-body>
      <RemoteDirTree :accounts="accounts" style="height: 380px" @pick="onPickRemote" />
      <div class="picked-line">
        已选：<b>{{ remote ? remote.path : '（还没选）' }}</b>
      </div>
      <template #footer>
        <el-button @click="pickerVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!remote" @click="confirmPick">
          就用这个目录
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { categoryApi, diskApi, mappingApi } from '@/api'
import RemoteDirTree from '@/components/RemoteDirTree.vue'

const tree = ref([])
const icons = ref([])
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const parentName = ref('')
const currentId = ref(null)

// ============ 从网盘目录创建（可选） ============
const accounts = ref([])
const pickerVisible = ref(false)
// 选中的远端目录（null = 用户没打算建映射）
const remote = ref(null)
// 当前编辑的目录已挂的映射（编辑时用来就地同步）
const catMappings = ref([])
const syncing = ref(false)

const form = ref({
  name: '',
  icon: 'folder',
  color: '#4a90d9',
  description: '',
  isPublic: false,
  hidden: false,
  sortOrder: 0,
  parentId: null,
})

const presetColors = [
  '#4a90d9', '#5ad8a6', '#f6bd16', '#d9554a', '#9270ca',
  '#6dc8ec', '#ff9d4d', '#269a99', '#f08bb4', '#5b8ff9',
]

function iconEmoji(key) {
  const found = icons.value.find((i) => i.key === key)
  return found ? found.emoji : '📁'
}

async function loadTree() {
  try {
    tree.value = await categoryApi.tree(true)
  } catch (e) {
    /* 忽略 */
  }
}

async function loadIcons() {
  try {
    icons.value = await categoryApi.icons()
  } catch (e) {
    /* 忽略 */
  }
}

/** 接入源列表。失败不打断建目录本身（不建映射也用得上这个弹窗）。 */
async function loadAccounts() {
  try {
    accounts.value = (await diskApi.list()) || []
  } catch (e) {
    accounts.value = []
  }
}

function openPicker() {
  if (!accounts.value.length) {
    ElMessage.warning('还没有接入源，先到「资源接入」添加一个网盘')
    return
  }
  pickerVisible.value = true
}

function onPickRemote(p) {
  remote.value = p
}

function confirmPick() {
  if (!remote.value) return
  // 自动填目录名。远端"根目录"没有名字，这时别覆盖用户已经填好的。
  if (remote.value.dirName) form.value.name = remote.value.dirName
  pickerVisible.value = false
}

function clearRemote() {
  remote.value = null
}

function openCreate(parent) {
  editing.value = false
  currentId.value = null
  remote.value = null
  parentName.value = parent?.name || ''
  form.value = {
    name: '',
    icon: 'folder',
    color: '#4a90d9',
    description: '',
    isPublic: false,
    hidden: false,
    sortOrder: 0,
    parentId: parent?.id || null,
  }
  dialogVisible.value = true
}

function openEdit(data) {
  editing.value = true
  currentId.value = data.id
  form.value = {
    name: data.name,
    icon: data.icon || 'folder',
    color: data.color || '#4a90d9',
    description: data.description || '',
    isPublic: data.isPublic || false,
    hidden: data.hidden || false,
    sortOrder: data.sortOrder || 0,
    parentId: data.parentId,
  }
  dialogVisible.value = true
  loadCatMappings(data.id)
}

/** 拉这个目录已挂的映射。失败只当作"没有"，不打断编辑本身。 */
async function loadCatMappings(categoryId) {
  catMappings.value = []
  if (!categoryId) return
  try {
    catMappings.value = (await mappingApi.list({ categoryId })) || []
  } catch (e) {
    catMappings.value = []
  }
}

/** 映射的一行说明：上次同步结果比"有没有配"更值得看 —— 配了但从没同步过才是常态。 */
function syncHint(m) {
  if (!m.lastSyncAt) return '还没同步过'
  const t = fmtSyncTime(m.lastSyncAt)
  if (m.lastSyncError) return `${t} 同步失败：${m.lastSyncError}`
  return `${t} 同步 +${m.lastSyncCreated || 0} 条`
}

function fmtSyncTime(v) {
  try {
    const d = new Date(v)
    if (Number.isNaN(d.getTime())) return String(v)
    const p = (n) => String(n).padStart(2, '0')
    return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
  } catch (e) {
    return String(v)
  }
}

/**
 * 就地同步这个目录挂着的映射。
 *
 * ⚠️ 逐条串行而不是 Promise.all：同步会真的去列网盘目录（递归可能几十秒），
 * 并发发出去容易被网盘侧风控，而且报错会糊成一条 —— 用户看不清是哪条失败。
 */
async function syncCat() {
  if (!catMappings.value.length) return
  syncing.value = true
  let created = 0
  const errs = []
  try {
    for (const m of catMappings.value) {
      try {
        const r = await mappingApi.sync(m.id)
        created += (r && r.created) || 0
        if (r && r.error) errs.push(`${m.name || m.id}：${r.error}`)
      } catch (e) {
        errs.push(`${m.name || m.id}：${(e && e.message) || '同步失败'}`)
      }
    }
    if (errs.length) {
      ElMessage.warning(`同步有失败：${errs.join('；')}`)
    } else if (created > 0) {
      ElMessage.success(`同步完成，新登记 ${created} 个资源`)
    } else {
      ElMessage.success('同步完成，没有新资源（已是最新）')
    }
    loadCatMappings(currentId.value)
    loadTree()
  } finally {
    syncing.value = false
  }
}

async function save(andSync) {
  if (!form.value.name.trim()) {
    ElMessage.warning('请输入目录名称')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await categoryApi.update(currentId.value, form.value)
      ElMessage.success('更新成功')
    } else {
      const created = await categoryApi.create(form.value)
      // 建映射是"顺手做的事"，它失败**不能吞掉**：
      // 目录已经建出来了，用户会以为映射也成了，回头发现孩子端空空如也。
      let mappingErr = ''
      let mappingId = 0
      if (remote.value && created && created.id) {
        try {
          const m = await mappingApi.create({
            name: remote.value.dirName || created.name,
            accountId: remote.value.accountId,
            remoteDirId: remote.value.dirId || '',
            remoteDirName: remote.value.dirName || '',
            categoryId: created.id,
            recursive: true,
            enabled: true,
          })
          mappingId = (m && m.id) || 0
        } catch (e) {
          mappingErr = (e && e.message) || '未知原因'
        }
      }
      if (mappingErr) {
        ElMessage.warning(`目录已创建，但映射没建上：${mappingErr}。请到「资源映射」手动补一条`)
      } else if (remote.value) {
        // 「保存并同步」：建完映射**当场**把资源拉下来。
        // ⚠️ 同步失败不能把"目录+映射已建好"这件事说成失败 ——
        //    它们是两步：目录/映射已经落库，同步只是没拉到资源，重试即可。
        if (andSync && mappingId) {
          try {
            const r = await mappingApi.sync(mappingId)
            if (r && r.error) {
              ElMessage.warning(`目录与映射已建好，但同步失败：${r.error}。稍后可再同步`)
            } else {
              ElMessage.success(
                `创建成功，已同步登记 ${(r && r.created) || 0} 个资源` +
                  ((r && r.skipped) ? `（跳过 ${r.skipped} 个已存在的）` : '')
              )
            }
          } catch (e) {
            ElMessage.warning(
              `目录与映射已建好，但同步没能执行：${(e && e.message) || '未知原因'}。稍后可再同步`
            )
          }
        } else {
          ElMessage.success('创建成功，并建好了映射（可用「保存并同步」一步到位）')
        }
      } else {
        ElMessage.success('创建成功')
      }
    }
    dialogVisible.value = false
    remote.value = null
    loadTree()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function removeNode(data) {
  try {
    // 说清"会连带删掉什么"：后端会把该目录（含子目录）下的资源与
    // 资源映射一起删掉，所以提示里必须写明，否则用户以为只是删个空目录。
    const cnt = data.resourceCount ? `该目录（含子目录）下有 ${data.resourceCount} 个资源，` : ''
    await ElMessageBox.confirm(
      `确定删除目录「${data.name}」吗？${cnt}其下的资源与「资源映射」将一并删除，此操作不可撤销。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '确定删除', cancelButtonText: '取消' }
    )
    // 响应拦截器已经把 data.data 拆出来了，这里拿到的是
    // { removedResources, removedMappings }
    const res = await categoryApi.remove(data.id, true)
    const r = res?.removedResources || 0
    const m = res?.removedMappings || 0
    ElMessage.success(r || m ? `删除成功，并清理了 ${r} 个资源、${m} 条映射` : '删除成功')
    loadTree()
  } catch (e) {
    /* 取消或失败 */
  }
}

async function onNodeDrop(draggingNode, dropNode, dropType) {
  // 拖拽后同步到后端
  try {
    let targetParentId = null
    if (dropType === 'inner') {
      targetParentId = dropNode.data.id
    } else {
      targetParentId = dropNode.data.parentId
    }
    await categoryApi.move(draggingNode.data.id, {
      targetParentId,
      sortOrder: 0,
    })
    ElMessage.success('移动成功')
    loadTree()
  } catch (e) {
    loadTree()
  }
}

onMounted(() => {
  loadTree()
  loadIcons()
  loadAccounts()
})
</script>

<style scoped>
.tree-node {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding-right: 12px;
}

.node-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.node-icon {
  width: 26px;
  height: 26px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
}

.node-name {
  font-weight: 500;
}

.node-count {
  font-size: 12px;
  color: var(--ls-text-secondary);
}

.icon-picker {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 160px;
  overflow-y: auto;
}

.icon-item {
  width: 34px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
  border: 1px solid var(--ls-border);
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.15s;
}

.icon-item:hover {
  border-color: var(--ls-primary);
}

.icon-item.active {
  border-color: var(--ls-primary);
  background: #ecf5ff;
}

.remote-pick {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
}

.form-tip {
  font-size: 12px;
  color: var(--ls-text-secondary);
  line-height: 1.7;
}

.picked-line {
  margin-top: 10px;
  font-size: 13px;
  color: var(--ls-text-secondary);
  word-break: break-all;
}

/* 编辑目录时的「映射同步」块。
   ⚠️ 「上次同步结果」用等宽数字：时间与条数上下对齐后，
   扫一眼就能看出哪条最近同步过、哪条一直是失败的。 */
.sync-box {
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: flex-start;
}

.sync-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 12.5px;
  line-height: 1.6;
}

.sync-name {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
</style>
