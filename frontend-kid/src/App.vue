<template>
  <router-view v-slot="{ Component }">
    <transition name="fade">
      <component :is="Component" />
    </transition>
  </router-view>

  <!-- 底部导航：全屏页（如播放页）隐藏 -->
  <TabBar v-if="showTab && isLoggedIn" />
</template>

<script setup>
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import TabBar from '@/components/TabBar.vue'
import { useUserStore } from '@/stores/user'
import { autoClean as offlineAutoClean } from '@/utils/offline'

const route = useRoute()
const userStore = useUserStore()

// 登录页与播放页不显示 tab
const showTab = computed(
  () => !route.meta.fullscreen && route.name !== 'login'
)
const isLoggedIn = computed(() => userStore.isLoggedIn)

// 每次打开 App（页面加载）清一次本地缓存。
//
// 放在这里而不是「我的缓存」页：后者要家长主动点进去，而缓存撑爆手机
// 恰恰是"家长从来不看那个页面"造成的。启动时静默跑，清掉了也不打扰 ——
// 具体策略见 utils/offline.js 的 AUTO_CLEAN。
function runAutoClean() {
  offlineAutoClean()
    .then((r) => {
      if (r && r.removed > 0) {
        console.info(`[offline] 自动清理 ${r.removed} 条，释放 ${r.freed} 字节`)
      }
    })
    .catch(() => {})
}

onMounted(() => {
  // 已登录则预热用户信息与目录树
  if (userStore.isLoggedIn) {
    userStore.loadProfile().catch(() => {})
    userStore.loadTree().catch(() => {})
  }
  // 缓存清理与登录无关，任何人打开都跑
  runAutoClean()
})
</script>
