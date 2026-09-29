import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ElMessage } from 'element-plus'
import AppFileManager from '@/components/AppFileManager.vue'
import BrowseSortControl from '@/components/BrowseSortControl.vue'
import { httpKey } from '@/http/client'
import { browseSortOptions } from '../support/browseSort'
import { createDeferred } from '../support/deferred'

const page = (name = '文件', sort_by = 'name') => ({
  data: {
    code: 200,
    data: {
      list: [{ id: 'file', name, is_directory: false, size: 1 }],
      total: 1,
      sort_by,
      sort_order: 'asc',
    },
  },
})

function mountPage(source = '115') {
  sessionStorage.setItem(
    'qmediasync-page-state',
    JSON.stringify({
      'file-manager': {
        currentPage: 2,
        pageSize: 50,
        filters: {
          selectedAccountId: 1,
          currentPath: '',
          pathItems: '[]',
          sortBy: 'size',
          sortOrder: 'desc',
        },
      },
    }),
  )
  const list = vi.fn(async () => page())
  const http = {
    get: vi.fn((url: string, config?: { params?: Record<string, unknown> }) => {
      if (url.endsWith('/account/list'))
        return Promise.resolve({
          data: {
            code: 200,
            data: [
              { id: 1, source_type: source },
              { id: 2, source_type: source },
            ],
          },
        })
      if (url.endsWith('/path/sort-options'))
        return Promise.resolve({ data: { code: 200, data: browseSortOptions(source) } })
      if (url.endsWith('/path/files')) return list()
      return Promise.reject(new Error(`unexpected URL ${url} ${Boolean(config)}`))
    }),
  }
  const wrapper = shallowMount(AppFileManager, {
    global: {
      plugins: [createPinia()],
      provide: { [httpKey]: http },
      stubs: { ElCard: { template: '<section><slot name="header" /><slot /></section>' } },
    },
  })
  return { wrapper, http, list }
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  vi.restoreAllMocks()
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

describe('文件管理排序', () => {
  it.each(['115', 'baidupan', 'openlist'])(
    '按 %s 能力初始化请求，不恢复失效的会话排序',
    async (source) => {
      const { wrapper, http } = mountPage(source)
      await flushPromises()
      await flushPromises()
      const control = wrapper.findComponent(BrowseSortControl)
      expect(control.props('options')).toEqual(browseSortOptions(source))
      const params = http.get.mock.calls.find(([url]) => url.endsWith('/path/files'))?.[1]?.params
      expect(params).toMatchObject(
        source === 'openlist' ? { sort_by: 'default' } : { sort_by: 'name', sort_order: 'asc' },
      )
      if (source !== '115') expect(params).not.toHaveProperty('folders_first')
    },
  )

  it('跟随网盘意图不被响应回显覆盖，失败恢复最近成功选择', async () => {
    const { wrapper, http, list } = mountPage()
    await flushPromises()
    await flushPromises()
    const control = wrapper.findComponent(BrowseSortControl)
    control.vm.$emit('change', { sort_by: 'default', sort_order: 'asc' })
    await flushPromises()
    expect(control.props('modelValue')).toEqual({ sort_by: 'default', sort_order: 'asc' })
    const last = http.get.mock.calls
      .filter(([url]) => url.endsWith('/path/files'))
      .at(-1)?.[1]?.params
    expect(last).toMatchObject({ page: 1, sort_by: 'default' })
    expect(last).not.toHaveProperty('sort_order')
    expect(last).not.toHaveProperty('folders_first')
    list.mockRejectedValueOnce(new Error('failed'))
    control.vm.$emit('change', { sort_by: 'size', sort_order: 'desc', folders_first: false })
    await flushPromises()
    expect(control.props('modelValue')).toEqual({ sort_by: 'default', sort_order: 'asc' })
    expect(
      Object.keys(localStorage)
        .filter((key) => key.startsWith('qmediasync-browse-sort'))
        .map((key) => JSON.parse(localStorage.getItem(key)!)),
    ).toEqual([{ sort_by: 'default', sort_order: 'asc' }])
  })

  it('快速切换排序或账号时旧结果不能覆盖，卸载后的失败不提示', async () => {
    const { wrapper, list, http } = mountPage()
    await flushPromises()
    await flushPromises()
    const old = createDeferred<ReturnType<typeof page>>()
    list.mockReturnValueOnce(old.promise)
    const control = wrapper.findComponent(BrowseSortControl)
    control.vm.$emit('change', { sort_by: 'time', sort_order: 'desc', folders_first: true })
    await flushPromises()
    control.vm.$emit('change', { sort_by: 'size', sort_order: 'desc', folders_first: false })
    old.resolve(page('旧顺序'))
    await flushPromises()
    await flushPromises()
    expect(control.props('modelValue')).toEqual({
      sort_by: 'size',
      sort_order: 'desc',
      folders_first: false,
    })
    const params = http.get.mock.calls
      .filter(([url]) => url.endsWith('/path/files'))
      .at(-1)?.[1]?.params
    expect(params).toMatchObject({ sort_by: 'size', folders_first: false })
    await wrapper.findAll('.account-item')[1]!.trigger('click')
    await flushPromises()
    expect(wrapper.findComponent(BrowseSortControl).props('modelValue')).toMatchObject({
      sort_by: 'name',
      sort_order: 'asc',
    })
    const late = createDeferred<ReturnType<typeof page>>()
    list.mockReturnValueOnce(late.promise)
    wrapper
      .findComponent(BrowseSortControl)
      .vm.$emit('change', { sort_by: 'time', sort_order: 'desc' })
    await flushPromises()
    wrapper.unmount()
    const failed = page()
    failed.data.code = 500
    late.resolve(failed)
    await flushPromises()
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
