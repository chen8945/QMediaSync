<script setup lang="ts">
import type { QueueTaskDetailColumns, QueueTaskDetailGroup } from '@/utils/queueTaskDetailUtils'

const props = withDefaults(
  defineProps<{
    groups: QueueTaskDetailGroup[]
    maxColumns: 2 | 5
  }>(),
  {
    maxColumns: 5,
  },
)

const getGroupColumns = (group: QueueTaskDetailGroup): QueueTaskDetailColumns =>
  Math.min(group.columns, props.maxColumns) as QueueTaskDetailColumns

const getFieldSpan = (group: QueueTaskDetailGroup, fieldIndex: number): QueueTaskDetailColumns => {
  const columns = getGroupColumns(group)
  let occupiedColumns = 0

  for (let index = 0; index <= fieldIndex; index += 1) {
    const field = group.fields[index]
    if (field.fullWidth) {
      if (index === fieldIndex) {
        return columns
      }
      occupiedColumns = 0
      continue
    }

    const fillsRowBeforeFullWidthField = group.fields[index + 1]?.fullWidth
    const span = fillsRowBeforeFullWidthField ? columns - occupiedColumns : 1
    if (index === fieldIndex) {
      return span as QueueTaskDetailColumns
    }
    occupiedColumns = (occupiedColumns + span) % columns
  }

  return 1
}
</script>

<template>
  <div class="queue-task-details">
    <section v-for="group in groups" :key="group.key" class="queue-task-detail-group">
      <h3 class="queue-task-detail-title">{{ group.label }}</h3>
      <el-descriptions
        class="queue-task-detail-descriptions"
        :column="getGroupColumns(group)"
        border
        direction="vertical"
        size="small"
      >
        <el-descriptions-item
          v-for="(field, fieldIndex) in group.fields"
          :key="field.key"
          :label="field.label"
          :span="getFieldSpan(group, fieldIndex)"
        >
          <el-tag v-if="field.tagType" :type="field.tagType" size="small">
            {{ field.value }}
          </el-tag>
          <span v-else class="queue-task-detail-value">{{ field.value }}</span>
        </el-descriptions-item>
      </el-descriptions>
    </section>
  </div>
</template>

<style scoped>
.queue-task-details {
  display: grid;
  gap: 14px;
  padding: 4px 0;
}

.queue-task-detail-group {
  min-width: 0;
}

.queue-task-detail-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-color-primary);
}

.queue-task-detail-descriptions {
  overflow: hidden;
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  background-color: var(--el-bg-color);
}

.queue-task-detail-descriptions :deep(.el-descriptions__table) {
  width: calc(100% + 2px);
  margin: -1px;
  table-layout: fixed;
}

.queue-task-detail-descriptions :deep(.el-descriptions__cell) {
  border-color: var(--el-border-color-light) !important;
}

.queue-task-detail-descriptions :deep(.el-descriptions__label.el-descriptions__cell) {
  padding: 8px 10px 2px !important;
  border-bottom: 0 !important;
  background-color: var(--el-bg-color) !important;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  font-weight: 400;
}

.queue-task-detail-descriptions :deep(.el-descriptions__content.el-descriptions__cell) {
  padding: 0 10px 9px !important;
  border-top: 0 !important;
  background-color: var(--el-bg-color) !important;
  color: var(--el-text-color-primary);
  font-size: 13px;
  font-weight: 500;
}

.queue-task-detail-value {
  display: block;
  min-width: 0;
  font-variant-numeric: tabular-nums;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (max-width: 768px) {
  .queue-task-details {
    gap: 10px;
  }

  .queue-task-detail-descriptions :deep(.el-descriptions__label.el-descriptions__cell) {
    padding: 7px 8px 2px !important;
  }

  .queue-task-detail-descriptions :deep(.el-descriptions__content.el-descriptions__cell) {
    padding: 0 8px 8px !important;
  }
}
</style>
