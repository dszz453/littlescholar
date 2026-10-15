import axios from 'axios'
import router from '@/router'
import { showToast } from 'vant'

const BASE = import.meta.env.VITE_API_BASE || '/api'

const http = axios.create({
  baseURL: BASE,
  timeout: 60000,
})

// ============ access token 续期 ============
//
// 之前 401 的处理是**直接清 token 跳登录页** —— 而 access token 只有几小时
// 有效期，于是"孩子隔天打开 App 就得重新输账号密码"成了常态。
// refreshToken 明明在登录时就存进 localStorage 了，却从来没有被用过。
//
// 现在：401 → 拿 refreshToken 换一对新令牌 → **把刚才那次请求原样重放**，
// 整个过程对孩子无感；只有 refreshToken 本身也失效了才真的回登录页。
let refreshing = null // 并发去重：同时飞出去几个 401，只允许刷一次

function refreshAccessToken() {
  const rt = localStorage.getItem('kid_refresh_token')
  if (!rt) return Promise.reject(new Error('no refresh token'))
  // 用裸 axios：走 http 实例的话，refresh 自己返回 401 又会触发本拦截器，
  // 变成自己咬自己。
  return axios
    .post(`${BASE}/auth/refresh`, { refreshToken: rt }, { timeout: 20000 })
    .then((res) => {
      const body = res.data
      if (!body || body.code !== 0 || !body.data || !body.data.token) {
        throw new Error((body && body.message) || 'refresh failed')
      }
      localStorage.setItem('kid_token', body.data.token)
      if (body.data.refreshToken) {
        localStorage.setItem('kid_refresh_token', body.data.refreshToken)
      }
      return body.data.token
    })
}

// 请求拦截：注入 token
http.interceptors.request.use((config) => {
  const token = localStorage.getItem('kid_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 401 统一出口：先续期重放，续不动才登出。
// 后端鉴权失败走的是 `HTTP 200 + body.code=401`（util.Fail），
// 但反向代理/网关也可能直接回 HTTP 401，两条路都得兜住。
async function recover401(config) {
  const cfg = config || {}
  if (localStorage.getItem('kid_refresh_token') && !cfg.__retried) {
    try {
      if (!refreshing) {
        refreshing = refreshAccessToken().finally(() => {
          refreshing = null
        })
      }
      const fresh = await refreshing
      cfg.__retried = true
      cfg.headers = cfg.headers || {}
      cfg.headers.Authorization = `Bearer ${fresh}`
      return http.request(cfg)
    } catch (e) {
      // 续期也失败（refreshToken 过期 / 账号被停用）→ 落到下面的登出
    }
  }
  localStorage.removeItem('kid_token')
  localStorage.removeItem('kid_refresh_token')
  if (router.currentRoute.value.path !== '/login') {
    router.replace('/login')
  }
  return null
}

// 响应拦截：解包统一响应格式 {code, message, data}
http.interceptors.response.use(
  async (response) => {
    const data = response.data
    if (data && typeof data.code !== 'undefined') {
      if (data.code === 0) return data.data
      if (data.code === 401) {
        const replay = await recover401(response.config)
        if (replay) return replay
        return Promise.reject(new Error(data.message || '登录已失效'))
      }
      // 孩子端用轻提示而非弹窗，避免打断
      showToast(data.message || '操作失败')
      return Promise.reject(new Error(data.message))
    }
    return data
  },
  async (error) => {
    // HTTP 401（网关直接拦下、没走到业务层）
    if (error && error.response && error.response.status === 401) {
      const replay = await recover401(error.config)
      if (replay) return replay
      return Promise.reject(error)
    }
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

export const digestApi = {
  // 小书童对这个目录的「知识沉淀」。
  // 没有沉淀时后端返回 data:null（不是错误）—— 前端据此不显示卡片即可。
  forCategory: (categoryId) => http.get(`/app/digests/${categoryId}`),
  // 可见目录下的沉淀列表（首页挑一条展示用）
  list: (params) => http.get('/app/digests', { params }),
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
