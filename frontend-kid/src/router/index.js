import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/Login.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/',
    name: 'home',
    component: () => import('@/views/Home.vue'),
    meta: { title: '首页' },
  },
  {
    path: '/category/:id',
    name: 'category',
    component: () => import('@/views/Category.vue'),
    meta: { title: '目录' },
  },
  {
    path: '/search',
    name: 'search',
    component: () => import('@/views/Search.vue'),
    meta: { title: '搜索' },
  },
  {
    path: '/tutor',
    name: 'tutor',
    component: () => import('@/views/Tutor.vue'),
    meta: { title: '小书童' },
  },
  {
    path: '/me',
    name: 'me',
    component: () => import('@/views/Me.vue'),
    meta: { title: '我的' },
  },
  {
    path: '/plan',
    name: 'plan',
    component: () => import('@/views/Plan.vue'),
    meta: { title: '学习计划' },
  },
  {
    path: '/offline',
    name: 'offline',
    component: () => import('@/views/Offline.vue'),
    meta: { title: '我的缓存' },
  },
  {
    path: '/play/:id',
    name: 'play',
    component: () => import('@/views/Play.vue'),
    meta: { title: '播放', fullscreen: true },
  },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach((to) => {
  const token = localStorage.getItem('kid_token')
  // 未登录且目标不是公开页 → 去登录
  if (!to.meta.public && !token) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  // 已登录还去登录页 → 回首页
  if (to.meta.public && token) {
    return { name: 'home' }
  }
  return true
})

router.afterEach((to) => {
  if (to.meta.title) {
    document.title = `${to.meta.title} · 小书童`
  }
})

export default router
