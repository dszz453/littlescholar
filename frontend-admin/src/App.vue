<template>
  <!--
    用 ElConfigProvider 提供中文语言包。
    原来这一步是 app.use(ElementPlus, { locale: zhCn }) 做的，
    但那会全量注册整个组件库（主包 +1MB）。ConfigProvider 是官方推荐的
    「只配语言/尺寸、不引入组件」的做法，效果一致而体积为零。
    少了它，el-date-picker / el-pagination 等会显示英文。

    注意：<el-config-provider> 由 unplugin-vue-components 自动解析，
    不需要（也不应该）在这里手动 import。
  -->
  <el-config-provider :locale="zhCn">
    <router-view />
  </el-config-provider>
</template>

<script setup>
// 从 es 目录直取语言包，不要从 'element-plus' 主入口取
// （主入口会拖入 index.full，正是我们要避开的）。
import zhCn from 'element-plus/es/locale/lang/zh-cn'
</script>
