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
        path: 'resources',
        name: 'Resources',
        component: () => import('@/views/Resources.vue'),
        meta: { title: '资源列表', icon: 'Files', group: '资源管理', groupIcon: 'Files' },
      },
      {
        // WebDAV 与资源映射同属"资源管理"：它们解决的都是
        // "把已有文件变成系统里的资源"，和网盘账号那种"凭证管理"
        // 不是一回事，所以不放在「网盘账号」下。
        path: 'resources/webdav',
        name: 'WebdavResources',
        component: () => import('@/views/WebdavResources.vue'),
        meta: { title: 'WebDAV 与映射', icon: 'Connection', group: '资源管理', groupIcon: 'Files' },
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
        meta: { title: '网盘账号', icon: 'Cloudy' },
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
        path: 'ai-config',
        name: 'AIConfig',
        component: () => import('@/views/AIConfig.vue'),
        meta: { title: 'AI 配置', icon: 'MagicStick' },
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
