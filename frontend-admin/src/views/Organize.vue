<template>
  <div class="page-container">
    <div class="page-header">
      <div>
        <h2 class="page-title">AI 整理助手</h2>
        <div class="page-sub">
          选一个目录，让小书童读完里面的内容，给你一份「新建哪些子目录、把什么搬到哪儿」的方案。
          方案只是建议 —— 每一项都可以单独取消勾选，确认后才会真正改动。
        </div>
      </div>
    </div>

    <!-- ===== 范围与要求 ===== -->
    <el-card shadow="never" class="cfg-card">
      <el-form label-width="96px" label-position="left">
        <el-form-item label="整理范围">
          <el-tree-select
            v-model="form.categoryId"
            :data="catTree"
            :props="treeProps"
            node-key="id"
            check-strictly
            filterable
            clearable
            placeholder="选择一个目录（含它的全部子目录）"
            style="width: 100%"
          />
          <div class="hint">
            可以选到任意层级：<b>点左侧箭头</b>展开下级目录，<b>点目录名</b>即选中它
            （整理范围 = 该目录 + 它的全部子目录）。目录多的时候直接输入名字搜索更快。
          </div>
        </el-form-item>

        <el-form-item label="接入源">
          <div class="row-2">
            <el-select
              v-model="form.accountId"
              clearable
              placeholder="可选：选中后才允许 AI 建议新建资源映射"
              style="width: 100%"
            >
              <el-option
                v-for="a in accounts"
                :key="a.id"
                :label="accountLabel(a)"
                :value="a.id"
              />
            </el-select>
            <el-input
              v-model="form.remoteDirPrefix"
              placeholder="可选：远端目录前缀，如 /动画"
              :disabled="!form.accountId"
            />
          </div>
          <div class="hint">
            新建映射要求 AI 能对上真实远端路径。不选接入源时，AI 只会建议目录与移动，
            不会碰映射。
          </div>
        </el-form-item>

        <el-form-item label="额外要求">
          <el-input
            v-model="form.instruction"
            type="textarea"
            :rows="2"
            maxlength="300"
            show-word-limit
            placeholder="可选。例如：按册分成子目录；把重复的合并；不要动已经分好的部分"
          />
        </el-form-item>

        <el-form-item>
          <el-button
            type="primary"
            :icon="MagicStick"
            :loading="planning"
            :disabled="!form.categoryId"
            @click="makePlan"
          >
            生成整理方案
          </el-button>
          <el-button :disabled="planning" @click="resetAll">清空</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- ===== 方案 ===== -->
    <template v-if="plan">
      <el-card shadow="never" class="plan-card">
        <div class="scope-row">
          <el-tag effect="dark" size="large">{{ plan.scope.name }}</el-tag>
          <span class="scope-meta">
            {{ plan.scope.resourceCount }} 个内容 · {{ plan.scope.categoryCount }} 个子目录
            <template v-if="plan.scope.breadcrumb"> · {{ plan.scope.breadcrumb }}</template>
          </span>
        </div>

        <div v-if="plan.summary" class="summary">「小书童」{{ plan.summary }}</div>

        <div class="stats">
          <el-tag v-for="s in statTags" :key="s.label" :type="s.type" effect="plain">
            {{ s.label }} {{ s.value }}
          </el-tag>
          <span class="stats-note">
            已勾选 {{ selectedCount }} / {{ plan.steps.length }} 步
          </span>
        </div>
      </el-card>

      <el-card v-if="plan.steps.length" shadow="never" class="table-card">
        <el-table
          ref="tableRef"
          :data="plan.steps"
          row-key="index"
          @selection-change="onSelectionChange"
        >
          <el-table-column type="selection" width="46" :selectable="selectable" />
          <el-table-column label="#" width="50">
            <template #default="{ row }">{{ row.index + 1 }}</template>
          </el-table-column>
          <el-table-column label="动作" width="110">
            <template #default="{ row }">
              <el-tag :type="typeMeta(row.type).color" size="small" effect="light">
                {{ typeMeta(row.type).label }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="内容" min-width="360">
            <template #default="{ row }">
              <div class="step-main" :class="{ bad: !row.valid }">{{ describe(row) }}</div>
              <div v-if="row.preview && row.preview.length" class="step-preview">
                {{ row.preview.join('、') }}
              </div>
              <div v-if="row.reason" class="step-reason">理由：{{ row.reason }}</div>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="180">
            <template #default="{ row }">
              <el-tag v-if="row.valid" type="success" size="small">可执行</el-tag>
              <el-tooltip v-else :content="row.invalid" placement="top">
                <el-tag type="danger" size="small">已跳过：{{ row.invalid }}</el-tag>
              </el-tooltip>
            </template>
          </el-table-column>
        </el-table>

        <div class="actions">
          <el-button
            :loading="applying"
            :disabled="!selectedCount"
            @click="apply(true)"
          >试运行</el-button>
          <el-button
            type="primary"
            :icon="Check"
            :loading="applying"
            :disabled="!selectedCount"
            @click="apply(false)"
          >
            确认执行选中 {{ selectedCount }} 步
          </el-button>
        </div>
      </el-card>

      <el-card v-else shadow="never" class="table-card">
        <el-empty description="小书童认为这个目录已经很整齐，不需要动" />
      </el-card>
    </template>

    <!-- ===== 执行结果 ===== -->
    <el-card v-if="applyResult" shadow="never" class="table-card">
      <template #header>
        <span class="card-title">
          {{ applyResult.dryRun ? '试运行结果（没有改动任何数据）' : '执行结果' }}
        </span>
      </template>
      <el-table :data="applyResult.results" size="small">
        <el-table-column label="#" width="50">
          <template #default="{ row }">{{ row.index + 1 }}</template>
        </el-table-column>
        <el-table-column label="动作" width="110">
          <template #default="{ row }">
            <el-tag :type="typeMeta(row.type).color" size="small" effect="light">
              {{ typeMeta(row.type).label }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="结果" min-width="300">
          <template #default="{ row }">
            <span :class="row.ok ? 'ok' : 'bad'">{{ row.message }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.ok ? 'success' : 'danger'" size="small">
              {{ row.ok ? '成功' : '失败' }}
            </el-tag>
          </template>
        </el-table-column>
      </el-table>
      <div class="apply-summary">
        成功 {{ applyResult.applied }} 步，失败 {{ applyResult.failed }} 步。
        <template v-if="!applyResult.dryRun && applyResult.applied">
          目录结构已变化，孩子端与权限缓存的刷新是即时的。
        </template>
      </div>
    </el-card>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Check, MagicStick } from '@element-plus/icons-vue'
import { categoryApi, diskApi, organizeApi } from '@/api'

// value 必须显式写 'id'。
// el-tree-select 的 props.value 默认是字面量 'value'，而我们的节点字段叫 id；
// 只写 node-key 在部分版本上不足以让 v-model 拿到 id，结果是"看着选中了、
// 提交上去却是空/整个节点对象"。显式声明一处，比事后猜版本行为靠谱。
const treeProps = { label: 'name', children: 'children', value: 'id' }

const catTree = ref([])
const accounts = ref([])

const form = ref({
  categoryId: null,
  accountId: null,
  remoteDirPrefix: '',
  instruction: '',
})

const planning = ref(false)
const applying = ref(false)
const plan = ref(null)
const applyResult = ref(null)

const tableRef = ref(null)
// 已勾选的步骤（勾选状态由表格维护，这里只是镜像出来给按钮算数量）
const selectedSteps = ref([])
const selectedCount = computed(() => selectedSteps.value.length)

const typeMetaMap = {
  createCategory: { label: '新建目录', color: 'primary' },
  renameCategory: { label: '目录改名', color: 'warning' },
  moveResource: { label: '移动内容', color: 'success' },
  createMapping: { label: '新建映射', color: 'info' },
}
function typeMeta(t) {
  return typeMetaMap[t] || { label: t, color: 'info' }
}

const statTags = computed(() => {
  if (!plan.value) return []
  const s = plan.value.stats
  return [
    { label: '新建目录', value: s.createCategory, type: 'primary' },
    { label: '目录改名', value: s.renameCategory, type: 'warning' },
    { label: '移动内容', value: s.moveResource, type: 'success' },
    { label: '新建映射', value: s.createMapping, type: 'info' },
    { label: '受影响内容', value: s.affectedResources, type: 'danger' },
  ].filter((t) => t.value > 0)
})

function accountLabel(a) {
  const name = a.nickname || a.diskType
  return `${a.diskType} · ${name}`
}

function describe(row) {
  switch (row.type) {
    case 'createCategory':
      return `在「${row.targetLabel || '当前目录'}」下新建子目录「${row.name}」`
    case 'renameCategory':
      return `把「${row.targetLabel || ''}」改名`
    case 'moveResource':
      return `把 ${row.resourceIds?.length || 0} 个内容移动到「${row.targetLabel || ''}」`
    case 'createMapping':
      return `登记映射：${row.remoteDirId} → 「${row.targetLabel || ''}」`
    default:
      return row.type
  }
}

function selectable(row) {
  // 校验不通过的步骤不让勾 —— 与其让人勾了再报错，不如直接不可选
  return !!row.valid
}

function onSelectionChange(rows) {
  selectedSteps.value = rows
}

async function loadTree() {
  try {
    const tree = await categoryApi.tree(false)
    catTree.value = Array.isArray(tree) ? tree : tree?.list || []
  } catch {
    catTree.value = []
  }
}

async function loadAccounts() {
  try {
    const list = await diskApi.list()
    accounts.value = Array.isArray(list) ? list : list?.list || []
  } catch {
    accounts.value = []
  }
}

function payload() {
  return {
    categoryId: form.value.categoryId,
    accountId: form.value.accountId || 0,
    remoteDirPrefix: form.value.remoteDirPrefix || '',
    instruction: form.value.instruction || '',
  }
}

async function makePlan() {
  planning.value = true
  applyResult.value = null
  plan.value = null
  try {
    plan.value = await organizeApi.plan(payload())
    // 默认全选可执行的步骤：多数情况下用户就是想要方案里的全部动作，
    // 让他一个个勾反而更烦。要少做哪一步，取消那一步即可。
    await nextTick()
    plan.value.steps.forEach((s) => {
      if (s.valid) tableRef.value?.toggleRowSelection(s, true)
    })
    if (!plan.value.steps.length) {
      ElMessage.info('小书童认为这个目录已经很整齐，不需要调整')
    }
  } catch {
    /* 拦截器已提示 */
  } finally {
    planning.value = false
  }
}

async function apply(dryRun) {
  if (!selectedSteps.value.length) return
  applying.value = true
  try {
    const res = await organizeApi.apply({
      ...payload(),
      steps: selectedSteps.value,
      dryRun,
    })
    applyResult.value = res
    if (dryRun) {
      ElMessage.success('试运行完成，没有改动任何数据')
      return
    }
    if (res.failed) {
      ElMessage.warning(`执行完成：成功 ${res.applied} 步，失败 ${res.failed} 步`)
    } else {
      ElMessage.success(`已执行 ${res.applied} 步`)
    }
    // 目录结构变了，树的缓存要重新拿；方案本身已经过期，一并清掉
    await loadTree()
    plan.value = null
    selectedSteps.value = []
  } catch {
    /* 拦截器已提示 */
  } finally {
    applying.value = false
  }
}

function resetAll() {
  plan.value = null
  applyResult.value = null
  selectedSteps.value = []
  form.value = { categoryId: null, accountId: null, remoteDirPrefix: '', instruction: '' }
}

onMounted(() => {
  loadTree()
  loadAccounts()
})
</script>

<style scoped>
.page-sub {
  margin-top: 6px;
  max-width: 820px;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--ls-text-secondary);
}

.cfg-card {
  margin-bottom: 16px;
}

.row-2 {
  display: grid;
  grid-template-columns: 1.2fr 1fr;
  gap: 10px;
  width: 100%;
}

.hint {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--ls-text-secondary);
}

.plan-card {
  margin-bottom: 16px;
}

.scope-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.scope-meta {
  font-size: 12.5px;
  color: var(--ls-text-secondary);
}

.summary {
  margin-top: 12px;
  padding: 12px 14px;
  border-radius: 8px;
  background: #f5f9ff;
  font-size: 13.5px;
  line-height: 1.7;
}

.stats {
  margin-top: 12px;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.stats-note {
  margin-left: auto;
  font-size: 12.5px;
  color: var(--ls-text-secondary);
}

.table-card {
  margin-bottom: 16px;
}
.card-title {
  font-weight: 600;
}

.step-main {
  font-size: 13.5px;
  line-height: 1.6;
}
.step-preview {
  margin-top: 4px;
  font-size: 12px;
  color: var(--ls-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.step-reason {
  margin-top: 4px;
  font-size: 12px;
  color: #9aa6b5;
}
.bad {
  color: #d9554a;
}
.ok {
  color: #2e9e6b;
}

.actions {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}

.apply-summary {
  margin-top: 12px;
  font-size: 12.5px;
  color: var(--ls-text-secondary);
}
</style>
