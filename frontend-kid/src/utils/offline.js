// 离线缓存（下载到本机）。
//
// # 为什么放在孩子端前端而不是各自的 App 里
//
// 标准版和小书童PRO 共用**同一份孩子端前端**（PRO 只是把它原样嵌进 WebView，
// 另外把播放地址换成自己的加速流）。所以缓存能力做在这一层，两个 App
// 就都有了，而且行为完全一致 —— 不需要在两个壳里各写一套下载逻辑，
// 也就不会出现"标准版能缓存、PRO 不行"这种漂移。
//
// # 为什么不下载原网盘直链
//
// 网络盘的直链都要求带 Cookie/Referer/UA，浏览器 fetch 带不上；
// 而且夸克那类链接还有跨域限制。我们下的是**后端代理地址**
// （`/api/stream/:id?t=…`，或 PRO 的 `/edge/stream/:ticket`）——
// 同源、无需额外头、还顺带享受服务端的多连接加速。
//
// # 存哪
//
// IndexedDB 而不是 Cache Storage：需要按资源 id 精确查询、算占用、
// 逐条删除，Cache Storage 那套按请求 URL 来的模型对不上。
// Blob 单独一个 store，列表页查询时不把几十 MB 的数据读出来。

const DB_NAME = 'littlescholar-offline'
const DB_VERSION = 1
const STORE_META = 'items'
const STORE_BLOB = 'blobs'

let dbPromise = null

function openDB() {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('当前环境不支持离线缓存'))
      return
    }
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE_META)) {
        db.createObjectStore(STORE_META, { keyPath: 'id' })
      }
      if (!db.objectStoreNames.contains(STORE_BLOB)) {
        db.createObjectStore(STORE_BLOB, { keyPath: 'id' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error || new Error('打开离线库失败'))
  })
  return dbPromise
}

function tx(db, stores, mode) {
  const t = db.transaction(stores, mode)
  return {
    t,
    store: (name) => t.objectStore(name),
    done: new Promise((resolve, reject) => {
      t.oncomplete = () => resolve()
      t.onerror = () => reject(t.error)
      t.onabort = () => reject(t.error || new Error('事务已回滚'))
    }),
  }
}

