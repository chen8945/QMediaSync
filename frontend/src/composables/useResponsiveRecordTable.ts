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
    const columns = toValue(options.columns)
    if (options.showAllDetails && toValue(options.showAllDetails)) {
      return columns
        .filter((column) => column.detailField)
        .map((column) => column.detailField as RecordDetailField<TRow>)
    }

    const visibleKeys = new Set(visibleColumns.value.map((column) => column.key))
    return columns
      .filter(
        (column) =>
          column.detailField && (!visibleKeys.has(column.key) || column.priority === 'detail'),
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
