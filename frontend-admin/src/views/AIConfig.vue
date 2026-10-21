<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">AI 配置</h2>
    </div>

    <div class="row-between mb-16">
      <el-alert type="info" :closable="false" class="flex-1">
        兼容 OpenAI Chat Completions 协议，支持 OpenAI、DeepSeek、通义千问、月之暗面、Ollama 及任意兼容服务。
        密钥会加密存储。
      </el-alert>
      <el-button type="primary" :icon="Plus" @click="openDialog()">新增配置</el-button>
    </div>

    <el-row :gutter="16">
      <el-col :span="8" v-for="cfg in configs" :key="cfg.id">
        <div class="card-box ai-card" :class="{ default: cfg.isDefault }">
          <div class="ai-header">
            <div>
              <div class="ai-name">
                {{ cfg.name }}
                <el-tag v-if="cfg.isDefault" size="small" type="warning">默认</el-tag>
              </div>
              <div class="text-secondary">{{ cfg.provider }}</div>
            </div>
            <el-tag :type="statusType(cfg.status)" size="small">{{ statusText(cfg.status) }}</el-tag>
          </div>

          <el-descriptions :column="1" size="small" class="mt-16">
            <el-descriptions-item label="接口地址">{{ cfg.baseUrl }}</el-descriptions-item>
            <el-descriptions-item label="模型">{{ cfg.model }}</el-descriptions-item>
            <el-descriptions-item label="密钥">{{ cfg.apiKeyMasked || '未设置' }}</el-descriptions-item>
            <el-descriptions-item label="温度">{{ cfg.temperature }}</el-descriptions-item>
            <el-descriptions-item label="最大 Token">{{ cfg.maxTokens }}</el-descriptions-item>
          </el-descriptions>

          <div class="ai-actions">
            <el-button link type="primary" size="small" @click="test(cfg)">测试连接</el-button>
            <el-button link type="primary" size="small" @click="openDialog(cfg)">编辑</el-button>
            <el-button link type="danger" size="small" @click="remove(cfg)">删除</el-button>
          </div>
        </div>
      </el-col>
    </el-row>

    <el-empty v-if="!configs.length" description="还没有配置 AI 接口，AI 学情分析和智能体问答依赖此配置" />

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑 AI 配置' : '新增 AI 配置'" width="580px">
      <el-form :model="form" label-width="110px">
        <el-form-item label="配置名称" required>
          <el-input v-model="form.name" placeholder="如：DeepSeek" />
        </el-form-item>
        <el-form-item label="服务商">
          <el-select v-model="form.provider" style="width: 100%" @change="onProviderChange">
            <el-option label="OpenAI" value="openai" />
            <el-option label="DeepSeek" value="deepseek" />
            <el-option label="通义千问" value="qwen" />
            <el-option label="月之暗面 Kimi" value="moonshot" />
            <el-option label="智谱 GLM" value="zhipu" />
            <el-option label="Ollama（本地）" value="ollama" />
            <el-option label="自定义（OpenAI 兼容）" value="custom" />
          </el-select>
        </el-form-item>
        <el-form-item label="接口地址" required>
          <el-input v-model="form.baseUrl" placeholder="https://api.deepseek.com/v1" />
        </el-form-item>
        <el-form-item label="API Key" :required="!editing">
          <el-input
            v-model="form.apiKey"
            placeholder="留空表示不修改"
            show-password
          />
        </el-form-item>
        <el-form-item label="模型名称" required>
          <el-input v-model="form.model" placeholder="deepseek-chat" />
        </el-form-item>
        <el-form-item label="温度">
          <el-slider v-model="form.temperature" :min="0" :max="2" :step="0.1" show-input />
        </el-form-item>
        <el-form-item label="最大 Token">
          <el-input-number v-model="form.maxTokens" :min="256" :max="128000" :step="512" />
        </el-form-item>
        <el-form-item label="超时（秒）">
          <el-input-number v-model="form.timeoutSec" :min="10" :max="600" :step="10" />
        </el-form-item>
        <el-form-item label="设为默认">
          <el-switch v-model="form.isDefault" />
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
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { aiApi } from '@/api'

