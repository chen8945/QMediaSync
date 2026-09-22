import { SERVER_URL } from '@/const'
import { useV115AppIdSearch } from '@/composables/useV115AppIdSearch'
import { effectScope } from 'vue'
import axios, { AxiosError, CanceledError } from 'axios'
import { markAuthInvalidationHandled } from '@/http/errors'
import { describe, expect, it, vi } from 'vitest'
import { createDeferred } from '../support/deferred'

const searchResponse = (appId: string, total = 1) => ({
  data: {
    code: 200,
    data: {
      items: [{ app_id: appId, app_name: `应用${appId}`, display_name: `应用${appId}` }],
      total,
    },
  },
})

describe('useV115AppIdSearch', () => {
  it('不会把可调用的 axios 实例当作 getter 执行', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        code: 200,
        data: {
          items: [{ app_id: '1001', app_name: '应用1', display_name: '应用1' }],
          total: 1,
        },
      },
    })
    const http = Object.assign(vi.fn(), { get })

    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http: http as never }))

    expect(search).toBeDefined()
    search!.keyword.value = '应用'
    await search!.search()

    expect(http).not.toHaveBeenCalled()
    expect(get).toHaveBeenCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '应用', offset: 0, limit: 50 },
    })

    scope.stop()
  })

  it('加载更多使用当前关键词和 offset 追加下一页结果', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            items: [{ app_id: '1001', app_name: '应用1', display_name: '应用1' }],
            total: 2,
          },
        },
      })
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            items: [{ app_id: '1002', app_name: '应用2', display_name: '应用2' }],
            total: 2,
          },
        },
      })

    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http: { get } as never, pageSize: 1 }))

    expect(search).toBeDefined()
    search!.keyword.value = '应用'
    await search!.search()

    expect(search!.hasMore.value).toBe(true)
    await search!.loadMore()

    expect(get).toHaveBeenNthCalledWith(1, `${SERVER_URL}/115/appids`, {
      params: { keyword: '应用', offset: 0, limit: 1 },
    })
    expect(get).toHaveBeenNthCalledWith(2, `${SERVER_URL}/115/appids`, {
      params: { keyword: '应用', offset: 1, limit: 1 },
    })
    expect(search!.items.value.map((item) => item.app_id)).toEqual(['1001', '1002'])
    expect(search!.hasMore.value).toBe(false)

    scope.stop()
  })

  it.each([
    [
      '来源错误',
      {
        response: {
          status: 403,
          data: { code: 500, error_code: 'REQUEST_ORIGIN_INVALID', message: 'private-token' },
        },
      },
      '访问地址校验失败',
    ],
    ['传输错误', new AxiosError('private-token', 'ERR_NETWORK'), '无法获取服务器响应'],
    ['程序异常', new Error('private-token'), '搜索 APP ID 失败'],
  ])('搜索%s展示安全文案且不显示其他关键词的结果', async (_name, error, message) => {
    const http = axios.create()
    http.get = vi.fn().mockResolvedValueOnce(searchResponse('1001')).mockRejectedValueOnce(error)
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http }))!
    await search.search()
    search.keyword.value = '新应用'
    await search.search()
    expect(search.items.value).toEqual([])
    expect(search.total.value).toBe(0)
    expect(search.errorMessage.value).toContain(message)
    expect(search.errorMessage.value).not.toContain('private-token')
    expect(search.keyword.value).toBe('新应用')
    expect(search.loading.value).toBe(false)
    scope.stop()
  })

  it('新关键词搜索失败不能沿用旧分页，重试从新关键词第一页开始', async () => {
    const http = axios.create()
    const get = vi
      .fn()
      .mockResolvedValueOnce(searchResponse('old-1', 2))
      .mockResolvedValueOnce({ data: { code: 500, data: null } })
      .mockResolvedValueOnce(searchResponse('new-1', 2))
      .mockResolvedValueOnce(searchResponse('new-2', 2))
    http.get = get
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http, pageSize: 1 }))!
    search.keyword.value = '旧应用'
    await search.search()
    search.keyword.value = '新应用'
    await search.search()
    await search.loadMore()
    expect(get).toHaveBeenCalledTimes(2)
    expect(search.items.value).toEqual([])
    expect(search.hasMore.value).toBe(false)
    expect(search.errorMessage.value).toBe('搜索 APP ID 失败')

    await search.search()
    expect(get).toHaveBeenLastCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '新应用', offset: 0, limit: 1 },
    })
    await search.loadMore()
    expect(get).toHaveBeenLastCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '新应用', offset: 1, limit: 1 },
    })
    expect(search.items.value.map((item) => item.app_id)).toEqual(['new-1', 'new-2'])
    scope.stop()
  })

  it('关键词已改变但新查询尚未发出时隐藏旧结果并拒绝旧分页', async () => {
    const http = axios.create()
    const get = vi.fn().mockResolvedValue(searchResponse('old-1', 2))
    http.get = get
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http, pageSize: 1 }))!
    await search.search()
    search.keyword.value = '新应用'
    expect(search.items.value).toEqual([])
    expect(search.total.value).toBe(0)
    await search.loadMore()
    expect(get).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('加载更多业务失败保留分页，成功重试会清除错误', async () => {
    const http = axios.create()
    const get = vi
      .fn()
      .mockResolvedValueOnce(searchResponse('1001', 2))
      .mockResolvedValueOnce({
        data: { code: 500, message: 'APP ID 服务暂不可用', data: null },
      })
      .mockResolvedValueOnce(searchResponse('1002', 2))
    http.get = get
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http, pageSize: 1 }))!
    search.keyword.value = '应用'
    await search.search()
    await search.loadMore()
    expect(search.items.value.map((item) => item.app_id)).toEqual(['1001'])
    expect(search.errorMessage.value).toBe('APP ID 服务暂不可用')
    expect(search.hasMore.value).toBe(true)
    await search.loadMore()
    expect(get).toHaveBeenLastCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '应用', offset: 1, limit: 1 },
    })
    expect(search.items.value.map((item) => item.app_id)).toEqual(['1001', '1002'])
    expect(search.errorMessage.value).toBe('')
    scope.stop()
  })

  it('取消和已处理认证错误都不会显示搜索错误', async () => {
    const handled = { response: { status: 401, data: { code: 500, message: 'private-token' } } }
    markAuthInvalidationHandled(handled)
    const http = axios.create()
    http.get = vi
      .fn()
      .mockRejectedValueOnce(new CanceledError('private-token'))
      .mockRejectedValueOnce(handled)
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http }))!
    await search.search()
    expect(search.errorMessage.value).toBe('')
    await search.search()
    expect(search.errorMessage.value).toBe('')
    expect(search.loading.value).toBe(false)
    scope.stop()
  })

  it.each(['success', 'failure'])('较早搜索%s不能覆盖新搜索结果和错误状态', async (result) => {
    const oldRequest = createDeferred<unknown>()
    const http = axios.create()
    http.get = vi
      .fn()
      .mockReturnValueOnce(oldRequest.promise)
      .mockResolvedValueOnce(searchResponse('new'))
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http }))!
    search.keyword.value = '旧'
    const oldSearch = search.search()
    search.keyword.value = '新'
    await search.search()
    oldRequest.resolve(
      result === 'success'
        ? searchResponse('old')
        : { data: { code: 500, message: 'private-token', data: null } },
    )
    await oldSearch
    expect(search.items.value.map((item) => item.app_id)).toEqual(['new'])
    expect(search.keyword.value).toBe('新')
    expect(search.errorMessage.value).toBe('')
    expect(search.loading.value).toBe(false)
    scope.stop()
  })

  it('旧请求完成不能提前结束新请求的 loading', async () => {
    const first = createDeferred<ReturnType<typeof searchResponse>>()
    const second = createDeferred<ReturnType<typeof searchResponse>>()
    const http = axios.create()
    http.get = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http }))!
    const firstSearch = search.search()
    const secondSearch = search.search()
    first.resolve(searchResponse('old'))
    await firstSearch
    expect(search.loading.value).toBe(true)
    expect(search.items.value).toEqual([])
    second.resolve(searchResponse('new'))
    await secondSearch
    expect(search.loading.value).toBe(false)
    expect(search.items.value[0]?.app_id).toBe('new')
    scope.stop()
  })

  it('旧分页完成不能把结果追加到新搜索', async () => {
    const pendingPage = createDeferred<ReturnType<typeof searchResponse>>()
    const http = axios.create()
    http.get = vi
      .fn()
      .mockResolvedValueOnce(searchResponse('old-1', 2))
      .mockReturnValueOnce(pendingPage.promise)
      .mockResolvedValueOnce(searchResponse('new'))
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http, pageSize: 1 }))!
    await search.search()
    const loadMore = search.loadMore()
    search.keyword.value = '新'
    await search.search()
    pendingPage.resolve(searchResponse('old-2', 2))
    await loadMore
    expect(search.items.value.map((item) => item.app_id)).toEqual(['new'])
    expect(search.total.value).toBe(1)
    scope.stop()
  })

  it.each(['reset', 'dispose'])('%s 使在途搜索失效', async (action) => {
    const pending = createDeferred<ReturnType<typeof searchResponse>>()
    const http = axios.create()
    http.get = vi.fn().mockReturnValueOnce(pending.promise)
    const scope = effectScope()
    const search = scope.run(() => useV115AppIdSearch({ http }))!
    const request = search.search()
    if (action === 'reset') search.reset()
    else scope.stop()
    pending.resolve(searchResponse('late'))
    await request
    expect(search.items.value).toEqual([])
    expect(search.total.value).toBe(0)
    expect(search.errorMessage.value).toBe('')
    scope.stop()
  })
})
