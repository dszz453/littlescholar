import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [
    vue(),
    AutoImport({
      imports: ['vue', 'vue-router', 'pinia'],
      resolvers: [ElementPlusResolver()],
    }),
    Components({
      // 保持 ElementPlusResolver 默认配置。
      //
      // 试过传 { importStyle: 'es' }，结果组件样式大面积丢失
      // （el-alert / el-tree / el-form / el-dialog 的 CSS 全没了，
      //   CSS 总量从 300+ KB 掉到 60 KB）—— 那个值不是它支持的写法。
      // 默认配置（importStyle: true → 走 style/css 入口）是正确的。
      //
      // 服务式组件（ElMessage / ElMessageBox）的样式模板扫描不到，
      // 在 src/main.js 里显式引入。
      resolvers: [ElementPlusResolver()],
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.VITE_PROXY_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
      '/media': {
        target: process.env.VITE_PROXY_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    // 原来是 1500 —— 恰好把 1,188 KB 的警告压掉了，等于把问题藏起来。
    // 改回 Vite 默认的 500，让体积异常重新可见。
    chunkSizeWarningLimit: 500,
  },
})
