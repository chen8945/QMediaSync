import { describe, expect, it } from 'vitest'

import { isUpdateRunningStatus, isUpdateTerminalStatus } from '@/composables/useUpdate'

const terminalStatuses = ['completed', 'failed', 'cancelled'] as const

describe('useUpdate 进度状态分类', () => {
  it.each(terminalStatuses)('把 %s 识别为终态', (status) => {
    expect(isUpdateTerminalStatus(status)).toBe(true)
  })

  it.each(['downloading', 'install'])('把 %s 识别为进行态', (status) => {
    expect(isUpdateRunningStatus(status)).toBe(true)
  })

  it('终态不属于进行态', () => {
    expect(isUpdateRunningStatus('completed')).toBe(false)
    expect(isUpdateRunningStatus('failed')).toBe(false)
    expect(isUpdateRunningStatus('cancelled')).toBe(false)
  })
})
