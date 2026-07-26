<script setup lang="ts">
import { CaretRight } from '@element-plus/icons-vue'

defineProps<{
  expanded: boolean
  label: string
  controls?: string
}>()

const emit = defineEmits<{
  toggle: []
}>()
</script>

<template>
  <el-button
    class="queue-task-expand-button"
    :class="{ 'is-expanded': expanded }"
    link
    type="primary"
    :icon="CaretRight"
    :aria-label="label"
    :title="label"
    :aria-expanded="expanded"
    :aria-controls="controls"
    @click.stop="emit('toggle')"
  />
</template>

<style scoped>
.queue-task-expand-button {
  width: 24px;
  min-height: 24px;
  padding: 0;
}

.queue-task-expand-button :deep(.el-icon) {
  font-size: 14px;
  transition: transform 0.15s ease;
}

.queue-task-expand-button.is-expanded :deep(.el-icon) {
  transform: rotate(90deg);
}

@media (prefers-reduced-motion: reduce) {
  .queue-task-expand-button :deep(.el-icon) {
    transition: none;
  }
}
</style>
