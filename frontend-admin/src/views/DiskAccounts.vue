<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源接入</h2>
      <div>
        <el-button :icon="Plus" type="primary" @click="openAdd(tab)">添加接入源</el-button>
        <el-button :icon="Refresh" @click="loadAll">刷新</el-button>
      </div>
    </div>

    <el-alert type="info" :closable="false" class="mb-16">
      <template #title>
        这里统一管理所有<b>内容来源</b>：网盘接入（夸克/百度/UC/天翼/阿里云盘）、
        WebDAV 存储、服务器<b>本地硬盘</b>目录。
        <b>夸克 / UC / 百度</b>支持扫码登录自动获取 Cookie；
        <b>天翼</b>与<b>阿里云盘</b>官方未开放网页扫码，请在「手动填写」里录入
        （阿里云盘填 refresh_token，天翼填浏览器里的完整 Cookie）。
        凭据会以 AES-256-GCM 加密后存储，不会明文落库。
      </template>
    </el-alert>

    <el-tabs v-model="tab" @tab-change="onTabChange">
      <el-tab-pane
        v-for="g in groups"
        :key="g.name"
        :label="g.label"
        :name="g.name"
      >
        <div class="tab-bar">
          <span class="text-secondary">
            共 {{ accountsOf(g.name).length }} 个{{ g.label }}接入源
          </span>
          <el-button :icon="Plus" type="primary" plain size="small" @click="openAdd(g.name)">
            添加{{ g.label }}
          </el-button>
        </div>

        <el-row :gutter="16">
          <el-col :span="8" v-for="acc in accountsOf(g.name)" :key="acc.id">
            <div class="card-box account-card">
              <div class="acc-header">
                <el-avatar :size="44" :src="acc.avatar">
                  {{ (acc.nickname || diskName(acc.diskType)).charAt(0) }}
                </el-avatar>
                <div class="acc-info">
                  <div class="acc-name">
                    {{ acc.nickname }}
                    <el-tag v-if="acc.isDefault" size="small" type="warning">默认</el-tag>
                  </div>
                  <div class="acc-type">{{ diskName(acc.diskType) }}</div>
                </div>
                <el-tag :type="statusTagType(acc.status)" size="small">
                  {{ statusText(acc.status) }}
                </el-tag>
              </div>

              <!-- 存储源展示地址 / 路径 -->
              <div v-if="acc.isStorage" class="acc-path">
                <el-icon><Link /></el-icon>
                <span :title="storagePath(acc)">{{ storagePath(acc) }}</span>
              </div>

              <div class="acc-capacity" v-if="acc.capacityTotal > 0">
                <el-progress
                  :percentage="capacityPercent(acc)"
                  :stroke-width="8"
                  :show-text="false"
                />
                <span class="text-secondary">
                  {{ formatSize(acc.capacityUsed) }} / {{ formatSize(acc.capacityTotal) }}
                </span>
              </div>

              <div class="acc-footer">
                <span class="text-secondary">
                  {{ acc.lastCheckAt ? '校验于 ' + formatTime(acc.lastCheckAt) : '未校验' }}
                </span>
                <div class="acc-actions">
                  <el-button link type="primary" size="small" @click="validate(acc)">校验</el-button>
                  <el-button
                    link
                    type="primary"
                    size="small"
                    v-if="!acc.isDefault && !acc.isStorage"
                    @click="setDefault(acc)"
                  >
                    设为默认
                  </el-button>
                  <el-button link type="primary" size="small" @click="openEdit(acc)">编辑</el-button>
                  <el-button link type="danger" size="small" @click="remove(acc)">删除</el-button>
                </div>
              </div>
            </div>
          </el-col>
        </el-row>

        <el-empty
          v-if="!accountsOf(g.name).length"
          :description="`还没有添加${g.label}接入源`"
        />
      </el-tab-pane>
    </el-tabs>

    <!-- 添加接入源 -->
    <el-dialog v-model="addDialog" title="添加接入源" width="640px" @closed="stopPolling">
      <el-tabs v-model="addTab" v-if="addKind === 'disk'">
        <el-tab-pane label="扫码登录" name="qr" v-if="currentSupportQR">
          <div class="qr-pane">
            <div class="qr-left">
              <div class="field-label">选择网盘</div>
              <el-radio-group v-model="addDiskType" @change="startQR">
                <el-radio-button v-for="t in qrTypes" :key="t.type" :value="t.type">
                  {{ t.name }}
                </el-radio-button>
              </el-radio-group>

              <div class="qr-status mt-16">
                <el-tag :type="qrStatusType" size="large">{{ qrStatusText }}</el-tag>
              </div>

              <el-button class="mt-16" size="small" @click="startQR" :loading="qrLoading">
                重新获取二维码
              </el-button>
            </div>

            <div class="qr-right">
              <div class="qr-box" v-loading="qrLoading">
                <img v-if="qrImage" :src="qrImage" class="qr-img" alt="二维码" />
                <canvas v-else ref="qrCanvas" class="qr-img"></canvas>
                <el-empty
                  v-if="!qrContent && !qrImage && !qrLoading"
                  description="请选择网盘并获取二维码"
                  :image-size="60"
                />
              </div>
              <p class="text-secondary qr-tip">用对应网盘 App 扫描二维码并确认授权</p>
            </div>
          </div>
        </el-tab-pane>

        <el-tab-pane label="手动填写" name="manual">
          <el-form :model="form" label-width="110px">
            <el-form-item label="网盘类型" required>
              <el-select v-model="form.diskType" style="width: 100%">
                <el-option
                  v-for="t in diskTypes"
                  :key="t.type"
                  :label="t.name"
                  :value="t.type"
                />
              </el-select>
            </el-form-item>
            <el-form-item v-if="form.diskType === 'aliyun'" label="refresh_token" required>
              <el-input
                v-model="form.cookie"
                type="textarea"
                :rows="3"
                placeholder="阿里云盘 refresh_token（从网页版登录态里获取）"
              />
              <div class="form-tip">
                阿里云盘不支持扫码，请在阿里云盘网页版登录后，从浏览器开发者工具的
                Local Storage / 网络请求中取出 refresh_token 粘贴到此处。
              </div>
            </el-form-item>
            <el-form-item v-else label="Cookie" required>
              <el-input
                v-model="form.cookie"
                type="textarea"
                :rows="5"
                placeholder="从浏览器开发者工具中复制完整 Cookie 字符串"
              />
            </el-form-item>
            <el-form-item label="备注名称">
              <el-input v-model="form.nickname" placeholder="如：我的夸克" />
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>

      <!-- WebDAV -->
      <el-form v-else-if="addKind === 'webdav'" :model="form" label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="form.nickname" placeholder="如：家里 NAS" />
        </el-form-item>
        <el-form-item label="地址" required>
          <el-input v-model="form.url" placeholder="http://192.168.1.10:5006/dav" />
          <div class="form-tip">WebDAV 根地址（Alist / 群晖 / rclone 各不相同，照抄即可）</div>
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.username" placeholder="可留空（匿名 WebDAV）" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" show-password />
        </el-form-item>
      </el-form>

      <!-- 本地硬盘 -->
      <el-form v-else :model="form" label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="form.nickname" placeholder="如：下载盘影视目录" />
        </el-form-item>
        <el-form-item label="目录路径" required>
          <el-input v-model="form.rootPath" placeholder="/mnt/docker/ls-data/media" />
          <div class="form-tip">
            服务器（运行 core-server 的机器）上的<b>绝对路径</b>。
            若部署在 Docker 里，需要先把宿主目录挂载进容器，这里填<b>容器内</b>的路径。
          </div>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="addDialog = false">关闭</el-button>
        <el-button
          v-if="addTab === 'manual' || addKind !== 'disk'"
          type="primary"
          :loading="saving"
          @click="save"
        >
          校验并保存
        </el-button>
      </template>
    </el-dialog>

    <!-- 编辑接入源 -->
    <el-dialog v-model="editDialog" title="编辑接入源" width="560px">
      <el-form :model="editForm" label-width="110px">
        <el-form-item label="名称">
          <el-input v-model="editForm.nickname" />
        </el-form-item>
        <template v-if="editForm.isStorage">
          <el-form-item v-if="editForm.diskType === 'webdav'" label="地址">
            <el-input v-model="editForm.url" />
          </el-form-item>
          <el-form-item v-else label="目录路径">
            <el-input v-model="editForm.rootPath" />
          </el-form-item>
        </template>
        <el-form-item v-else label="Cookie">
          <el-input
            v-model="editForm.cookie"
            type="textarea"
            :rows="4"
            placeholder="留空表示不修改"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editDialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Link } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { diskApi } from '@/api'

