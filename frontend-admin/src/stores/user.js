import { defineStore } from 'pinia'
import { authApi } from '@/api'

export const useUserStore = defineStore('user', {
  state: () => ({
    token: localStorage.getItem('ls_token') || '',
    refreshToken: localStorage.getItem('ls_refresh_token') || '',
    profile: JSON.parse(localStorage.getItem('ls_profile') || 'null'),
  }),
  getters: {
    isLogin: (s) => !!s.token,
    nickname: (s) => s.profile?.nickname || s.profile?.username || '管理员',
    role: (s) => s.profile?.role || '',
  },
  actions: {
    async login(payload) {
      const res = await authApi.login(payload)
      this.token = res.token
      this.refreshToken = res.refreshToken
      localStorage.setItem('ls_token', res.token)
      localStorage.setItem('ls_refresh_token', res.refreshToken)
      this.profile = res.user
      localStorage.setItem('ls_profile', JSON.stringify(res.user))
      return res
    },
    async fetchProfile() {
      const res = await authApi.profile()
      this.profile = res
      localStorage.setItem('ls_profile', JSON.stringify(res))
      return res
    },
    async logout() {
      try {
        await authApi.logout()
      } catch (e) {
        // 忽略
      }
      this.token = ''
      this.refreshToken = ''
      this.profile = null
      localStorage.removeItem('ls_token')
      localStorage.removeItem('ls_refresh_token')
      localStorage.removeItem('ls_profile')
    },
  },
})
