<template>
  <div class="rdt">
    <div v-if="showToolbar" class="rdt-toolbar">
      <el-input
        v-model="filterText"
        size="small"
        placeholder="输入关键字过滤已展开的目录"
        clearable
        :prefix-icon="Search"
      />
      <el-button size="small" :icon="Refresh" @click="reload">刷新</el-button>
    </div>

    <div class="rdt-body" v-loading="firstLoading">
      <el-tree
        ref="treeRef"
        :key="treeKey"
        :props="treeProps"
        :load="loadNode"
        :filter-node-method="filterNode"
        lazy
        node-key="key"
        :show-checkbox="multiple"
        highlight-current
        :expand-on-click-node="false"
        :default-expanded-keys="defaultExpanded"
        @node-click="onNodeClick"
        @check="onCheck"
      >
        <template #default="{ data }">
          <span class="rdt-node">
            <span class="rdt-icon">{{ nodeIcon(data) }}</span>
            <span class="rdt-name" :title="data.name">{{ data.name }}</span>
            <el-tag v-if="data.isAccount" size="small" effect="plain" class="rdt-tag">
              {{ diskName(data.diskType) }}
            </el-tag>
            <span v-else-if="data.isFile" class="rdt-size">{{ fmtSize(data.size) }}</span>
            <el-tag v-if="isMapped(data)" size="small" type="success" effect="plain" class="rdt-tag">
              已映射
            </el-tag>
            <el-tag v-if="data.invalid" size="small" type="danger" effect="plain" class="rdt-tag">
              账号失效
            </el-tag>
          </span>
        </template>
      </el-tree>

      <el-empty
        v-if="!firstLoading && !accounts.length"
        description="还没有接入源，先到「资源接入」添加一个"
        :image-size="70"
      />
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, Search } from '@element-plus/icons-vue'
import { diskApi } from '@/api'

/**
 * 接入源目录树 —— 懒加载整棵「账号 → 远端目录 → 子目录」的树。
 *
 * 抽成独立组件是因为有三个地方要用它，而它们要的必须是**同一套**行为：
 *   · 建目录弹窗（选一个网盘路径，顺手把映射也建了）
 *   · 资源映射页（左右双树里右边那棵）
 *   · AI 整理助手（选一个网盘目录做匹配来源）
 * 各写一份的话，迟早出现"这个页面能展开那个不能"的差异。
 */
const props = defineProps({
  accounts: { type: Array, default: () => [] },
  /** 已有映射的远端目录，元素形如 `accountId::dirId`，命中就打「已映射」标 */
  mappedKeys: { type: Array, default: () => [] },
  /** 默认展开的账号 id 列表 */
  expandedAccountIds: { type: Array, default: () => [] },
  showToolbar: { type: Boolean, default: true },
  /**
   * 多选模式：节点前出小方框，用 check 事件回传一组目录。
   * 单选模式（默认）：点击即选中，用 pick 事件回传一个目录。
   *
   * 两种模式都必须有 —— 建目录弹窗只要一个目录，
   * 而映射页要一次把好几个网盘目录映射到同一个系统目录。
   */
  multiple: { type: Boolean, default: false },
  /**
   * 是否把**文件**也列出来（默认为否，只看目录）。
   *
   * 只有「资源映射」页会打开它：那里要把某一集单独挂进系统目录，
   * 而目录级的映射解决不了"这个文件夹里我只想要这一集"。
   * 建目录弹窗和 AI 整理助手都要的是目录，打开它只会让它们多出一堆噪音行。
   */
  includeFiles: { type: Boolean, default: false },
})

const emit = defineEmits(['pick', 'check'])

const treeRef = ref(null)
const treeKey = ref(0)
const filterText = ref('')
// 初始为 true：账号列表是父组件异步拿的，等第一层 resolve 完再摘掉转圈
const firstLoading = ref(true)
const picked = ref(null)

// el-tree 的 lazy 模式靠 isLeaf 判断要不要显示展开箭头。
// 远端目录我们无法预先知道它有没有子目录，所以一律先当"有"，
// 点开返回空数组时 el-tree 自己就不显示箭头了。
const treeProps = { label: 'name', isLeaf: 'isLeaf' }

const defaultExpanded = computed(() =>
  props.expandedAccountIds.map((id) => accountKey(id))
)

function accountKey(id) {
  return `acct:${id}`
}

