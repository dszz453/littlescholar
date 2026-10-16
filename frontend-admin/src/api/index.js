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
  // 从存储源（WebDAV 等）导入已有文件：不转存，直接登记账号 + 文件路径
  importFromDisk: (data) => http.post('/admin/resources/import-disk', data),
}

export const searchApi = {
  search: (data) => http.post('/admin/search', data),
  health: () => http.get('/admin/search/health'),
}

export const diskApi = {
  types: () => http.get('/admin/disk/types'),
  // 账号/接入源列表。
  //  - list('webdav')         按类型过滤（旧用法，保留兼容）
  //  - list({ group:'storage' }) 按分组过滤（网盘 / 存储源）
  list: (params) => {
    let query = {}
    if (typeof params === 'string') query = { diskType: params }
    else if (params) query = params
    return http.get('/admin/disk/accounts', { params: query })
  },
  getQR: (diskType) => http.post('/admin/disk/accounts/qr', { diskType }),
  pollQR: (sessionId) => http.get(`/admin/disk/accounts/qr/${sessionId}`),
  addManual: (data) => http.post('/admin/disk/accounts/manual', data),
  update: (id, data) => http.put(`/admin/disk/accounts/${id}`, data),
  validate: (id) => http.post(`/admin/disk/accounts/${id}/validate`),
  updateCookie: (id, cookie) => http.put(`/admin/disk/accounts/${id}/cookie`, { cookie }),
  setDefault: (id) => http.post(`/admin/disk/accounts/${id}/default`),
  remove: (id) => http.delete(`/admin/disk/accounts/${id}`),
  browse: (id, dirId = '') => http.get(`/admin/disk/accounts/${id}/browse`, { params: { dirId } }),
  mkdir: (id, parentId, name) => http.post(`/admin/disk/accounts/${id}/mkdir`, { parentId, name }),
  // 远端目录的重命名 / 删除（写操作直通网盘 / WebDAV / 本地硬盘）
  rename: (id, fileId, newName) => http.put(`/admin/disk/accounts/${id}/rename`, { fileId, newName }),
  deleteRemote: (id, fileIds) => http.post(`/admin/disk/accounts/${id}/delete`, { fileIds }),
}

// 资源映射：远端存储目录 → 系统目录，同步即批量登记资源
export const mappingApi = {
  // params 可选：{ accountId } 看"某个网盘挂了哪些目录"，
  // { categoryId } 看"某个系统目录挂了哪条映射"（目录管理页的「立即同步」要用）。
  // 都不传就是全部。
  list: (params) => http.get('/admin/mappings', { params: params || {} }),
  create: (data) => http.post('/admin/mappings', data),
  update: (id, data) => http.put(`/admin/mappings/${id}`, data),
  // 解绑映射。
  //
  // ⚠️ 参数含义在 2026-10-16 **反转**过一次：
  //   · 不传            → 连同这条映射导入的资源一起移除（默认，资源是软删除）
  //   · keepResources=true → 只解绑配置，资源留在原处（?withResources=0）
  //
  // 原来的语义正好相反（默认保留资源），理由是"删资源不可逆"。资源改成
  // 软删除之后那条理由不成立了：既然能恢复，就该默认做用户直觉上要的事 ——
  // 把资源一起拿掉。保留孤儿资源反而出过事故（删了映射孩子端还在列那 26 集）。
  remove: (id, keepResources) =>
    http.delete(`/admin/mappings/${id}`, {
      params: keepResources ? { withResources: 0 } : {},
    }),
  // 解绑**全部**映射（用户第 4 条）。
  //
  //   · dryRun: true        → 只统计不落库，给二次确认做预览
  //   · keepResources: true → 只解绑配置，资源留在原处（?withResources=0）
  //
  // 预览和真删走的是后端**同一套**清理规则（mappingPurge），所以预览报出来的
  // 数字就是真删会动的数字 —— 单测专门锁了这条，改后端时别让它们分叉。
  removeAll: (opts = {}) =>
    http.delete('/admin/mappings', {
      params: {
        ...(opts.keepResources ? { withResources: 0 } : {}),
        ...(opts.dryRun ? { dryRun: 1 } : {}),
      },
    }),
  sync: (id) => http.post(`/admin/mappings/${id}/sync`),
  syncAll: () => http.post('/admin/mappings/sync'),
}

// 设置共享：把后台网盘账号里的资源变成**分享链接 + 提取码**。
//
// 为什么这一步是必需的一环：资源是映射进后台那个网盘账号的，
// 而孩子端 PRO 用的是家长自己扫码接入的**另一个**账号 —— 两个账号之间
// 唯一的合法通道就是分享链接。没有它，孩子端拿到的资源详情里 shareUrl
// 永远是空的，只能一路退回小书童的原地址，"加速"形同不存在。
export const shareApi = {
  // opts: { resourceId } | { resourceIds: [] } | { mappingId }
  //       + expireDays（0/省略=默认 30 天，-1=永久）
  //       + passcode（留空自动生成）
  //       + force（连已共享过的一起重做）
  //       + dryRun（只预览，不打网盘接口、不落库）
  create: (opts = {}) =>
    http.post(
      '/admin/shares',
      {
        resourceId: opts.resourceId || 0,
        resourceIds: opts.resourceIds || [],
        mappingId: opts.mappingId || 0,
        expireDays: opts.expireDays,
        passcode: opts.passcode || '',
        force: !!opts.force,
      },
      { params: opts.dryRun ? { dryRun: 1 } : {} }
    ),
  // 校验已建的分享对孩子端是否**真的**可用。
  //
  // 回答的是整条链路里唯一"看着全对、其实全错"的问题：分享根目录里
  // 能不能按 **fileId** 找到这个资源。孩子端 PRO 就是按 fileId 挑文件的，
  // 对不上时它只会说"分享里没有找到指定的文件" —— 而后台看起来一切正常。
  verify: (opts = {}) =>
    http.post('/admin/shares/verify', {
      resourceId: opts.resourceId || 0,
      resourceIds: opts.resourceIds || [],
      mappingId: opts.mappingId || 0,
    }),
  // 清除库里的分享信息（不动网盘上那条分享本身）。
  clear: (opts = {}) =>
    http.delete('/admin/shares', {
      params: {
        ...(opts.resourceId ? { resourceId: opts.resourceId } : {}),
        ...(opts.mappingId ? { mappingId: opts.mappingId } : {}),
        // 后端按逗号分隔解析：数组走 axios 序列化会变成 ids[]=1&ids[]=2，
        // 那种形态那边收不到，清理会静默地一条都不动。
        ...(opts.resourceIds && opts.resourceIds.length
          ? { resourceIds: opts.resourceIds.join(',') }
          : {}),
      },
    }),
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

// 知识沉淀：小书童每天把目录读成人话
export const digestApi = {
  list: () => http.get('/admin/digests'),
  // 传 categoryId 只刷这一个（同步返回）；不传或传 0 是全量刷新（后台异步跑）
  refresh: (categoryId, force = false) =>
    http.post('/admin/digests/refresh', { categoryId, force }),
}

// AI 整理助手：先出方案（只读），人工确认后再执行
export const organizeApi = {
  plan: (data) => http.post('/admin/organize/plan', data),
  apply: (data) => http.post('/admin/organize/apply', data),
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
