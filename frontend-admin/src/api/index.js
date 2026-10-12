import axios from 'axios'
import {  ElMessage  } from 'element-plus'
import router from '@/router'

const http = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api',
  timeout: 120000,
})

// 请求拦截：带上 token
http.interceptors.request.use((config) => {
  const token = localStorage.getItem('ls_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 响应拦截：统一处理业务码
http.interceptors.response.use(
  (response) => {
    const data = response.data
    if (data && typeof data.code !== 'undefined') {
      if (data.code === 0) {
        return data.data
      }
      if (data.code === 401) {
        localStorage.removeItem('ls_token')
        localStorage.removeItem('ls_refresh_token')
        if (router.currentRoute.value.path !== '/login') {
          ElMessage.error('登录已失效，请重新登录')
          router.push('/login')
        }
        return Promise.reject(new Error(data.message))
      }
      ElMessage.error(data.message || '请求失败')
      return Promise.reject(new Error(data.message))
    }
    return data
  },
  (error) => {
    const msg = error.response?.data?.message || error.message || '网络错误'
    ElMessage.error(msg)
    return Promise.reject(error)
  }
)

export default http

// ============ API 模块 ============

export const authApi = {
  login: (data) => http.post('/admin/auth/login', data),
  profile: () => http.get('/admin/auth/profile'),
  logout: () => http.post('/admin/auth/logout'),
}

export const categoryApi = {
  icons: () => http.get('/admin/categories/icons'),
  tree: (withCount = true) => http.get('/admin/categories/tree', { params: { withResourceCount: withCount } }),
  create: (data) => http.post('/admin/categories', data),
  update: (id, data) => http.put(`/admin/categories/${id}`, data),
  move: (id, data) => http.put(`/admin/categories/${id}/move`, data),
  remove: (id, cascade = false) => http.delete(`/admin/categories/${id}`, { params: { cascade } }),
}

export const resourceApi = {
  list: (params) => http.get('/admin/resources', { params }),
  detail: (id) => http.get(`/admin/resources/${id}`),
  create: (data) => http.post('/admin/resources', data),
  update: (id, data) => http.put(`/admin/resources/${id}`, data),
  batchDelete: (ids) => http.delete('/admin/resources/batch', { data: { ids } }),
  move: (ids, categoryId) => http.put('/admin/resources/move', { ids, categoryId }),
  checkAlive: (ids) => http.post('/admin/resources/check-alive', { ids }),
  import: (data) => http.post('/admin/resources/import', data),
}

export const searchApi = {
  search: (data) => http.post('/admin/search', data),
  health: () => http.get('/admin/search/health'),
}

export const diskApi = {
  types: () => http.get('/admin/disk/types'),
  list: () => http.get('/admin/disk/accounts'),
  getQR: (diskType) => http.post('/admin/disk/accounts/qr', { diskType }),
  pollQR: (sessionId) => http.get(`/admin/disk/accounts/qr/${sessionId}`),
  addManual: (data) => http.post('/admin/disk/accounts/manual', data),
  validate: (id) => http.post(`/admin/disk/accounts/${id}/validate`),
  updateCookie: (id, cookie) => http.put(`/admin/disk/accounts/${id}/cookie`, { cookie }),
  setDefault: (id) => http.post(`/admin/disk/accounts/${id}/default`),
  remove: (id) => http.delete(`/admin/disk/accounts/${id}`),
  browse: (id, dirId = '') => http.get(`/admin/disk/accounts/${id}/browse`, { params: { dirId } }),
  mkdir: (id, parentId, name) => http.post(`/admin/disk/accounts/${id}/mkdir`, { parentId, name }),
}

export const transferApi = {
  create: (data) => http.post('/admin/transfer', data),
  batch: (data) => http.post('/admin/transfer/batch', data),
  tasks: (params) => http.get('/admin/transfer/tasks', { params }),
  retry: (id) => http.post(`/admin/transfer/tasks/${id}/retry`),
  cancel: (id) => http.post(`/admin/transfer/tasks/${id}/cancel`),
}

export const userApi = {
  list: (params) => http.get('/admin/users', { params }),
  create: (data) => http.post('/admin/users', data),
  update: (id, data) => http.put(`/admin/users/${id}`, data),
  resetPassword: (id, password) => http.put(`/admin/users/${id}/password`, { password }),
  getGrants: (id) => http.get(`/admin/users/${id}/grants`),
  setGrants: (id, grants) => http.put(`/admin/users/${id}/grants`, { grants }),
  remove: (id) => http.delete(`/admin/users/${id}`),
}

export const aiApi = {
  listConfigs: () => http.get('/admin/ai/configs'),
  createConfig: (data) => http.post('/admin/ai/configs', data),
  updateConfig: (id, data) => http.put(`/admin/ai/configs/${id}`, data),
  deleteConfig: (id) => http.delete(`/admin/ai/configs/${id}`),
  testConfig: (id) => http.post(`/admin/ai/configs/${id}/test`),
}

export const studyApi = {
  overview: (params) => http.get('/admin/study/overview', { params }),
  records: (params) => http.get('/admin/study/records', { params }),
  analyze: (data) => http.post('/admin/study/analyze', data),
  reports: (params) => http.get('/admin/study/reports', { params }),
  reportDetail: (id) => http.get(`/admin/study/reports/${id}`),
  deleteReport: (id) => http.delete(`/admin/study/reports/${id}`),
}

export const systemApi = {
  getConfig: () => http.get('/admin/system/config'),
  updateConfig: (data) => http.put('/admin/system/config', data),
  status: () => http.get('/admin/system/status'),
}

export const aliveApi = {
  status: () => http.get('/admin/alive/status'),
  start: (mode, trigger = 'manual') => http.post('/admin/alive/check', { mode, trigger }),
  tasks: (params) => http.get('/admin/alive/tasks', { params }),
  detail: (id) => http.get(`/admin/alive/tasks/${id}`),
  dead: (limit = 50) => http.get('/admin/alive/dead', { params: { limit } }),
}

export const transcodeApi = {
  status: () => http.get('/admin/transcode/status'),
  create: (resourceId) => http.post('/admin/transcode', { resourceId }),
  tasks: (params) => http.get('/admin/transcode/tasks', { params }),
  detail: (id) => http.get(`/admin/transcode/tasks/${id}`),
  cancel: (id) => http.post(`/admin/transcode/tasks/${id}/cancel`),
  remove: (id) => http.delete(`/admin/transcode/tasks/${id}`),
  gc: () => http.post('/admin/transcode/gc'),
}

export const playApi = {
  info: (id) => http.get(`/play/${id}`),
  progress: (id, data) => http.post(`/app/play/${id}/progress`, data),
}
