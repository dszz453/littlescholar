import fs from 'node:fs'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { VantResolver } from '@vant/auto-import-resolver'
import { VitePWA } from 'vite-plugin-pwa'
import { fileURLToPath, URL } from 'node:url'

// pdf.js 的 CMap（169 个 .bcmap，中日韩字体必需）和标准字体（16 个 .pfb）
// 是**运行时按需拉**的独立文件：pdf.js 只把 cMapUrl 当目录前缀去拼文件名，
// 没法靠 import 打包成 chunk。所以构建前从 pdfjs-dist 拷进 public/，由 vite 原样搬到 dist。
//
// 不进版本库（1.5MB / 185 个文件，纯噪音），每次构建现拷一份。
function pdfjsStaticAssets() {
  return {
    name: 'copy-pdfjs-static-assets',
    buildStart() {
      for (const name of ['cmaps', 'standard_fonts']) {
        const src = fileURLToPath(new URL(`./node_modules/pdfjs-dist/${name}`, import.meta.url))
        const dst = fileURLToPath(new URL(`./public/${name}`, import.meta.url))
        if (!fs.existsSync(src)) continue
        fs.mkdirSync(dst, { recursive: true })
        fs.cpSync(src, dst, { recursive: true })
      }
    },
  }
}

export default defineConfig({
  plugins: [
    pdfjsStaticAssets(),
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
        // ⚠️ index.html 不进预缓存。
        //
        // 原来它在预缓存里、navigateFallback 也指向它，于是**所有导航请求都离线优先
        // 从缓存喂** —— 服务端发了新版，App 里可能一连几天都跑着旧的那份 JS。
        // 孩子端装成 APK 后常年不关，正好卡死在这个模式：新代码永远下不去，
        // 只有手动清应用数据才能破。改由下面 runtimeCaching 的 NetworkFirst 接管导航。
        globIgnores: ['index.html'],
        navigateFallback: null,
        runtimeCaching: [
          {
            // 页面导航：网络优先。学习平板基本都在线，这条保证每次打开 App
            // 都拿到最新页面；真断网时再退回缓存。
            urlPattern: ({ request }) => request.mode === 'navigate',
            handler: 'NetworkFirst',
            options: {
              cacheName: 'kid-pages',
              networkTimeoutSeconds: 6,
              expiration: { maxEntries: 8, maxAgeSeconds: 7 * 24 * 3600 },
              cacheableResponse: { statuses: [0, 200] },
            },
          },
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
