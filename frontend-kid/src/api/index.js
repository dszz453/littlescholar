import axios from 'axios'
import router from '@/router'
import { showToast } from 'vant'

const http = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/api',
  timeout: 60000,
})

// 请求拦截：注入 token
http.interceptors.request.use((config) => {
  const token = localStorage.getItem('kid_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 响应拦截：解包统一响应格式 {code, message, data}
http.interceptors.response.use(
  (response) => {
    const data = response.data
    if (data && typeof data.code !== 'undefined') {
      if (data.code === 0) return data.data
      if (data.code === 401) {
        localStorage.removeItem('kid_token')
        if (router.currentRoute.value.path !== '/login') {
          router.replace('/login')
        }
        return Promise.reject(new Error(data.message || '登录已失效'))
      }
      // 孩子端用轻提示而非弹窗，避免打断
      showToast(data.message || '操作失败')
      return Promise.reject(new Error(data.message))
    }
    return data
  },
  (error) => {
    const msg = error.response?.data?.message || error.message || '网络异常'
    showToast(msg)
    return Promise.reject(error)
  }
)

export default http

// ============ C 端接口 ============

export const authApi = {
  login: (data) => http.post('/app/auth/login', data),
  profile: () => http.get('/app/auth/profile'),
  logout: () => http.post('/app/auth/logout'),
}

export const categoryApi = {
  // 返回已按权限过滤的目录树
  tree: () => http.get('/app/categories/tree'),
}

export const resourceApi = {
  list: (params) => http.get('/app/resources', { params }),
  detail: (id) => http.get(`/app/resources/${id}`),
  // 资源分布：孩子端「找一找」按网盘筛选用
  facets: (params) => http.get('/app/resources/facets', { params }),
}

export const searchApi = {
  // 「找一找」：系统内全文检索，一次请求同时返回命中的**目录**与**资源**。
  // 后端会自动按孩子可见目录过滤，这里不需要也不能传权限参数。
  all: (params) => http.get('/app/search', { params }),
}

export const playApi = {
  // 获取播放信息（含播放地址、断点位置）
  //
  // quality：'original'（默认，不转码、秒开）/ 'smooth'（转码成 HLS，弱网更稳）。
  // 不传即由后端按默认的 original 处理，但播放页始终显式带上，
  // 免得"默认值"改动时前后端理解不一致。
  info: (id, quality) => http.get(`/app/play/${id}`, { params: quality ? { quality } : {} }),
  // 上报进度
  progress: (id, data) => http.post(`/app/play/${id}/progress`, data),
  // 播放列表（同目录下的其他资源）
  playlist: (id) => http.get(`/app/play/${id}/playlist`),
  history: (params) => http.get('/app/play-history', { params }),
}

export const aiApi = {
  // 与小书童对话
  chat: (data) => http.post('/app/ai/chat', data),
}

export const studyApi = {
  overview: (params) => http.get('/app/study/overview', { params }),
  // 学习记录明细。groupBy=resource 时按资源聚合（一次播放每 15 秒一条心跳，
  // 不聚合的话同一个资源会把列表刷屏）。
  records: (params) => http.get('/app/study/records', { params }),
}

export const planApi = {
  list: (params) => http.get('/app/study/plans', { params }),
  create: (data) => http.post('/app/study/plans', data),
  update: (id, data) => http.put(`/app/study/plans/${id}`, data),
  remove: (id) => http.delete(`/app/study/plans/${id}`),
  // 今日总览：返回 { date, hasPlan, completed, plans[] }。
  // 是否"达标"由后端按当天学习时长实时算，前端只负责据此提示。
  today: () => http.get('/app/study/today'),
  awards: (params) => http.get('/app/study/awards', { params }),
  // 立刻尝试为本周期生成奖状（达标才生成）
  refreshAward: () => http.post('/app/study/awards/refresh'),
}
