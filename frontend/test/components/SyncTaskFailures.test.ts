// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import SyncTaskFailures from '@/components/sync-task/SyncTaskFailures.vue'
import { fetchSyncTaskFailures } from '@/api/syncTasks'

vi.mock('@/api/syncTasks', () => ({ fetchSyncTaskFailures: vi.fn() }))
enableAutoUnmount(afterEach)
afterEach(() => vi.resetAllMocks())

it('翻页失败保留原有正文，重试实际失败的页码', async () => {
  const first = { kind: 'file' as const, path: '/第一页', reason: '读取失败' }
  const second = { kind: 'directory' as const, path: '/第二页', reason: '目录失败' }
  vi.mocked(fetchSyncTaskFailures)
    .mockResolvedValueOnce({ entries: [first], total: 300, recorded: true })
    .mockRejectedValueOnce(new TypeError('Failed to fetch'))
    .mockResolvedValueOnce({ entries: [second], total: 300, recorded: true })
  const wrapper = shallowMount(SyncTaskFailures, {
    props: { syncId: 8, status: 5 },
    global: { directives: { loading: () => {} }, stubs: { ResponsivePagination: false } },
  })
  await flushPromises()
  wrapper.findComponent({ name: 'ElPagination' }).vm.$emit('current-change', 2)
  await flushPromises()
  expect(wrapper.text()).toContain('/第一页')
  await wrapper.findComponent({ name: 'ElButton' }).trigger('click')
  await flushPromises()
  expect(vi.mocked(fetchSyncTaskFailures).mock.calls.map((call) => call[1])).toEqual([0, 100, 100])
  expect(wrapper.text()).toContain('/第二页')
  expect(wrapper.text()).not.toContain('/第一页')
})

it('失败范围按页加载，不把总数误用为文件失败数', async () => {
  const entries = Array.from({ length: 100 }, (_, i) => ({
    kind: 'directory' as const,
    path: `/目录${i}`,
    reason: '读取失败',
  }))
  vi.mocked(fetchSyncTaskFailures)
    .mockResolvedValueOnce({ entries, total: 10001, recorded: true })
    .mockResolvedValueOnce({ entries: entries.slice(0, 1), total: 10001, recorded: true })
  const wrapper = shallowMount(SyncTaskFailures, {
    props: { syncId: 8, status: 5 },
    global: { directives: { loading: () => {} }, stubs: { ResponsivePagination: false } },
  })
  await flushPromises()
  expect(wrapper.findAll('li')).toHaveLength(100)
  const pagination = wrapper.findComponent({ name: 'ElPagination' })
  expect(pagination.props('total')).toBe(10001)
  pagination.vm.$emit('current-change', 101)
  await flushPromises()
  expect(fetchSyncTaskFailures).toHaveBeenLastCalledWith(8, 10000, expect.any(AbortSignal))
  expect(wrapper.findAll('li')).toHaveLength(1)
  wrapper.unmount()
})
