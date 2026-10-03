import { describe, expect, it } from 'vitest'
import { strmResultStatus } from '@/api/strmResults'

describe('STRM 后处理状态', () => {
  it('区分跳过、完成、失败并保留未知状态', () => {
    expect(strmResultStatus('skipped')).toBe('已跳过')
    expect(strmResultStatus('completed')).toBe('已完成')
    expect(strmResultStatus('failed')).toBe('失败')
    expect(strmResultStatus('future')).toBe('未知状态（future）')
    expect(strmResultStatus('')).toContain('未记录')
  })
})
