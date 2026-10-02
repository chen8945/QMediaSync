import { browseSortOptions } from '../support/browseSort'
import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElDropdown, ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { createPinia } from 'pinia'
import { defineComponent, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppFileManager from '@/components/AppFileManager.vue'
import AppSyncRecords from '@/components/AppSyncRecords.vue'
import { useRealtimeEvent } from '@/composables/useRealtimeEvents'
import DirectorySelector from '@/components/DirectorySelector.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { createDeferred } from '../support/deferred'

vi.mock('@/composables/useRealtimeEvents', () => ({ useRealtimeEvent: vi.fn() }))

const DialogStub = defineComponent({
  props: { modelValue: Boolean, title: String },
  template:
    '<section v-if="modelValue" role="dialog" :aria-label="title"><slot /><slot name="footer" /></section>',
})
const recordTableStub = defineComponent({
  props: ['rows', 'showSelection', 'columns'],
  emits: ['action', 'selectionChange'],
  template: `<div>
    <div v-for="row in rows" :key="row.id" class="record-row">
      <span>{{ row.remote_path }}</span>
      <slot name="cell-status" :row="row" />
      <span v-for="column in columns" :key="column.key" :data-field="column.key">{{ column.detailField?.value(row) }}</span>
      <button @click="$emit('action', { actionKey: 'delete', row })">删除记录</button>
    </div>
    <button v-if="showSelection" @click="$emit('selectionChange', rows)">选择全部记录</button>
  </div>`,
})
const pageStub = defineComponent({
  props: ['total', 'currentPage'],
  emits: ['currentChange'],
  template: '<button @click="$emit(\'currentChange\', 2)">第二页</button>',
})
const envelope = (data: unknown, code = 200, message = '') => ({ code, data, message })
const file = { id: 'file', name: 'movie.mkv', size: 1024, modified_time: 1, is_directory: false }
const filesPage = { list: [file], total: 1, page: 1, page_size: 50 }
const directory = { id: '/strm', name: '目标目录', path: '/strm' }
const record = {
  id: 1,
  created_at: 1,
  finish_at: 2,
  status: 2,
  sub_status: 0,
  total: 3,
  new_strm: 2,
  new_meta: 0,
  new_upload: 0,
  local_path: '/strm',
  remote_path: '原记录',
  fail_reason: '',
}
const accounts = [
  {
    id: 1,
    name: '媒体',
    username: '媒体',
    user_id: '1',
    source_type: '115',
    authorized: true,
    created_at: 1,
  },
  {
    id: 2,
    name: '第二账号',
    username: '第二账号',
    user_id: '2',
    source_type: 'openlist',
    authorized: true,
    created_at: 1,
  },
]
const failure = (errorCode: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, error_code: errorCode, message: 'private SQL token' },
    config: { method: 'post', url: '/api/path/create?token=private' },
  })
const handled401 = () => {
  const error = failure('AUTHENTICATION_REQUIRED', 401)
  markAuthInvalidationHandled(error)
  return error
}
const button = (wrapper: VueWrapper, label: string) =>
  wrapper.findAll('button').find((item) => item.text() === label)!
