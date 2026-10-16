<template>
  <div class="mapping-panel">
    <div class="mp-bar">
      <span class="text-secondary">
        左边是系统目录，右边是接入源。左边勾系统目录、右边勾要挂进来的内容，用中间的箭头搬运：
        <b>←</b> 挂过来（勾目录 = 建一条映射并递归导入；勾文件 = 只导入这一个），
        <b>→</b> 放回去。
        系统目录展开能一路看到<b>已经导入的每一个资源</b>，每条映射、每个资源旁边都有 ✕ 可以单独取消。
        资源旁边的<b>「共享」</b>用来创建分享链接 —— 孩子端用的是另一个网盘账号，
        必须靠这条链接才能把动画转存过去加速播放。
      </span>
      <div class="mp-bar-actions">
        <el-button
          :icon="RefreshRight"
          type="success"
          size="small"
          :disabled="!mappings.length"
          @click="syncAll"
        >
          全部同步
        </el-button>
        <!-- 导出摆在「解绑全部」左边是刻意的：这两个按钮的推荐用法是
             先点左边留个档、再点右边。顺序本身就是提示。 -->
        <el-button
          size="small"
          :icon="Download"
          :loading="exporting"
          :disabled="!mappings.length"
          @click="exportSnapshot"
        >
          导出快照
        </el-button>
        <el-button
          size="small"
          type="danger"
          plain
          :disabled="!mappings.length"
          @click="unmapAll"
        >
          解绑全部
        </el-button>
        <el-button :icon="Refresh" size="small" @click="loadAll" />
      </div>
    </div>

    <!-- ============ 三栏操作区 ============ -->
    <div class="mp-cols">
      <!-- 左：系统目录（已映射的网盘目录直接长在它下面，再往下是它导入的资源） -->
      <div class="mp-col">
        <div class="mp-col-head">
          <span class="mp-col-title">系统目录</span>
          <span class="text-secondary">{{ catCheckedIds.length }} 个已选</span>
        </div>
        <div class="mp-col-body" v-loading="leftLoading">
          <!-- ⚠️ 左树保持 check-strictly（不联动）。用户 2026-10-16 明确：
               "勾选上级默认全勾"只作用在**右侧**资源树。左树这边的勾选是
              "选一个系统目录"，联动只会把它的子孙目录一起带进勾选集，
              而 canMap 要求恰好一个目录 —— 一联动就永远挂不上去。 -->
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
            @node-expand="onLeftExpand"
            @node-collapse="onLeftCollapse"
          >
            <template #default="{ data }">
              <span class="mp-node">
                <span class="mp-icon">{{ nodeIcon(data) }}</span>
                <span
                  class="mp-node-name"
                  :class="{
                    'is-mapping': data.kind === 'mapping',
                    'is-off': data.kind === 'mapping' && !data.enabled,
                    'is-resource': data.kind === 'resource',
                  }"
                  :title="data.remotePath || data.name"
                >{{ data.name }}</span>

                <!-- ⚠️ 这几颗标签一律用**独立的 v-if**，不要写成 v-else-if 链。
                     写成链条时，只要前面某一环是 v-if（比如上面那两颗 mapping 的），
                     后面的 v-else-if 就挂在**最近的那一个** v-if 上 ——
                     于是 kind === 'category' 时整条链全 false，
                     "N 条映射"永远不显示。这种错编译不报、页面上只是"少了个角标"，
                     极容易一直留着。 -->
                <el-tag
                  v-if="data.kind === 'mapping'"
                  size="small"
                  :type="data.enabled ? 'success' : 'info'"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ diskName(data.diskType) }}{{ data.enabled ? '' : ' · 已停用' }}
                </el-tag>
                <!-- 角标一律**现算**，不读节点自带的字段：左树不再整棵重建，
                     而节点数据是 load 那一刻定死的 —— 读老字段的话，
                     新加的映射数、新同步的资源数都不会变。 -->
                <el-tag
                  v-if="data.kind === 'mapping' && mappingResCount(data)"
                  size="small"
                  type="info"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ mappingResCount(data) }} 个资源
                </el-tag>
                <el-tag
                  v-if="data.kind === 'category' && catMapCount(data)"
                  size="small"
                  type="success"
                  effect="plain"
                  class="mp-node-tag"
                >
                  {{ catMapCount(data) }} 条映射
                </el-tag>
                <!-- 资源是否已共享。孩子端点开一个动画能不能"转存到自己网盘后加速"，
                     完全取决于资源上有没有分享链接 —— 而这件事以前在后台**完全看不见**，
                     只能等孩子端报"资源没有分享链接"。所以状态必须显式标出来。 -->
                <el-tag
                  v-if="data.kind === 'resource' && data.shareUrl"
                  size="small"
                  type="success"
                  effect="plain"
                  class="mp-node-tag"
                  title="已有分享链接，孩子端可以转存后加速播放"
                >
                  已共享
                </el-tag>
                <span v-if="data.kind === 'resource'" class="mp-size">
                  {{ fmtSize(data.size) }}
                </span>

                <!-- 单个取消：挂错了就地下掉，不必去下面那张明细表里找。
                     ⚠️ 必须 .stop —— el-tree 的行点击会冒泡到节点上，
                     不拦住的话点一次 ✕ 会顺带把节点展开/收起。 -->
                <button
                  v-if="data.kind === 'mapping'"
                  class="mp-act mp-act-share"
                  title="为这条映射下的所有资源创建分享链接（孩子端才能转存到自己的网盘）"
                  @click.stop="openShare('mapping', data)"
                >共享</button>
                <button
                  v-if="data.kind === 'mapping'"
                  class="mp-act"
                  title="解绑这条映射"
                  @click.stop="unmapOne(data)"
                >✕</button>
                <button
                  v-if="data.kind === 'resource'"
                  class="mp-act mp-act-share"
                  :title="data.shareUrl
                    ? '查看这条资源的分享链接与提取码'
                    : '为它创建分享链接，孩子端才能转存到自己网盘并加速播放'"
                  @click.stop="openShare('resource', data)"
                >{{ data.shareUrl ? '链接' : '共享' }}</button>
                <button
                  v-if="data.kind === 'resource'"
                  class="mp-act"
                  title="把这个资源移出目录（其他资源不受影响）"
                  @click.stop="removeResource(data)"
                >✕</button>
              </span>
            </template>
          </el-tree>
        </div>
      </div>

      <!-- 中：箭头 -->
      <!-- ⚠️ 箭头的**朝向**不能按"按钮在中间"来随便选，它指的是数据流向：
           左边是系统目录（库里的），右边是接入源（网盘上的）。
             · 挂过来 —— 网盘 → 库，所以是 **←**（从右指向左）
             · 放回去 —— 库 → 网盘，所以是 **→**（从左指向右）
           用户 2026-10-16 反馈："箭头肯定是右到左，现在是反的"。
           之前两个箭头恰好写反，看着像"把系统目录推到网盘去"。 -->
      <div class="mp-arrows">
        <el-tooltip :content="mapTip" placement="top" :disabled="canMap">
          <span class="mp-arrow-wrap">
            <el-button class="mp-arrow" type="primary" :disabled="!canMap" @click="doMap">
              ←
            </el-button>
          </span>
        </el-tooltip>
        <el-tooltip :content="unmapTip" placement="bottom" :disabled="canUnmap">
          <span class="mp-arrow-wrap">
            <el-button class="mp-arrow" :disabled="!canUnmap" @click="doUnmap"> → </el-button>
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
          <!-- 这里显示的是**裁剪后**的待挂项数，不是树上勾了几个格子。
              勾一个父目录会把子孙一起勾上（联动），但真正要建的映射只有
              父目录那一条 —— 显示格子数会让人以为要建十几条。 -->
          <span class="text-secondary">{{ remoteChecked.length }} 项待挂</span>
        </div>
        <div class="mp-col-body">
          <RemoteDirTree
            ref="remoteTreeRef"
            :accounts="accounts"
            :mapped-keys="mappedKeys"
            :show-toolbar="false"
            multiple
            include-files
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
          <!-- 已导入的资源数（后端现算，不含软删的）。
              它是"这条映射到底干活了没有"最直接的证据 ——
               没有它，用户只能解绑时才知道倒进来几条。 -->
          <el-table-column label="资源" width="70" align="center">
            <template #default="{ row }">
              <span :class="row.resourceCount ? 'text-ok' : 'text-secondary'">
                {{ row.resourceCount || 0 }}
              </span>
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

    <!-- ============ 设置共享 ============ -->
    <!--
      为什么"建分享"要单独做一个弹层、而不是一个安静的按钮：
      这一步跨越了两个网盘账号（后台映射用的账号 ↔ 孩子端扫码的账号），
      用户唯一能看到的结果就是链接和提取码，所以他需要**看到**它们、
      能复制、能确认有效期 —— 而不是收到一句"操作成功"。
    -->
    <el-dialog v-model="shareDialog" :title="shareTitle" width="600px">
      <div class="sh-body">
        <p class="sh-lead">
          <template v-if="shareTarget.kind === 'mapping'">
            为这条映射下的<b>全部资源</b>创建一条分享链接。孩子端扫的是另一个网盘账号，
            必须靠这条链接才能把这些动画转存过去、然后加速播放。
          </template>
          <template v-else>
            <b>{{ shareTarget.name }}</b> 的分享链接。孩子端用它把这一条转存到自己的网盘，
            再走直链加速播放。
          </template>
        </p>

        <el-form label-width="88px" class="sh-form">
          <el-form-item label="有效期">
            <el-radio-group v-model="shareForm.expireDays" size="small">
              <el-radio-button :value="30">30 天</el-radio-button>
              <el-radio-button :value="-1">永久</el-radio-button>
            </el-radio-group>
            <!-- 只给这两档不是偷懒：夸克 expired_type 的中间几档在不同
                 参考资料里编号互相矛盾，猜错的后果是链接在用户没预期的
                 时候失效，且要到失效那天才发现。 -->
            <span class="sh-hint">只提供已验证的两档</span>
          </el-form-item>
          <el-form-item label="提取码">
            <el-input
              v-model="shareForm.passcode"
              size="small"
              placeholder="留空自动生成"
              maxlength="8"
              style="width:180px"
            />
            <span class="sh-hint">留空时由网盘生成 4 位码</span>
          </el-form-item>
          <el-form-item v-if="shareTarget.kind === 'mapping'" label="已有分享">
            <el-checkbox v-model="shareForm.force" size="small">
              重新生成（否则只补没共享过的）
            </el-checkbox>
            <!-- 默认"只补缺"是可逆性考虑：老链接可能已经给过人、
                 或已经让孩子端转过存，每次点按钮都重做一遍等于把它们作废。 -->
          </el-form-item>
        </el-form>

        <div v-if="shareResult" class="sh-result">
          <div class="sh-line">
            <span class="sh-k">链接</span>
            <el-input v-model="shareResult.shareUrl" size="small" readonly />
            <el-button size="small" @click="copyText(shareResult.shareUrl)">复制</el-button>
          </div>
          <div class="sh-line">
            <span class="sh-k">提取码</span>
            <el-input v-model="shareResult.passcode" size="small" readonly style="width:120px" />
            <el-button size="small" @click="copyText(shareResult.passcode)">复制</el-button>
          </div>
          <p class="sh-summary">{{ shareResult.summary }}</p>
        </div>
      </div>
      <template #footer>
        <el-button v-if="shareTarget.kind === 'resource' && shareTarget.hasShare" @click="clearShare">
          清除共享
        </el-button>
        <el-button v-if="shareResult" :loading="shareVerifying" @click="verifyShare">
          验证共享
        </el-button>
        <el-button @click="shareDialog = false">关闭</el-button>
        <el-button type="primary" :loading="shareBusy" @click="doShare">
          {{ shareResult ? '再生成一次' : '创建分享' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, h, nextTick, onMounted, ref } from 'vue'
import { ElCheckbox, ElMessage, ElMessageBox } from 'element-plus'
import { Download, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { diskApi, mappingApi, categoryApi, resourceApi, shareApi } from '@/api'
import { copyWithToast } from '@/utils/clipboard'
import RemoteDirTree from '@/components/RemoteDirTree.vue'

const mappings = ref([])
const accounts = ref([])
const categoryTree = ref([])
const loading = ref(false)
const saving = ref(false)
const exporting = ref(false)
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

/**
 * 左树当前**展开着**的节点 id 集合。
 *
 * 只为了一件事：映射增删之后左树必须能更新，但**不能整棵重建**。
 *
 * 用户 2026-10-16 反馈："每匹配一个资源，我展开的两边目录树就自动刷新、
 * 又收回去，还得一级一级重新点开 —— 因为我还要在这个目录下继续映射。"
 * 根因就是重建：`:key="leftKey"` 一变，el-tree 整个重新挂载，
 * 展开状态、滚动位置全丢。挂完一个目录必然要刷新一次，于是每挂一次收一次。
 *
 * 所以改成"只重载展开着的那些节点"。这份集合由 el-tree 的
 * node-expand / node-collapse 事件维护 —— 都是公开事件，
 * 不依赖组件内部结构。
 */
const leftExpandedKeys = new Set()

function onLeftExpand(data) {
  if (data && data.id != null) leftExpandedKeys.add(String(data.id))
}

function onLeftCollapse(data) {
  if (data && data.id != null) leftExpandedKeys.delete(String(data.id))
}

/** 系统目录树的"形状指纹"：所有节点 id 按顺序拼起来。 */
function catTreeShape(nodes, out = []) {
  for (const n of nodes || []) {
    out.push(n.id)
    catTreeShape(n.children, out)
  }
  return out.join(',')
}

// 上一次见的形状。只有当它变了（首次加载、目录被增删）才值得整棵重建。
let lastCatShape = null

/**
 * 局部刷新左树：只重载"当前展开着"的节点，其余节点的展开状态原样保留。
 *
 * 顺序不能反：系统目录重载会**重建它下面的全部子节点**（包括映射节点，
 * 于是映射的展开状态一并消失）。所以先把系统目录刷完，再把原本展开的
 * 映射节点重新展开回去。反过来做的话，映射节点刚展开就被目录重载丢掉。
 */
async function refreshLeftTree() {
  const tree = catTreeRef.value
  if (!tree || !leftExpandedKeys.size) return
  try {
    const keys = [...leftExpandedKeys]
    const cats = keys.filter((k) => !k.startsWith('map-'))
    const maps = keys.filter((k) => k.startsWith('map-'))

    for (const k of cats) {
      const node = tree.getNode(k)
      if (!node) continue
      // 标成未加载再展开 = 重新拉一次这个节点的子级（el-tree 的 lazy 会
      // 因为 !loaded 重新调 load）。节点本来就是收起的就不动它 ——
      // 只置 loaded=false，等用户下次点开时自然拿新数据。
      node.loaded = false
      if (node.expanded) node.expand()
    }
    await nextTick()
    for (const k of maps) {
      const node = tree.getNode(k)
      if (node && !node.expanded) node.expand()
    }
  } catch (e) {
    // `node.loaded` 是 el-tree 节点的内部字段（`node.expand()` 是公开的）。
    // 万一将来升级不再认它，宁可退回到"整棵重建"——展开状态会丢，
    // 但绝不能什么都不做：那样用户看到的是"映射挂上了、树却没更新"。
    leftExpandedKeys.clear()
    leftKey.value += 1
  }
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

    // ⚠️ 这里原来是无条件 `leftKey += 1`（整棵重建）。
    // 现在分成两条路，判据是"系统目录本身有没有变"：
    //   · 变了（首次加载 / 有人加了目录）→ 必须重建，否则新目录不出现；
    //   · 只是映射或资源变了 → 局部刷新，用户的展开状态原地保留。
    const shape = catTreeShape(categoryTree.value)
    if (shape !== lastCatShape) {
      lastCatShape = shape
      leftExpandedKeys.clear()
      leftKey.value += 1
    } else {
      await refreshLeftTree()
    }

    // 勾选状态必须和这份数据一起清 —— 只清数组、不清树上的勾，
    // 就会出现"格子还打着勾，canMap 却按没选算"的矛盾状态。
    catCheckedIds.value = []
    try {
      catTreeRef.value?.setCheckedKeys([])
    } catch (e) {
      /* 树尚未挂载时忽略 */
    }
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
    // 后端列表接口现算出来的（gorm:"-"），左树在**展开之前**就能标出
    // "这条映射倒进来几个资源"。没有它，用户只能一条条点开看。
    resourceCount: m.resourceCount || 0,
  }
}

/**
 * 节点上的两个角标一律**现算**，不读节点自带的 `mapCount` / `resourceCount`。
 *
 * 原因是左树不再整棵重建了：节点数据是 load 那一刻构造出来的，之后就冻结了。
 * 再挂一条映射、再同步几个资源，节点对象上的老数值不会变 ——
 * 用户会看到一个"加了映射但角标还是 0"的树。现算就跟着 mappings 走。
 */
function catMapCount(data) {
  return mapCountByCat.value[data.id] || 0
}

function mappingResCount(data) {
  const m = mappings.value.find((x) => x.id === data.mappingId)
  return (m && m.resourceCount) || 0
}

/**
 * 目录与资源用**不同图标**（用户第 2 条明确要求）。
 *
 * 光靠缩进区分是不够的：映射点开之后，一行"01.mp4"和一行子目录
 * 在视觉上几乎一样，而它们一个是资源、一个是远端目录，能做的操作完全不同。
 */
function nodeIcon(data) {
  if (data.kind === 'mapping') return '☁️'
  if (data.kind === 'resource') return mediaIcon(data)
  if (data.kind === 'note') return '⋯'
  return '🗂'
}

function mediaIcon(data) {
  const ext = String(data.ext || '').toLowerCase()
  if (data.mediaType === 'video') return '🎬'
  if (data.mediaType === 'audio') return '🎵'
  if (data.mediaType === 'image') return '🖼️'
  if (data.mediaType === 'pdf') return '📕'
  if (/^(mp4|mkv|avi|mov|wmv|flv|rmvb|rm|ts|m3u8|webm|m4v|mpg|mpeg|3gp)$/.test(ext)) return '🎬'
  if (/^(mp3|wav|flac|aac|m4a|ogg|wma|ape)$/.test(ext)) return '🎵'
  if (ext === 'pdf') return '📕'
  return '📄'
}

/**
 * 展开一条映射：列出它**当前导入的资源**。
 *
 * ============ 左树为什么不再往下钻远端目录 ============
 *
 * 早先映射节点展开的是"远端目录里有什么"（还能继续往下钻）。用户
 * 2026-10-16 要的是"左树显示到具体播放资源"—— 即**已经进来的东西**，
 * 而不是"对面还有什么"。这两件事长在同一棵树上会互相打架：
 * 一个回答"库里有什么"，一个回答"网盘里有什么"，混在一起之后
 * 用户看到一集视频根本分不清"已经导进来了"还是"只是网盘里有"。
 *
 * 远端长什么样，右边那棵树一直在给，而且现在还多了勾到单文件的能力。
 *
 * 顺带解决了另一个诉求：孩子端点开的每一集都能在这里对上账 ——
 * 看得到，也能单独下掉。
 */
async function mappingResources(mappingId) {
  try {
    // pageSize 上限就是 200（见 util.GetPage），这里要的就是一次尽量多拿。
    const res = await resourceApi.list({ mappingId, pageSize: 200 })
    const items = (res && res.list) || []
    const nodes = items.map((r) => ({
      id: `res-${r.id}`,
      name: r.title || `资源#${r.id}`,
      kind: 'resource',
      resourceId: r.id,
      mappingId,
      mediaType: r.mediaType,
      ext: r.ext,
      size: r.totalSize,
      // 共享状态：孩子端 PRO 能不能转存、能不能加速，全看这两个字段有没有值。
      // 放到节点上是为了让"这个资源没有分享链接"在树上直接可见 ——
      // 否则只能等到孩子端点开某个动画、看到"资源没有分享链接"才知道。
      shareUrl: r.shareUrl || '',
      passcode: r.passcode || '',
      isLeaf: true,
    }))
    // 截断了必须说出来，否则"怎么少了几集"会被当成同步没跑完。
    if (res && res.total > items.length) {
      nodes.push({
        id: `res-more-${mappingId}`,
        name: `…还有 ${res.total - items.length} 个资源，请在下面的明细表里查看`,
        kind: 'note',
        isLeaf: true,
      })
    }
    return nodes
  } catch (e) {
    ElMessage.warning('读取这条映射的资源失败：' + ((e && e.message) || '未知原因'))
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
  if (d.kind === 'mapping') {
    resolve(await mappingResources(d.mappingId))
    return
  }
  if (d.kind !== 'category') {
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

/* ============ → 挂过去 ============ */
//
// 右树现在能勾到**文件**，于是这个按钮要处理两类目标，走两条完全不同的路：
//
//   · 目录 → 建一条**映射**（持久绑定，以后每次同步都按它重扫）；
//   · 文件 → 直接登记成**一条资源**（一次性，不属于任何映射）。
//
// 为什么文件不走映射：映射的语义是"这个远端目录对应这个系统目录"。
// 一条只指向单个文件的映射会让"同步"变成一个说不清的动作（远端那个文件
// 改名或消失之后，这条映射每次同步都要报一次错）。用户要的只是
// "这一集我要"，登记一条资源就够 —— 而且它 mapping_id=0，
// 将来解绑同目录的映射时也不会被误伤。
async function doMap() {
  const catId = Number(catCheckedIds.value.map(String).filter((k) => !k.startsWith('map-'))[0])
  const targets = remoteChecked.value
  if (!catId || !targets.length) return

  const dirs = targets.filter((t) => !t.isFile)
  const files = targets.filter((t) => t.isFile)

  const created = []
  const failed = []
  for (const t of dirs) {
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

  // 单文件：按账号分组调一次导入接口（一次请求登记一批，别一个文件一趟）。
  let fileAdded = 0
  const byAccount = new Map()
  for (const f of files) {
    if (!byAccount.has(f.accountId)) byAccount.set(f.accountId, [])
    byAccount.get(f.accountId).push(f)
  }
  for (const [accountId, list] of byAccount) {
    try {
      const r = await resourceApi.importFromDisk({
        categoryId: catId,
        accountId,
        items: list.map((f) => ({
          fileId: f.dirId,
          title: f.dirName,
          size: f.size,
          isDir: false,
        })),
        recursive: false,
      })
      fileAdded += Number(r && r.created) || 0
      if (r && r.message) failed.push(r.message)
    } catch (e) {
      failed.push(`单文件导入失败（${(e && e.message) || '失败'}）`)
    }
  }

  if (failed.length) ElMessage.warning(`有 ${failed.length} 项没挂上：${failed.join('；')}`)
  if (!created.length && !fileAdded) return

  await loadAll()
  remoteTreeRef.value?.clearChecked()

  // 建完直接同步一次 —— 只挂映射不导资源的话孩子端还是空的，
  // 用户会以为"没生效"。这里多花几秒，换掉一次困惑。
  const syncMsg = created.length ? await syncIds(created) : ''
  const parts = []
  if (created.length) parts.push(`${created.length} 条映射`)
  if (fileAdded) parts.push(`${fileAdded} 个文件`)
  ElMessage.success(`已挂上 ${parts.join('、')}${syncMsg}`)
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

/**
 * 解绑前问一句。返回值：
 *   null  → 用户取消
 *   true  → 连同已导入的资源一起移除（默认）
 *   false → 只解绑配置，资源留着
 *
 * 默认值定成"一起移除"是刻意的：资源走**软删除**（用户 2026-10-16 拍板），
 * 重新映射再同步就能原样回来（连 id 都不变），所以没什么可犹豫的。
 * 早先默认"只解绑"，理由是"删资源不可逆" —— 那个理由已经不成立了。
 */
async function askUnmap(hits, title) {
  const withRes = ref(true)
  const names = hits.slice(0, 5).map((m) => m.name).join('、')
  const more = hits.length > 5 ? ` 等 ${hits.length} 条` : ''
  try {
    await ElMessageBox({
      title: title || '解绑映射',
      type: 'warning',
      showCancelButton: true,
      confirmButtonText: '确定解绑',
      cancelButtonText: '取消',
      message: h('div', [
        h('p', { style: 'margin:0 0 12px' }, `将解绑 ${hits.length} 条映射：${names}${more}`),
        h(
          ElCheckbox,
          {
            modelValue: withRes.value,
            'onUpdate:modelValue': (v) => {
              withRes.value = v
            },
          },
          () => '同时移除已导入的资源'
        ),
        h(
          'p',
          { style: 'margin:8px 0 0;color:#909399;font-size:12px;line-height:1.6' },
          h('span', [
            '移除是',
            h('b', ['软删除']),
            '：以后把同样的目录重新映射、再同步一次，资源会原样回来（连 id 都不变，'
              + '孩子的播放进度和收藏都还在）。不勾选则只解绑配置、资源留在原处。',
          ])
        ),
      ]),
    })
  } catch (e) {
    return null // 取消
  }
  return withRes.value
}

async function unmap(hits, title) {
  const withResources = await askUnmap(hits, title)
  if (withResources === null) return

  let done = 0
  let removed = 0
  const failed = []
  for (const m of hits) {
    try {
      // keepResources = !withResources（见 api/index.js 里对参数反转的说明）
      const res = await mappingApi.remove(m.id, !withResources)
      done++
      removed += Number(res && res.removedResources) || 0
    } catch (e) {
      failed.push((e && e.message) || '失败')
    }
  }
  await loadAll()
  if (failed.length) ElMessage.warning(`解绑了 ${done} 条，${failed.length} 条失败：${failed[0]}`)
  else ElMessage.success(`已解绑 ${done} 条映射${removed ? `，移除 ${removed} 个资源（可恢复）` : ''}`)
}

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
  await unmap(hits)
}

/**
 * 解绑**全部**映射（用户第 4 条：「所有映射全部删除要重新映射」）。
 *
 * 映射表被折腾乱了 —— 老映射指向的远端目录早就不在、账号换过一轮、
 * 同一个目录挂重了十几条 —— 与其一条条找着 ✕，不如一次清空重来。
 *
 * 三步，一步都不能省：
 *
 *  1. **先预览**。不拿前端的 resourceCount 自己加：那只是"有来源标记"
 *     的那部分，历史资源（mapping_id 为 NULL 的那批，线上占绝大多数）
 *     前端根本看不见，自己加必然少算，等于给用户一个骗人的数。
 *     后端的 dryRun 在同一套清理规则上算，报出来就是真删会动的数字。
 *
 *  2. **手打确认词**。不是走形式 —— 这个按钮一次清整张映射表，
 *     误点的代价是孩子端所有资源突然消失。同时把"资源是软删除、
 *     重新映射能复活"写在最显眼处，否则没人敢按，功能等于没做。
 *
 *  3. 执行时把 withResources 传明白，别依赖默认值 —— 默认值将来若再反转
 *     （它在 2026-10-16 反转过一次），这里会跟着悄悄变语义。
 */
async function unmapAll() {
  if (!mappings.value.length) {
    ElMessage.info('本来就没有映射')
    return
  }

  let preview
  try {
    preview = (await mappingApi.removeAll({ dryRun: true })) || {}
  } catch (e) {
    return // 拦截器已提示
  }

  const withRes = ref(true)
  const WORD = '全部解绑'
  try {
    // ⚠️⚠️ 必须用 `ElMessageBox.prompt`，**不能**写成 `ElMessageBox({...})`。
    //
    // Element Plus 里这两句判断都只认 prompt 变体：
    //   · 校验：`if (props.boxType === 'prompt' && action === 'confirm' && !validate()) return`
    //     （在 index.vue 的 handleAction 里）
    //   · 输入框：`showInput` 只由 `MessageBox.prompt` 自动带上，或在 options 里显式传；
    //     `inputType` 只是 props（默认 'text'），**不决定渲染与否**。
    //
    // 写成普通 `ElMessageBox({...})` 的后果非常隐蔽：弹窗看着一切正常 ——
    // 标题、说明、勾选框、两个按钮全都在，**只是没有输入框**，
    // 而 inputValidator 也永远不会被调用。于是「手打确认词」这层防护
    // 静默失效，用户点一下确定就直接清空整张映射表。
    //
    // 这个坑是 scripts/admin-ui-check.cjs 截图抓出来的（截图里弹窗没有输入框）。
    // 那个脚本现在有两条硬断言守着：输入框必须存在、空着点确定弹窗必须不关。
    await ElMessageBox.prompt(
      // 文案一律**静态**：ElMessageBox 的 message 是一次性渲染的 VNode，
      // 在里面读 withRes.value 只会拿到初始值，不会随勾选更新。
      h('div', { style: 'line-height:1.7;font-size:13.5px' }, [
        h('p', { style: 'margin:0 0 10px' }, [
          '将解绑全部 ',
          h('b', [String(preview.mappings || 0)]),
          ' 条映射。',
        ]),
        h(
          ElCheckbox,
          {
            modelValue: withRes.value,
            'onUpdate:modelValue': (v) => {
              withRes.value = v
            },
          },
          () => `同时移除已导入的资源（${preview.removedResources || 0} 个）`
        ),
        h('p', { style: 'margin:8px 0 0;color:#909399;font-size:12px' }, [
          '移除是',
          h('b', ['软删除']),
          '：把同样的目录重新映射、再同步一次，资源会原样回来（连 id 都不变，'
            + '孩子的播放进度和收藏都还在）。所以清空之后请记得重新映射一遍。',
        ]),
        preview.removedLegacy
          ? h(
              'p',
              { style: 'margin:6px 0 0;color:#909399;font-size:12px' },
              `其中 ${preview.removedLegacy} 个是没有来源标记的历史资源`
                + '（按账号 + 目录认出来的），重新映射后同样能回来。'
            )
          : null,
      ]),
      '解绑全部映射',
      {
        type: 'warning',
        showCancelButton: true,
        confirmButtonText: '确定解绑全部',
        cancelButtonText: '取消',
        inputPlaceholder: `输入「${WORD}」才能确认`,
        inputValidator: (v) => (v === WORD ? true : `请原样输入「${WORD}」`),
      }
    )
  } catch (e) {
    return // 取消
  }

  let res
  try {
    res = await mappingApi.removeAll({ keepResources: !withRes.value })
  } catch (e) {
    return // 拦截器已提示
  }
  await loadAll()

  const removed = Number(res && res.removedResources) || 0
  if (withRes.value && removed) {
    ElMessage.success(`已解绑全部映射，移除 ${removed} 个资源（重新映射同步后可恢复）`)
  } else {
    ElMessage.success('已解绑全部映射（资源保留）')
  }
}

/**
 * 左树上那条 ✕：只解绑**这一条**映射。
 *
 * 后端按 mapping_id 精确清理，所以同一个系统目录下别的映射导入的资源
 * 一条都不会被牵连 —— 这正是用户第 2 条要的"取消单个，同文件夹其他资源保留"。
 */
async function unmapOne(data) {
  const m = mappings.value.find((x) => x.id === data.mappingId)
  if (!m) return
  await unmap([m], '解绑这条映射')
}

/**
 * 把**一个**资源移出系统目录（软删除），同一条映射导入的其他资源不受影响。
 *
 * 早先只能整体解绑：想让孩子端少一集，只能把整条映射拆掉重来，
 * 于是同目录里其他几十集也跟着消失。这就是用户第 2 条要解决的问题。
 *
 * 走后端的资源批量删除接口，和「资源列表 → 删除」是同一条路 ——
 * 不另造一套"取消"语义，免得两处行为迟早不一致。
 */
async function removeResource(data) {
  // 资源来自哪里，决定了"移出之后还会不会回来"，这一点必须如实说：
  //   · 目录映射同步进来的（mappingId > 0）→ 下次同步会把它**复活**回来；
  //   · 手工 / 单文件导入的（mappingId = 0）→ 没有任何映射会重新导入它。
  // 不说清楚，用户点完发现它又回来了，只会认为按钮坏了。
  const fromMapping = !!data.mappingId
  try {
    await ElMessageBox({
      title: '移出资源',
      type: 'warning',
      showCancelButton: true,
      confirmButtonText: '移出',
      cancelButtonText: '取消',
      message: h('div', { style: 'line-height:1.7;font-size:13.5px' }, [
        h('p', { style: 'margin:0 0 8px' }, `把「${data.name}」移出系统目录？`),
        h('p', { style: 'margin:0;color:#909399;font-size:12px' }, fromMapping
          ? '它是由这一条映射同步进来的，同一条映射的其他资源不受影响。'
            + '但它下次同步会重新出现 —— 要它彻底不回来，请解绑左树上对应的映射。'
          : '它是单独导入的，移出后不会随同步回来。'),
      ]),
    })
  } catch (e) {
    return
  }
  try {
    await resourceApi.batchDelete([data.resourceId])
    ElMessage.success(fromMapping ? '已移出（下次同步会重新出现）' : '已移出')
    await loadAll()
  } catch (e) {
    /* 拦截器已提示 */
  }
}

/* ============ 快照导出 ============ */

/**
 * 把当前所有映射（连同每条映射导入的资源清单）导成一份 JSON 留档。
 *
 * 为什么值得单独做一个按钮：「解绑全部」动的是整张映射表。资源虽然是软删除、
 * 重新映射能复活，但「**曾经有哪些远端目录被映射到哪个系统目录**」这个信息
 * 只存在于映射表里 —— 删之前不落一份，重挂就只能靠翻记录和记忆。
 * 所以它和「解绑全部」并排放，顺序本身也在提示：先留档，再动手。
 *
 * 为什么连资源清单一起导：重新映射后能对回来什么，取决于远端目录还在不在。
 * 这份清单（标题 + fileId）是「当时到底导进来了哪些」的唯一记录 ——
 * 只导映射本身，事后没法判断「少的那几集是本来就没有、还是同步没跑到」。
 */
async function exportSnapshot() {
  if (!mappings.value.length) {
    ElMessage.info('还没有映射，没什么可导出的')
    return
  }
  exporting.value = true
  try {
    const rows = []
    for (const m of mappings.value) {
      rows.push({
        id: m.id,
        name: m.name,
        accountId: m.accountId,
        accountNickname: m.account ? m.account.nickname : '',
        diskType: m.account ? m.account.diskType : '',
        remoteDirId: m.remoteDirId || '',
        remoteDirName: m.remoteDirName || '',
        categoryId: m.categoryId,
        categoryName: m.category ? m.category.name : '',
        recursive: !!m.recursive,
        enabled: !!m.enabled,
        lastSyncAt: m.lastSyncAt || null,
        resourceCount: m.resourceCount || 0,
        resources: await snapshotResources(m.id),
      })
    }
    const dump = {
      kind: 'littlescholar.mapping-snapshot',
      exportedAt: new Date().toISOString(),
      note:
        '解绑全部映射前的留档。resources 是每条映射当前导入的资源；'
        + '按 accountId + remoteDirId + categoryId 重新建映射并同步，这些资源会原样回来（id 不变）。',
      count: rows.length,
      mappings: rows,
    }
    downloadJson(dump, `映射快照-${stamp()}.json`)
    const total = rows.reduce((n, r) => n + r.resources.length, 0)
    ElMessage.success(`已导出 ${rows.length} 条映射、${total} 个资源`)
  } catch (e) {
    ElMessage.warning('导出失败：' + ((e && e.message) || '未知原因'))
  } finally {
    exporting.value = false
  }
}

/** 拉全一条映射导入的资源。分页走，pageSize 200 是后端 GetPage 的上限。 */
async function snapshotResources(mappingId) {
  const out = []
  // 50 页 × 200 = 一万条封顶。够用，也避免远端某处对不上时分页转不出去。
  for (let page = 1; page <= 50; page++) {
    const res = await resourceApi.list({ mappingId, page, pageSize: 200 })
    const list = (res && res.list) || []
    for (const r of list) {
      out.push({ id: r.id, title: r.title, fileId: r.fileId, ext: r.ext, size: r.totalSize })
    }
    const total = Number(res && res.total) || 0
    if (!list.length || out.length >= total) break
  }
  return out
}

function downloadJson(obj, filename) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 立刻 revoke 在个别浏览器上会把下载截断，延后一拍再放。
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/** 文件名用本地时间到分钟，同一天导两次也不会互相覆盖。 */
function stamp() {
  const d = new Date()
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`
}

/* ============ 明细里的操作 ============ */
async function toggleEnabled(row, v) {
  try {
    await mappingApi.update(row.id, { enabled: v })
    row.enabled = v
    // 左树上那条映射的"已停用"标记要跟着变。
    //
    // ⚠️ 原来这里是 `leftKey += 1`（整棵重建）。用户此刻多半正展开着几个
    // 目录在里面继续挂东西，为一个小开关把他的展开状态全收掉，代价远大于收益。
    // 局部刷新同样能让标记更新：重载展开的系统目录会连带重建它下面的映射节点。
    await refreshLeftTree()
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

/* ============ 设置共享 ============ */

/**
 * 把后台网盘账号里的资源变成一条分享链接。
 *
 * 这一步为什么必须有：资源是映射进**后台那个**网盘账号的，而孩子端 PRO
 * 扫的是**家长自己的**账号。两个账号之间没有别的通道 —— 孩子端拿到的
 * 资源详情里如果没有 shareUrl，它就只会退回小书童的原地址，
 * 界面上表现为"能播但一点不加速"，而且看不出任何错误。
 *
 * 分享的粒度刻意是**一条映射一次**而不是一条资源一次：
 * 网盘的建分享接口本来就收 fid 列表，一次调用就能覆盖整个目录；
 * 逐条建分享会瞬间打出几十个请求撞上限流，也会把用户的"我的分享"
 * 列表塞满几十条孤立链接（见后端 shareChunkSize 的注释）。
 */
const shareDialog = ref(false)
const shareBusy = ref(false)
const shareTarget = ref({ kind: 'mapping', id: 0, name: '', hasShare: false })
const shareForm = ref({ expireDays: 30, passcode: '', force: false })
const shareResult = ref(null)

const shareTitle = computed(() =>
  shareTarget.value.kind === 'mapping'
    ? `设置共享 · ${shareTarget.value.name || '这条映射'}`
    : `设置共享 · ${shareTarget.value.name || '资源'}`
)

function openShare(kind, data) {
  shareResult.value = null
  shareForm.value = { expireDays: 30, passcode: '', force: false }
  shareTarget.value = {
    kind,
    id: kind === 'mapping' ? data.mappingId : data.resourceId,
    name: data.name,
    hasShare: !!(data && data.shareUrl),
    // 单条资源可能已经有链接，直接把现值摆出来，省一次点击
    shareUrl: (data && data.shareUrl) || '',
    passcode: (data && data.passcode) || '',
  }
  if (kind === 'resource' && data.shareUrl) {
    shareResult.value = {
      shareUrl: data.shareUrl,
      passcode: data.passcode || '',
      summary: '这条资源已经有分享链接，孩子端已在用它。',
    }
  }
  shareDialog.value = true
}

async function doShare() {
  const t = shareTarget.value
  shareBusy.value = true
  try {
    const req = {
      expireDays: shareForm.value.expireDays,
      passcode: shareForm.value.passcode,
      force: shareForm.value.force,
    }
    if (t.kind === 'mapping') req.mappingId = t.id
    else req.resourceId = t.id

    const res = await shareApi.create(req)

    // ⚠️ 成功与否按 **failed** 判，不是按 HTTP 通不通。
    // 后端在"资源是 WebDAV / 缺 fileId / 网盘拒绝"这些情况下都会回
    // code=0 + failed>0 —— 只看有没有抛异常，会把整批失败显示成成功。
    const failed = res.failed || 0
    const created = res.created || 0
    if (failed && !created) {
      ElMessage.error(res.failures && res.failures.length
        ? `创建失败：${res.failures[0].error}`
        : '创建分享失败，请稍后重试')
      shareResult.value = {
        shareUrl: '',
        passcode: '',
        summary: `失败 ${failed} 条`+(res.failures && res.failures.length ? `：${res.failures[0].error}` : ''),
      }
      return
    }

    let shareUrl = ''
    let passcode = ''
    let summary = res.message || ''
    if (t.kind === 'resource') {
      // 单条：把链接取回来直接给用户看，省得再去树上点一次
      const r = await resourceApi.detail(t.id)
      shareUrl = (r && r.shareUrl) || ''
      passcode = (r && r.passcode) || ''
    } else {
      // 批量：链接是同一条（整批资源共用一个分享），从第一条有分享的资源上取。
      // 取不到就只报数字，不编造一个链接出来。
      const r = await resourceApi.list({ mappingId: t.id, pageSize: 1 })
      const first = (r && r.list && r.list[0]) || null
      if (first && first.shareUrl) {
        shareUrl = first.shareUrl
        passcode = first.passcode || ''
      }
      summary = `成功 ${created} 条，跳过 ${skippedOf(res)} 条已共享，失败 ${failed} 条`
    }
    if (failed) summary += `（有 ${failed} 条失败）`

    shareResult.value = { shareUrl, passcode, summary }
    ElMessage.success(summary)
    // 树上的「已共享」角标要跟着变。这里用**局部刷新**而不是重建整棵树 ——
    // 用户可能正展开着某一级目录找下一个要处理的东西。
    await refreshLeftTree()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    shareBusy.value = false
  }
}

function skippedOf(res) {
  return res && typeof res.skipped === 'number' ? res.skipped : 0
}

/**
 * 验证共享：把"孩子端到底能不能用这条分享"问清楚。
 *
 * 为什么值得单独给一个按钮：加速失败的表现是"能播、但不加速"，
 * 而原因可能有三层 —— 没建分享 / 分享里的 fileId 对不上 / 账号登录态失效。
 * 这三层在界面上长得一模一样。验证接口会逐条回答，家长自己就能判断
 * 是该重新设置共享，还是该重新扫码接入账号。
 */
const shareVerifying = ref(false)

async function verifyShare() {
  const t = shareTarget.value
  shareVerifying.value = true
  try {
    const req = t.kind === 'mapping' ? { mappingId: t.id } : { resourceId: t.id }
    const res = await shareApi.verify(req)
    const found = res.found || 0
    const missing = res.missing || 0
    if (!missing) {
      ElMessage.success(`校验通过：${found} 条资源在分享里都能按 fileId 找到`)
    } else {
      const first = (res.items || []).find((x) => !x.found && x.note)
      ElMessage.warning(
        `${found} 条通过、${missing} 条未通过` + (first ? `：${first.note}` : '')
      )
    }
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    shareVerifying.value = false
  }
}

async function clearShare() {
  const t = shareTarget.value
  try {
    await ElMessageBox({
      title: '清除共享',
      type: 'warning',
      showCancelButton: true,
      confirmButtonText: '清除',
      cancelButtonText: '取消',
      message: h('div', { style: 'line-height:1.7;font-size:13.5px' }, [
        h('p', { style: 'margin:0 0 8px' }, '清除后台记录里的分享链接与提取码？'),
        h('p', { style: 'margin:0;color:#909399;font-size:12px' },
          '只清后台这一份记录，不会去删除网盘上那条分享本身。'
          + '清除后孩子端就转存不了这一条了（已转存成功的仍可播放）。'),
      ]),
    })
  } catch (e) {
    return
  }
  try {
    await shareApi.clear({ resourceId: t.id })
    ElMessage.success('已清除')
    shareDialog.value = false
    await refreshLeftTree()
  } catch (e) {
    /* 拦截器已提示 */
  }
}

/**
 * 复制到剪贴板。
 *
 * 具体实现抽到 utils/clipboard.js：**必须**带 execCommand 兜底，
 * 因为后台很可能跑在 http（非 https）下，那种来源里 navigator.clipboard
 * 是 undefined，直接 await 会抛 TypeError —— 现象是"点了复制没反应"，
 * 而复制分享链接恰恰是这个界面最高频的操作。
 */
async function copyText(text) {
  await copyWithToast(text)
}

onMounted(loadAll)
</script>

<style scoped>
.mp-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  /* 按钮多了，窄屏放不下就整块换行，别把说明文字压成一列单字 */
  flex-wrap: wrap;
  margin-bottom: 12px;
}
.mp-bar-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 0 0 auto;
}
/* Element Plus 给相邻 el-button 自带的 margin-left 会和上面的 flex gap
   叠加，不抵消掉的话按钮间距会一宽一窄。 */
.mp-bar-actions :deep(.el-button + .el-button) {
  margin-left: 0;
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
  width: 100%;
}
.mp-icon {
  flex: 0 0 auto;
  width: 17px;
  text-align: center;
}
.mp-node-name {
  /* ⚠️ 不能写成 flex:1 —— 那会把右边那颗 el-tag 一起推到行尾。
     名字保持"够用就好、超出省略"，让 ✕ 用 margin-left:auto 单独靠右。 */
  flex: 0 1 auto;
  min-width: 0;
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
/* 资源：比目录轻一档，一眼看出它是最末一级、不是可展开的目录 */
.mp-node-name.is-resource {
  color: #4a5a6a;
  font-weight: 400;
}
.mp-node-tag {
  flex: 0 0 auto;
  transform: scale(0.88);
}
/* 资源行的大小 */
.mp-size {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--ls-text-secondary, #909399);
  font-variant-numeric: tabular-nums;
}

/* 节点上的 ✕（解绑这条映射 / 移出这个资源）。
   靠 margin-left:auto 顶到行尾，平时是浅灰、悬停才变红 ——
   它是"删除类"操作，但不该在一棵满是数据的树上一直喊。 */
.mp-act {
  flex: 0 0 auto;
  margin-left: auto;
  appearance: none;
  border: 0;
  background: transparent;
  color: #c8cdd4;
  font-family: inherit;
  font-size: 11px;
  line-height: 1;
  padding: 4px 6px;
  border-radius: 6px;
  cursor: pointer;
  transition: color .15s, background .15s;
}
.mp-act:hover {
  color: #fff;
  background: #f56c6c;
}

/* 「共享」按钮与 ✕ 同形但不同色：两者挨着，靠得太近的破坏性操作
   必须一眼能区分，不能都长一个样。 */
.mp-act-share:hover {
  color: #fff;
  background: #409eff;
}

/* ============ 设置共享弹层 ============ */
.sh-lead {
  margin: 0 0 14px;
  font-size: 13.5px;
  line-height: 1.75;
}
.sh-form {
  margin-bottom: 4px;
}
.sh-hint {
  margin-left: 10px;
  font-size: 12px;
  color: var(--ls-text-secondary, #909399);
}
.sh-result {
  margin-top: 6px;
  padding: 12px 14px;
  border-radius: 8px;
  background: var(--ls-fill-light, #f5f7fa);
}
.sh-line {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}
.sh-k {
  flex: 0 0 42px;
  font-size: 12px;
  color: var(--ls-text-secondary, #909399);
}
.sh-summary {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--ls-text-secondary, #909399);
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
