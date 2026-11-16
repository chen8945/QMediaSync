import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppDownloadQueue from '@/views/queues/AppDownloadQueue.vue'
import AppUploadQueue from '@/views/queues/AppUploadQueue.vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import { SERVER_URL } from '@/const'
import { httpKey } from '@/http/client'
import { emptyQueueStatusSnapshot } from '@/utils/queueStatusUtils'

vi.mock('@/composables/useRealtimeEvents', () => ({ useRealtimeEvent: vi.fn() }))

enableAutoUnmount(afterEach)

beforeEach(() => {
  sessionStorage.clear()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe.each([
  { component: AppDownloadQueue, queue: 'download', label: '下载' },
  { component: AppUploadQueue, queue: 'upload', label: '上传' },
])('$label 队列任务统计', ({ component, queue, label }) => {
  it('复用全局快照显示剩余、排队和处理中，筛选与分页不改变统计口径', async () => {
    let snapshot = {
      running: true,
      pending: 89,
      processing: 11,
      completed: 500,
      failed: 30,
      cancelled: 20,
      total: 650,
    }
    let filteredTotal = 650
    const endpoint = `${SERVER_URL}/${queue}/queue`
    const get = vi.fn(async (url: string) => {
      if (url === `${endpoint}/status`)
        return { data: { status: 200, data: snapshot }, status: 200 }
      if (url !== endpoint) throw new Error(`unexpected url: ${url}`)
      return {
        data: {
          status: 200,
          data: {
            list: [],
            total: filteredTotal,
            downloading: 1,
            uploading: 1,
            queue_status: snapshot,
          },
        },
        status: 200,
      }
    })
    const wrapper = shallowMount(component, {
      global: {
        plugins: [createPinia()],
        provide: { [httpKey]: { get } },
        stubs: {
          PageHeader: { template: '<header><slot name="actions" /></header>' },
          ElSelect: { name: 'ElSelect', template: '<div><slot /></div>' },
          ElButton: false,
        },
      },
    })
    const statsText = () =>
      wrapper.get(`[aria-label="${label}队列任务统计"]`).text().replace(/\s+/g, '')
    const refreshButton = wrapper.findAll('button').find((button) => button.text() === '刷新')!

    if (queue === 'download') {
      expect(
        wrapper
          .findAllComponents({ name: 'ElOption' })
          .map((option) => [option.props('value'), option.props('label')]),
      ).toEqual([
        [-1, '全部状态'],
        [0, '等待下载'],
        [1, '正在下载'],
        [2, '下载完成'],
        [3, '下载失败'],
        [4, '已取消'],
      ])
    }

    expect(statsText()).toBe('剩余0·排队0·处理中0')
    await flushPromises()
    expect(statsText()).toBe('剩余100·排队89·处理中11')
    expect(get).toHaveBeenCalledTimes(2)
    expect(wrapper.find(`[aria-label="查看${label}队列统计说明"]`).exists()).toBe(false)

    filteredTotal = 30
    wrapper.getComponent({ name: 'ElSelect' }).vm.$emit('change', 3)
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith(endpoint, {
      params: { page: 1, page_size: 20, status: 3 },
    })
    expect(wrapper.getComponent(ResponsivePagination).props('total')).toBe(30)
    expect(statsText()).toBe('剩余100·排队89·处理中11')

    wrapper.getComponent(ResponsivePagination).vm.$emit('current-change', 2)
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith(endpoint, {
      params: { page: 2, page_size: 20, status: 3 },
    })
    expect(statsText()).toBe('剩余100·排队89·处理中11')

    snapshot = { ...snapshot, pending: 7, processing: 3 }
    await refreshButton.trigger('click')
    await flushPromises()
    expect(statsText()).toBe('剩余10·排队7·处理中3')

    snapshot = emptyQueueStatusSnapshot()
    filteredTotal = 0
    await refreshButton.trigger('click')
    await flushPromises()
    expect(statsText()).toBe('剩余0·排队0·处理中0')
    expect(get).toHaveBeenCalledTimes(6)
  })
})
