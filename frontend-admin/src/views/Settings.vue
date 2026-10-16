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
            <el-form-item label="播放分块并发">
              <el-slider v-model.number="config.multi_conn" :min="0" :max="32" style="width: 300px" />
              <span class="text-secondary" style="margin-left: 12px">
                {{
                  Number(config.multi_conn) > 1
                    ? `当前 ${config.multi_conn} 连接`
                    : '已关闭（单流）'
                }}
              </span>
              <div class="text-secondary">
                播放时把网盘直链切成多段<b>并发拉取</b>、再按序吐给浏览器 —— 这就是「非会员加速」。
                网盘对非会员普遍做单连接限速，拆成几条连接总吞吐常常能翻好几倍。
                默认 4，<b>0 或 1 表示关闭</b>。并非越大越好：并发开太大容易被网盘判为滥用，
                反而限速甚至封号。
              </div>
            </el-form-item>
            <el-form-item label="外部加速代理">
              <el-input v-model="config.accel_proxy" placeholder="http://192.168.1.5:8888/proxy?url={url}" />
              <div class="text-secondary">
                留空则不启用。<b>填了它就优先走它</b>，上面的分块并发会自动让位
                —— 两者不叠加，叠加等于把同一件事做两遍。
                适合"服务器带宽不够、需要外部节点中转"的场景，可对接 aliproxy 一类的多连接代理。
                支持 <code>{url}</code> 占位符，也支持直接作为前缀拼接。
              </div>
            </el-form-item>
          </el-form>
        </div>

      </el-col>

      <el-col :span="10">
        <div class="card-box">
          <h3 class="section-title">系统状态</h3>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="版本">
              <span class="ver">v{{ status.version || '…' }}</span>
              <span v-if="status.commit || status.buildTime" class="build">{{ buildLabel(status) }}</span>
            </el-descriptions-item>
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

  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { systemApi } from '@/api'
import { buildLabel } from '@/utils/buildinfo'

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
  multi_conn: 4,
  accel_proxy: '',
})

const status = ref({})
const saving = ref(false)

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
})
</script>

<style scoped>
.section-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px;
}

.ver {
  font-weight: 600;
}

/* 构建信息（短 commit + 构建时刻）是次要信息：小字挂在版本号后面，
   不抢眼但随时可读。取不到时整段不渲染，见 utils/buildinfo.js。 */
.build {
  margin-left: 6px;
  font-size: 12px;
  color: var(--ls-text-secondary);
}
</style>
