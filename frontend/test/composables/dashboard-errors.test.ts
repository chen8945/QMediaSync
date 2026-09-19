import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { AxiosError, CanceledError } from 'axios'
import { ElMessage } from 'element-plus'
import { defineComponent } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAnnouncement } from '@/composables/useAnnouncement'
import { useHourlyStats } from '@/composables/useHourlyStats'
import { useQueueStats } from '@/composables/useQueueStats'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'

const wrappers: VueWrapper[] = []
const announcement = [{ id: 1, title: '公告', content: '内容', time: '2026-09-20' }]
const hourly = {
  start_date: '2026-09-19',
  end_date: '2026-09-20',
  total_requests: 0,
  total_throttled: 0,
  hourly_stats: [],
  query_time_range_days: 1,
}
const queue = {
  total_requests: 0,
  qps_count: 0,
  qpm_count: 0,
  qph_count: 0,
  is_throttled: false,
  last_throttle_time: null,
  throttle_recover_time: null,
  throttle_wait_time: '',
  throttled_count: 0,
  throttled_elapsed_time: '',
  throttled_remaining_time: '',
  avg_response_time_ms: 0,
  time_window_seconds: 3600,
}
const success = (data: unknown = null) => ({ status: 200, data: { code: 200, data } })
const failedResponse = { status: 200, data: { code: 500, message: 'SQL token=secret', data: null } }
const withSetup = <T>(composable: () => T, get: ReturnType<typeof vi.fn>) => {
  let state!: T
  const wrapper = mount(
    defineComponent({
      setup() {
        state = composable()
        return () => null
      },
    }),
    { global: { provide: { [httpKey]: { get } } } },
  )
  wrappers.push(wrapper)
  return { state, wrapper }
}

beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('公告与统计错误处理', () => {
  it('公告兼容旧数组，失败清空公告，只输出安全诊断', async () => {
    const get = vi.fn().mockResolvedValue({ status: 200, data: announcement })
    const { state } = withSetup(useAnnouncement, get)
    await flushPromises()
    expect(state.announcementList.value).toEqual(announcement)
    get.mockResolvedValue(failedResponse)
    await state.loadAnnouncements()
    expect(state.announcementList.value).toEqual([])
    expect(state.announcementLoading.value).toBe(false)
    expect(console.error).toHaveBeenCalledExactlyOnceWith('加载公告列表错误：', { status: 200 })
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('每小时统计失败保留缓存和图表，成功 null 恢复空状态', async () => {
    const get = vi.fn().mockResolvedValue(success(hourly))
    const { state } = withSetup(useHourlyStats, get)
    await flushPromises()
    expect(state.hourlyStats.value).toEqual(hourly)
    get.mockResolvedValue(failedResponse)
    await state.loadHourlyStats()
    expect(state.hourlyStats.value).toEqual(hourly)
    expect(state.chartOption.value).toHaveProperty('series')
    expect(state.hourlyStatsLoading.value).toBe(false)
    expect(console.error).toHaveBeenCalledExactlyOnceWith('加载每小时请求统计错误：', {
      status: 200,
    })
    expect(ElMessage.error).not.toHaveBeenCalled()
    get.mockResolvedValue(success())
    await state.loadHourlyStats()
    expect(state.hourlyStats.value).toBeNull()
    expect(state.chartOption.value).toEqual({})
  })

  it('统计首次业务失败保持 null，普通异常不输出内部信息', async () => {
    const get = vi.fn().mockResolvedValue(failedResponse)
    const { state } = withSetup(useHourlyStats, get)
    await flushPromises()
    expect(state.hourlyStats.value).toBeNull()
    get.mockRejectedValue(new Error('SQL secret'))
    await state.loadHourlyStats()
    expect(state.hourlyStatsLoading.value).toBe(false)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
  })

  it('队列统计失败保留缓存并延长间隔，成功后恢复原轮询间隔', async () => {
    vi.useFakeTimers()
    const get = vi.fn().mockResolvedValue(success(queue))
    const { state } = withSetup(() => useQueueStats(1000), get)
    await flushPromises()
    get.mockResolvedValueOnce(failedResponse)
    await vi.advanceTimersByTimeAsync(1000)
    expect(get).toHaveBeenCalledTimes(2)
    expect(state.queueStats.value).toEqual(queue)
    expect(state.queueStatsLoading.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1999)
    expect(get).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(get).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1000)
    expect(get).toHaveBeenCalledTimes(4)
    expect(console.error).toHaveBeenCalledExactlyOnceWith('加载 115 接口请求统计错误：', {
      status: 200,
    })
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('队列首次失败为 null，隐藏时暂停、恢复时读取且请求不重叠', async () => {
    vi.useFakeTimers()
    const get = vi.fn().mockResolvedValue(failedResponse)
    const { state } = withSetup(() => useQueueStats(1000), get)
    await flushPromises()
    expect(state.queueStats.value).toBeNull()
    vi.mocked(Object.getOwnPropertyDescriptor(document, 'hidden')!.get!).mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(10000)
    expect(get).toHaveBeenCalledTimes(1)
    let resolve!: (value: ReturnType<typeof success>) => void
    get.mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done
        }),
    )
    vi.mocked(Object.getOwnPropertyDescriptor(document, 'hidden')!.get!).mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await state.loadQueueStats()
    await vi.advanceTimersByTimeAsync(5000)
    expect(get).toHaveBeenCalledTimes(2)
    resolve(success(queue))
    await flushPromises()
    expect(state.queueStats.value).toEqual(queue)
    expect(state.queueStatsLoading.value).toBe(false)
  })

  it.each(['cancel', 'handled'])('公告与两种统计的 %s 请求不弹窗或记录异常', async (kind) => {
    const error =
      kind === 'cancel' ? new CanceledError() : new HttpResponseError({ status: 401, data: null })
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const get = vi.fn().mockRejectedValue(error)
    withSetup(useAnnouncement, get)
    withSetup(useHourlyStats, get)
    withSetup(useQueueStats, get)
    await flushPromises()
    expect(get).toHaveBeenCalledTimes(3)
    expect(console.error).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('无响应错误的诊断只保留方法和路径，不输出请求凭据', async () => {
    const error = new AxiosError('Network Error secret', 'ERR_NETWORK')
    error.config = {
      method: 'get',
      url: '/api/announce?api_key=secret#private',
      headers: {},
    } as typeof error.config
    const get = vi.fn().mockRejectedValue(error)
    withSetup(useAnnouncement, get)
    await flushPromises()
    expect(console.error).toHaveBeenCalledExactlyOnceWith('加载公告列表错误：', {
      method: 'GET',
      path: '/api/announce',
    })
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