function diskName(t) {
  return (
    { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}

function isMapped(data) {
  if (!data || data.isAccount || data.isFile) return false
  return props.mappedKeys.includes(`${data.accountId}::${data.dirId}`)
}

/** 节点图标：账号 / 目录 / 文件（文件再按类型细分，一眼看出能不能播）。 */
function nodeIcon(data) {
  if (data.isAccount) return '☁️'
  if (!data.isFile) return '📁'
  const n = String(data.name || '').toLowerCase()
  if (/\.(mp4|mkv|avi|mov|wmv|flv|rmvb|rm|ts|m3u8|webm|m4v|mpg|mpeg|3gp)$/.test(n)) return '🎬'
  if (/\.(mp3|wav|flac|aac|m4a|ogg|wma|ape)$/.test(n)) return '🎵'
  if (/\.pdf$/.test(n)) return '📄'
  if (/\.(jpg|jpeg|png|gif|webp|bmp)$/.test(n)) return '🖼️'
  return '📄'
}

function fmtSize(n) {
  const v = Number(n) || 0
  if (!v) return ''
  if (v >= 1024 * 1024 * 1024) return (v / 1024 / 1024 / 1024).toFixed(1) + 'G'
  if (v >= 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + 'M'
  if (v >= 1024) return (v / 1024).toFixed(0) + 'K'
  return v + 'B'
}

/**
 * 把一份 FileItem 列表转成树节点。
 *
 * includeFiles 打开时文件也进来，且 isLeaf=true —— 文件没有下级，
 * 不这么标的话 el-tree 会给每一集都画一个展开箭头，点开却是空的。
 */
function toNodes(accountId, items) {
  return (items || [])
    .filter((i) => i.isDir || props.includeFiles)
    .map((i) => ({
      key: `${accountId}::${i.id}`,
      name: i.name,
      accountId,
      dirId: i.id,
      size: i.size,
      isFile: !i.isDir,
      isLeaf: !i.isDir,
    }))
}

/** 远端目录列表。失败必须**如实反馈**：账号失效和"空目录"长得一模一样。 */
async function fetchDirs(accountId, dirId) {
  try {
    const res = await diskApi.browse(accountId, dirId)
    return toNodes(accountId, res && res.items)
  } catch (e) {
    const msg = e && e.message ? e.message : '目录打不开'
    ElMessage.warning(`「${accountName(accountId)}」${dirId ? '这个目录' : '根目录'}打不开：${msg}`)
    return []
  }
}

function accountName(id) {
  const a = props.accounts.find((x) => x.id === id)
  return a ? a.nickname || `账号#${id}` : `账号#${id}`
}

async function loadNode(node, resolve) {
  if (node.level === 0) {
    firstLoading.value = false
    resolve(
      props.accounts.map((a) => ({
        key: accountKey(a.id),
        name: a.nickname || `账号#${a.id}`,
        diskType: a.diskType,
        isAccount: true,
        accountId: a.id,
        dirId: '',
        isLeaf: false,
      }))
    )
    return
  }
  const d = node.data || {}
  resolve(await fetchDirs(d.accountId, d.dirId || ''))
}

function onNodeClick(data, node, comp) {
  // 多选模式下，点整行就等于勾选/取消 —— 让用户必须精确点到那个小方框
  // 是没必要的折磨（树干区域那么宽）。
  if (props.multiple) {
    // ⚠️ deep 必须传 true。多选现在是**联动**的（见 onCheck 的注释），
    // 而 deep=false 只改自己那一个格子：父节点勾上了、子孙却没动，
    // el-tree 内部立刻处于"父子不一致"的状态，再点一下还会把状态
    // 解回去 —— 表现就是"点这一行没反应"。
    const n = treeRef.value?.getNode(data.key)
    const isOn = !!(n && n.checked)
    try {
      treeRef.value.setChecked(data.key, !isOn, true)
    } catch (e) {
      /* 忽略 */
    }
    return
  }
  picked.value = { data, node }
  emit('pick', describe(data, node))
}

/**
 * 多选回传。
 *
 * ============ 为什么不再用 check-strictly ============
 *
 * 原先多选走的是严格模式（父子互不牵连）。用户 2026-10-16 明确要求
 * 「勾选上级目录时下级默认全勾」—— 这才是符合直觉的做法：想整个挂过去
 * 的时候，本来就不该再手动把每个子目录点一遍。
 *
 * 但联动立刻带来一个新问题：勾中父级之后，**它的子孙也全在
 * checkedNodes 里**。原样提交就是给每个子目录各建一条映射 ——
 * 后端按 file_id 去重，第二条开始全是"新增 0"，用户看到的是一串废话，
 * 左树也会长出七八条指向同一个目录的映射，收都收不起来。
 *
 * 所以这里做一次**最小覆盖集**裁剪：只保留"祖先没有被勾中"的节点。
 * 一条父目录的递归映射本来就覆盖了它的所有子孙。
 */
function onCheck(_data, info) {
  if (!props.multiple) return
  const checkedKeys = new Set(((info && info.checkedKeys) || []).map(String))
  const nodes = ((info && info.checkedNodes) || []).filter((d) => !d.isAccount)

  const items = []
  for (const d of nodes) {
    const node = treeRef.value?.getNode(d.key)
    let covered = false
    let cur = node && node.parent
    while (cur) {
      const pd = cur.data
      if (pd && checkedKeys.has(String(pd.key))) {
        covered = true
        break
      }
      cur = cur.parent
    }
    if (covered) continue
    items.push(describe(d, node))
  }
  emit('check', items)
}

/** 把节点描述成「账号 / 目录 / 目录」的路径，交给父组件展示与提交。 */
function describe(data, node) {
  const parts = []
  let cur = node
  while (cur) {
    const nd = cur.data
    if (nd && nd.name) parts.unshift(nd.name)
    cur = cur.parent
  }
  return {
    accountId: data.accountId,
    accountName: accountName(data.accountId),
    diskType: data.isAccount ? data.diskType : findAccount(data.accountId)?.diskType,
    dirId: data.isAccount ? '' : data.dirId,
    dirName: data.isAccount ? '' : data.name,
    isRoot: !!data.isAccount,
    // 文件与目录的区分由父组件决定走哪条路：
    // 目录 → 建映射（可递归）；文件 → 直接登记成一条资源。
    isFile: !!data.isFile,
    size: data.size || 0,
    path: parts.join(' / '),
  }
}

function findAccount(id) {
  return props.accounts.find((x) => x.id === id)
}

function filterNode(value, data) {
  if (!value) return true
  return String(data.name || '').toLowerCase().includes(String(value).toLowerCase())
}

watch(filterText, (v) => {
  treeRef.value?.filter(v)
})

// accounts 是父组件异步拿的：本组件挂载时它多半还是空数组，
// 而 el-tree 的 lazy **只在挂载那一刻请求一次根节点** —— 那一次 resolve 出空数组
// 之后就永远停在「暂无数据」，后续 accounts 到货也不会重新加载。
// 这种坏法特别隐蔽：元素、类名、遮罩、loader 状态全都正常，就是树里一个节点都没有，
// 只有真去数 `.el-tree-node` 才看得出来。
//
// ⚠️ 所以这里监听的是**账号 id 拼成的指纹**，而不是 `props.accounts` 本身。
//
// 早先写的是 `watch(() => props.accounts, reload)`，那是个陷阱：父组件
// 每次拉完列表都是 `accounts.value = a || []` —— 一个**新数组对象**，
// 哪怕内容一个字节都没变，引用比较也会判定"变了"。于是映射页每挂上
// 一个目录（挂完必然重新拉一次列表），右树就整棵重建一次。
//
// 表现就是用户 2026-10-16 反馈的那句"匹配一个资源后，两边展开的目录
// 自动收回去，还得一级一级重新点开"。真正需要重建的只有"账号增删了"
// 这一种情况（根节点变了），而那时 id 指纹本来就会变。
watch(
  () => props.accounts.map((a) => a.id).join(','),
  () => reload()
)

/**
 * 重新拉整棵树。
 *
 * 用换 key 重建的方式，而不是去动 el-tree 内部的 store ——
 * lazy 模式的已加载节点缓存在组件实例里，内部结构又随版本变，
 * 摸内部属性是"今天能跑、升级就炸"的写法。
 * 换 key 会让整个树重新挂载，行为上就是"全部收起、重新加载"。
 */
function reload() {
  picked.value = null
  firstLoading.value = true
  treeKey.value += 1
}

/** 清空勾选（映射建完之后调用，避免误以为要再点一次）。 */
function clearChecked() {
  try {
    treeRef.value?.setCheckedKeys([])
  } catch (e) {
    /* 忽略 */
  }
  emit('check', [])
}

defineExpose({ reload, picked, clearChecked })
</script>

<style scoped>
.rdt {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}
.rdt-toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
.rdt-body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  border: 1px solid var(--ls-border, #e5e7eb);
  border-radius: 8px;
  padding: 6px 4px;
  background: #fff;
}
.rdt-node {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
  width: 100%;
}
.rdt-icon {
  flex: 0 0 auto;
}
.rdt-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rdt-tag {
  flex: 0 0 auto;
  transform: scale(0.9);
}
/* 文件行的大小：目录不需要（它没有大小），有它就够判断"这一集是不是我要的" */
.rdt-size {
  flex: 0 0 auto;
  font-size: 11.5px;
  color: var(--ls-text-secondary, #909399);
  font-variant-numeric: tabular-nums;
}
</style>
