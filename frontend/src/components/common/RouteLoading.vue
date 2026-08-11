<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { getPageTitle } from '@/router/pageMeta'

const route = useRoute()
const pageTitle = computed(() => getPageTitle(route.meta))
</script>

<template>
  <section
    class="route-loading"
    data-testid="route-loading"
    role="status"
    aria-live="polite"
    aria-busy="true"
  >
    <header class="route-loading-header">
      <h1 class="route-loading-title">{{ pageTitle }}</h1>
      <span class="route-loading-status"><span class="route-loading-spinner" />加载中…</span>
    </header>
    <div class="route-loading-skeleton" aria-hidden="true">
      <span v-for="index in 5" :key="index" class="route-loading-skeleton-row" />
    </div>
  </section>
</template>

<style scoped>
.route-loading {
  min-height: 360px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #fff;
}

.route-loading-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 24px;
  border-bottom: 1px solid #ebeef5;
}

.route-loading-title {
  margin: 0;
  color: #303133;
  font-size: 20px;
  font-weight: 600;
}

.route-loading-status {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #909399;
  font-size: 13px;
}

.route-loading-spinner {
  width: 15px;
  height: 15px;
  border: 2px solid #c6e2ff;
  border-top-color: #409eff;
  border-radius: 50%;
  animation: route-loading-spin 0.75s linear infinite;
}

.route-loading-skeleton {
  display: grid;
  gap: 16px;
  padding: 24px;
}

.route-loading-skeleton-row {
  display: block;
  height: 42px;
  border-radius: 4px;
  background: linear-gradient(90deg, #f2f6fc 20%, #f7f9fc 40%, #f2f6fc 60%);
  background-size: 200% 100%;
  animation: route-loading-shimmer 1.5s ease-in-out infinite;
}

@keyframes route-loading-spin {
  to {
    transform: rotate(360deg);
  }
}

@keyframes route-loading-shimmer {
  to {
    background-position: -200% 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .route-loading-spinner,
  .route-loading-skeleton-row {
    animation: none;
  }
}

@media (max-width: 768px) {
  .route-loading-header {
    justify-content: center;
  }

  .route-loading-title {
    display: none;
  }
}
</style>
