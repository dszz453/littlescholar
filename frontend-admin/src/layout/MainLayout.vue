<template>
  <el-container class="layout">
    <el-aside width="220px" class="aside">
      <div class="logo">
        <span class="logo-icon">📚</span>
        <span class="logo-text">小书童</span>
      </div>
      <el-menu
        :default-active="activeMenu"
        router
        class="menu"
        background-color="#1f2d3d"
        text-color="#c0c4cc"
        active-text-color="#ffffff"
      >
        <el-menu-item
          v-for="item in menus"
          :key="item.path"
          :index="item.path"
        >
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.title }}</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="header">
        <div class="header-left">
          <el-breadcrumb separator="/">
            <el-breadcrumb-item :to="{ path: '/dashboard' }">首页</el-breadcrumb-item>
            <el-breadcrumb-item>{{ currentTitle }}</el-breadcrumb-item>
          </el-breadcrumb>
        </div>
        <div class="header-right">
          <el-tag
            :type="status.pansouOnline ? 'success' : 'danger'"
            size="small"
            class="mr-8"
          >
            PanSou {{ status.pansouOnline ? '在线' : '离线' }}
          </el-tag>
          <el-tag
            :type="status.dbOnline ? 'success' : 'danger'"
            size="small"
            class="mr-8"
          >
            数据库 {{ status.dbOnline ? '正常' : '异常' }}
          </el-tag>
          <el-dropdown @command="handleCommand">
            <span class="user-info">
              <el-avatar :size="30" class="mr-8">
                {{ nickname.charAt(0) }}
              </el-avatar>
              {{ nickname }}
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <el-main class="main">
        <router-view v-slot="{ Component }">
          <keep-alive :max="7">
            <component :is="Component" />
          </keep-alive>
        </router-view>
      </el-main>
    </el-container>
  </el-container>
</template>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {  ElMessageBox  } from 'element-plus'
import { useUserStore } from '@/stores/user'
import { systemApi } from '@/api'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()

const status = ref({ pansouOnline: false, dbOnline: false })

const menus = computed(() =>
  (router.getRoutes().find((r) => r.path === '/')?.children || [])
    .filter((c) => c.meta?.title)
    .map((c) => ({
      path: '/' + c.path,
      title: c.meta.title,
      icon: c.meta.icon,
    }))
)

const activeMenu = computed(() => route.path)
const currentTitle = computed(() => route.meta?.title || '')
const nickname = computed(() => userStore.nickname)

async function loadStatus() {
  try {
    status.value = await systemApi.status()
  } catch (e) {
    // 忽略
  }
}

async function handleCommand(cmd) {
  if (cmd === 'logout') {
    await ElMessageBox.confirm('确定要退出登录吗？', '提示', { type: 'warning' })
    await userStore.logout()
    router.push('/login')
  }
}

onMounted(() => {
  loadStatus()
  setInterval(loadStatus, 30000)
})
</script>

<style scoped>
.layout {
  height: 100vh;
}

.aside {
  background: #1f2d3d;
  overflow-x: hidden;
}

.logo {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 18px;
  font-weight: 600;
  border-bottom: 1px solid #2c3e50;
}

.logo-icon {
  font-size: 22px;
  margin-right: 8px;
}

.menu {
  border-right: none;
}

.header {
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: space-between;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.06);
  z-index: 10;
}

.header-right {
  display: flex;
  align-items: center;
}

.user-info {
  display: flex;
  align-items: center;
  cursor: pointer;
  outline: none;
}

.main {
  background: var(--ls-bg);
  padding: 0;
  overflow-y: auto;
}
</style>
