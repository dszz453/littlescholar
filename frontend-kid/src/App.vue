<template>
  <router-view v-slot="{ Component }">
    <transition name="fade" mode="out-in">
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

const route = useRoute()
const userStore = useUserStore()

// 登录页与播放页不显示 tab
const showTab = computed(
  () => !route.meta.fullscreen && route.name !== 'login'
)
const isLoggedIn = computed(() => userStore.isLoggedIn)

onMounted(() => {
  // 已登录则预热用户信息与目录树
  if (userStore.isLoggedIn) {
    userStore.loadProfile().catch(() => {})
    userStore.loadTree().catch(() => {})
  }
})
</script>