const groups = [
  { name: 'disk', label: '网盘接入' },
  { name: 'webdav', label: 'WebDAV' },
  { name: 'local', label: '本地硬盘' },
]
const tab = ref('disk')
const accounts = ref([])
const diskTypes = ref([])

const addDialog = ref(false)
const addTab = ref('qr')
const addDiskType = ref('quark')
const addKind = ref('disk')
const saving = ref(false)
const form = ref({})

const qrContent = ref('')
const qrImage = ref('')
const qrLoading = ref(false)
const qrStatusText = ref('未开始')
const qrStatusType = ref('info')
const qrSessionId = ref('')
const qrCanvas = ref()
let pollTimer = null
// 串行轮询的"该停了"标记：stopPolling 之后在途的那一次响应回来时，
// 靠它阻止再排下一轮（clearTimeout 管不到已经发出的请求）。
let pollStopped = false

const editDialog = ref(false)
const editForm = ref({})

const diskTypesOnly = computed(() => diskTypes.value.filter((t) => !t.isStorage))
const qrTypes = computed(() => diskTypesOnly.value.filter((t) => t.supportQR))
const currentSupportQR = computed(
  () => diskTypesOnly.value.some((t) => t.type === addDiskType.value && t.supportQR) && qrTypes.value.length > 0
)