function wrap(req) {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

/** 当前环境是否支持离线缓存（WebView 里都有，桌面浏览器无痕模式会没有）。 */
export async function offlineSupported() {
  try {
    await openDB()
    return true
  } catch (e) {
    return false
  }
}

/** 是否已经缓存过这个资源。 */
export async function isCached(id) {
  const db = await openDB()
  const { store, done } = tx(db, [STORE_META], 'readonly')
  const row = await wrap(store(STORE_META).get(String(id)))
  await done
  return !!row
}

/** 取一条元信息（不含数据）。 */
export async function getMeta(id) {
  const db = await openDB()
  const { store, done } = tx(db, [STORE_META], 'readonly')
  const row = await wrap(store(STORE_META).get(String(id)))
  await done
  return row || null
}

/** 列出所有已缓存的条目（按缓存时间倒序）。 */
export async function listCached() {
  const db = await openDB()
  const { store, done } = tx(db, [STORE_META], 'readonly')
  const rows = (await wrap(store(STORE_META).getAll())) || []
  await done
  rows.sort((a, b) => (b.savedAt || 0) - (a.savedAt || 0))
  return rows
}

/** 总占用字节数。 */
export async function totalBytes() {
  const rows = await listCached()
  return rows.reduce((n, r) => n + (Number(r.size) || 0), 0)
}

/** 删除一条。 */
export async function removeCache(id) {
  const db = await openDB()
  const { store, done } = tx(db, [STORE_META, STORE_BLOB], 'readwrite')
  store(STORE_META).delete(String(id))
  store(STORE_BLOB).delete(String(id))
  await done
  revokeUrl(id)
}

/** 清空全部缓存。 */
export async function clearAll() {
  const db = await openDB()
  const { store, done } = tx(db, [STORE_META, STORE_BLOB], 'readwrite')
  store(STORE_META).clear()
  store(STORE_BLOB).clear()
  await done
  for (const id of Object.keys(urlCache)) revokeUrl(id)
}

// ---------- 取回 Blob 与对象 URL ----------

const urlCache = Object.create(null)

function revokeUrl(id) {
  const u = urlCache[id]
  if (u) {
    try {
      URL.revokeObjectURL(u)
    } catch (e) {
      /* 忽略：某些 WebView 在页面销毁后调用会抛 */
    }
    delete urlCache[id]
  }
}

/**
 * 取已缓存文件的临时地址（blob:）供 <video>/<img>/pdf.js 使用。
 *
 * 同一个资源在页面生命周期内复用同一个 objectURL —— 每次重建会让
 * 播放器认为"换了源"，拖动进度、续播位置全丢。
 */
export async function cachedURL(id) {
  const key = String(id)
  if (urlCache[key]) return urlCache[key]
  const db = await openDB()
  const { store, done } = tx(db, [STORE_BLOB], 'readonly')
  const row = await wrap(store(STORE_BLOB).get(key))
  await done
  if (!row || !row.data) return ''
  const u = URL.createObjectURL(row.data)
  urlCache[key] = u
  return u
}

/** 释放页面级的 objectURL（离开播放页时调用，不影响已下载的数据）。 */
export function releaseUrls() {
  for (const id of Object.keys(urlCache)) revokeUrl(id)
}

// ---------- 下载 ----------

/**
 * 把一个播放地址下到本地。
 *
 * @param {number|string} id     资源 id
 * @param {string} url           播放地址（同源代理地址）
 * @param {object} meta          标题/类型/封面等，列表页要显示
 * @param {(p:{loaded:number,total:number,percent:number})=>void} onProgress
 * @param {AbortSignal} signal   用于用户取消
 */
export async function download(id, url, meta, onProgress, signal) {
  if (!url) throw new Error('没有可下载的地址')
  // 每次都带最新 token：缓存是长时间操作，中途可能正好跨过令牌续期。
  const token = localStorage.getItem('kid_token')
  const headers = {}
  if (token) headers.Authorization = `Bearer ${token}`

  const resp = await fetch(url, { headers, signal, credentials: 'same-origin' })
  if (!resp.ok) {
    throw new Error(resp.status === 401 ? '登录已失效，请重新登录后再下载' : `下载失败（HTTP ${resp.status}）`)
  }

  const total = Number(resp.headers.get('Content-Length')) || 0
  const type = resp.headers.get('Content-Type') || 'application/octet-stream'

  let blob
  if (resp.body && typeof resp.body.getReader === 'function') {
    const reader = resp.body.getReader()
    const chunks = []
    let loaded = 0
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      chunks.push(value)
      loaded += value.byteLength
      if (onProgress) {
        onProgress({
          loaded,
          total,
          percent: total > 0 ? Math.min(99, Math.round((loaded / total) * 100)) : 0,
        })
      }
    }
    blob = new Blob(chunks, { type })
  } else {
    // 老 WebView 没有流式 body：退回一次性 arrayBuffer（没有进度）。
    blob = await resp.blob()
  }

  const db = await openDB()
  const { store, done } = tx(db, [STORE_META, STORE_BLOB], 'readwrite')
  store(STORE_BLOB).put({ id: String(id), data: blob })
  store(STORE_META).put({
    id: String(id),
    title: (meta && meta.title) || '未命名',
    mediaType: (meta && meta.mediaType) || '',
    cover: (meta && meta.cover) || '',
    categoryName: (meta && meta.categoryName) || '',
    duration: Number(meta && meta.duration) || 0,
    mime: type,
    size: blob.size,
    savedAt: Date.now(),
  })
  await done
  revokeUrl(id) // 数据换了，旧的 objectURL 必须作废
  if (onProgress) onProgress({ loaded: blob.size, total: blob.size, percent: 100 })
  return { size: blob.size, mime: type }
}

/** 把字节数说成人话。 */
export function fmtSize(bytes) {
  const n = Number(bytes) || 0
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}
