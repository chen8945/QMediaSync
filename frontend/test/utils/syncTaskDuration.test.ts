import { describe, expect, it } from 'vitest'
import { formatDurationMs } from '@/utils/timeUtils'
import {
  formatSyncDuration,
  getSyncGenerationDuration,
  getSyncTotalDuration,
} from '@/utils/syncTaskStatusUtils'

describe('同步耗时精度', () => {
  it.each([
    [0, '<1 ms'],
    [1, '1 ms'],
    [123, '123 ms'],
    [999, '999 ms'],
    [1000, '1 秒'],
    [1234, '1.23 秒'],
    [1010, '1.01 秒'],
    [59999, '59.99 秒'],
    [60000, '1 分'],
    [61230, '1 分 1 秒'],
    [125800, '2 分 5 秒'],
    [3661200, '1 小时 1 分 1 秒'],
  ])('%i ms 显示为 %s', (milliseconds, text) => {
    expect(formatDurationMs(milliseconds)).toBe(text)
  })

  it('仅新记录使用毫秒精度，历史记录保留秒显示', () => {
    expect(formatSyncDuration(1, 123)).toBe('123 ms')
    expect(formatSyncDuration(0, 0)).toBe('<1 ms')
    expect(formatSyncDuration(0, null)).toBe('0 秒')
    expect(getSyncGenerationDuration(10, 10, 123)).toBe('123 ms')
    expect(getSyncGenerationDuration(10, 11)).toBe('1 秒')
    expect(getSyncGenerationDuration(10, 0, null)).toBe('统计中')
  })

  it('后台终点未确认时不显示完整总耗时', () => {
    expect(
      getSyncTotalDuration(10, 10, { ledger_status: 'running', total_duration_ms: null }),
    ).toBe('统计中')
    expect(
      getSyncTotalDuration(10, 10, {
        ledger_status: 'interrupted',
        ledger_finished_at: null,
        total_duration_ms: null,
      }),
    ).toBe('未完整统计')
    expect(
      getSyncTotalDuration(10, 10, { ledger_status: 'not_required', total_duration_ms: 0 }),
    ).toBe('<1 ms')
    expect(
      getSyncTotalDuration(10, 11, {
        ledger_status: 'completed',
        ledger_finished_at: 11,
        total_duration_ms: 1234,
      }),
    ).toBe('1.23 秒')
  })
})
