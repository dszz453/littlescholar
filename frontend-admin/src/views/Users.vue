<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">用户管理</h2>
      <div>
        <el-input
          v-model="keyword"
          placeholder="搜索用户名/昵称"
          size="default"
          style="width: 200px"
          clearable
          @keyup.enter="load"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-button :icon="Refresh" @click="load">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">新建用户</el-button>
      </div>
    </div>

    <div class="card-box">
      <el-table :data="list" v-loading="loading" empty-text="暂无用户">
        <el-table-column label="用户" min-width="180">
          <template #default="{ row }">
            <div class="user-cell">
              <el-avatar :size="34">{{ (row.nickname || row.username).charAt(0) }}</el-avatar>
              <div>
                <div class="user-name">{{ row.nickname || row.username }}</div>
                <div class="text-secondary">@{{ row.username }}</div>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="角色" width="90">
          <template #default="{ row }">
            <el-tag :type="row.role === 'admin' ? 'danger' : 'primary'" size="small">
              {{ row.role === 'admin' ? '管理员' : '孩子' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="grade" label="年级" width="90">
          <template #default="{ row }">{{ row.grade || '-' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.status === 1 ? 'success' : 'info'" size="small">
              {{ row.status === 1 ? '正常' : '禁用' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="有效期" width="200">
          <template #default="{ row }">
            <span v-if="!row.expiresAt" class="text-secondary">永久</span>
            <span v-else :class="isExpired(row.expiresAt) ? 'expired' : ''">
              {{ formatDate(row.expiresAt) }}
              <el-tag v-if="isExpired(row.expiresAt)" size="small" type="danger">已过期</el-tag>
            </span>
          </template>
        </el-table-column>
        <el-table-column label="可访问目录" width="110">
          <template #default="{ row }">
            <span>{{ row.grantCount }} 个</span>
          </template>
        </el-table-column>
        <el-table-column label="最近登录" width="170">
          <template #default="{ row }">
            <span class="text-secondary">{{ row.lastLoginAt ? formatTime(row.lastLoginAt) : '从未登录' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openGrants(row)">
              授权目录
            </el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">
              编辑
            </el-button>
            <el-button link type="primary" size="small" @click="openResetPwd(row)">
              改密码
            </el-button>
            <el-button
              link
              type="danger"
              size="small"
              :disabled="row.role === 'admin'"
              @click="remove(row)"
            >
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="mt-16 flex justify-end">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[20, 50]"
          layout="sizes, prev, pager, next"
          @current-change="load"
          @size-change="load"
        />
      </div>
    </div>

    <!-- 创建/编辑用户 -->
    <el-dialog v-model="dialogVisible" :title="editing ? '编辑用户' : '新建用户'" width="640px">
      <el-form :model="form" label-width="100px">
        <el-form-item label="用户名" required v-if="!editing">
          <el-input v-model="form.username" placeholder="用于登录，英文数字" />
        </el-form-item>
        <el-form-item label="密码" :required="!editing" v-if="!editing">
          <el-input v-model="form.password" placeholder="至少 4 位" />
        </el-form-item>
        <el-form-item label="昵称">
          <el-input v-model="form.nickname" placeholder="如：小明" />
        </el-form-item>
        <el-form-item label="年级">
          <el-select v-model="form.grade" placeholder="选择年级" clearable style="width: 100%">
            <el-option v-for="g in grades" :key="g" :label="g" :value="g" />
          </el-select>
        </el-form-item>
        <el-form-item label="账号有效期">
          <el-date-picker
            v-model="form.expiresAt"
            type="datetime"
            placeholder="留空表示永久有效"
            value-format="YYYY-MM-DDTHH:mm:ssZ"
            style="width: 100%"
          />
        </el-form-item>
        <el-form-item label="状态" v-if="editing">
          <el-switch
            v-model="form.status"
            :active-value="1"
            :inactive-value="0"
            active-text="启用"
            inactive-text="禁用"
          />
        </el-form-item>

        <!-- 目录授权（仅新建时可选，编辑时用独立入口） -->
        <el-form-item label="可访问目录" v-if="!editing">
          <div class="grant-box">
            <el-tree
              ref="grantTreeRef"
              :data="categoryTree"
              show-checkbox
              node-key="id"
              :props="{ label: 'name', children: 'children' }"
              default-expand-all
            >
              <template #default="{ data }">
                <span>{{ data.name }}</span>
              </template>
            </el-tree>
            <p class="text-secondary">
              勾选父目录即自动获得其所有子目录的访问权限
            </p>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>

    <!-- 授权目录（独立，支持逐个设置有效期） -->
    <el-dialog v-model="grantDialog" :title="`授权目录 - ${grantUser?.nickname || ''}`" width="720px">
      <el-alert type="info" :closable="false" class="mb-16">
        可为每个目录单独设置有效期。不设置则永久有效，跟随账号有效期。
      </el-alert>

      <el-table :data="grantList" size="small" empty-text="还未授权任何目录">
        <el-table-column label="目录" min-width="180">
          <template #default="{ row }">
            <el-tree-select
              v-model="row.categoryId"
              :data="categoryTree"
              :props="{ label: 'name', value: 'id', children: 'children' }"
              check-strictly
              node-key="id"
              style="width: 100%"
              size="small"
            />
          </template>
        </el-table-column>
        <el-table-column label="允许下载" width="100">
          <template #default="{ row }">
            <el-switch v-model="row.canDownload" size="small" />
          </template>
        </el-table-column>
        <el-table-column label="授权有效期" width="200">
          <template #default="{ row }">
            <el-date-picker
              v-model="row.expiresAt"
              type="datetime"
              size="small"
              placeholder="永久"
              value-format="YYYY-MM-DDTHH:mm:ssZ"
              style="width: 100%"
            />
          </template>
        </el-table-column>
        <el-table-column label="" width="60">
          <template #default="{ $index }">
            <el-button link type="danger" size="small" @click="grantList.splice($index, 1)">
              删除
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-button class="mt-16" size="small" :icon="Plus" @click="addGrantRow">
        添加目录授权
      </el-button>

      <template #footer>
        <el-button @click="grantDialog = false">取消</el-button>
        <el-button type="primary" :loading="grantSaving" @click="saveGrants">保存授权</el-button>
      </template>
    </el-dialog>

    <!-- 重置密码 -->
    <el-dialog v-model="pwdDialog" title="重置密码" width="420px">
      <el-input v-model="newPassword" placeholder="输入新密码（至少 4 位）" show-password />
      <template #footer>
        <el-button @click="pwdDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmResetPwd">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, Refresh, Plus } from '@element-plus/icons-vue'
import { userApi, categoryApi } from '@/api'

const grades = ['幼儿园', '一年级', '二年级', '三年级', '四年级', '五年级', '六年级', '初一', '初二', '初三']

const list = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const keyword = ref('')
const loading = ref(false)

const categoryTree = ref([])
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const grantTreeRef = ref()
const currentId = ref(null)

const form = reactive({
  username: '',
  password: '',
  nickname: '',
  grade: '',
  expiresAt: null,
  status: 1,
})

const grantDialog = ref(false)
const grantUser = ref(null)
const grantList = ref([])
const grantSaving = ref(false)

const pwdDialog = ref(false)
const newPassword = ref('')
const pwdTarget = ref(null)

function isExpired(t) {
  return t && new Date(t) < new Date()
}

function formatDate(t) {
  if (!t) return '-'
  return new Date(t).toLocaleDateString('zh-CN')
}

function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

async function load() {
  loading.value = true
  try {
    const res = await userApi.list({
      page: page.value,
      pageSize: pageSize.value,
      keyword: keyword.value,
    })
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

function openCreate() {
  editing.value = false
  currentId.value = null
  Object.assign(form, {
    username: '',
    password: '',
    nickname: '',
    grade: '',
    expiresAt: null,
    status: 1,
  })
  dialogVisible.value = true
}

function openEdit(row) {
  editing.value = true
  currentId.value = row.id
  Object.assign(form, {
    username: row.username,
    password: '',
    nickname: row.nickname,
    grade: row.grade,
    expiresAt: row.expiresAt,
    status: row.status,
  })
  dialogVisible.value = true
}

async function save() {
  if (!editing.value) {
    if (!form.username.trim()) {
      ElMessage.warning('请填写用户名')
      return
    }
    if (!form.password || form.password.length < 4) {
      ElMessage.warning('密码至少 4 位')
      return
    }
  }
  saving.value = true
  try {
    if (editing.value) {
      await userApi.update(currentId.value, {
        nickname: form.nickname,
        grade: form.grade,
        expiresAt: form.expiresAt,
        status: form.status,
      })
      ElMessage.success('更新成功')
    } else {
      // 收集勾选的目录
      const checkedIds = grantTreeRef.value?.getCheckedKeys() || []
      const grants = checkedIds.map((id) => ({
        categoryId: id,
        canDownload: true,
        expiresAt: form.expiresAt,
      }))
      await userApi.create({
        username: form.username,
        password: form.password,
        nickname: form.nickname,
        grade: form.grade,
        expiresAt: form.expiresAt,
        grants,
      })
      ElMessage.success('创建成功')
    }
    dialogVisible.value = false
    load()
  } catch (e) {
    /* 忽略 */
  } finally {
    saving.value = false
  }
}

async function openGrants(row) {
  grantUser.value = row
  await loadCategories()
  try {
    const grants = await userApi.getGrants(row.id)
    grantList.value = (grants || []).map((g) => ({
      categoryId: g.categoryId,
      canDownload: g.canDownload,
      expiresAt: g.expiresAt,
    }))
  } catch (e) {
    grantList.value = []
  }
  grantDialog.value = true
}

function addGrantRow() {
  grantList.value.push({ categoryId: null, canDownload: true, expiresAt: null })
}

async function saveGrants() {
  const valid = grantList.value.filter((g) => g.categoryId)
  grantSaving.value = true
  try {
    await userApi.setGrants(grantUser.value.id, valid)
    ElMessage.success('授权已保存')
    grantDialog.value = false
    load()
  } catch (e) {
    /* 忽略 */
  } finally {
    grantSaving.value = false
  }
}

function openResetPwd(row) {
  pwdTarget.value = row
  newPassword.value = ''
  pwdDialog.value = true
}

async function confirmResetPwd() {
  if (!newPassword.value || newPassword.value.length < 4) {
    ElMessage.warning('密码至少 4 位')
    return
  }
  await userApi.resetPassword(pwdTarget.value.id, newPassword.value)
  ElMessage.success('密码已重置')
  pwdDialog.value = false
}

async function remove(row) {
  try {
    await ElMessageBox.confirm(`确定删除用户「${row.nickname || row.username}」吗？`, '删除确认', {
      type: 'warning',
    })
    await userApi.remove(row.id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    /* 取消 */
  }
}

onMounted(() => {
  load()
  loadCategories()
})
</script>

<style scoped>
.user-cell {
  display: flex;
  align-items: center;
  gap: 10px;
}

.user-name {
  font-weight: 500;
}

.expired {
  color: #f56c6c;
}

.grant-box {
  border: 1px solid var(--ls-border);
  border-radius: 8px;
  padding: 10px;
  max-height: 220px;
  overflow-y: auto;
  width: 100%;
}
</style>
