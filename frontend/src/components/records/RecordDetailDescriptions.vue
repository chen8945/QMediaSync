<script setup lang="ts" generic="TRow">
import type { RecordDetailField } from '@/types/recordTable'

const props = withDefaults(
  defineProps<{
    row: TRow
    fields: RecordDetailField<TRow>[]
    columns?: 1 | 2 | 3
  }>(),
  { columns: 2 },
)

const getValue = (field: RecordDetailField<TRow>) => {
  const value = field.value(props.row)
  return value === null || value === undefined || value === '' ? '-' : String(value)
}
</script>

<template>
  <el-descriptions
    :class="['record-detail', { 'record-detail--three-columns': columns === 3 }]"
    :column="columns"
    border
    size="small"
  >
    <el-descriptions-item
      v-for="field in fields"
      :key="field.key"
      :label="field.label"
      :span="columns === 1 ? 1 : (field.span ?? 1)"
    >
      <span :class="['record-detail__value', { 'record-detail__value--long': field.isLongText }]">
        {{ getValue(field) }}
      </span>
    </el-descriptions-item>
  </el-descriptions>
</template>

<style scoped>
.record-detail__value {
  font-variant-numeric: tabular-nums;
}

.record-detail__value--long {
  overflow-wrap: anywhere;
}

.record-detail--three-columns :deep(.el-descriptions__table) {
  table-layout: fixed;
}
</style>
