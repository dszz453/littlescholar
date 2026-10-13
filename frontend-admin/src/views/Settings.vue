<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">系统设置</h2>
      <el-button type="primary" :loading="saving" @click="save">保存配置</el-button>
    </div>

    <el-row :gutter="16">
      <el-col :span="14">
        <div class="card-box">
          <h3 class="section-title">外部服务</h3>
          <el-form :model="config" label-width="140px">
            <el-form-item label="PanSou 地址">
              <el-input v-model="config.pansou_url" placeholder="http://pansou:8888" />
              <div class="text-secondary">网盘资源搜索服务地址</div>
            </el-form-item>
            <el-form-item label="PanSou Token">
              <el-input v-model="config.pansou_token" placeholder="若启用了认证则填写" show-password />
            </el-form-item>
            <el-form-item label="PanCheck 地址">
              <el-input v-model="config.pancheck_url" placeholder="http://pancheck:6080" />
              <div class="text-secondary">批量存活检测服务（可选）</div>
            </el-form-item>
            <el-form-item label="PanCheck 密码">
              <el-input v-model="config.pancheck_password" show-password />
            </el-form-item>
          </el-form>
        </div>

        <div class="card-box mt-16">
          <h3 class="section-title">播放与转码</h3>
          <el-form :model="config" label-width="140px">
            <el-form-item label="默认播放模式">
              <el-radio-group v-model="config.play_mode_default">
                <el-radio-button value="direct">直链</el-radio-button>
                <el-radio-button value="proxy">中转</el-radio-button>
                <el-radio-button value="hls">HLS 转码</el-radio-button>
              </el-radio-group>
              <div class="text-secondary mt-8">
                直链：直接播放网盘链接，省服务器带宽，但受网盘限速<br />
                中转：服务端下载后转发，可加速、兼容性好，消耗带宽<br />
                HLS 转码：转码为标准 HLS，全设备兼容，支持息屏播放，消耗 CPU
              </div>
            </el-form-item>
            <el-form-item label="启用转码">
              <el-switch v-model="config.enable_transcode" active-value="true" inactive-value="false" />
            </el-form-item>
            <el-form-item label="转码并发数">
              <el-input-number v-model.number="config.transcode_concurrency" :min="1" :max="8" />
              <div class="text-secondary">转码是 CPU 密集型任务，请根据服务器性能设置</div>
            </el-form-item>
          </el-form>
        </div>

        <div class="card-box mt-16">
          <h3 class="section-title">下载加速</h3>
          <el-form :model="config" label-width="140px">
            <el-form-item label="启用加速">
              <el-switch v-model="config.download_accelerate" active-value="true" inactive-value="false" />
              <div class="text-secondary">
                通过分块并发突破单连接限速。注意：无法突破账号级总量限速与付费墙。
              </div>
            </el-form-item>
            <el-form-item label="并发数">
              <el-slider v-model.number="config.download_concurrency" :min="4" :max="64" style="width: 300px" />
              <span class="text-secondary" style="margin-left: 12px">
                当前 {{ config.download_concurrency }} 线程
              </span>
            </el-form-item>
            <el-form-item label="外部加速代理">
              <el-input v-model="config.accel_proxy" placeholder="http://192.168.1.5:8888/proxy?url={url}" />
              <div class="text-secondary">
                留空则不启用。填写后，网盘直链会先经该代理（如 aliproxy）多连接拉流再转发，
                用于突破非会员的单连接限速。支持 <code>{url}</code> 占位符，也支持直接作为前缀拼接。
              </div>
            </el-form-item>
          </el-form>
        </div>

        <div class="card-box mt-16">
          <h3 class="section-title">搜一搜 · 搜全网</h3>
          <el-form :model="config" label-width="140px">
            <el-form-item label="开启即时播放">
              <el-switch v-model="config.instant_enabled" active-value="true" inactive-value="false" />
              <div class="text-secondary">
                开启后，孩子端「搜一搜」多出「搜全网」：选中结果会<b>自动转存</b>到下面指定的
                网盘目录，并立即开始播放（边下边播）。
              </div>
            </el-form-item>
            <el-form-item label="默认转存账号">
              <el-select v-model="config.instant_account_id" clearable placeholder="留空则用该网盘的默认账号" style="width: 100%">
                <el-option
                  v-for="a in transferAccounts"
                  :key="a.id"
                  :label="`${a.nickname}（${diskName(a.diskType)}）`"
                  :value="String(a.id)"
                />
              </el-select>
            </el-form-item>
            <el-form-item label="转存到目录">
              <el-input v-model="config.instant_remote_dir" readonly placeholder="根目录">
                <template #append>
                  <el-button :disabled="!browseAccountId" @click="openBrowse">浏览</el-button>
                </template>
              </el-input>
              <div class="text-secondary">
                转存到该账号里的哪个目录（留空 = 根目录）。选「默认转存账号」后才可浏览。
              </div>
            </el-form-item>
            <el-form-item label="收录到目录">
              <el-tree-select
                v-model="config.instant_category_id"
                :data="categoryTree"
                :props="{ label: 'name', value: 'id', children: 'children' }"
                check-strictly
                clearable
                node-key="id"
                placeholder="留空则自动使用「搜全网」目录"
                style="width: 100%"
              />
              <div class="text-secondary">转存后的资源会登记到这个系统目录，供孩子端播放与回看。</div>
            </el-form-item>
          </el-form>
        </div>
      </el-col>

      <el-col :span="10">
        <div class="card-box">
          <h3 class="section-title">系统状态</h3>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="版本">{{ status.version }}</el-descriptions-item>
            <el-descriptions-item label="运行时长">{{ formatUptime(status.uptime) }}</el-descriptions-item>
            <el-descriptions-item label="数据库">
              <el-tag :type="status.dbOnline ? 'success' : 'danger'" size="small">
                {{ status.dbOnline ? '正常' : '异常' }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="PanSou">
              <el-tag :type="status.pansouOnline ? 'success' : 'danger'" size="small">
                {{ status.pansouOnline ? '在线' : '离线' }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="内存占用">{{ status.memoryMB }} MB</el-descriptions-item>
            <el-descriptions-item label="协程数">{{ status.goroutines }}</el-descriptions-item>
            <el-descriptions-item label="转存队列">{{ status.transferQueue }}</el-descriptions-item>
            <el-descriptions-item label="待处理任务">{{ status.pendingTasks }}</el-descriptions-item>
          </el-descriptions>
          <el-button class="mt-16" :icon="Refresh" @click="loadStatus" style="width: 100%">
            刷新状态
          </el-button>
        </div>

        <div class="card-box mt-16">
          <h3 class="section-title">数据统计</h3>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="用户总数">{{ status.userCount || 0 }}</el-descriptions-item>
            <el-descriptions-item label="目录总数">{{ status.categoryCount || 0 }}</el-descriptions-item>
            <el-descriptions-item label="资源总数">{{ status.resourceCount || 0 }}</el-descriptions-item>
          </el-descriptions>
        </div>
      </el-col>
    </el-row>

    <!-- 转存目录浏览 -->
    <el-dialog v-model="browseDialog" title="选择转存目录" width="560px">
      <div class="dir-nav mb-8">
        <el-button size="small" :icon="ArrowLeft" :disabled="!stack.length" @click="goUp">上级</el-button>
        <span class="text-secondary ml-8">
          {{ stack.map((s) => s.name).join(' / ') || '根目录' }}
        </span>
      </div>
      <div class="dir-list" v-loading="browsing">
        <div v-for="d in dirs" :key="d.id" class="dir-item" @click="enterDir(d)">
          <el-icon><Folder /></el-icon>
          <span>{{ d.name }}</span>
        </div>
        <el-empty v-if="!dirs.length && !browsing" description="该目录下没有子目录" :image-size="60" />
      </div>
      <template #footer>
        <el-button @click="browseDialog = false">取消</el-button>
        <el-button type="primary" @click="confirmBrowse">选择当前目录</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, ArrowLeft, Folder } from '@element-plus/icons-vue'
import { systemApi, diskApi, categoryApi } from '@/api'

const config = ref({
  pansou_url: '',
  pansou_token: '',
  pancheck_url: '',
  pancheck_password: '',
  play_mode_default: 'proxy',
  enable_transcode: 'true',
  transcode_concurrency: 2,
  download_accelerate: 'true',
  download_concurrency: 16,
  accel_proxy: '',
  instant_enabled: 'true',
  instant_account_id: '',
  instant_remote_dir: '',
  instant_category_id: '',
})
const status = ref({})
const saving = ref(false)

// 可用来转存的账号（排除 WebDAV / 本地硬盘这类存储源）
const accounts = ref([])
const categoryTree = ref([])
const transferAccounts = computed(() => accounts.value.filter((a) => !a.isStorage))
const browseAccountId = computed(() => {
  if (config.value.instant_account_id) return Number(config.value.instant_account_id)
  return transferAccounts.value.length ? transferAccounts.value[0].id : null
})

// 目录浏览
const browseDialog = ref(false)
const browsing = ref(false)
const dirs = ref([])
const stack = ref([])

function diskName(t) {
  return (
    { quark: '夸克', baidu: '百度', uc: 'UC', tianyi: '天翼', aliyun: '阿里云盘', webdav: 'WebDAV', local: '本地硬盘' }[t] ||
    t
  )
}

function formatUptime(sec) {
  if (!sec) return '-'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}天 ${h}小时`
  if (h > 0) return `${h}小时 ${m}分`
  return `${m}分钟`
}

async function load() {
  try {
    const res = await systemApi.getConfig()
    const numeric = ['transcode_concurrency', 'download_concurrency']
    Object.keys(config.value).forEach((k) => {
      if (res[k] !== undefined && res[k] !== '') {
        config.value[k] = numeric.includes(k) ? Number(res[k]) : res[k]
      }
    })
  } catch (e) {
    /* 忽略 */
  }
}

async function loadAccounts() {
  try {
    accounts.value = await diskApi.list()
  } catch (e) {
    /* 忽略 */
  }
}

async function loadCategories() {
  try {
    categoryTree.value = await categoryApi.tree(false)
  } catch (e) {
    /* 忽略 */
  }
}

// ===== 转存目录浏览 =====
async function openBrowse() {
  if (!browseAccountId.value) {
    ElMessage.warning('请先在「资源接入」里添加一个网盘账号')
    return
  }
  stack.value = []
  // 已配置的远端目录作为起点无法反查路径，这里固定从根目录开始选
  dirs.value = []
  browseDialog.value = true
  await loadBrowse('')
}

async function loadBrowse(dirId) {
  browsing.value = true
  try {
    const res = await diskApi.browse(browseAccountId.value, dirId)
    dirs.value = (res.items || []).filter((i) => i.isDir)
  } catch (e) {
    dirs.value = []
  } finally {
    browsing.value = false
  }
}

async function enterDir(d) {
  stack.value.push({ id: d.id, name: d.name })
  await loadBrowse(d.id)
}

async function goUp() {
  stack.value.pop()
  await loadBrowse(stack.value.length ? stack.value[stack.value.length - 1].id : '')
}

function confirmBrowse() {
  const cur = stack.value.length ? stack.value[stack.value.length - 1] : null
  config.value.instant_remote_dir = cur ? cur.id : ''
  browseDialog.value = false
}

// 换了转存账号后，之前选的目录 ID 在新账号里多半不存在，直接清掉
watch(
  () => config.value.instant_account_id,
  () => {
    config.value.instant_remote_dir = ''
  }
)

async function loadStatus() {
  try {
    status.value = await systemApi.status()
  } catch (e) {
    /* 忽略 */
  }
}

async function save() {
  saving.value = true
  try {
    const payload = {}
    Object.keys(config.value).forEach((k) => {
      payload[k] = String(config.value[k])
    })
    await systemApi.updateConfig(payload)
    ElMessage.success('配置已保存')
    loadStatus()
  } catch (e) {
    /* 忽略 */
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  load()
  loadStatus()
  loadAccounts()
  loadCategories()
})
</script>

<style scoped>
.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px;
}
.dir-nav {
  display: flex;
  align-items: center;
}
.dir-list {
  max-height: 320px;
  overflow-y: auto;
}
.dir-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-radius: 6px;
  cursor: pointer;
}
.dir-item:hover {
  background: var(--ls-bg);
}
</style>