function diskName(t) {
  return (
    { quark: '夸克网盘', baidu: '百度网盘', uc: 'UC网盘', tianyi: '天翼网盘', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}

function statusText(s) {
  // 空状态显示"待检测"而不是原始空串：老数据没有 status 字段，
  // 之前渲染成空白，看起来像坏了，其实只是还没校验过。
  return { valid: '正常', invalid: '失效', expired: '过期' }[s] || '待检测'
}
function statusTagType(s) {
  return { valid: 'success', invalid: 'danger', expired: 'warning' }[s] || 'info'
}
function formatSize(bytes) {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let n = bytes
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(1)} ${units[i]}`
}
function capacityPercent(acc) {
  if (!acc.capacityTotal) return 0
  return Math.min(100, Math.round((acc.capacityUsed / acc.capacityTotal) * 100))
}
function formatTime(t) {
  if (!t) return '-'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}

// storagePath 展示存储源的地址/路径（来自 extraPublic 白名单字段）
function storagePath(acc) {
  const e = acc.extraPublic || {}
  return e.url || e.rootPath || '-'
}

function accountsOf(group) {
  return accounts.value.filter((a) => groupOf(a) === group)
}
function groupOf(a) {
  if (a.diskType === 'local') return 'local'
  if (a.diskType === 'webdav') return 'webdav'
  return 'disk'
}

async function loadAll() {
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
}
async function loadTypes() {
  try {
    diskTypes.value = await diskApi.types()
  } catch (e) {
    /* 忽略 */
  }
}

function onTabChange(name) {
  // 切换分组时重置默认类型，避免"本地硬盘"tab 里默认选中夸克
  const map = { disk: 'quark', webdav: 'webdav', local: 'local' }
  addDiskType.value = map[name] || 'quark'
}

function openAdd(kind) {
  addKind.value = kind || 'disk'
  form.value = { diskType: addKind.value === 'disk' ? addDiskType.value : addKind.value, cookie: '', nickname: '', url: '', username: '', password: '', rootPath: '' }
  addTab.value = 'qr'
  qrContent.value = ''
  qrImage.value = ''
  qrStatusText.value = '未开始'
  qrStatusType.value = 'info'
  addDialog.value = true
  if (addKind.value === 'disk' && addTab.value === 'qr' && currentSupportQR.value) {
    startQR()
  }
}

async function startQR() {
  stopPolling()
  if (!qrTypes.value.length) return
  if (!currentSupportQR.value) {
    addTab.value = 'manual'
    return
  }
  qrLoading.value = true
  qrContent.value = ''
  qrImage.value = ''
  qrStatusText.value = '获取二维码中...'
  qrStatusType.value = 'warning'
  try {
    const res = await diskApi.getQR(addDiskType.value)
    qrSessionId.value = res.sessionId
    qrContent.value = res.qrContent
    if (res.qrImage) {
      qrImage.value = res.qrImage
    } else {
      await nextTick()
      if (qrCanvas.value) {
        await QRCode.toCanvas(qrCanvas.value, res.qrContent, { width: 200, margin: 1 })
      }
    }
    qrStatusText.value = '等待扫码...'
    qrStatusType.value = 'warning'
    startPolling()
  } catch (e) {
    qrStatusText.value = '获取失败（该网盘可能不支持扫码，请用手动填写）'
    qrStatusType.value = 'danger'
  } finally {
    qrLoading.value = false
  }
}

// ⚠️ 必须写成「收到响应再排下一次」的串行循环，不能用 setInterval。
//
// 百度的 unicast 是**长轮询**：没人扫码时服务端会把请求挂住（后端内部
// 压到 8 秒才返回 waiting）。用 setInterval(2000) 的话，每 2 秒发一个新请求、
// 每个都要挂 8 秒，于是一个对话框里同时飞着 4 个请求：
//   · 用户扫完码，拿到 confirmed 的那个会触发 stopPolling，可另外 3 个还在路上，
//     回来后各自再弹一次提示、再拉一次列表；
//   · 它们还会和已失效的通道交叉，把状态在「等待扫码 / 已扫码 / 失败」之间来回翻。
// 表现出来就是"扫了码没反应" —— 与后端无关，是前端把长轮询当成短轮询用了。
// 串行化之后任何时刻只有一个请求在飞，扫码结果一到就能立刻拿到。
const POLL_GAP_MS = 300

function startPolling() {
  stopPolling()
  pollStopped = false
  const tick = async () => {
    if (pollStopped || !qrSessionId.value) return
    try {
      const res = await diskApi.pollQR(qrSessionId.value)
      if (res.status === 'scanned') {
        qrStatusText.value = '已扫码，请在手机上确认'
        qrStatusType.value = 'warning'
      } else if (res.status === 'confirmed' || res.status === 'partial') {
        // partial = 拿到了 BDUSS 但没换到 STOKEN，账号能读不能写。
        // 这种情况必须说清楚，否则"绑定成功"四个字会把人带偏：
        // 用户会以为万事俱备，直到下一次转存才失败，根本联想不到绑定环节。
        const partial = res.status === 'partial'
        qrStatusText.value = partial ? '已绑定（缺少写权限）' : '绑定成功！'
        qrStatusType.value = partial ? 'warning' : 'success'
        stopPolling()
        if (partial) {
          ElMessage.warning(res.message || '已绑定，但缺少写权限，转存可能失败')
        } else {
          ElMessage.success(`已绑定账号：${res.nickname || ''}`)
        }
        setTimeout(() => {
          addDialog.value = false
          loadAll()
        }, partial ? 2600 : 800)
      } else if (res.status === 'expired') {
        qrStatusText.value = '二维码已过期，请重新获取'
        qrStatusType.value = 'danger'
        stopPolling()
      } else if (res.status === 'failed') {
        qrStatusText.value = res.message || '扫码失败，请重新获取二维码'
        qrStatusType.value = 'danger'
        stopPolling()
      }
    } catch (e) {
      /* 网络抖动不算失败，下一轮继续 */
    }
    if (!pollStopped) pollTimer = setTimeout(tick, POLL_GAP_MS)
  }
  pollTimer = setTimeout(tick, 0)
}

function stopPolling() {
  pollStopped = true
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function save() {
  const f = form.value
  let payload = null

  if (addKind.value === 'disk') {
    if (!f.cookie || !f.cookie.trim()) {
      ElMessage.warning('请填写 Cookie / Token')
      return
    }
    payload = { diskType: f.diskType, cookie: f.cookie.trim(), nickname: f.nickname }
  } else if (addKind.value === 'webdav') {
    if (!f.url || !f.url.trim()) {
      ElMessage.warning('请填写 WebDAV 地址')
      return
    }
    payload = {
      diskType: 'webdav',
      nickname: f.nickname,
      cookie: `${(f.username || '').trim()}:${f.password || ''}`,
      extra: { url: f.url.trim() },
    }
  } else {
    if (!f.rootPath || !f.rootPath.trim()) {
      ElMessage.warning('请填写目录路径')
      return
    }
    payload = {
      diskType: 'local',
      nickname: f.nickname,
      cookie: '',
      extra: { rootPath: f.rootPath.trim() },
    }
  }

  saving.value = true
  try {
    await diskApi.addManual(payload)
    ElMessage.success('添加成功')
    addDialog.value = false
    loadAll()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function validate(acc) {
  try {
    const res = await diskApi.validate(acc.id)
    if (res.valid) ElMessage.success('接入正常')
    else ElMessage.error(res.message || '接入失效')
    loadAll()
  } catch (e) {
    /* 忽略 */
  }
}

async function setDefault(acc) {
  await diskApi.setDefault(acc.id)
  ElMessage.success('已设为默认账号')
  loadAll()
}

function openEdit(acc) {
  const e = acc.extraPublic || {}
  editForm.value = {
    id: acc.id,
    diskType: acc.diskType,
    isStorage: acc.isStorage,
    nickname: acc.nickname,
    cookie: '',
    url: e.url || '',
    rootPath: e.rootPath || '',
  }
  editDialog.value = true
}

async function saveEdit() {
  const f = editForm.value
  const payload = { nickname: f.nickname }
  if (f.isStorage) {
    payload.extra = f.diskType === 'webdav' ? { url: (f.url || '').trim() } : { rootPath: (f.rootPath || '').trim() }
  } else if (f.cookie && f.cookie.trim()) {
    payload.cookie = f.cookie.trim()
  }
  saving.value = true
  try {
    await diskApi.update(f.id, payload)
    ElMessage.success('已保存')
    editDialog.value = false
    loadAll()
  } catch (e) {
    /* 拦截器已提示 */
  } finally {
    saving.value = false
  }
}

async function remove(acc) {
  try {
    await ElMessageBox.confirm(`确定删除接入源「${acc.nickname}」吗？`, '删除确认', { type: 'warning' })
    await diskApi.remove(acc.id)
    ElMessage.success('已删除')
    loadAll()
  } catch (e) {
    /* 取消 */
  }
}

onMounted(() => {
  loadAll()
  loadTypes()
})
</script>

<style scoped>
.account-card {
  margin-bottom: 16px;
}
.acc-header {
  display: flex;
  align-items: center;
  gap: 12px;
}
.acc-info {
  flex: 1;
  min-width: 0;
}
.acc-name {
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
}
.acc-type {
  font-size: 12px;
  color: var(--ls-text-secondary);
}
.acc-path {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--ls-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.acc-capacity {
  margin: 14px 0 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.acc-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-top: 1px solid var(--ls-border);
  padding-top: 10px;
  margin-top: 10px;
}
.acc-actions {
  display: flex;
  gap: 4px;
}
.tab-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.qr-pane {
  display: flex;
  gap: 32px;
  padding: 12px 0;
}
.qr-left {
  flex: 1;
}
.qr-right {
  width: 220px;
  text-align: center;
}
.field-label {
  font-size: 13px;
  color: var(--ls-text-secondary);
  margin-bottom: 8px;
}
.qr-box {
  width: 220px;
  height: 220px;
  border: 1px solid var(--ls-border);
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
}
.qr-img {
  width: 200px;
  height: 200px;
}
.qr-tip {
  margin-top: 10px;
  font-size: 12px;
}
.form-tip {
  font-size: 12px;
  color: var(--ls-text-secondary);
  line-height: 1.7;
}
</style>
