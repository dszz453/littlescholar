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
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { categoryApi } from '@/api'

const tree = ref([])
const icons = ref([])
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const parentName = ref('')
const currentId = ref(null)

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

function openCreate(parent) {
  editing.value = false
  currentId.value = null
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
}

async function save() {
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
      await categoryApi.create(form.value)
      ElMessage.success('创建成功')
    }
    dialogVisible.value = false
    loadTree()
  } catch (e) {
    /* 忽略 */
  } finally {
    saving.value = false
  }
}

async function removeNode(data) {
  try {
    await ElMessageBox.confirm(
      `确定删除目录「${data.name}」吗？若包含子目录将一并删除。`,
      '删除确认',
      { type: 'warning', confirmButtonText: '确定删除', cancelButtonText: '取消' }
    )
    await categoryApi.remove(data.id, true)
    ElMessage.success('删除成功')
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
</style>
