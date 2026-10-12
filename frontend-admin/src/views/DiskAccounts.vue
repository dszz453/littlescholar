<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">网盘账号</h2>
      <div>
        <el-button :icon="Plus" type="primary" @click="openAddDialog">
          添加账号
        </el-button>
        <el-button :icon="Refresh" @click="loadAccounts">刷新</el-button>
      </div>
    </div>

    <el-alert type="info" :closable="false" class="mb-16">
      <template #title>
        支持夸克、百度、UC、天翼、阿里云盘。推荐使用<b>扫码登录</b>自动获取 Cookie；
        若扫码不可用，可选择<b>手动粘贴 Cookie</b>。
        Cookie 会以 AES-256-GCM 加密后存储，不会明文落库。
      </template>
    </el-alert>

    <!-- 账号列表 -->
    <el-row :gutter="16">
      <el-col :span="8" v-for="acc in accounts" :key="acc.id">
        <div class="card-box account-card">
          <div class="acc-header">
            <el-avatar :size="44" :src="acc.avatar">
              {{ diskName(acc.diskType).charAt(0) }}
            </el-avatar>
            <div class="acc-info">
              <div class="acc-name">
                {{ acc.nickname }}
                <el-tag v-if="acc.isDefault" size="small" type="warning">默认</el-tag>
              </div>
              <div class="acc-type" :class="`disk-${acc.diskType}`">
                {{ diskName(acc.diskType) }}
              </div>
            </div>
            <el-tag :type="statusTagType(acc.status)" size="small">
              {{ statusText(acc.status) }}
            </el-tag>
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
              <el-button link type="primary" size="small" @click="validate(acc)">
                校验
              </el-button>
              <el-button
                link
                type="primary"
                size="small"
                v-if="!acc.isDefault"
                @click="setDefault(acc)"
              >
                设为默认
              </el-button>
              <el-button link type="primary" size="small" @click="openUpdateCookie(acc)">
                更新Cookie
              </el-button>
              <el-button link type="danger" size="small" @click="remove(acc)">
                删除
              </el-button>
            </div>
          </div>
        </div>
      </el-col>
    </el-row>

    <el-empty v-if="!accounts.length" description="还没有绑定任何网盘账号" />

    <!-- 添加账号对话框 -->
    <el-dialog v-model="addDialog" title="添加网盘账号" width="600px" @closed="stopPolling">
      <el-tabs v-model="addTab">
        <!-- 扫码 -->
        <el-tab-pane label="扫码登录" name="qr">
          <div class="qr-pane">
            <div class="qr-left">
              <div class="field-label">选择网盘</div>
              <el-radio-group v-model="qrDiskType" @change="startQR">
                <el-radio-button value="quark">夸克</el-radio-button>
                <el-radio-button value="baidu">百度</el-radio-button>
                <el-radio-button value="uc">UC</el-radio-button>
                <el-radio-button value="tianyi">天翼</el-radio-button>
              </el-radio-group>

              <div class="qr-status mt-16">
                <el-tag :type="qrStatusType" size="large">{{ qrStatusText }}</el-tag>
              </div>

              <el-button
                class="mt-16"
                size="small"
                @click="startQR"
                :loading="qrLoading"
              >
                重新获取二维码
              </el-button>
            </div>

            <div class="qr-right">
              <div class="qr-box" v-loading="qrLoading">
                <img v-if="qrImage" :src="qrImage" class="qr-img" alt="二维码" />
                <canvas v-else ref="qrCanvas" class="qr-img"></canvas>
                <el-empty v-if="!qrContent && !qrImage && !qrLoading" description="请选择网盘并获取二维码" :image-size="60" />
              </div>
              <p class="text-secondary qr-tip">
                用对应网盘 App 扫描二维码并确认授权
              </p>
            </div>
          </div>
        </el-tab-pane>

        <!-- 手动 -->
        <el-tab-pane label="手动填写 Cookie" name="manual">
          <el-form :model="manualForm" label-width="90px">
            <el-form-item label="网盘类型" required>
              <el-select v-model="manualForm.diskType" style="width: 100%">
                <el-option label="夸克网盘" value="quark" />
                <el-option label="百度网盘" value="baidu" />
                <el-option label="UC网盘" value="uc" />
                <el-option label="天翼网盘" value="tianyi" />
                <el-option label="阿里云盘（填 refresh_token）" value="aliyun" />
              </el-select>
            </el-form-item>
            <el-form-item label="备注名称">
              <el-input v-model="manualForm.nickname" placeholder="如：我的夸克" />
            </el-form-item>
            <el-form-item label="Cookie" required>
              <el-input
                v-model="manualForm.cookie"
                type="textarea"
                :rows="5"
                placeholder="从浏览器开发者工具中复制完整 Cookie 字符串"
              />
            </el-form-item>
            <el-form-item>
              <el-alert type="warning" :closable="false" title="获取方式">
                <div style="font-size: 12px; line-height: 1.8">
                  1. 浏览器登录网盘网页版<br />
                  2. 按 F12 打开开发者工具 → Network 面板<br />
                  3. 刷新页面，点击任意请求，在 Request Headers 中找到 Cookie<br />
                  4. 复制完整内容粘贴到上方
                </div>
              </el-alert>
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>

      <template #footer>
        <el-button @click="addDialog = false">关闭</el-button>
        <el-button
          v-if="addTab === 'manual'"
          type="primary"
          :loading="manualSaving"
          @click="saveManual"
        >
          校验并保存
        </el-button>
      </template>
    </el-dialog>

    <!-- 更新 Cookie 对话框 -->
    <el-dialog v-model="updateDialog" title="更新 Cookie" width="520px">
      <el-input
        v-model="newCookie"
        type="textarea"
        :rows="5"
        placeholder="粘贴新的 Cookie"
      />
      <template #footer>
        <el-button @click="updateDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmUpdateCookie">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { nextTick, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { diskApi } from '@/api'

const accounts = ref([])
const addDialog = ref(false)
const addTab = ref('qr')

const qrDiskType = ref('quark')
const qrContent = ref('')
const qrImage = ref('')
const qrLoading = ref(false)
const qrStatus = ref('')
const qrSessionId = ref('')
const qrCanvas = ref()
let pollTimer = null

const manualForm = ref({ diskType: 'quark', nickname: '', cookie: '' })
const manualSaving = ref(false)

const updateDialog = ref(false)
const newCookie = ref('')
const updateTarget = ref(null)

function diskName(t) {
  return { quark: '夸克网盘', baidu: '百度网盘', uc: 'UC网盘', tianyi: '天翼网盘', aliyun: '阿里云盘' }[t] || t
}

function statusText(s) {
  return { valid: '正常', invalid: '失效', expired: '过期' }[s] || s
}

function statusTagType(s) {
  return { valid: 'success', invalid: 'danger', expired: 'warning' }[s] || 'info'
}

const qrStatusText = ref('未开始')
const qrStatusType = ref('info')

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

async function loadAccounts() {
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
}

function openAddDialog() {
  addDialog.value = true
  addTab.value = 'qr'
  qrContent.value = ''
  qrImage.value = ''
  qrStatusText.value = '未开始'
  qrStatusType.value = 'info'
}

async function startQR() {
  stopPolling()
  qrLoading.value = true
  qrContent.value = ''
  qrImage.value = ''
  qrStatusText.value = '获取二维码中...'
  qrStatusType.value = 'warning'

  try {
    const res = await diskApi.getQR(qrDiskType.value)
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
    qrStatusText.value = '获取失败'
    qrStatusType.value = 'danger'
  } finally {
    qrLoading.value = false
  }
}

function startPolling() {
  pollTimer = setInterval(async () => {
    if (!qrSessionId.value) return
    try {
      const res = await diskApi.pollQR(qrSessionId.value)
      if (res.status === 'scanned') {
        qrStatusText.value = '已扫码，请在手机上确认'
        qrStatusType.value = 'warning'
      } else if (res.status === 'confirmed') {
        qrStatusText.value = '绑定成功！'
        qrStatusType.value = 'success'
        stopPolling()
        ElMessage.success(`已绑定账号：${res.nickname || ''}`)
        setTimeout(() => {
          addDialog.value = false
          loadAccounts()
        }, 1000)
      } else if (res.status === 'expired') {
        qrStatusText.value = '二维码已过期，请重新获取'
        qrStatusType.value = 'danger'
        stopPolling()
      }
    } catch (e) {
      /* 继续轮询 */
    }
  }, 2000)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function saveManual() {
  if (!manualForm.value.cookie.trim()) {
    ElMessage.warning('请填写 Cookie')
    return
  }
  manualSaving.value = true
  try {
    await diskApi.addManual({
      diskType: manualForm.value.diskType,
      cookie: manualForm.value.cookie.trim(),
      nickname: manualForm.value.nickname,
    })
    ElMessage.success('添加成功')
    addDialog.value = false
    manualForm.value = { diskType: 'quark', nickname: '', cookie: '' }
    loadAccounts()
  } catch (e) {
    /* 忽略 */
  } finally {
    manualSaving.value = false
  }
}

async function validate(acc) {
  try {
    const res = await diskApi.validate(acc.id)
    if (res.valid) {
      ElMessage.success('账号有效')
    } else {
      ElMessage.error(res.message || '账号已失效')
    }
    loadAccounts()
  } catch (e) {
    /* 忽略 */
  }
}

async function setDefault(acc) {
  await diskApi.setDefault(acc.id)
  ElMessage.success('已设为默认账号')
  loadAccounts()
}

function openUpdateCookie(acc) {
  updateTarget.value = acc
  newCookie.value = ''
  updateDialog.value = true
}

async function confirmUpdateCookie() {
  if (!newCookie.value.trim()) {
    ElMessage.warning('请填写 Cookie')
    return
  }
  await diskApi.updateCookie(updateTarget.value.id, newCookie.value.trim())
  ElMessage.success('Cookie 已更新')
  updateDialog.value = false
  loadAccounts()
}

async function remove(acc) {
  try {
    await ElMessageBox.confirm(`确定删除账号「${acc.nickname}」吗？`, '删除确认', {
      type: 'warning',
    })
    await diskApi.remove(acc.id)
    ElMessage.success('已删除')
    loadAccounts()
  } catch (e) {
    /* 取消 */
  }
}

onMounted(loadAccounts)
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
}

.acc-name {
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
}

.acc-type {
  font-size: 12px;
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
</style>
