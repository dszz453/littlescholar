<template>
  <div class="page offline-page">
    <header class="head">
      <button class="back pressable" aria-label="返回" @click="goBack">‹</button>
      <h1 class="title">我的缓存</h1>
      <button
        v-if="items.length"
        class="clear pressable"
        @click="onClearAll"
      >清空</button>
    </header>

    <div class="page-pad">
      <p v-if="items.length" class="summary">
        共 {{ items.length }} 个 · 占用 {{ fmtSize(used) }}
        <span class="summary-tip">缓存的内容不占流量，随时能看</span>
        <span class="summary-tip auto-tip">
          自动清理：超过 {{ AUTO_CLEAN.maxAgeDays }} 天没看、或总共超过
          {{ fmtSize(AUTO_CLEAN.maxBytes) }} 时，会从最久没看的开始删
        </span>
      </p>

      <div v-if="loading" class="empty"><span class="emoji">⏳</span><p>正在读取…</p></div>

      <template v-else-if="items.length">
        <div v-for="it in items" :key="it.id" class="row pressable" @click="open(it)">
          <div class="thumb">
            <img v-if="it.cover" :src="it.cover" alt="" loading="lazy" />
            <span v-else class="thumb-emoji">{{ typeEmoji(it.mediaType) }}</span>
          </div>
          <div class="body">
            <div class="name">{{ it.title }}</div>
            <div class="sub">
              {{ fmtSize(it.size) }} · {{ fmtDate(it.savedAt) }}
            </div>
          </div>
          <button class="del pressable" aria-label="删除" @click.stop="onRemove(it)">✕</button>
        </div>
      </template>

      <div v-else class="empty">
        <span class="emoji">📥</span>
        <p>还没有缓存的内容</p>
        <p class="text-sub">在播放页点「下载」，看过一次也不用再等网</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { showConfirmDialog, showToast } from 'vant'
import {
  AUTO_CLEAN,
  autoClean,
  clearAll,
  listCached,
  offlineSupported,
  removeCache,
  fmtSize,
} from '@/utils/offline'

const router = useRouter()
const items = ref([])
const used = ref(0)
const loading = ref(true)

const TE = { video: '🎬', audio: '🎧', pdf: '📖', image: '🖼️', ebook: '📖' }
function typeEmoji(t) {
  return TE[t] || '📄'
}

function fmtDate(ts) {
  const d = new Date(Number(ts) || 0)
  if (!d.getTime()) return ''
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}月${d.getDate()}日 ${p(d.getHours())}:${p(d.getMinutes())}`
}

async function reload() {
  loading.value = true
  try {
    if (!(await offlineSupported())) {
      items.value = []
      return
    }
    // 进这个页面也清一遍：App 启动时清过，但用户可能在里面待了很久，
    // 或者刚下完几集把空间顶超了。清掉的条数告诉用户一声 ——
    // 悄悄消失比"删了但有提示"更让人困惑。
    const cleaned = await autoClean()
    if (cleaned && cleaned.removed > 0) {
      showToast(`已自动清理 ${cleaned.removed} 个较久没看的缓存`)
    }
    items.value = await listCached()
    used.value = items.value.reduce((n, r) => n + (Number(r.size) || 0), 0)
  } catch (e) {
    showToast(e?.message || '读取缓存失败')
  } finally {
    loading.value = false
  }
}

function open(it) {
  router.push({ name: 'play', params: { id: String(it.id) }, query: { offline: '1' } })
}

async function onRemove(it) {
  try {
    await showConfirmDialog({
      title: '删除缓存',
      message: `删除「${it.title}」的本地文件？下次看要重新下载。`,
      confirmButtonText: '删除',
    })
  } catch (e) {
    return // 用户取消
  }
  try {
    await removeCache(it.id)
    showToast('已删除')
    await reload()
  } catch (e) {
    showToast(e?.message || '删除失败')
  }
}

async function onClearAll() {
  try {
    await showConfirmDialog({
      title: '清空缓存',
      message: '会把所有已下载的内容从这台设备上删掉，确定吗？',
      confirmButtonText: '清空',
    })
  } catch (e) {
    return
  }
  try {
    await clearAll()
    showToast('已清空')
    await reload()
  } catch (e) {
    showToast(e?.message || '清空失败')
  }
}

function goBack() {
  if (window.history.length > 1) router.back()
  else router.push({ name: 'me' })
}

onMounted(reload)
</script>

<style scoped>
.offline-page {
  min-height: 100vh;
  background: #f7f8fb;
  padding-bottom: 40px;
}
.head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 14px 10px;
}
.back {
  width: 34px;
  height: 34px;
  border: none;
  border-radius: 50%;
  background: #fff;
  color: #333;
  font-size: 20px;
  line-height: 1;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.06);
}
.title {
  flex: 1;
  margin: 0;
  font-size: 18px;
  font-weight: 700;
  color: #222;
}
.clear {
  border: none;
  background: transparent;
  color: #ff6b6b;
  font-size: 14px;
  padding: 6px 4px;
}
.summary {
  margin: 2px 4px 12px;
  font-size: 13px;
  color: #8a8f99;
}
.summary-tip {
  display: block;
  margin-top: 2px;
  color: #b0b4bc;
  font-size: 12px;
}
.auto-tip {
  margin-top: 6px;
  line-height: 1.5;
}
.row {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #fff;
  border-radius: 16px;
  padding: 10px;
  margin-bottom: 10px;
  box-shadow: 0 2px 10px rgba(20, 30, 60, 0.05);
}
.thumb {
  width: 62px;
  height: 62px;
  border-radius: 12px;
  overflow: hidden;
  background: #eef1f7;
  display: flex;
  align-items: center;
  justify-content: center;
  flex: none;
}
.thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.thumb-emoji {
  font-size: 26px;
}
.body {
  flex: 1;
  min-width: 0;
}
.name {
  font-size: 15px;
  font-weight: 600;
  color: #26292e;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub {
  margin-top: 4px;
  font-size: 12px;
  color: #9aa0a8;
}
.del {
  flex: none;
  width: 30px;
  height: 30px;
  border: none;
  border-radius: 50%;
  background: #f2f4f8;
  color: #9aa0a8;
  font-size: 13px;
}
.empty {
  text-align: center;
  padding: 60px 20px;
  color: #8a8f99;
}
.empty .emoji {
  font-size: 40px;
  display: block;
  margin-bottom: 10px;
}
</style>
