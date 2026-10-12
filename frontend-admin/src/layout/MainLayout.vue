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
        <template v-for="m in menus" :key="m.key">
          <el-sub-menu v-if="m.children" :index="m.key">
            <template #title>
              <el-icon><component :is="m.icon" /></el-icon>
              <span>{{ m.title }}</span>
            </template>
            <el-menu-item v-for="c in m.children" :key="c.path" :index="c.path">
              <el-icon><component :is="c.icon" /></el-icon>
              <span>{{ c.title }}</span>
            </el-menu-item>
          </el-sub-menu>
          <el-menu-item v-else :index="m.path">
            <el-icon><component :is="m.icon" /></el-icon>
            <span>{{ m.title }}</span>
          </el-menu-item>
        </template>
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

// 菜单由路由推导，避免"加了页面忘了加菜单"。
//
// meta.group 存在时归入同名分组（渲染成 el-sub-menu），不存在就是顶级项。
// 分组的图标取其第一个子项的 meta.groupIcon —— 这样每个子页面只声明
// 自己那两项，不用为了一个分组再单独维护一份菜单表。
const menus = computed(() => {
  const children = router.getRoutes().find((r) => r.path === '/')?.children || []
  const items = children
    .filter((c) => c.meta?.title)
    .map((c) => ({
      path: '/' + c.path,
      title: c.meta.title,
      icon: c.meta.icon,
      group: c.meta.group || '',
      groupIcon: c.meta.groupIcon || c.meta.icon,
    }))

  const out = []
  const groups = new Map()
  for (const it of items) {
    if (!it.group) {
      out.push({ key: it.path, path: it.path, title: it.title, icon: it.icon })
      continue
    }
    let g = groups.get(it.group)
    if (!g) {
      g = { key: 'group:' + it.group, title: it.group, icon: it.groupIcon, children: [] }
      groups.set(it.group, g)
      out.push(g)
    }
    g.children.push({ path: it.path, title: it.title, icon: it.icon })
  }
  return out
})

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
