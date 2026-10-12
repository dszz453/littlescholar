import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { VantResolver } from '@vant/auto-import-resolver'
import { VitePWA } from 'vite-plugin-pwa'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [
    vue(),
    AutoImport({
      imports: ['vue', 'vue-router', 'pinia'],
      resolvers: [VantResolver()],
      dts: false,
    }),
    Components({
      resolvers: [VantResolver()],
      dts: false,
    }),
    // PWA：支持"添加到主屏幕"、离线缓存
    VitePWA({
      registerType: 'autoUpdate',
      includeAssets: ['favicon.ico', 'apple-touch-icon.png', 'icons.svg'],
      manifest: {
        name: '小书童 · 学习乐园',
        short_name: '小书童',
        description: '给孩子的一个学习乐园：看视频、听故事、读绘本',
        lang: 'zh-CN',
        theme_color: '#5B8FF9',
        background_color: '#F7F9FC',
        display: 'standalone',
        display_override: ['standalone', 'minimal-ui'],
        orientation: 'portrait',
        start_url: '/',
        scope: '/',
        categories: ['education', 'kids'],
        icons: [
          { src: '/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/icon-512.png', sizes: '512x512', type: 'image/png' },
          {
            src: '/icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
        ],
        shortcuts: [
          { name: '找一找', short_name: '搜索', url: '/search' },
          { name: '问小书童', short_name: '小书童', url: '/tutor' },
        ],
      },
      workbox: {
        // 音频/视频走网络，不进预缓存（体积大且需要 Range 请求）
        globPatterns: ['**/*.{js,css,html,ico,png,svg,woff2}'],
        maximumFileSizeToCacheInBytes: 5 * 1024 * 1024,
        // 导航请求全部回退到 index.html（SPA history 模式）
        navigateFallback: '/index.html',
        // API 与媒体请求不交给 SW 处理，避免破坏 Range 与鉴权
        navigateFallbackDenylist: [/^\/api\//, /^\/media\//, /^\/transcode\//],
        runtimeCaching: [
          {
            // 网盘图片封面：走缓存优先，省流量
            urlPattern: /\.(?:png|jpg|jpeg|webp|gif|svg)$/,
            handler: 'CacheFirst',
            options: {
              cacheName: 'kid-images',
              expiration: { maxEntries: 300, maxAgeSeconds: 30 * 24 * 3600 },
              cacheableResponse: { statuses: [0, 200] },
            },
          },
        ],
      },
      devOptions: {
        // 开发环境默认关闭 SW，避免调试时被缓存干扰
        enabled: false,
      },
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '0.0.0.0',
    port: 5174,
    proxy: {
      // 开发环境代理到 Go 后端
      '/api': {
        target: process.env.VITE_PROXY_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
      '/media': {
        target: process.env.VITE_PROXY_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
      '/transcode': {
        target: process.env.VITE_PROXY_TARGET || 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    chunkSizeWarningLimit: 1500,
  },
})
