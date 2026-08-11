<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { getPageTitle } from '@/router/pageMeta'

const { error } = defineProps<{ error: Error }>()
const route = useRoute()
const pageTitle = computed(() => getPageTitle(route.meta))
const reload = () => window.location.reload()
</script>

<template>
  <section class="route-load-error" data-testid="route-load-error" role="alert">
    <h1 class="route-load-error-title">{{ pageTitle }}</h1>
    <p v-if="error" class="route-load-error-message">页面加载失败</p>
    <button class="route-load-error-retry" type="button" aria-label="重新加载页面" @click="reload">
      重新加载
    </button>
  </section>
</template>

<style scoped>
.route-load-error {
  display: grid;
  min-height: 300px;
  place-content: center;
  justify-items: center;
  gap: 12px;
  border: 1px solid #fde2e2;
  border-radius: 8px;
  background: #fef0f0;
}

.route-load-error-title {
  margin: 0;
  color: #303133;
  font-size: 20px;
}

.route-load-error-message {
  margin: 0;
  color: #f56c6c;
}

.route-load-error-retry {
  padding: 9px 14px;
  border: 0;
  border-radius: 4px;
  background: #409eff;
  color: #fff;
  cursor: pointer;
  font: inherit;
}

.route-load-error-retry:focus-visible {
  outline: 2px solid #409eff;
  outline-offset: 2px;
}

@media (max-width: 768px) {
  .route-load-error-title {
    display: none;
  }
}
</style>
