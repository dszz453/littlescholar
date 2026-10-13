<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源管理</h2>
      <div class="header-actions">
        <el-radio-group v-model="viewMode" size="default" @change="onViewChange">
          <el-radio-button value="list">列表</el-radio-button>
          <el-radio-button value="tree">目录</el-radio-button>
        </el-radio-group>
        <el-button :icon="FolderAdd" @click="openCreateDir">新建目录</el-button>
        <el-button :icon="Refresh" @click="loadList">刷新</el-button>
        <el-button
          v-if="viewMode === 'list'"
          :icon="FolderOpened"
          :disabled="!selected.length"
          @click="openMoveDialog"
        >
          移动到目录
        </el-button>
        <el-button
          v-if="viewMode === 'list'"
          :icon="VideoPlay"
          :disabled="!selected.length"
          @click="batchCheck"
        >
          存活检测
        </el-button>
        <el-button
          v-if="viewMode === 'list'"
          type="danger"
          :icon="Delete"
          :disabled="!selected.length"
          @click="batchDelete"
        >
          批量删除
        </el-button>
      </div>
    </div>

    <!-- 筛选 -->
    <div class="card-box mb-16">
      <el-row :gutter="12">
        <el-col :span="6">
          <el-tree-select
            v-model="query.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            clearable
            node-key="id"
            placeholder="按目录筛选"
            style="width: 100%"
            @change="loadList"
          />
        </el-col>
        <el-col :span="5">
          <el-input
            v-model="query.keyword"
            placeholder="搜索资源标题"
            clearable
            @keyup.enter="loadList"
          >
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
        </el-col>
        <el-col :span="4">
          <el-select v-model="query.diskType" placeholder="网盘类型" clearable style="width: 100%" @change="loadList">
            <el-option label="夸克" value="quark" />
            <el-option label="百度" value="baidu" />
            <el-option label="UC" value="uc" />
            <el-option label="天翼" value="tianyi" />
            <el-option label="阿里" value="aliyun" />
            <el-option label="WebDAV" value="webdav" />
          </el-select>
        </el-col>
        <el-col :span="4">
          <el-select v-model="query.status" placeholder="存活状态" clearable style="width: 100%" @change="loadList">
            <el-option label="有效" value="alive" />
            <el-option label="失效" value="dead" />
            <el-option label="未知" value="unknown" />
          </el-select>
        </el-col>
        <el-col :span="5">
          <el-checkbox v-model="query.includeSub" @change="loadList">包含子目录</el-checkbox>
          <el-button type="primary" @click="loadList">查询</el-button>
        </el-col>
      </el-row>
    </div>

    <!-- 列表 -->
    <div class="card-box" v-show="viewMode === 'list'">
      <el-table
        :data="list"
        v-loading="loading"
        @selection-change="(r) => (selected = r)"
        empty-text="暂无资源"
      >
        <el-table-column type="selection" width="45" />
        <el-table-column label="资源标题" min-width="240">
          <template #default="{ row }">
            <div class="res-title">{{ row.title }}</div>
            <div class="text-secondary">{{ row.category?.name || '未分类' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="网盘" width="90">
          <template #default="{ row }">
            <span :class="`disk-${row.diskType}`">{{ diskName(row.diskType) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ mediaTypeText(row.mediaType) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="存活" width="90">
          <template #default="{ row }">
            <el-tag :type="aliveTagType(row.status)" size="small">
              {{ aliveText(row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="播放量" width="80" prop="viewCount" />
        <el-table-column label="操作" width="180" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="play(row)">
              播放
            </el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">
              编辑
            </el-button>
            <el-button link type="danger" size="small" @click="removeOne(row)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-16 flex justify-between items-center">
        <span class="text-secondary">共 {{ total }} 条</span>
        <el-pagination
          v-model:current-page="query.page"
          v-model:page-size="query.pageSize"
          :total="total"
          :page-sizes="[20, 50, 100]"
          layout="sizes, prev, pager, next, jumper"
          @current-change="loadList"
          @size-change="loadList"
        />
      </div>
    </div>

    <!-- 目录树视图 -->
    <div class="card-box" v-show="viewMode === 'tree'">
      <div class="tree-toolbar">
        <span class="text-secondary">
          共 {{ treeFileCount }} 个资源，按网盘目录折叠
        </span>
        <div>
          <el-button link type="primary" size="small" @click="expandAll">全部展开</el-button>
          <el-button link type="primary" size="small" @click="collapseAll">全部折叠</el-button>
        </div>
      </div>
      <el-tree
        ref="treeRef"
        :data="treeData"
        node-key="key"
        :props="{ label: 'label', children: 'children' }"
        v-loading="loading"
        empty-text="暂无资源"
        :expand-on-click-node="false"
      >
        <template #default="{ data }">
          <div class="tree-node" :class="{ 'is-file': data.isFile }">
            <el-icon v-if="!data.isFile" class="node-ico folder">
              <Folder />
            </el-icon>
            <el-icon v-else class="node-ico">
              <Document />
            </el-icon>
            <span class="node-label" :title="data.label">{{ data.label }}</span>

            <template v-if="data.isFile">
              <span class="node-tags">
                <el-tag size="small" type="info">{{ mediaTypeText(data.mediaType) }}</el-tag>
                <el-tag :type="aliveTagType(data.status)" size="small">{{ aliveText(data.status) }}</el-tag>
              </span>
              <span class="node-ops">
                <el-button link type="primary" size="small" @click.stop="play(data.resource)">
                  播放
                </el-button>
                <el-button link type="primary" size="small" @click.stop="openEdit(data.resource)">
                  编辑
                </el-button>
                <el-button link type="danger" size="small" @click.stop="removeOne(data.resource)">
                  删除
                </el-button>
              </span>
            </template>
            <span v-else class="node-count">{{ data.count }}</span>
          </div>
        </template>
      </el-tree>
    </div>

    <!-- 新建目录对话框 -->
    <el-dialog v-model="dirDialog" title="新建目录" width="480px">
      <el-form :model="dirForm" label-width="80px">
        <el-form-item label="目录名称">
          <el-input v-model="dirForm.name" placeholder="例如：新概念英语" />
        </el-form-item>
        <el-form-item label="上级目录">
          <el-tree-select
            v-model="dirForm.parentId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            clearable
            node-key="id"
            placeholder="不选则为顶级目录"
            style="width: 100%"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dirDialog = false">取消</el-button>
        <el-button type="primary" @click="saveCreateDir">创建</el-button>
      </template>
    </el-dialog>

    <!-- 移动资源到目录对话框 -->
    <el-dialog v-model="moveDialog" title="移动到目录" width="480px">
      <div class="mb-12 text-secondary">
        已选 {{ selected.length }} 个资源，移动到：
      </div>
      <el-tree-select
        v-model="moveCategoryId"
        :data="categoryTree"
        :props="{ label: 'name', value: 'id', children: 'children' }"
        check-strictly
        node-key="id"
        placeholder="选择目标目录"
        style="width: 100%"
      />
      <template #footer>
        <el-button @click="moveDialog = false">取消</el-button>
        <el-button type="primary" @click="saveMove">移动</el-button>
      </template>
    </el-dialog>

    <!-- 编辑对话框 -->
    <el-dialog v-model="editDialog" title="编辑资源" width="520px">
      <el-form :model="editForm" label-width="80px">
        <el-form-item label="标题">
          <el-input v-model="editForm.title" />
        </el-form-item>
        <el-form-item label="所属目录">
          <el-tree-select
            v-model="editForm.categoryId"
            :data="categoryTree"
            :props="{ label: 'name', value: 'id', children: 'children' }"
            check-strictly
            node-key="id"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="editForm.description" type="textarea" :rows="2" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editDialog = false">取消</el-button>
        <el-button type="primary" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref, computed } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search, Delete, VideoPlay, Folder, Document, FolderAdd, FolderOpened } from '@element-plus/icons-vue'
import { resourceApi, categoryApi } from '@/api'

const route = useRoute()

const viewMode = ref('list') // list 平铺 / tree 目录树
const list = ref([])
const total = ref(0)
const loading = ref(false)
const selected = ref([])
const categoryTree = ref([])
const treeRef = ref(null)

const query = reactive({
  page: 1,
  pageSize: 20,
  categoryId: null,
  keyword: '',
  diskType: '',
  status: '',
  includeSub: true,
})

const editDialog = ref(false)
const editForm = ref({})

const dirDialog = ref(false)
const dirForm = reactive({ name: '', parentId: null })
const moveDialog = ref(false)
const moveCategoryId = ref(null)

function openCreateDir() {
  dirForm.name = ''
  dirForm.parentId = null
  dirDialog.value = true
}

async function saveCreateDir() {
  if (!dirForm.name.trim()) {
    ElMessage.warning('请输入目录名称')
    return
  }
  try {
    await categoryApi.create({
      name: dirForm.name.trim(),
      parentId: dirForm.parentId || null,
    })
    ElMessage.success('目录已创建')
    dirDialog.value = false
    await loadCategories()
  } catch (e) {
    /* 忽略 */
  }
}

function openMoveDialog() {
  moveCategoryId.value = null
  moveDialog.value = true
}

async function saveMove() {
  if (!moveCategoryId.value) {
    ElMessage.warning('请选择目标目录')
    return
  }
  try {
    await resourceApi.move(selected.value.map((r) => r.id), moveCategoryId.value)
    ElMessage.success('已移动')
    moveDialog.value = false
    loadList()
  } catch (e) {
    /* 忽略 */
  }
}

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里', webdav: 'WebDAV' }[t] || t || '-'
}

function mediaTypeText(t) {
  return { video: '视频', audio: '音频', pdf: 'PDF', image: '图片', other: '其他' }[t] || '未知'
}

function aliveText(s) {
  return { alive: '有效', dead: '失效', unknown: '未知' }[s] || '未知'
}

function aliveTagType(s) {
  return { alive: 'success', dead: 'danger', unknown: 'info' }[s] || 'info'
}

async function loadList() {
  loading.value = true
  try {
    const params = { ...query }
    if (!params.categoryId) delete params.categoryId
    // 目录树视图需要完整数据才能构建层级，拉满后端分页上限（200）。
    if (viewMode.value === 'tree') {
      params.page = 1
      params.pageSize = 200
    }
    const res = await resourceApi.list(params)
    list.value = res.list || []
    total.value = res.total || 0
  } catch (e) {
    /* 忽略 */
  } finally {
    loading.value = false
  }
}

function onViewChange() {
  loadList()
}

// ============ 目录树视图 ============

// treeData：把资源的 ownerDirId（网盘内目录路径）拆成层级，
// 文件挂在叶子目录下，可展开/折叠。
const treeData = computed(() => buildDirTree(list.value))
const treeFileCount = computed(() => {
  let n = 0
  for (const r of list.value) n++
  return n
})

// buildDirTree 把平铺资源转成目录树。
//
// ownerDirId 形如 /kuake/孩子学习/英语学习/《新概念英语》大合集/…/001-020【6.91GB】
// 所有资源通常共享一个或多个根目录，直接按完整路径建树会得到一个
// 很深的、几乎每层只有一个子节点的链。所以先找出「公共前缀」砍掉，
// 只保留有区分度的后缀层级，树才真正起到「折叠」的作用。
function buildDirTree(resources) {
  const dirs = resources
    .map((r) => r.ownerDirId || '')
    .filter((d) => d)
  // 公共前缀：取所有目录路径的公共段
  const common = commonPrefix(dirs)
  const root = { key: 'root', label: '全部资源', children: [] }
  const map = { root }

  for (const r of resources) {
    const dir = r.ownerDirId || ''
    // 去掉公共前缀，得到相对路径段
    const rel = dir.startsWith(common) ? dir.slice(common.length) : dir
    const parts = rel.split('/').filter((s) => s !== '')
    // 逐段下钻，建立文件夹节点
    let cur = root
    let curKey = 'root'
    for (const seg of parts) {
      const nextKey = curKey + '|' + seg
      if (!map[nextKey]) {
        const node = { key: nextKey, label: seg, isFile: false, count: 0, children: [] }
        map[nextKey] = node
        cur.children.push(node)
      }
      cur = map[nextKey]
      curKey = nextKey
    }
    // 文件叶子节点
    const fileKey = 'file|' + r.id
    const fileNode = {
      key: fileKey,
      label: r.title,
      isFile: true,
      mediaType: r.mediaType,
      status: r.status,
      resource: r,
    }
    cur.children.push(fileNode)
    // 沿路径累加文件夹计数
    let walk = root
    for (const seg of parts) {
      const k = (walk === root ? 'root' : walk.key) + '|' + seg
      const node = map[k]
      if (node) node.count++
      walk = node
    }
  }
  return root.children
}

// commonPrefix 返回一组以 / 分隔的路径的公共前缀（含末尾 /）。
function commonPrefix(paths) {
  if (!paths.length) return ''
  const first = paths[0]
  let end = first.length
  for (const p of paths.slice(1)) {
    let i = 0
    const max = Math.min(end, p.length)
    while (i < max && first[i] === p[i]) i++
    // 不能截断到路径段的中间，回退到上一个 /
    while (i > 0 && first[i - 1] !== '/') i--
    if (i < end) end = i
    if (end === 0) break
  }
  return first.slice(0, end)
}

function expandAll() {
  if (!treeRef.value) return
  const keys = []
  collectKeys(treeData.value, keys)
  keys.forEach((k) => treeRef.value.store.nodesMap[k]?.expand())
}

function collapseAll() {
  if (!treeRef.value) return
  const keys = []
  collectKeys(treeData.value, keys)
  keys.forEach((k) => treeRef.value.store.nodesMap[k]?.collapse())
}

function collectKeys(nodes, out) {
  for (const n of nodes) {
    if (!n.isFile) {
      out.push(n.key)
      collectKeys(n.children || [], out)
    }
  }
}

async function loadCategories() {
  try {
    categoryTree.value = await categoryApi.tree(false)
  } catch (e) {
    /* 忽略 */
  }
}

function play(row) {
  window.open(`/play/${row.id}`, '_blank')
}

function openEdit(row) {
  editForm.value = {
    id: row.id,
    title: row.title,
    categoryId: row.categoryId,
    description: row.description,
  }
  editDialog.value = true
}

async function saveEdit() {
  await resourceApi.update(editForm.value.id, {
    title: editForm.value.title,
    categoryId: editForm.value.categoryId,
    description: editForm.value.description,
  })
  ElMessage.success('保存成功')
  editDialog.value = false
  loadList()
}

async function removeOne(row) {
  try {
    await ElMessageBox.confirm(`确定删除「${row.title}」吗？`, '删除确认', { type: 'warning' })
    await resourceApi.batchDelete([row.id])
    ElMessage.success('已删除')
    loadList()
  } catch (e) {
    /* 取消 */
  }
}

async function batchDelete() {
  try {
    await ElMessageBox.confirm(`确定删除选中的 ${selected.value.length} 个资源吗？`, '删除确认', {
      type: 'warning',
    })
    await resourceApi.batchDelete(selected.value.map((r) => r.id))
    ElMessage.success('已删除')
    loadList()
  } catch (e) {
    /* 取消 */
  }
}

async function batchCheck() {
  try {
    const res = await resourceApi.checkAlive(selected.value.map((r) => r.id))
    ElMessage.success(`检测完成，更新了 ${res.updated} 条状态`)
    loadList()
  } catch (e) {
    /* 忽略 */
  }
}

onMounted(() => {
  // 支持从其他页面带筛选条件跳进来（如资源巡检页的「处理」按钮）。
  // 不带这个处理的话，跳转过去只是打开一个未筛选的列表，
  // 用户还得自己再搜一遍，等于跳转没起到作用。
  const q = route.query
  if (q.keyword) query.keyword = String(q.keyword)
  if (q.status) query.status = String(q.status)
  if (q.diskType) query.diskType = String(q.diskType)

  loadList()
  loadCategories()
})
</script>

<style scoped>
.res-title {
  font-weight: 500;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

/* 目录树视图 */
.tree-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.tree-node {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 2px 0;
  font-size: 14px;
}

.tree-node .node-ico {
  flex: 0 0 auto;
  font-size: 15px;
}

.tree-node .node-ico.folder {
  color: #f7ba2a;
}

.tree-node .node-ico:not(.folder) {
  color: #909399;
}

.tree-node .node-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tree-node.is-file .node-label {
  font-weight: 500;
}

.tree-node .node-tags {
  flex: 0 0 auto;
  display: flex;
  gap: 4px;
}

.tree-node .node-ops {
  flex: 0 0 auto;
  opacity: 0;
  transition: opacity 0.15s;
}

.tree-node:hover .node-ops {
  opacity: 1;
}

.tree-node .node-count {
  flex: 0 0 auto;
  color: #a8abb2;
  font-size: 12px;
}
</style>
