import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { useQueueStats } from '@/composables/useQueueStats'
import { httpKey } from '@/http/client'

type Deferred<T> = {
  promise: Promise<T>
  resolve: (value: T) => void
}

const createDeferred = <T>(): Deferred<T> => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })

  return { promise, resolve }
}

const queueStatsResponse = {
  status: 200,
  data: {
    status: 200,
    data: {
      avg_response_time_ms: 0,
      is_throttled: false,
      last_throttle_time: null,
      qph_count: 0,
      qpm_count: 0,
      qps_count: 0,
      throttle_recover_time: '',
      throttle_wait_time: '',
      throttled_count: 0,
      throttled_elapsed_time: '',
      throttled_remaining_time: '',
      time_window_seconds: 3600,
      total_requests: 0,
    },
  },
}

const QueueStatsHarness = defineComponent({
  setup() {
    useQueueStats()
    return () => null
  },
})

describe('useQueueStats', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('首页在轮询请求完成前卸载时不续排下一次轮询', async () => {
    vi.useFakeTimers()
    const requests: Deferred<typeof queueStatsResponse>[] = []
    const get = vi.fn(() => {
      const request = createDeferred<typeof queueStatsResponse>()
      requests.push(request)
      return request.promise
    })
    const wrapper = mount(QueueStatsHarness, {
      global: {
        provide: {
          [httpKey]: { get },
        },
      },
    })

    expect(get).toHaveBeenCalledTimes(1)
    requests[0].resolve(queueStatsResponse)
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    expect(get).toHaveBeenCalledTimes(2)

    wrapper.unmount()
    requests[1].resolve(queueStatsResponse)
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    expect(get).toHaveBeenCalledTimes(2)
  })
})