const configs = ref([])
const dialogVisible = ref(false)
const editing = ref(false)
const saving = ref(false)
const currentId = ref(null)

const presets = {
  openai: { baseUrl: 'https://api.openai.com/v1', model: 'gpt-4o' },
  deepseek: { baseUrl: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
  qwen: { baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1', model: 'qwen-plus' },
  moonshot: { baseUrl: 'https://api.moonshot.cn/v1', model: 'moonshot-v1-8k' },
  zhipu: { baseUrl: 'https://open.bigmodel.cn/api/paas/v4', model: 'glm-4' },
  ollama: { baseUrl: 'http://localhost:11434/v1', model: 'qwen2.5' },
  custom: { baseUrl: '', model: '' },
}

const form = reactive({
  name: '',
  provider: 'deepseek',
  baseUrl: '',
  apiKey: '',
  model: '',
  temperature: 0.7,
  maxTokens: 4096,
  timeoutSec: 60,
  isDefault: false,
})

function statusType(s) {
  return { valid: 'success', invalid: 'danger' }[s] || 'info'
}

function statusText(s) {
  return { valid: '可用', invalid: '不可用', unknown: '未测试' }[s] || '未测试'
}

async function load() {
  try {
    configs.value = await aiApi.listConfigs()
  } catch (e) {
    /* 忽略 */
  }
}

function onProviderChange(p) {
  const preset = presets[p]
  if (preset) {
    form.baseUrl = preset.baseUrl
    if (!form.model) form.model = preset.model
  }
}

function openDialog(cfg) {
  if (cfg) {
    editing.value = true
    currentId.value = cfg.id
    Object.assign(form, {
      name: cfg.name,
      provider: cfg.provider,
      baseUrl: cfg.baseUrl,
      apiKey: '',
      model: cfg.model,
      temperature: cfg.temperature,
      maxTokens: cfg.maxTokens,
      timeoutSec: cfg.timeoutSec || 60,
      isDefault: cfg.isDefault,
    })
  } else {
    editing.value = false
    currentId.value = null
    Object.assign(form, {
      name: '',
      provider: 'deepseek',
      baseUrl: presets.deepseek.baseUrl,
      apiKey: '',
      model: presets.deepseek.model,
      temperature: 0.7,
      maxTokens: 4096,
      timeoutSec: 60,
      isDefault: false,
    })
  }
  dialogVisible.value = true
}

async function save() {
  if (!form.name || !form.baseUrl || !form.model) {
    ElMessage.warning('请填写完整信息')
    return
  }
  if (!editing.value && !form.apiKey) {
    ElMessage.warning('请填写 API Key')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await aiApi.updateConfig(currentId.value, { ...form })
      ElMessage.success('更新成功')
    } else {
      await aiApi.createConfig({ ...form })
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

async function test(cfg) {
  const loading = ElMessage({ message: '测试中...', duration: 0 })
  try {
    const res = await aiApi.testConfig(cfg.id)
    loading.close()
    if (res.ok) {
      ElMessage.success(`连接正常，延迟 ${res.latencyMs}ms`)
    } else {
      ElMessage.error(`连接失败：${res.message}`)
    }
    load()
  } catch (e) {
    loading.close()
  }
}

async function remove(cfg) {
  try {
    await ElMessageBox.confirm(`确定删除「${cfg.name}」吗？`, '删除确认', { type: 'warning' })
    await aiApi.deleteConfig(cfg.id)
    ElMessage.success('已删除')
    load()
  } catch (e) {
    /* 取消 */
  }
}

onMounted(load)
</script>

<style scoped>
.row-between {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.flex-1 {
  flex: 1;
}

.ai-card {
  margin-bottom: 16px;
  border: 2px solid transparent;
}

.ai-card.default {
  border-color: #e6a23c;
}

.ai-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
}

.ai-name {
  font-size: 16px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 6px;
}

.ai-actions {
  border-top: 1px solid var(--ls-border);
  padding-top: 10px;
  margin-top: 4px;
  display: flex;
  gap: 8px;
}
</style>
