import { flushPromises, shallowMount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppEmbySettings from '@/components/AppEmbySettings.vue'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

describe('AppEmbySettings 同步状态轮询', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('启动同步时合并的状态请求会在原请求结束后补发', async () => {
    vi.useFakeTimers()
    const initialStatus = createDeferred<{
      data: { code: number; data: { is_running: boolean } }
    }>()
    let statusRequestCount = 0
    const http = {
      get: vi.fn((url: string) => {
        if (url.endsWith('/setting/emby-config')) {
          return Promise.resolve({ data: { code: 200, data: { exists: false } } })
        }
        if (url.endsWith('/emby/sync/status')) {
          statusRequestCount += 1
          if (statusRequestCount === 1) return initialStatus.promise
          return Promise.resolve({ data: { code: 200, data: { is_running: true } } })
        }
        return Promise.reject(new Error(`unexpected url: ${url}`))
      }),
      post: vi.fn().mockResolvedValue({ data: { code: 200 } }),
    }
    const wrapper = shallowMount(AppEmbySettings, {
      global: { provide: { [httpKey]: http } },
    })
    const vm = wrapper.vm as unknown as { startSync: () => Promise<void> }
    await flushPromises()

    await vm.startSync()
    expect(statusRequestCount).toBe(1)

    initialStatus.resolve({ data: { code: 200, data: { is_running: false } } })
    await flushPromises()

    expect(statusRequestCount).toBe(2)
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(statusRequestCount).toBe(3)
    wrapper.unmount()
  })
})
