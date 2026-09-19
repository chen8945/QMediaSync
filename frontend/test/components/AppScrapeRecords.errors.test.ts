// @vitest-environment happy-dom
import axios, { AxiosError, CanceledError, type InternalAxiosRequestConfig } from 'axios'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import AppScrapeRecords from '@/components/AppScrapeRecords.vue'
import { httpKey } from '@/http/client'
import { markAuthInvalidationHandled } from '@/http/errors'
import type { ScrapeRecord } from '@/api/scrapeRecords'
import { createDeferred } from '../support/deferred'

vi.mock('@/composables/useRealtimeEvents', () => ({ useRealtimeEvent: vi.fn() }))

const makeRecord = (
  id: number,
  status: ScrapeRecord['status'] = 'scrape_failed',
): ScrapeRecord => ({
  id,
  status,
  type: 'movie',
  path: '/media',
  file_name: `movie-${id}.mkv`,
  media_name: `电影 ${id}`,
  original_name: '',
  year: 2024,
  tmdb_id: 0,
  season_number: -1,
  episode_number: -1,
  failed_reason: '无法识别',
  created_at: 0,
  updated_at: 0,
  scanned_at: 0,
  scraped_at: 0,
  renamed_at: 0,
  audio_count: 0,
  subtitle_count: 0,
  resolution: '',
  resolution_level: '',
  is_hdr: false,
  category_name: '',
  new_dest_path: '',
  new_dest_name: '',
  path_is_scraping: false,
  source_full_path: '',
  dest_full_path: '',
  source_type: 'local',
  rename_type: '',
  scrape_type: '',
})

const records = [makeRecord(2), makeRecord(7), makeRecord(9, 'scraped')]
const result = {
  tmdb_id: 550,
  title: '找到的电影',
  original_title: 'Movie',
  year: 2024,
  poster_url: '',
  overview: '',
}
const success = (data: unknown = null, message = '') => ({ code: 200, message, data })
const failureBody = {
  code: 500,
  message: '刮削请求失败，请稍后重试',
  data: { field_errors: { id: 'private details' } },
}

function forbidden(config: InternalAxiosRequestConfig, errorCode: string) {
  return new AxiosError(
    'Request failed with status code 403',
    'ERR_BAD_REQUEST',
    config,
    {},
    {
      config,
      data: { code: 500, message: 'private-secret', error_code: errorCode, data: null },
      status: 403,
      statusText: 'Forbidden',
      headers: {},
    },
  )
}

const wrappers: VueWrapper[] = []
let messageError: ReturnType<typeof vi.spyOn>
let messageSuccess: ReturnType<typeof vi.spyOn>
let consoleError: ReturnType<typeof vi.spyOn>

