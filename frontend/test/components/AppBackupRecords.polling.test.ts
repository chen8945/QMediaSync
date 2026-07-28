// @vitest-environment happy-dom
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppBackupRecords from '@/components/AppBackupRecords.vue'
import { httpKey } from '@/http/client'

const scanningResponse = {
  status: 200,
  data: {
    code: 200,
    data: {
      list: [],
      total: 0,
      page: 1,
      page_size: 20,
      inventory_status: 'scanning' as const,
    },
  },
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('AppBackupRecords 目录清点轮询', () => {
  it('隐藏时暂停、可见时恢复，并在卸载后不让飞行请求重建定时器', async () => {
    vi.useFakeTimers()
    let hidden = false
    const originalHidden = Object.getOwnPropertyDescriptor(document, 'hidden')
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })

    let resolveFirstRequest!: (value: typeof scanningResponse) => void
    const firstRequest = new Promise<typeof scanningResponse>((resolve) => {
      resolveFirstRequest = resolve
    })
    const get = vi.fn().mockReturnValueOnce(firstRequest).mockResolvedValue(scanningResponse)
    const wrapper = shallowMount(AppBackupRecords, {
      global: {
        directives: { loading: {} },
        plugins: [createPinia()],
        provide: { [httpKey]: { get } },
      },
    })

    expect(get).toHaveBeenCalledTimes(1)
    hidden = true
    document.dispatchEvent(new Event('visibilitychange'))
    resolveFirstRequest(scanningResponse)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(3000)
    expect(get).toHaveBeenCalledTimes(1)

    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(3000)
    expect(get).toHaveBeenCalledTimes(2)
    if (originalHidden) Object.defineProperty(document, 'hidden', originalHidden)
  })
})
