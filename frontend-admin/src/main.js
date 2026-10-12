import { createApp } from 'vue'
import { createPinia } from 'pinia'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

// ---------------------------------------------------------------------------
// 服务式组件的样式：必须手动引入。
//
// ElMessage / ElMessageBox 是「服务式组件」—— 靠 JS 调用（ElMessage.success()）
// 动态挂载，不出现在任何 <template> 里。unplugin-vue-components 只能给
// 模板里的组件注入样式，扫不到它们。
//
// 原来这些样式是靠 main.js 里 `import 'element-plus/dist/index.css'`
// 全量带进来的（那一下就是 360 KB CSS）。去掉全量注册后必须显式补回来，
// 否则弹窗会变成无样式的裸 DOM —— 这是真实踩过的回归。
//
// 这里用组件级样式入口（不是 theme-chalk/el-message.css 单文件），
// 因为它会自动带上依赖（base、badge 等）：
//   es/components/message/style/css.mjs
//     ├── import "../../base/style/css.mjs"
//     ├── import "../../badge/style/css.mjs"
//     └── import "element-plus/theme-chalk/el-message.css"
// ---------------------------------------------------------------------------
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'

import App from './App.vue'
import router from './router'
import './styles/main.css'

const app = createApp(App)

// 图标全量注册：图标是「按名字动态解析」的（<el-icon><Edit /></el-icon>），
// 无法被 unplugin-vue-components 静态分析，所以这里保持全量。
// 实测这部分体积很小（@element-plus/icons-vue 是纯 SVG 定义），
// 不值得为它引入动态解析方案。
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

app.use(createPinia())
app.use(router)

// ---------------------------------------------------------------------------
// 注意：这里**故意没有** app.use(ElementPlus)。
//
// 原来写的是 `import ElementPlus from 'element-plus'` + `app.use(ElementPlus)`，
// 那是**全量注册**——会把整个组件库（element-plus/dist/index.full.js，
// 未压缩 2.1 MB）打进主包。实测主包因此达到 1,187 KB，并塞进了
// date-picker、table-column、virtual-list 等大量用不到的组件代码。
//
// 组件由 unplugin-vue-components 的 ElementPlusResolver 按需解析（见 vite.config.js），
// ElMessage / ElMessageBox 在各文件里显式 import，
// 都不依赖 app.use 的全局注入。
//
// 全局配置（语言包）改用 <el-config-provider>，见 App.vue。
// 不要退回 app.use(ElementPlus)。
// ---------------------------------------------------------------------------

app.mount('#app')