async function createWrapper(
  respond?: (config: InternalAxiosRequestConfig) => unknown | Promise<unknown>,
) {
  const adapter = vi.fn(async (config: InternalAxiosRequestConfig) => ({
    config,
    data:
      (await respond?.(config)) ??
      (config.url?.endsWith('/scrape/records') && config.method === 'get'
        ? success({ list: records, total: records.length })
        : config.url?.endsWith('/tmdb-search')
          ? success([result])
          : success()),
    status: 200,
    statusText: 'OK',
    headers: {},
  }))
  const http = axios.create({ adapter })
  const wrapper = shallowMount(AppScrapeRecords, {
    global: {
      plugins: [createPinia()],
      provide: { [httpKey]: http },
      renderStubDefaultSlot: true,
      directives: { loading: {} },
      stubs: {
        Loading: true,
        ElButton: {
          props: ['disabled', 'loading'],
          template: '<button :disabled="disabled || loading"><slot /></button>',
        },
        ElInput: {
          props: ['modelValue', 'placeholder'],
          emits: ['update:modelValue'],
          template:
            '<input :value="modelValue" :placeholder="placeholder" @input="$emit(\'update:modelValue\', $event.target.value)" />',
        },
        ElDialog: {
          props: ['modelValue', 'title'],
          template:
            '<section v-if="modelValue" role="dialog" :aria-label="title"><slot /><slot name="footer" /></section>',
        },
        ResponsiveRecordTable: {
          props: ['rows', 'actions'],
          emits: ['selection-change', 'action'],
          template: `
            <div>
              <button @click="$emit('selection-change', rows)">选择记录</button>
              <div v-for="row in rows" :key="row.id">
                <template v-for="action in actions" :key="action.key">
                  <button v-if="!action.visible || action.visible(row)"
                    @click="$emit('action', { actionKey: action.key, row })"
                  >{{ action.label }} {{ row.id }}</button>
                </template>
              </div>
            </div>
          `,
        },
      },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return {
    wrapper,
    adapter,
    listCalls: () =>
      adapter.mock.calls.filter(
        ([config]) => config.url?.endsWith('/scrape/records') && config.method === 'get',
      ).length,
  }
}

async function click(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button').find((item) => item.text() === label)
  expect(button, `按钮 ${label}`).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

async function openSearch(wrapper: VueWrapper, id = 2) {
  await click(wrapper, `重新识别 ${id}`)
  await wrapper.find('input[placeholder="请输入影视剧名称"]').setValue('保留的搜索词')
  await wrapper.find('input[placeholder="年份"]').setValue('2023')
}

function expectNoSuccessOrSecrets() {
  expect(messageSuccess).not.toHaveBeenCalled()
  expect(JSON.stringify(messageError.mock.calls)).not.toContain('private-secret')
  expect(JSON.stringify(consoleError.mock.calls)).not.toContain('private-secret')
  for (const [, diagnostics] of consoleError.mock.calls) {
    expect(Object.keys(diagnostics)).toEqual(expect.arrayContaining(['method', 'path', 'status']))
    expect(
      Object.keys(diagnostics).every((key) =>
        ['method', 'path', 'status', 'errorCode'].includes(key),
      ),
    ).toBe(true)
  }
}

beforeEach(() => {
  sessionStorage.clear()
  messageError = vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  messageSuccess = vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  // Element Plus 声明为输入结果与 Action 的交叉类型，但普通确认框实际返回字符串 Action。
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
})

afterEach(() => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  vi.restoreAllMocks()
})

describe('AppScrapeRecords 请求失败', () => {
  it('搜索业务失败保留输入，不展示成功或空结果状态', async () => {
    const { wrapper, adapter } = await createWrapper((config) =>
      config.url?.endsWith('/tmdb-search') ? failureBody : undefined,
    )
    await openSearch(wrapper)
    await click(wrapper, '搜索')

    expect(wrapper.find('input[placeholder="请输入影视剧名称"]').element).toHaveProperty(
      'value',
      '保留的搜索词',
    )
    expect(wrapper.find('input[placeholder="年份"]').element).toHaveProperty('value', '2023')
    expect(wrapper.find('.result-item').exists()).toBe(false)
    expect(wrapper.find('.empty-results').exists()).toBe(false)
    expect(messageError).toHaveBeenCalledWith('搜索失败：刮削请求失败，请稍后重试')
    const searchConfig = adapter.mock.calls.find(([config]) =>
      config.url?.endsWith('/tmdb-search'),
    )?.[0]
    expect(searchConfig).toMatchObject({
      params: { type: 'movie', name: '保留的搜索词', year: '2023' },
      timeout: 30000,
    })
    expectNoSuccessOrSecrets()
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('搜索 %s 显示准确原因并保留输入', async (code, message) => {
    const { wrapper } = await createWrapper((config) => {
      if (config.url?.endsWith('/tmdb-search')) throw forbidden(config, code)
    })
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    expect(messageError).toHaveBeenCalledOnce()
    expect(messageError).toHaveBeenCalledWith(expect.stringContaining(`搜索失败：${message}`))
    expect(wrapper.find('input[placeholder="请输入影视剧名称"]').element).toHaveProperty(
      'value',
      '保留的搜索词',
    )
    expectNoSuccessOrSecrets()
  })

  it.each(['business', 'REQUEST_ORIGIN_INVALID', 'CSRF_TOKEN_INVALID'])(
    '选择结果 %s 失败保持弹窗、结果和输入，不刷新列表',
    async (kind) => {
      const { wrapper, listCalls, adapter } = await createWrapper((config) => {
        if (!config.url?.endsWith('/re-scrape')) return
        if (kind === 'business') return failureBody
        throw forbidden(config, kind)
      })
      await openSearch(wrapper)
      await click(wrapper, '搜索')
      await click(wrapper, '选择')
      expect(wrapper.find('[role="dialog"][aria-label="重新识别"]').exists()).toBe(true)
      expect(wrapper.find('.result-item').text()).toContain('找到的电影')
      expect(wrapper.find('input[placeholder="请输入影视剧名称"]').element).toHaveProperty(
        'value',
        '保留的搜索词',
      )
      expect(listCalls()).toBe(1)
      expect(messageError).toHaveBeenCalledOnce()
      expect(messageError).toHaveBeenCalledWith(expect.stringContaining('重新识别失败：'))
      const request = adapter.mock.calls.find(([config]) => config.url?.endsWith('/re-scrape'))?.[0]
      expect(request?.timeout).toBe(60000)
      expect(JSON.parse(request!.data)).toEqual({ id: 2, tmdb_id: 550, season: -1, episode: -1 })
      expectNoSuccessOrSecrets()
    },
  )

  it.each([
    ['删除所选记录', '/scrape/records?ids=2,7,9'],
    ['重新整理所选', '/scrape/rename-failed?ids=2,7,9'],
    ['清除失败记录', '/scrape/clear-failed'],
    ['清空记录', '/scrape/truncate-all'],
    ['标记已整理 9', '/scrape/finish'],
  ])('%s 业务失败不清空选择、不刷新列表', async (label, endpoint) => {
    const { wrapper, listCalls } = await createWrapper((config) =>
      config.url?.endsWith(endpoint) ? failureBody : undefined,
    )
    await click(wrapper, '选择记录')
    await click(wrapper, label)
    expect(wrapper.find('.selected-count').text()).toBe('已选择 3 条记录')
    expect(listCalls()).toBe(1)
    expect(messageError).toHaveBeenCalledOnce()
    expectNoSuccessOrSecrets()
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('删除 %s 不误报网络故障或清空选择', async (code, message) => {
    const { wrapper, listCalls } = await createWrapper((config) => {
      if (config.method === 'delete') throw forbidden(config, code)
    })
    await click(wrapper, '选择记录')
    await click(wrapper, '删除所选记录')
    expect(wrapper.find('.selected-count').text()).toBe('已选择 3 条记录')
    expect(listCalls()).toBe(1)
    expect(messageError).toHaveBeenCalledWith(expect.stringContaining(`删除失败：${message}`))
    expectNoSuccessOrSecrets()
  })

  it.each(['cancelled', 'handled401'])('%s 不重复提示、不执行成功动作', async (kind) => {
    const { wrapper, listCalls } = await createWrapper((config) => {
      if (!config.url?.endsWith('/re-scrape')) return
      if (kind === 'cancelled') throw new CanceledError('cancelled', config)
      const error = new AxiosError(
        'Unauthorized',
        'ERR_BAD_REQUEST',
        config,
        {},
        {
          config,
          data: { code: 401, error_code: 'SESSION_INVALID', data: null },
          status: 401,
          statusText: 'Unauthorized',
          headers: {},
        },
      )
      markAuthInvalidationHandled(error)
      throw error
    })
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    await click(wrapper, '选择')
    expect(messageError).not.toHaveBeenCalled()
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
    expect(listCalls()).toBe(1)
    expect(wrapper.find('.result-item').exists()).toBe(true)
  })

  it('重新识别超时说明结果未确认，且不自动重试', async () => {
    const { wrapper, adapter, listCalls } = await createWrapper((config) => {
      if (config.url?.endsWith('/re-scrape')) throw new AxiosError('timeout', 'ETIMEDOUT', config)
    })
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    await click(wrapper, '选择')
    expect(messageError).toHaveBeenCalledWith(expect.stringContaining('操作结果尚未确认'))
    expect(
      adapter.mock.calls.filter(([config]) => config.url?.endsWith('/re-scrape')),
    ).toHaveLength(1)
    expect(listCalls()).toBe(1)
    expect(messageSuccess).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'])(
    '关闭旧搜索弹窗后，迟到的 %s 不会影响另一条记录',
    async (kind) => {
      const pending = createDeferred<ReturnType<typeof success>>()
      const { wrapper } = await createWrapper((config) =>
        config.url?.endsWith('/tmdb-search') ? pending.promise : undefined,
      )
      await openSearch(wrapper)
      await click(wrapper, '搜索')
      await click(wrapper, '取消')
      await click(wrapper, '重新识别 7')
      pending.resolve(kind === 'success' ? success([result]) : failureBody)
      await flushPromises()
      expect(wrapper.find('input[placeholder="请输入影视剧名称"]').element).toHaveProperty(
        'value',
        '电影 7',
      )
      expect(wrapper.find('.result-item').exists()).toBe(false)
      expect(messageError).not.toHaveBeenCalled()
    },
  )

  it.each(['success', 'failure'])('旧重新识别 %s 不能关闭新的弹窗或触发刷新', async (kind) => {
    const pending = createDeferred<ReturnType<typeof success>>()
    const { wrapper, listCalls } = await createWrapper((config) =>
      config.url?.endsWith('/re-scrape') ? pending.promise : undefined,
    )
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    await click(wrapper, '选择')
    await click(wrapper, '取消')
    await click(wrapper, '重新识别 7')
    pending.resolve(kind === 'success' ? success() : failureBody)
    await flushPromises()
    expect(wrapper.find('input[placeholder="请输入影视剧名称"]').element).toHaveProperty(
      'value',
      '电影 7',
    )
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(messageError).not.toHaveBeenCalled()
    expect(listCalls()).toBe(1)
  })

  it('切换筛选后旧删除响应不能清空当前选择或重复刷新', async () => {
    const pending = createDeferred<ReturnType<typeof success>>()
    const { wrapper, listCalls } = await createWrapper((config) =>
      config.method === 'delete' ? pending.promise : undefined,
    )
    await click(wrapper, '选择记录')
    await click(wrapper, '删除所选记录')
    await wrapper.find('input[placeholder="按文件名模糊搜索"]').setValue('新筛选')
    await click(wrapper, '筛选')
    await click(wrapper, '选择记录')
    pending.resolve(success())
    await flushPromises()
    expect(wrapper.find('.selected-count').text()).toBe('已选择 3 条记录')
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(messageError).not.toHaveBeenCalled()
    expect(listCalls()).toBe(2)
  })

  it.each([
    ['network', '无法获取服务器响应'],
    ['program', '请稍后重试'],
  ])('搜索 %s 使用准确分类和安全提示', async (kind, message) => {
    const { wrapper } = await createWrapper((config) => {
      if (!config.url?.endsWith('/tmdb-search')) return
      if (kind === 'network') throw new AxiosError('private-secret', 'ERR_NETWORK', config)
      throw new Error('private-secret')
    })
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    expect(messageError).toHaveBeenCalledWith(expect.stringContaining(`搜索失败：${message}`))
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(JSON.stringify(consoleError.mock.calls)).not.toContain('private-secret')
  })

  it('重新识别成功保留服务端后续处理说明，关闭弹窗并刷新', async () => {
    const message = '下次扫描会按新的名称和年份重新刮削'
    const { wrapper, listCalls } = await createWrapper((config) =>
      config.url?.endsWith('/re-scrape') ? success(null, message) : undefined,
    )
    await openSearch(wrapper)
    await click(wrapper, '搜索')
    await click(wrapper, '选择')
    expect(messageSuccess).toHaveBeenCalledWith(message)
    expect(wrapper.find('[role="dialog"][aria-label="重新识别"]').exists()).toBe(false)
    expect(listCalls()).toBe(2)
  })

  it('导出窗口被拦截时提示原因，不误报下载成功', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    const { wrapper } = await createWrapper()
    await click(wrapper, '选择记录')
    await click(wrapper, '导出识别错误')
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(messageError).toHaveBeenCalledExactlyOnceWith(
      '浏览器阻止了下载窗口，请允许弹出窗口后重试',
    )
  })

  it('导出继续通过新窗口下载，不新增 Axios 下载请求', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(window)
    const { wrapper, adapter } = await createWrapper()
    await click(wrapper, '选择记录')
    await click(wrapper, '导出识别错误')
    expect(open).toHaveBeenCalledWith('/api/scrape/records/export?ids=2,7,9', '_blank')
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(messageSuccess).toHaveBeenCalledWith('导出请求已发送')
  })
})