const click = async (wrapper: VueWrapper, label: string) => {
  await button(wrapper, label).trigger('click')
  await flushPromises()
}
const mountPage = async (kind: 'files' | 'records' | 'directories' = 'files') => {
  const readReply = vi.fn(
    async (url: string, params?: Record<string, unknown>): Promise<unknown> => {
      if (url.endsWith('/path/sort-options'))
        return envelope(
          browseSortOptions(
            String(params?.source_type),
            params?.scope === 'directories' ? 'directories' : 'files',
          ),
        )
      if (url.endsWith('/account/list')) return envelope(accounts)
      if (url.endsWith('/path/files')) return envelope(filesPage)
      if (url.endsWith('/path/list')) return envelope(params?.parent_id ? [] : [directory])
      return envelope({ records: [record], total: 30 })
    },
  )
  const writeReply = vi.fn(async (): Promise<unknown> => envelope(null))
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await readReply(config.url, config.params) : await writeReply(),
  }))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  await router.isReady()
  sessionStorage.setItem(
    'qmediasync-page-state',
    JSON.stringify({
      'file-manager': {
        currentPage: 1,
        pageSize: 50,
        filters: { selectedAccountId: 1, currentPath: '', pathItems: '[]' },
        expandedRowKeys: [],
        scrollTop: 0,
      },
    }),
  )
  const wrapper = mount(
    kind === 'files' ? AppFileManager : kind === 'records' ? AppSyncRecords : DirectorySelector,
    {
      props: kind === 'directories' ? { sourceType: 'local' } : {},
      global: {
        plugins: [createPinia(), router],
        provide: { [httpKey]: axios.create({ adapter }) },
        stubs: {
          PageHeader: true,
          ElDialog: DialogStub,
          ResponsiveRecordTable: recordTableStub,
          ResponsivePagination: pageStub,
        },
      },
    },
  )
  await flushPromises()
  return { wrapper, adapter, readReply, writeReply }
}
const openCreate = async (wrapper: VueWrapper) => {
  await click(wrapper, '新建文件夹')
  await wrapper.get('input[placeholder="请输入文件夹名称"]').setValue(' 保留名称 ')
}
const fileAction = async (wrapper: VueWrapper, command: string) => {
  wrapper.findComponent(ElDropdown).vm.$emit('command', command)
  await flushPromises()
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  sessionStorage.clear()
  vi.restoreAllMocks()
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

describe('file manager HTTP feedback', () => {
  it('keeps the create dialog and name on HTTP 200 business failure and allows retry', async () => {
    const { wrapper, writeReply, adapter } = await mountPage()
    writeReply.mockResolvedValueOnce(envelope(null, 500, '创建目录失败：名称已存在'))
    await openCreate(wrapper)
    await click(wrapper, '确定')
    expect(ElMessage.error).toHaveBeenCalledWith('创建目录失败：名称已存在')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(
      (wrapper.get('input[placeholder="请输入文件夹名称"]').element as HTMLInputElement).value,
    ).toBe(' 保留名称 ')
    expect(
      adapter.mock.calls.filter(([config]) => config.url?.endsWith('/path/files')),
    ).toHaveLength(1)
    await click(wrapper, '确定')
    expect(ElMessage.success).toHaveBeenCalledWith('创建文件夹成功')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it.each(['REQUEST_ORIGIN_INVALID', 'CSRF_TOKEN_INVALID'])(
    'classifies %s without closing create form',
    async (code) => {
      const { wrapper, writeReply } = await mountPage()
      writeReply.mockRejectedValueOnce(failure(code))
      await openCreate(wrapper)
      await click(wrapper, '确定')
      expect(ElMessage.error).toHaveBeenCalledWith(
        expect.stringContaining(code === 'CSRF_TOKEN_INVALID' ? '刷新页面' : '转发配置'),
      )
      expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
    },
  )

  it.each([new CanceledError(), handled401()])(
    'silences cancellation and handled auth failures while retaining create input',
    async (error) => {
      const { wrapper, writeReply } = await mountPage()
      writeReply.mockRejectedValueOnce(error)
      await openCreate(wrapper)
      await click(wrapper, '确定')
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(
        (wrapper.get('input[placeholder="请输入文件夹名称"]').element as HTMLInputElement).value,
      ).toBe(' 保留名称 ')
    },
  )

  it('shows uncertain result on write timeout and does not retry automatically', async () => {
    const { wrapper, writeReply } = await mountPage()
    writeReply.mockRejectedValueOnce(
      new AxiosError('private', 'ETIMEDOUT', { method: 'post', url: '/x', headers: {} } as never),
    )
    await openCreate(wrapper)
    await click(wrapper, '确定')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('操作结果尚未确认'))
    expect(writeReply).toHaveBeenCalledTimes(1)
  })

  it('rejects deletion failure without refreshing or reporting success', async () => {
    const { wrapper, adapter, writeReply } = await mountPage()
    writeReply.mockResolvedValueOnce(envelope(null, 500, '文件正在使用'))
    await fileAction(wrapper, 'DELETE')
    expect(ElMessage.error).toHaveBeenCalledWith('文件正在使用')
    expect(ElMessage.success).not.toHaveBeenCalled()
    const deletion = adapter.mock.calls.find(([config]) => config.method === 'delete')![0]
    expect(deletion.params).toEqual({ parent_id: '', file_id: 'file', account_id: 1 })
    expect(deletion.data).toBeUndefined()
    expect(
      adapter.mock.calls.filter(([config]) => config.url?.endsWith('/path/files')),
    ).toHaveLength(1)
  })

  it('reports refresh failure as a read failure after successful deletion', async () => {
    const { wrapper, readReply } = await mountPage()
    readReply.mockResolvedValueOnce(envelope(null, 500, '网盘接口限流'))
    await fileAction(wrapper, 'DELETE')
    expect(ElMessage.success).toHaveBeenCalledWith('删除成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('加载文件列表失败：网盘接口限流')
    expect(wrapper.text()).toContain('movie.mkv')
  })

  it('submits STRM only once while pending and retains the directory for retry after failure', async () => {
    const { wrapper, writeReply, adapter } = await mountPage()
    const pending = createDeferred<unknown>()
    writeReply.mockReturnValueOnce(pending.promise)
    await fileAction(wrapper, 'STRM_GENERATE')
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')
    await click(wrapper, '选择')
    expect(writeReply).toHaveBeenCalledTimes(1)
    expect(
      adapter.mock.calls.filter(([config]) => config.url?.endsWith('/sync/manual')),
    ).toHaveLength(1)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()

    pending.resolve(envelope(null, 500, '目录未配置'))
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledWith('目录未配置')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(button(wrapper, '选择').attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('/strm/movie.mkv')
    await click(wrapper, '选择')
    expect(ElMessage.success).toHaveBeenCalledWith('STRM 生成任务已提交')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    const requests = adapter.mock.calls.filter(([config]) => config.url?.endsWith('/sync/manual'))
    expect(requests).toHaveLength(2)
    for (const [config] of requests)
      expect(JSON.parse(config.data)).toEqual({
        path_id: 'file',
        target_path: '/strm',
        account_id: 1,
      })
  })

  it('ignores a stale file-read failure after switching accounts', async () => {
    const { wrapper, readReply } = await mountPage()
    const old = createDeferred<unknown>()
    readReply
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce(envelope(browseSortOptions('openlist')))
      .mockResolvedValueOnce(
        envelope({ ...filesPage, list: [{ ...file, name: 'new-account.mkv' }] }),
      )
    await click(wrapper, '刷新')
    await wrapper.findAll('.account-item')[1]!.trigger('click')
    old.resolve(envelope(null, 500, 'private old read'))
    await flushPromises()
    expect(wrapper.text()).toContain('new-account.mkv')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})

describe('directory selector HTTP feedback', () => {
  it('keeps folder name and selection on create failure', async () => {
    const { wrapper, writeReply } = await mountPage('directories')
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await openCreate(wrapper)
    writeReply.mockResolvedValueOnce(envelope(null, 500, '创建目录失败：名称已存在'))
    await click(wrapper, '确定')
    expect(ElMessage.error).toHaveBeenCalledWith('创建目录失败：名称已存在')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(
      (wrapper.get('input[placeholder="请输入文件夹名称"]').element as HTMLInputElement).value,
    ).toBe(' 保留名称 ')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
  })

  it.each([new CanceledError(), handled401()])(
    'does not replace a cancelled/auth child refresh with a generic error',
    async (error) => {
      const { wrapper, readReply } = await mountPage('directories')
      await wrapper.get('[role="treeitem"]').trigger('click')
      await flushPromises()
      readReply.mockResolvedValueOnce(envelope([directory])).mockRejectedValueOnce(error)
      await click(wrapper, '刷新')
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(wrapper.text()).toContain('目标目录')
    },
  )

  it('classifies child load failure and keeps retry available', async () => {
    const { wrapper, readReply } = await mountPage('directories')
    readReply.mockRejectedValueOnce(new AxiosError('private', 'ERR_NETWORK'))
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('无法获取服务器响应'))
    expect(wrapper.find('[role="treeitem"]').attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('重试')
  })
})

describe('sync records HTTP feedback', () => {
  it('rejects single deletion business failure without reporting success', async () => {
    const { wrapper, writeReply, readReply } = await mountPage('records')
    writeReply.mockResolvedValueOnce(envelope(null, 500, '记录不存在'))
    await click(wrapper, '删除记录')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('记录不存在')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(readReply).toHaveBeenCalledTimes(1)
  })

  it('keeps batch selection after partial cleanup failure and uses safe complete message', async () => {
    const { wrapper, writeReply, adapter } = await mountPage('records')
    await wrapper.get('input[type="checkbox"]').setValue(true)
    await click(wrapper, '选择全部记录')
    writeReply.mockResolvedValueOnce(
      envelope(
        { deleted_ids: [], failures: [{ id: 1, reason: 'private log path' }] },
        500,
        '部分同步记录删除失败',
      ),
    )
    await click(wrapper, '批量删除')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('部分同步记录删除失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(button(wrapper, '批量删除').attributes('disabled')).toBeUndefined()
    const request = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(request.timeout).toBe(60000)
    expect(request.headers.get('Content-Type')).toBe('application/json')
    await click(wrapper, '批量删除')
    expect(ElMessage.success).toHaveBeenCalledWith('批量删除成功')
    expect(button(wrapper, '批量删除').attributes('disabled')).toBeDefined()
  })

  it.each([new CanceledError(), handled401()])(
    'silences cancellation and handled 401 during deletion',
    async (error) => {
      const { wrapper, writeReply } = await mountPage('records')
      writeReply.mockRejectedValueOnce(error)
      await click(wrapper, '删除记录')
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
    },
  )

  it('reports successful deletion separately from a failed snapshot reload', async () => {
    const { wrapper, readReply } = await mountPage('records')
    readReply.mockResolvedValueOnce(envelope(null, 500, '数据库繁忙'))
    await click(wrapper, '删除记录')
    expect(ElMessage.success).toHaveBeenCalledWith('删除成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('加载同步记录失败：数据库繁忙')
    expect(wrapper.text()).toContain('原记录')
  })

  it('ignores stale read failure when a newer page is pending', async () => {
    const { wrapper, readReply } = await mountPage('records')
    const old = createDeferred<unknown>()
    readReply
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce(
        envelope({ records: [{ ...record, remote_path: '新页面' }], total: 30 }),
      )
    await click(wrapper, '删除记录')
    await click(wrapper, '第二页')
    old.resolve(envelope(null, 500, 'private old SQL'))
    await flushPromises()
    await nextTick()
    expect(wrapper.text()).toContain('新页面')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})

describe('同步记录后台状态', () => {
  it('HTTP 刷新、后台事件和重连保留生成结果并更新总耗时', async () => {
    vi.mocked(useRealtimeEvent).mockClear()
    const { wrapper, readReply } = await mountPage('records')
    const field = (key: string) => wrapper.get(`[data-field="${key}"]`).text()
    expect(field('generation_duration')).toBe('1 秒')
    expect(field('ledger_status')).toBe('未记录')
    expect(field('total_duration')).toBe('未记录')
    const handlers = vi.mocked(useRealtimeEvent).mock.calls
    const reconnect = handlers.find(([name]) => name === 'sync_task_created')![2]!
    const update = handlers.find(([name]) => name === 'sync_task_updated')![1]
    const pending = { ...record, ledger_status: 'pending' }
    readReply.mockResolvedValueOnce(envelope({ records: [pending], total: 1 }))
    reconnect()
    await flushPromises()
    expect(wrapper.text()).toContain('等待后台更新记录')
    expect(field('total_duration')).toBe('统计中')
    update({
      sync_id: 1,
      sequence: 1,
      status: 1,
      total: 0,
      new_strm: 0,
      finish_at: 0,
      ledger_status: 'failed',
      ledger_finished_at: 10,
      ledger_error: '写入失败',
    })
    await flushPromises()
    expect(field('status')).toBe('已完成')
    expect(field('stats')).toContain('STRM 2')
    expect(field('generation_duration')).toBe('1 秒')
    expect(field('total_duration')).toBe('9 秒')
    expect(field('ledger_error')).toBe('写入失败')
    const failed = {
      ...record,
      ledger_status: 'failed',
      ledger_finished_at: 10,
      ledger_error: '写入失败',
    }
    readReply.mockResolvedValueOnce(envelope({ records: [failed], total: 1 }))
    reconnect()
    await flushPromises()
    expect(field('generation_duration')).toBe('1 秒')
    expect(field('total_duration')).toBe('9 秒')
    expect(wrapper.text()).toContain('记录更新失败')
  })
})

describe('同步记录 HTTP 快照与实时事件交错', () => {
  const snapshot = (ledger_status: string, ledger_finished_at?: number) =>
    envelope({ records: [{ ...record, ledger_status, ledger_finished_at }], total: 30 })
  const mountRecords = async () => {
    vi.mocked(useRealtimeEvent).mockClear()
    const page = await mountPage('records')
    const handlers = vi.mocked(useRealtimeEvent).mock.calls
    const refresh = handlers.find(([name]) => name === 'strm_sync_task_start')![1]
    const update = handlers.find(([name]) => name === 'sync_task_updated')![1]
    const patch = (ledger_status: string, sequence: number) =>
      update({
        ...record,
        sync_id: record.id,
        sync_path_id: 2,
        sequence,
        ledger_status,
        ledger_finished_at: ledger_status === 'running' ? null : 10,
        ledger_error: ledger_status === 'failed' ? '事务写入失败' : '',
      })
    return {
      ...page,
      refresh: () => refresh({}),
      patch,
      field: (key: string) => page.wrapper.get(`[data-field="${key}"]`).text(),
    }
  }

  it.each([
    ['completed', '记录更新完成'],
    ['failed', '记录更新失败'],
  ])('旧快照合并 %s 事件并保留终态，不补读', async (status, label) => {
    const { readReply, refresh, patch, field } = await mountRecords()
    const old = createDeferred<unknown>()
    readReply.mockReturnValueOnce(old.promise)
    refresh()
    patch(status, 1)
    await flushPromises()
    expect(field('ledger_status')).toBe(label)
    expect(field('generation_duration')).toBe('1 秒')
    expect(field('total_duration')).toBe('9 秒')
    expect(readReply).toHaveBeenCalledTimes(2)

    old.resolve(snapshot('running'))
    await flushPromises()
    expect(field('ledger_status')).toBe(label)
    expect(field('total_duration')).toBe('9 秒')
    if (status === 'failed') expect(field('ledger_error')).toBe('事务写入失败')
    expect(readReply).toHaveBeenCalledTimes(2)
  })

  it('在途请求收到多个有效事件后合并快照，不补读', async () => {
    const { readReply, refresh, patch, field } = await mountRecords()
    const old = createDeferred<unknown>()
    readReply.mockReturnValueOnce(old.promise).mockResolvedValueOnce(snapshot('completed', 10))
    refresh()
    patch('running', 1)
    patch('running', 2)
    patch('completed', 3)
    await flushPromises()
    expect(readReply).toHaveBeenCalledTimes(2)
    expect(field('ledger_status')).toBe('记录更新完成')

    old.resolve(snapshot('running'))
    await flushPromises()
    expect(field('ledger_status')).toBe('记录更新完成')
    expect(readReply).toHaveBeenCalledTimes(2)
  })

  it('普通生成进度连续到达时只合并状态，旧 HTTP 响应不回退生成终态', async () => {
    const { readReply, refresh, field } = await mountRecords()
    const update = vi
      .mocked(useRealtimeEvent)
      .mock.calls.find(([name]) => name === 'sync_task_updated')![1]
    readReply.mockResolvedValueOnce(envelope({ records: [{ ...record, status: 1 }], total: 1 }))
    refresh()
    await flushPromises()
    const old = createDeferred<unknown>()
    readReply.mockClear().mockReturnValueOnce(old.promise)
    refresh()
    for (let sequence = 1; sequence <= 6; sequence++) {
      update({
        sync_id: 1,
        sequence,
        status: sequence === 6 ? 2 : 1,
        sub_status: 0,
        total: sequence,
        new_strm: sequence,
        finish_at: sequence === 6 ? 10 : 0,
        created_at: 1,
      })
      await flushPromises()
    }
    old.resolve(envelope({ records: [{ ...record, status: 1, new_strm: 0 }], total: 1 }))
    await flushPromises()
    expect(field('status')).toBe('已完成')
    expect(field('stats')).toContain('STRM 6')
    expect(readReply).toHaveBeenCalledTimes(1)
  })

  it('在途请求的多个结构变化合并一次必要补读', async () => {
    const { readReply, refresh, field } = await mountRecords()
    const deleted = vi
      .mocked(useRealtimeEvent)
      .mock.calls.find(([name]) => name === 'sync_task_deleted')![1]
    const old = createDeferred<unknown>()
    readReply.mockReturnValueOnce(old.promise).mockResolvedValueOnce(snapshot('completed', 10))
    refresh()
    deleted({ sync_id: 50, deleted: true })
    deleted({ sync_id: 51, deleted: true })
    old.resolve(snapshot('running'))
    await flushPromises()
    expect(readReply).toHaveBeenCalledTimes(3)
    expect(field('ledger_status')).toBe('记录更新完成')
  })

  it('重复或旧序号事件不覆盖终态，也不使当前快照失效或补读', async () => {
    const { readReply, refresh, patch, field } = await mountRecords()
    patch('completed', 3)
    const current = createDeferred<unknown>()
    readReply.mockReturnValueOnce(current.promise)
    refresh()
    patch('failed', 3)
    patch('running', 2)
    await flushPromises()
    expect(field('ledger_status')).toBe('记录更新完成')

    current.resolve(snapshot('completed', 11))
    await flushPromises()
    expect(field('total_duration')).toBe('10 秒')
    expect(readReply).toHaveBeenCalledTimes(2)
  })

  it('没有在途请求时直接应用普通事件，不额外读取列表', async () => {
    const { readReply, patch, field } = await mountRecords()
    patch('running', 1)
    await flushPromises()
    expect(field('ledger_status')).toBe('后台更新记录中')
    patch('completed', 2)
    await flushPromises()
    expect(field('ledger_status')).toBe('记录更新完成')
    expect(readReply).toHaveBeenCalledTimes(1)
  })

  it('读取失败只提示一次并保留请求期间的事件结果，不补读', async () => {
    const { readReply, refresh, patch, field } = await mountRecords()
    const old = createDeferred<unknown>()
    readReply.mockReturnValueOnce(old.promise)
    refresh()
    patch('completed', 1)
    old.resolve(envelope(null, 500, '数据库繁忙'))
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('加载同步记录失败：数据库繁忙')
    expect(field('ledger_status')).toBe('记录更新完成')
    expect(readReply).toHaveBeenCalledTimes(2)
  })

  it('查询切换的补读使用新页码，不恢复旧页记录', async () => {
    const { wrapper, readReply, refresh, patch } = await mountRecords()
    const old = createDeferred<unknown>()
    readReply
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce(
        envelope({ records: [{ ...record, id: 2, remote_path: '第二页记录' }], total: 30 }),
      )
    refresh()
    patch('completed', 1)
    await click(wrapper, '第二页')
    old.resolve(snapshot('running'))
    await flushPromises()
    expect(wrapper.text()).toContain('第二页记录')
    expect(wrapper.text()).not.toContain('原记录')
    expect(readReply).toHaveBeenCalledTimes(3)
    expect(readReply.mock.lastCall?.[1]).toEqual({ page: 2, page_size: 20 })
  })

  it('卸载后不补读，也不显示旧请求错误', async () => {
    const { wrapper, readReply, refresh, patch } = await mountRecords()
    const old = createDeferred<unknown>()
    readReply.mockReturnValueOnce(old.promise)
    refresh()
    patch('completed', 1)
    wrapper.unmount()
    old.resolve(envelope(null, 500, '旧快照失败'))
    await flushPromises()
    expect(readReply).toHaveBeenCalledTimes(2)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
