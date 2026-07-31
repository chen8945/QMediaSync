import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import type { RecordColumn, RecordDetailField, RecordTableDensity } from '@/types/recordTable'

interface UseResponsiveRecordTableOptions<TRow> {
  columns: MaybeRefOrGetter<RecordColumn<TRow>[]>
  density: MaybeRefOrGetter<RecordTableDensity>
  isMobile: MaybeRefOrGetter<boolean>
  showAllDetails?: MaybeRefOrGetter<boolean>
}

export function useResponsiveRecordTable<TRow>(options: UseResponsiveRecordTableOptions<TRow>) {
  const visibleColumns = computed(() => {
    const columns = toValue(options.columns)
    if (toValue(options.isMobile)) {
      return columns.filter((column) => column.priority === 'primary')
    }
    return columns.filter((column) => column.priority !== 'detail')
  })

  const detailFields = computed<RecordDetailField<TRow>[]>(() => {
    const showAll = toValue(options.showAllDetails)
    const visibleKeys = new Set(visibleColumns.value.map((column) => column.key))
    // 展开详情默认只补表格里看不到的列；showAllDetails 时把所有带 detailField 的列都列出来
    return toValue(options.columns)
      .filter(
        (column) =>
          column.detailField &&
          (showAll || !visibleKeys.has(column.key) || column.priority === 'detail'),
      )
      .map((column) => column.detailField as RecordDetailField<TRow>)
  })

  const rowHeightClass = computed(() =>
    toValue(options.density) === 'compact' ? 'record-table--compact' : 'record-table--comfortable',
  )

  return {
    visibleColumns,
    detailFields,
    rowHeightClass,
  }
}
