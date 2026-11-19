import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { useLogBuffer } from '@/composables/useLogBuffer'

afterEach(() => vi.useRealTimers())

describe('实时日志缓冲', () => {
  it('持续突发只按节奏发布有界窗口，清空后不恢复旧待处理内容', () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const buffer = scope.run(() => useLogBuffer())!
    for (let batch = 0; batch < 100; batch++) {
      buffer.append(
        Array.from({ length: 100 }, (_, index) => ({
          level: 'info',
          timestamp: 't',
          message: `${batch * 100 + index}`,
        })),
      )
    }
    expect(buffer.logs.value).toHaveLength(0)
    vi.advanceTimersByTime(100)
    expect(buffer.logs.value).toHaveLength(2000)
    expect(buffer.logs.value[0].message).toBe('9999')
    expect(buffer.logs.value[1999].message).toBe('8000')
    buffer.append([{ level: 'info', timestamp: 't', message: 'pending' }])
    buffer.clear()
    vi.advanceTimersByTime(100)
    expect(buffer.logs.value).toHaveLength(0)
    scope.stop()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('字节预算限制窗口，但保留超长单行完整正文', () => {
    const scope = effectScope()
    const buffer = scope.run(() => useLogBuffer())!
    const message = '长'.repeat(2 * 1024 * 1024)
    buffer.append([
      { level: 'info', timestamp: 't', message: 'old' },
      { level: 'error', timestamp: 't', message },
    ])
    buffer.flush()
    expect(buffer.logs.value.map((entry) => entry.message)).toEqual([message])
    scope.stop()
  })
})
