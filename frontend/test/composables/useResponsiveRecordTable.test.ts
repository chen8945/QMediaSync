import { ref } from 'vue'
import { describe, expect, it } from 'vitest'

import { useResponsiveRecordTable } from '@/composables/useResponsiveRecordTable'
import type { RecordColumn } from '@/types/recordTable'

describe('useResponsiveRecordTable', () => {
  it('完整详情模式包含当前已显示的列', () => {
    const columns = ref<RecordColumn<{ id: number }>[]>([
      {
        key: 'id',
        label: 'ID',
        priority: 'primary',
        detailField: { key: 'id', label: 'ID', value: (row) => row.id },
      },
      {
        key: 'reason',
        label: '原因',
        priority: 'detail',
        detailField: { key: 'reason', label: '原因', value: () => '手动备份' },
      },
    ])

    const { detailFields } = useResponsiveRecordTable({
      columns,
      density: ref('compact'),
      isMobile: ref(false),
      showAllDetails: ref(true),
    })

    expect(detailFields.value.map((field) => field.key)).toEqual(['id', 'reason'])
  })
})
