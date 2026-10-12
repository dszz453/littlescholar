<template>
  <div class="login">
    <div class="deco">
      <span class="d1">📚</span>
      <span class="d2">🎨</span>
      <span class="d3">🎵</span>
      <span class="d4">⭐</span>
    </div>

    <div class="panel pop-in">
      <div class="brand">
        <span class="logo">🐻</span>
        <h1>小书童</h1>
        <p>欢迎回来，一起学习吧</p>
      </div>

      <form @submit.prevent="submit">
        <label class="field">
          <span class="f-ico">👤</span>
          <input
            v-model="username"
            type="text"
            placeholder="你的名字"
            autocomplete="username"
            required
          />
        </label>

        <label class="field">
          <span class="f-ico">🔒</span>
          <input
            v-model="password"
            type="password"
            placeholder="密码"
            autocomplete="current-password"
            required
          />
        </label>

        <p v-if="error" class="err">{{ error }}</p>

        <button class="btn pressable" type="submit" :disabled="loading">
          {{ loading ? '登录中…' : '开始学习' }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useUserStore } from '@/stores/user'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

async function submit() {
  if (loading.value) return
  error.value = ''
  loading.value = true
  try {
    await userStore.login(username.value.trim(), password.value)
    // 预热数据，进首页时不用等
    await Promise.allSettled([
      userStore.loadProfile(true),
      userStore.loadTree(true),
    ])
    const redirect = route.query.redirect
    router.replace(typeof redirect === 'string' ? redirect : '/')
  } catch (e) {
    error.value = e?.message || '登录失败，请检查名字和密码'
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login {
  position: relative;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: linear-gradient(160deg, #e8f0ff 0%, #f7f9fc 55%, #fff5e8 100%);
  overflow: hidden;
}

/* 背景漂浮装饰 */
.deco span {
  position: absolute;
  font-size: 40px;
  opacity: 0.16;
  animation: float 6s ease-in-out infinite;
}
.d1 { top: 12%; left: 12%; animation-delay: 0s; }
.d2 { top: 20%; right: 14%; animation-delay: 1.2s; }
.d3 { bottom: 18%; left: 16%; animation-delay: 2.4s; }
.d4 { bottom: 24%; right: 12%; animation-delay: 3.6s; }

@keyframes float {
  0%, 100% { transform: translateY(0) rotate(0deg); }
  50% { transform: translateY(-16px) rotate(8deg); }
}

.panel {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 360px;
}

.brand {
  text-align: center;
  margin-bottom: 28px;
}
.logo {
  display: inline-block;
  font-size: 62px;
  line-height: 1;
  margin-bottom: 10px;
}
.brand h1 {
  margin: 0;
  font-size: 26px;
  font-weight: 800;
  letter-spacing: 2px;
  color: var(--c-text);
}
.brand p {
  margin: 6px 0 0;
  font-size: 14px;
  color: var(--c-text-sub);
}

.field {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 52px;
  padding: 0 16px;
  background: #fff;
  border-radius: var(--r-md);
  box-shadow: var(--shadow-sm);
  margin-bottom: 12px;
  transition: box-shadow 0.2s ease;
}
.field:focus-within {
  box-shadow: 0 4px 18px rgba(91, 143, 249, 0.2);
}

.f-ico {
  font-size: 17px;
  line-height: 1;
}

.field input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: 0;
  background: transparent;
  font-size: 16px;
  font-family: inherit;
  color: var(--c-text);
}
.field input::placeholder {
  color: #aab6c4;
}

.err {
  margin: 0 0 12px;
  padding: 10px 14px;
  background: #fff1f0;
  border-radius: var(--r-sm);
  color: #d9554a;
  font-size: 13.5px;
}

.btn {
  width: 100%;
  height: 52px;
  border: 0;
  border-radius: var(--r-md);
  background: linear-gradient(135deg, #6b9bff, #5b8ff9);
  color: #fff;
  font-size: 17px;
  font-weight: 700;
  font-family: inherit;
  letter-spacing: 1px;
  box-shadow: 0 6px 20px rgba(91, 143, 249, 0.34);
}
.btn:disabled {
  opacity: 0.65;
  box-shadow: none;
}
</style>
