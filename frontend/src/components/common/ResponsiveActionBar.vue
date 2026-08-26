<script setup lang="ts">
withDefaults(
  defineProps<{
    isMobile?: boolean
  }>(),
  {
    isMobile: false,
  },
)
</script>

<template>
  <div class="responsive-action-bar" :class="{ 'responsive-action-bar--mobile': isMobile }">
    <div class="responsive-action-bar__actions">
      <slot name="actions" />
    </div>
    <div class="responsive-action-bar__help">
      <slot name="help" />
    </div>
  </div>
</template>

<style scoped>
.responsive-action-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  padding: 20px;
  background: white;
  border-radius: 12px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
  align-items: center;
}

.responsive-action-bar__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
}

.responsive-action-bar__actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.responsive-action-bar__help {
  flex: 1;
  min-width: 200px;
}

.responsive-action-bar__help :deep(p) {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}

.responsive-action-bar--mobile {
  flex-direction: column;
  align-items: stretch;
  padding: 16px;
  gap: 12px;
}

.responsive-action-bar--mobile .responsive-action-bar__actions {
  flex-direction: column;
  gap: 12px;
}

.responsive-action-bar--mobile .responsive-action-bar__actions :deep(.el-button) {
  width: 100%;
  min-width: 0;
  justify-content: center;
}

.responsive-action-bar--mobile .responsive-action-bar__help {
  min-width: 0;
}
</style>
