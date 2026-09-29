<script setup lang="ts">
import { computed } from 'vue'
import type { BrowseSortOptions, BrowseSortValue, NetFileSortBy } from '@/api/files'

const props = defineProps<{
  options: BrowseSortOptions
  modelValue: BrowseSortValue
  disabled?: boolean
}>()
const emit = defineEmits<{ change: [value: BrowseSortValue] }>()
const labels: Record<NetFileSortBy, string> = {
  default: '跟随网盘',
  name: '名称',
  time: '修改时间',
  size: '大小',
  type: '类型',
}
const directionLabels = computed(() => {
  if (props.modelValue.sort_by === 'time') return ['从旧到新', '从新到旧']
  if (props.modelValue.sort_by === 'size') return ['从小到大', '从大到小']
  return ['升序', '降序']
})
function changeField(field: NetFileSortBy) {
  emit('change', {
    sort_by: field,
    sort_order: field === 'time' || field === 'size' ? 'desc' : 'asc',
    ...(props.options.folders_first && field !== 'default'
      ? { folders_first: props.modelValue.folders_first ?? props.options.default.folders_first }
      : {}),
  })
}
</script>

<template>
  <div v-if="options.fields.length > 1" class="browse-sort-control" aria-label="列表排序">
    <el-select
      :model-value="modelValue.sort_by"
      class="browse-sort-field"
      size="small"
      aria-label="排序字段"
      :disabled="disabled"
      @change="changeField"
    >
      <el-option
        v-for="field in options.fields"
        :key="field"
        :value="field"
        :label="labels[field]"
      />
    </el-select>
    <el-select
      v-if="modelValue.sort_by !== 'default'"
      :model-value="modelValue.sort_order"
      class="browse-sort-order"
      size="small"
      aria-label="排序方向"
      :disabled="disabled"
      @change="emit('change', { ...modelValue, sort_order: $event })"
    >
      <el-option value="asc" :label="directionLabels[0]" />
      <el-option value="desc" :label="directionLabels[1]" />
    </el-select>
    <el-checkbox
      v-if="options.folders_first && modelValue.sort_by !== 'default'"
      :model-value="modelValue.folders_first"
      :disabled="disabled"
      size="small"
      title="根目录系统文件夹可能优先显示"
      @change="emit('change', { ...modelValue, folders_first: !!$event })"
      >文件夹置顶</el-checkbox
    >
    <el-button
      size="small"
      text
      :disabled="disabled"
      @click="emit('change', { ...options.default })"
    >
      恢复默认
    </el-button>
  </div>
  <span v-else class="browse-sort-follow">跟随服务端顺序</span>
</template>

<style scoped>
.browse-sort-control {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.browse-sort-field,
.browse-sort-order {
  width: 100px;
}
.browse-sort-follow {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}

@media (max-width: 768px) {
  .browse-sort-control {
    gap: 6px;
  }

  .browse-sort-field,
  .browse-sort-order {
    width: 88px;
  }
}
</style>
