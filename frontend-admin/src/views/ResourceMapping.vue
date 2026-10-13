<template>
  <div class="page-container">
    <div class="page-header">
      <h2 class="page-title">资源映射</h2>
      <span class="text-secondary">
        把接入源（网盘 / WebDAV / 本地硬盘）里的目录映射进系统目录
      </span>
    </div>

    <!-- 映射面板自带工具栏与弹窗，这里只负责页面外壳 -->
    <MappingPanel />
  </div>
</template>

<script setup>
import MappingPanel from '@/components/MappingPanel.vue'
</script>
