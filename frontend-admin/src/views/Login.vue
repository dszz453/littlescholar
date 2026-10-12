<template>
  <div class="login-page">
    <div class="login-card">
      <div class="brand">
        <div class="brand-icon">📚</div>
        <h1 class="brand-title">小书童</h1>
        <p class="brand-sub">孩子学习系统 · 管理后台</p>
      </div>

      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        size="large"
        @keyup.enter="handleLogin"
      >
        <el-form-item prop="username">
          <el-input
            v-model="form.username"
            placeholder="请输入用户名"
            :prefix-icon="User"
          />
        </el-form-item>
        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="请输入密码"
            :prefix-icon="Lock"
            show-password
          />
        </el-form-item>
        <el-form-item>
          <el-button
            type="primary"
            class="login-btn"
            :loading="loading"
            @click="handleLogin"
          >
            登 录
          </el-button>
        </el-form-item>
      </el-form>

      <p class="tip">默认账号 admin / admin123，请登录后及时修改</p>
    </div>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import {  ElMessage  } from 'element-plus'
import { User, Lock } from '@element-plus/icons-vue'
import { useUserStore } from '@/stores/user'

const router = useRouter()
const route = useRoute()
const userStore = useUserStore()

const formRef = ref()
const loading = ref(false)
const form = reactive({ username: '', password: '' })

const rules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }],
}

async function handleLogin() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    await userStore.login(form)
    ElMessage.success('登录成功')
    router.push(route.query.redirect || '/dashboard')
  } catch (e) {
    // 错误已由拦截器提示
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #4a90d9 0%, #6dc8ec 50%, #5ad8a6 100%);
}

.login-card {
  width: 380px;
  background: #fff;
  border-radius: 16px;
  padding: 40px 36px 28px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.15);
}

.brand {
  text-align: center;
  margin-bottom: 28px;
}

.brand-icon {
  font-size: 46px;
  line-height: 1;
}

.brand-title {
  font-size: 26px;
  margin: 12px 0 4px;
  color: #303133;
}

.brand-sub {
  font-size: 13px;
  color: #909399;
  margin: 0;
}

.login-btn {
  width: 100%;
}

.tip {
  text-align: center;
  font-size: 12px;
  color: #c0c4cc;
  margin: 8px 0 0;
}
</style>
