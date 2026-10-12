<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源管理</h2>
      <div>
        <el-button :icon="Refresh" @click="loadList">刷新</el-button>
        <el-button
          :icon="VideoPlay"
          :disabled="!selected.length"
          @click="batchCheck"
        >
          存活检测
        </el-button>
        <el-button
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
    <div class="card-box">
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
import { onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Search, Delete, VideoPlay } from '@element-plus/icons-vue'
import { resourceApi, categoryApi } from '@/api'

const route = useRoute()

const list = ref([])
const total = ref(0)
const loading = ref(false)
const selected = ref([])
const categoryTree = ref([])

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

function diskName(t) {
  return { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里' }[t] || t || '-'
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
    const res = await resourceApi.list(params)
    list.value = res.list || []
    total.value = res.total || 0
  } catch (e) {
    /* 忽略 */
  } finally {
    loading.value = false
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
</style>
