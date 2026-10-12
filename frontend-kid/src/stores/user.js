import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authApi, categoryApi } from '@/api'

export const useUserStore = defineStore('user', () => {
  const token = ref(localStorage.getItem('kid_token') || '')
  const profile = ref(null)
  const tree = ref([])       // 已按权限过滤的目录树
  const treeLoaded = ref(false)

  const isLoggedIn = computed(() => !!token.value)
  const nickname = computed(() => profile.value?.nickname || '小朋友')
  const grade = computed(() => profile.value?.grade || '')
  const avatar = computed(() => profile.value?.avatar || '')

  function setToken(t) {
    token.value = t
    if (t) localStorage.setItem('kid_token', t)
    else localStorage.removeItem('kid_token')
  }

  async function login(username, password) {
    const data = await authApi.login({ username, password })
    setToken(data.token)
    if (data.refreshToken) {
      localStorage.setItem('kid_refresh_token', data.refreshToken)
    }
    if (data.user) profile.value = data.user
    return data
  }

  async function loadProfile(force = false) {
    if (profile.value && !force) return profile.value
    profile.value = await authApi.profile()
    return profile.value
  }

  /** 加载目录树（带缓存，force=true 强制刷新） */
  async function loadTree(force = false) {
    if (treeLoaded.value && !force) return tree.value
    const data = await categoryApi.tree()
    tree.value = Array.isArray(data) ? data : data?.list || []
    treeLoaded.value = true
    return tree.value
  }

  /** 在已加载的树中按 id 查找节点（用于面包屑、目录页标题） */
  function findNode(id, nodes = tree.value) {
    const target = String(id)
    for (const n of nodes) {
      if (String(n.id) === target) return n
      if (n.children?.length) {
        const hit = findNode(id, n.children)
        if (hit) return hit
      }
    }
    return null
  }

  /** 取某节点的祖先链（用于面包屑） */
  function pathOf(id, nodes = tree.value, acc = []) {
    const target = String(id)
    for (const n of nodes) {
      const chain = [...acc, n]
      if (String(n.id) === target) return chain
      if (n.children?.length) {
        const hit = pathOf(id, n.children, chain)
        if (hit) return hit
      }
    }
    return null
  }

  async function logout() {
    try {
      await authApi.logout()
    } catch {
      /* 忽略：登出失败也要清本地态 */
    }
    setToken('')
    localStorage.removeItem('kid_refresh_token')
    profile.value = null
    tree.value = []
    treeLoaded.value = false
  }

  return {
    token,
    profile,
    tree,
    treeLoaded,
    isLoggedIn,
    nickname,
    grade,
    avatar,
    setToken,
    login,
    loadProfile,
    loadTree,
    findNode,
    pathOf,
    logout,
  }
})
