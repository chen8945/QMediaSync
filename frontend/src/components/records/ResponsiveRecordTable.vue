<script setup lang="ts" generic="TRow">
import { computed } from 'vue'
import { useResponsiveRecordTable } from '@/composables/useResponsiveRecordTable'
import type {
  RecordAction,
  RecordActionPayload,
  RecordColumn,
  RecordRowKey,
  RecordTableDensity,
} from '@/types/recordTable'
import RecordActionButtons from './RecordActionButtons.vue'
import RecordDetailDescriptions from './RecordDetailDescriptions.vue'

const props = withDefaults(
  defineProps<{
    rows: TRow[]
    columns: RecordColumn<TRow>[]
    actions?: RecordAction<TRow>[]
    rowKey: (row: TRow) => RecordRowKey
    loading?: boolean
    isMobile?: boolean
    density?: RecordTableDensity
    emptyText?: string
    height?: string
    expandedRowKeys?: RecordRowKey[]
    showSelection?: boolean
    selectable?: (row: TRow) => boolean
    showAllDetails?: boolean
    detailColumns?: 2 | 3
  }>(),
  {
    actions: () => [],
    loading: false,
    isMobile: false,
    density: 'compact',
    emptyText: '暂无记录',
    height: undefined,
    expandedRowKeys: () => [],
    showSelection: false,
    selectable: undefined,
    showAllDetails: false,
    detailColumns: 2,
  },
)

const emit = defineEmits<{
  action: [payload: RecordActionPayload<TRow>]
  selectionChange: [rows: TRow[]]
  expandChange: [row: TRow, expandedRows: TRow[]]
}>()

const { visibleColumns, detailFields, rowHeightClass } = useResponsiveRecordTable({
  columns: computed(() => props.columns),
  density: computed(() => props.density),
  isMobile: computed(() => props.isMobile),
  showAllDetails: computed(() => props.showAllDetails),
})

const hasDetails = computed(() => detailFields.value.length > 0)
const getCellValue = (row: TRow, key: string) => (row as Record<string, unknown>)[key] ?? '-'
const handleExpandChange = (row: TRow, expandedRows: TRow[]) => {
  emit('expandChange', row, expandedRows)
}
</script>

<template>
  <div :class="{ 'record-table-scroll': isMobile }">
    <el-table
      v-loading="loading"
      :data="rows"
      :row-key="rowKey"
      :expand-row-keys="expandedRowKeys"
      :height="height"
      :empty-text="emptyText"
      :class="['record-table', rowHeightClass]"
      :show-overflow-tooltip="true"
      :style="{ width: isMobile ? 'max-content' : '100%' }"
      stripe
      @selection-change="emit('selectionChange', $event)"
      @expand-change="handleExpandChange"
    >
      <el-table-column
        v-if="showSelection"
        type="selection"
        width="44"
        align="center"
        :selectable="selectable"
      />
      <el-table-column v-if="hasDetails" type="expand" width="42">
        <template #default="{ row }">
          <RecordDetailDescriptions
            :row="row"
            :fields="detailFields"
            :columns="isMobile ? 1 : detailColumns"
          />
        </template>
      </el-table-column>
      <el-table-column
        v-for="column in visibleColumns"
        :key="column.key"
        :label="column.label"
        :width="column.width"
        :min-width="column.minWidth"
        :align="column.align"
        :class-name="column.className"
        show-overflow-tooltip
      >
        <template #default="{ row }">
          <slot :name="`cell-${column.key}`" :row="row">
            {{ getCellValue(row, column.key) }}
          </slot>
        </template>
      </el-table-column>
      <el-table-column v-if="actions.length > 0" label="操作" width="132" align="center">
        <template #default="{ row }">
          <RecordActionButtons :row="row" :actions="actions" @action="emit('action', $event)" />
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.record-table-scroll {
  max-width: 100%;
  overflow-x: auto;
}

.record-table {
  min-width: 100%;
}

.record-table :deep(.cell) {
  min-width: 0;
}

.record-table--compact :deep(.el-table__cell) {
  padding: 6px 0;
}

.record-table--comfortable :deep(.el-table__cell) {
  padding: 10px 0;
}
</style>
