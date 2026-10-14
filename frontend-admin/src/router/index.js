import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/Login.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/',
    component: () => import('@/layout/MainLayout.vue'),
    redirect: '/dashboard',
    children: [
      {
        path: 'dashboard',
        name: 'Dashboard',
        component: () => import('@/views/Dashboard.vue'),
        meta: { title: '概览', icon: 'Odometer' },
      },
      {
        path: 'search',
        name: 'Search',
        component: () => import('@/views/Search.vue'),
        meta: { title: '资源搜索', icon: 'Search' },
      },
      {
        path: 'categories',
        name: 'Categories',
        component: () => import('@/views/Categories.vue'),
        meta: { title: '目录管理', icon: 'FolderOpened' },
      },
      {
        // 「资源管理」下两个二级菜单：资源列表（接入源的平铺文件夹视图）
        // 和资源映射（把接入源目录绑进系统目录）。两者操作对象不同、
        // 使用节奏也不同，拆开比塞进一个页面的三个页签更顺手。
        path: 'resources',
        name: 'ResourceList',
        component: () => import('@/views/ResourceList.vue'),
        meta: { title: '资源列表', icon: 'FolderOpened', group: '资源管理', groupIcon: 'Files' },
      },
      {
        path: 'resources/mapping',
        name: 'ResourceMapping',
        component: () => import('@/views/ResourceMapping.vue'),
        meta: { title: '资源映射', icon: 'Connection', group: '资源管理' },
      },
      {
        path: 'transfer',
        name: 'Transfer',
        component: () => import('@/views/Transfer.vue'),
        meta: { title: '转存任务', icon: 'Upload' },
      },
      {
        path: 'transcode',
        name: 'Transcode',
        component: () => import('@/views/Transcode.vue'),
        meta: { title: '视频转码', icon: 'VideoCamera' },
      },
      {
        path: 'alive',
        name: 'Alive',
        component: () => import('@/views/Alive.vue'),
        meta: { title: '资源巡检', icon: 'Warning' },
      },
      {
        path: 'disk-accounts',
        name: 'DiskAccounts',
        component: () => import('@/views/DiskAccounts.vue'),
        meta: { title: '资源接入', icon: 'Cloudy' },
      },
      {
        path: 'users',
        name: 'Users',
        component: () => import('@/views/Users.vue'),
        meta: { title: '用户管理', icon: 'User' },
      },
      {
        path: 'study',
        name: 'Study',
        component: () => import('@/views/Study.vue'),
        meta: { title: '学习分析', icon: 'DataAnalysis' },
      },
      {
        // 这三个都是"让 AI 干活"的功能，归到同一个分组里。
        // 分组的图标取第一个子项的 groupIcon（见 MainLayout 的 menus 计算）。
        path: 'ai-config',
        name: 'AIConfig',
        component: () => import('@/views/AIConfig.vue'),
        meta: { title: 'AI 配置', icon: 'MagicStick', group: 'AI 助手', groupIcon: 'MagicStick' },
      },
      {
        path: 'digests',
        name: 'Digests',
        component: () => import('@/views/Digests.vue'),
        meta: { title: '知识沉淀', icon: 'Notebook', group: 'AI 助手' },
      },
      {
        path: 'organize',
        name: 'Organize',
        component: () => import('@/views/Organize.vue'),
        meta: { title: 'AI 整理助手', icon: 'Tools', group: 'AI 助手' },
      },
      {
        path: 'settings',
        name: 'Settings',
        component: () => import('@/views/Settings.vue'),
        meta: { title: '系统设置', icon: 'Setting' },
      },
    ],
  },
  {
    path: '/play/:id',
    name: 'Play',
    component: () => import('@/views/Play.vue'),
    meta: { title: '资源播放' },
  },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach((to, from, next) => {
  const token = localStorage.getItem('ls_token')
  if (to.meta.public) {
    next()
    return
  }
  if (!token) {
    next({ path: '/login', query: { redirect: to.fullPath } })
    return
  }
  document.title = to.meta.title ? `${to.meta.title} · 小书童` : '小书童'
  next()
})

export default router
