import axios from 'axios'
import { ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { browseSortQuery, sortLocalDirectories, useBrowseSort } from '@/composables/useBrowseSort'
import { useAuthStore } from '@/stores/auth'
import { browseSortOptions } from '../support/browseSort'
import { createDeferred } from '../support/deferred'

function client() {
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: { code: 200, data: browseSortOptions(config.params.source_type, config.params.scope) },
  }))
  return { http: axios.create({ adapter }), adapter }
}

beforeEach(() => {
  localStorage.clear()
  setActivePinia(createPinia())
  useAuthStore().user = { id: 'user-1', username: 'test' }
})

describe('浏览排序偏好', () => {
  it('成功后跨实例恢复，按账号、场景和当前用户隔离', async () => {
    const { http, adapter } = client()
    const account = ref(1)
    const sort = useBrowseSort(http, '115', account, 'files')
    await sort.prepare()
    expect(sort.selection.value).toEqual({
      sort_by: 'name',
      sort_order: 'asc',
      folders_first: true,
    })
    sort.choose({ sort_by: 'size', sort_order: 'desc', folders_first: false })
    expect(localStorage.getItem(sort.contextKey.value)).toBeNull()
    sort.commit(sort.selection.value)
    const reopened = useBrowseSort(http, '115', 1, 'files')
    await reopened.prepare()
    expect(reopened.selection.value).toEqual(sort.selection.value)
    expect(adapter).toHaveBeenCalledTimes(1)
    account.value = 2
    await sort.prepare()
    expect(sort.selection.value.sort_by).toBe('name')
    const directorySort = useBrowseSort(http, '115', 1, 'directories')
    await directorySort.prepare()
    expect(directorySort.selection.value).toEqual({ sort_by: 'name', sort_order: 'asc' })
    useAuthStore().user = { id: 'user-2', username: 'another' }
    await reopened.prepare()
    expect(reopened.selection.value.sort_by).toBe('name')
  })

  it('损坏与不支持的旧值恢复默认，default 不附加方向或置顶请求', async () => {
    const { http } = client()
    for (const value of ['broken json', JSON.stringify({ sort_by: 'type', sort_order: 'desc' })]) {
      const sort = useBrowseSort(http, 'baidupan', 1, 'directories')
      localStorage.setItem(sort.contextKey.value, value)
      await sort.prepare()
      expect(sort.selection.value).toEqual({ sort_by: 'name', sort_order: 'asc' })
    }
    const sort = useBrowseSort(http, '115', 1, 'files')
    await sort.prepare()
    sort.choose({ sort_by: 'default', sort_order: 'desc', folders_first: true })
    expect(browseSortQuery(sort.selection.value)).toEqual({ sort_by: 'default' })
    sort.commit(sort.selection.value)
    sort.choose({ sort_by: 'time', sort_order: 'desc' })
    sort.rollback()
    expect(sort.selection.value).toEqual({ sort_by: 'default', sort_order: 'asc' })
  })

  it('localStorage 不可用时保留同一会话的成功偏好', async () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('disabled')
    })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('disabled')
    })
    try {
      const { http } = client()
      const sort = useBrowseSort(http, '115', 707, 'files')
      await sort.prepare()
      sort.choose({ sort_by: 'time', sort_order: 'desc', folders_first: false })
      sort.commit(sort.selection.value)
      const reopened = useBrowseSort(http, '115', 707, 'files')
      await reopened.prepare()
      expect(reopened.selection.value).toEqual(sort.selection.value)
    } finally {
      get.mockRestore()
      set.mockRestore()
    }
  })

  it('只有写入失败时仍优先恢复内存偏好，恢复写入后移除降级状态', async () => {
    const { http } = client()
    const sort = useBrowseSort(http, '115', 708, 'files')
    await sort.prepare()
    sort.commit(sort.selection.value)
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    sort.choose({ sort_by: 'size', sort_order: 'desc', folders_first: false })
    sort.commit(sort.selection.value)
    const reopened = useBrowseSort(http, '115', 708, 'files')
    await reopened.prepare()
    expect(reopened.selection.value.sort_by).toBe('size')
    set.mockRestore()
    reopened.choose({ sort_by: 'time', sort_order: 'desc', folders_first: true })
    reopened.commit(reopened.selection.value)
    expect(JSON.parse(localStorage.getItem(reopened.contextKey.value)!)).toEqual(
      reopened.selection.value,
    )
    const next = useBrowseSort(http, '115', 708, 'files')
    await next.prepare()
    expect(next.selection.value.sort_by).toBe('time')
  })

  it('能力查询失败可重试，旧账号的准备结果不能覆盖新上下文', async () => {
    const old = createDeferred<ReturnType<typeof browseSortOptions>>()
    const { http, adapter } = client()
    adapter.mockImplementationOnce(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { code: 200, data: await old.promise },
    }))
    const source = ref('115')
    const sort = useBrowseSort(http, source, 1, 'files')
    const pending = sort.prepare()
    source.value = 'openlist'
    await sort.prepare()
    old.resolve(browseSortOptions())
    expect(await pending).toBe(false)
    expect(sort.capabilities.value?.fields).toEqual(['default'])
    const retryClient = client()
    retryClient.adapter.mockRejectedValueOnce(new Error('unavailable'))
    const retry = useBrowseSort(retryClient.http, '115', 1, 'files')
    await expect(retry.prepare()).rejects.toThrow('unavailable')
    expect(retry.ready.value).toBe(false)
    expect(await retry.prepare()).toBe(true)
  })
})

it('本地目录自然排序，修改时间缺失始终置后，同时间按名称稳定排列', () => {
  const directories = [
    { id: '10', name: '第10集', path: '/10', modified_time: 20 },
    { id: 'missing', name: '第1集', path: '/1' },
    { id: '2', name: '第2集', path: '/2', modified_time: 20 },
    { id: 'old', name: '旧目录', path: '/old', modified_time: 0 },
  ]
  expect(
    sortLocalDirectories(directories, { sort_by: 'name', sort_order: 'asc' })
      .map((d) => d.id)
      .filter((id) => id !== 'old'),
  ).toEqual(['missing', '2', '10'])
  expect(
    sortLocalDirectories(directories, { sort_by: 'time', sort_order: 'desc' }).map((d) => d.id),
  ).toEqual(['2', '10', 'old', 'missing'])
  expect(
    sortLocalDirectories(directories, { sort_by: 'time', sort_order: 'asc' }).map((d) => d.id),
  ).toEqual(['old', '2', '10', 'missing'])
  expect(directories[0].id).toBe('10')
})
