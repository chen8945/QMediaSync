import axios, { CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage, ElMessageBox, ElOption, ElSelect, type MessageBoxData } from 'element-plus'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppSyncDirectories from '@/components/AppSyncDirectories.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'
import { createDeferred } from '../support/deferred'

const events = vi.hoisted(
  () =>
    new Map<
      string,
      { update: (payload: Record<string, unknown>) => void; reconnect?: () => void }
    >(),
)
vi.mock('@/composables/useRealtimeEvents', () => ({
  useRealtimeEvent: (
    name: string,
    update: (payload: Record<string, unknown>) => void,
    reconnect?: () => void,
  ) => events.set(name, { update, reconnect }),
}))
const DialogStub = defineComponent({
  props: { modelValue: Boolean },
  template: '<section v-if="modelValue" role="dialog"><slot /><slot name="footer" /></section>',
})
const rejection = (code: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, error_code: code, message: 'private token' },
    config: { method: 'post', url: '/api/sync/path/start?token=private' },
  })
const mountDirectories = async (running = 0, directoryIds = [12]) => {
  const getReply = vi.fn(async (url: string): Promise<APIResponse<unknown>> => {
    if (url.endsWith('/sync/path-list'))
      return {
        code: 200,
        message: '',
        data: {
          list: directoryIds.map((id) => ({
            id,
            base_cid: 'root',
            local_path: '/strm',
            remote_path: '/remote',
            source_type: '115',
            account_id: 1,
            account_name: 'my-account',
            enable_cron: false,
            directory_upload_enabled: true,
            is_running: running,
          })),
          total: directoryIds.length,
        },
      }
    if (url.endsWith('/directory-upload/rules'))
      return {
        code: 200,
        message: '',
        data: {
          list: [
            {
              id: 4,
              sync_path_id: 12,
              enabled: true,
              monitor_path: '/watch',
              remote_root_path: '/remote/upload',
              recursive: true,
            },
          ],
        },
      }
    if (url.endsWith('/scrape/pathes'))
      return { code: 200, message: '', data: [{ id: 5, source_path: '/scrape' }] }
    return { code: 200, message: '', data: [5] }
  })
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: null })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await getReply(config.url) : await reply(),
  }))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  const wrapper = mount(AppSyncDirectories, {
    global: {
      plugins: [router],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, PageStats: true, ElDialog: DialogStub },
    },
  })
  await flushPromises()
  const act = async (ariaLabel: string, index = 0) => {
    await wrapper.findAll(`button[aria-label="${ariaLabel}"]`)[index]!.trigger('click')
    await flushPromises()
  }
  return { wrapper, reply, getReply, adapter, act }
}

const dialogButton = (wrapper: VueWrapper, label: string) =>
  wrapper
    .get('[role="dialog"]')
    .findAll('button')
    .find((button) => button.text() === label)!

const relationSuccess = (data: unknown = null): APIResponse<unknown> => ({
  code: 200,
  message: '',
  data,
})
const relationFailure = (): APIResponse<unknown> => ({
  code: 500,
  message: '同步目录操作失败，请稍后重试',
  data: null,
})
const relationCandidates = (ids = [5, 6, 7]) =>
  relationSuccess(ids.map((id) => ({ id, source_path: `/scrape/${id}` })))

enableAutoUnmount(afterEach)
beforeEach(() => {
  events.clear()
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

describe('同步目录列表请求行为', () => {
  it.each(['增量同步', '全量同步', '删除同步目录', '目录监控扫描'])(
    '%s 业务失败不改变卡片、不刷新或报成功',
    async (action) => {
      const { wrapper, reply, getReply, act } = await mountDirectories()
      reply.mockResolvedValueOnce({
        code: 500,
        message: '同步目录操作失败，请稍后重试',
        data: { is_running: 2, accepted: 1 },
      })
      const reads = getReply.mock.calls.length
      await act(action)
      expect(reply).toHaveBeenCalledOnce()
      expect(wrapper.find('.directory-card').exists()).toBe(true)
      expect(wrapper.find('.is-running').exists()).toBe(false)
      expect(wrapper.find('.is-waiting').exists()).toBe(false)
      expect(getReply).toHaveBeenCalledTimes(reads)
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(ElMessage.error).toHaveBeenCalledOnce()
      expect(JSON.stringify(vi.mocked(ElMessage.error).mock.calls)).not.toContain('private')
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
    },
  )

  it.each(['success', 'failure'])('新列表开始后忽略旧列表的 %s 结果', async (outcome) => {
    const { wrapper, getReply } = await mountDirectories()
    const original = getReply.getMockImplementation()!
    const old = createDeferred<APIResponse<unknown>>()
    const current = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const reload = events.get('sync_task_created')!.reconnect!
    reload()
    reload()
    current.resolve(await original('/api/sync/path-list'))
    await flushPromises()
    const reads = getReply.mock.calls.length
    old.resolve(outcome === 'success' ? relationSuccess({ list: [], total: 0 }) : relationFailure())
    await flushPromises()
    expect(wrapper.find('.directory-card').exists()).toBe(true)
    expect(getReply).toHaveBeenCalledTimes(reads)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('新列表的上传规则不会被旧规则请求失败清空或改为失败状态', async () => {
    const { wrapper, getReply } = await mountDirectories()
    const original = getReply.getMockImplementation()!
    const oldRules = createDeferred<APIResponse<unknown>>()
    getReply
      .mockResolvedValueOnce(await original('/api/sync/path-list'))
      .mockReturnValueOnce(oldRules.promise)
    const reload = events.get('sync_task_created')!.reconnect!
    reload()
    await flushPromises()
    reload()
    await flushPromises()
    oldRules.resolve(relationFailure())
    await flushPromises()
    expect(wrapper.text()).toContain('/watch')
    expect(wrapper.text()).not.toContain('加载失败')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('列表替换后旧状态读取不回写新列表，卸载后失败不提示', async () => {
    const { wrapper, getReply } = await mountDirectories()
    const oldStatus = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(oldStatus.promise)
    const refreshStatus = events.get('strm_sync_task_start')!.update
    refreshStatus({})
    events.get('sync_task_created')!.reconnect!()
    await flushPromises()
    oldStatus.resolve(relationSuccess({ list: [{ id: 12, is_running: 2 }], total: 1 }))
    await flushPromises()
    expect(wrapper.get('.directory-card').classes()).not.toContain('is-running')
    const lateStatus = createDeferred<APIResponse<unknown>>()
    getReply.mockReturnValueOnce(lateStatus.promise)
    refreshStatus({})
    wrapper.unmount()
    lateStatus.resolve(relationFailure())
    await flushPromises()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('后台状态事件合并读取，连续失败只提示一次，成功后重新允许提示', async () => {
    const { getReply } = await mountDirectories()
    const original = getReply.getMockImplementation()!
    const first = createDeferred<APIResponse<unknown>>()
    getReply.mockClear()
    getReply.mockReturnValueOnce(first.promise).mockResolvedValueOnce(relationFailure())
    const refreshStatus = events.get('strm_sync_task_start')!.update
    refreshStatus({})
    refreshStatus({})
    refreshStatus({})
    await flushPromises()
    expect(getReply).toHaveBeenCalledTimes(1)
    first.resolve(relationFailure())
    await flushPromises()
    expect(getReply).toHaveBeenCalledTimes(2)
    expect(ElMessage.error).toHaveBeenCalledTimes(1)
    getReply.mockResolvedValueOnce(await original('/api/sync/path-list'))
    refreshStatus({})
    await flushPromises()
    getReply.mockResolvedValueOnce(relationFailure())
    refreshStatus({})
    await flushPromises()
    expect(ElMessage.error).toHaveBeenCalledTimes(2)
  })

  it('成功启动应用服务端运行状态；停止失败保持运行状态', async () => {
    const { wrapper, reply, act } = await mountDirectories()
    reply.mockResolvedValueOnce({ code: 200, message: '', data: { is_running: 2 } })
    await act('增量同步')
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
    vi.mocked(ElMessage.success).mockClear()
    reply.mockRejectedValueOnce(rejection('REQUEST_ORIGIN_INVALID'))
    await act('停止同步')
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('访问地址校验失败'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('定时开关安全校验失败回滚开关并展示真实原因', async () => {
    const { wrapper, reply } = await mountDirectories()
    reply.mockRejectedValueOnce(rejection('CSRF_TOKEN_INVALID'))
    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('请求安全校验失败'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['增量同步', '删除同步目录', '目录监控扫描'])(
    '%s 的取消和已处理 401 静默',
    async (action) => {
      const { reply, act } = await mountDirectories()
      const handled = rejection('SESSION_INVALID', 401)
      markAuthInvalidationHandled(handled)
      for (const error of [new CanceledError(), handled]) {
        reply.mockRejectedValueOnce(error)
        await act(action)
      }
      expect(reply).toHaveBeenCalledTimes(2)
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(console.error).not.toHaveBeenCalled()
    },
  )

  it('关联保存失败保留选择和对话框，候选查询保留 local 参数', async () => {
    const { wrapper, reply, adapter, act } = await mountDirectories()
    await act('关联刮削目录')
    expect(wrapper.findComponent(ElSelect).props('modelValue')).toEqual([5])
    reply.mockRejectedValueOnce(rejection('CSRF_TOKEN_INVALID'))
    await wrapper
      .get('[role="dialog"]')
      .findAll('button')
      .find((item) => item.text() === '确定')!
      .trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(wrapper.findComponent(ElSelect).props('modelValue')).toEqual([5])
    expect(
      adapter.mock.calls.find(([config]) => config.url.endsWith('/scrape/pathes'))![0].params,
    ).toEqual({ source_type: 'local' })
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('请求安全校验失败'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('已有关联读取失败阻止保存空集合', async () => {
    const { wrapper, getReply, reply, act } = await mountDirectories()
    getReply
      .mockResolvedValueOnce({ code: 200, message: '', data: [] })
      .mockRejectedValueOnce(rejection('REQUEST_ORIGIN_INVALID'))
    await act('关联刮削目录')
    const confirm = wrapper
      .get('[role="dialog"]')
      .findAll('button')
      .find((item) => item.text() === '确定')!
    expect(confirm.attributes('disabled')).toBeDefined()
    await confirm.trigger('click')
    expect(reply).not.toHaveBeenCalled()
  })

  it('重连刷新失败保留原目录，旧任务事件不会覆盖较新状态', async () => {
    const { wrapper, getReply } = await mountDirectories()
    const update = events.get('sync_task_updated')!.update
    update({ sync_path_id: 12, sync_id: 1, sequence: 2, is_running: 2, event_time: 10 })
    update({ sync_path_id: 12, sync_id: 1, sequence: 1, is_running: 0, event_time: 9 })
    await flushPromises()
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
    getReply.mockRejectedValueOnce(rejection('REQUEST_ORIGIN_INVALID'))
    events.get('sync_task_created')!.reconnect!()
    await flushPromises()
    expect(wrapper.find('.directory-card').exists()).toBe(true)
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
  })
  it('停止响应成功只表示请求已受理，等待实时状态确认停止', async () => {
    const { wrapper, act } = await mountDirectories(2)
    await act('停止同步')
    expect(ElMessage.success).toHaveBeenCalledWith('已请求停止同步任务')
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
    events
      .get('sync_task_updated')!
      .update({ sync_path_id: 12, sync_id: 1, sequence: 1, is_running: 0 })
    await flushPromises()
    expect(wrapper.get('.directory-card').classes()).not.toContain('is-running')
  })

  it.each(['running', 'completed', 'failed', 'interrupted'])(
    '旧任务后台 %s 不覆盖 HTTP 读到的新任务运行态或抬高事件水位',
    async (ledgerStatus) => {
      const { wrapper } = await mountDirectories(2)
      const update = events.get('sync_task_updated')!.update
      update({
        sync_path_id: 12,
        sync_id: 1,
        sequence: 20,
        status: 2,
        ledger_status: ledgerStatus,
        event_time: 100,
      })
      await flushPromises()
      expect(wrapper.get('.directory-card').classes()).toContain('is-running')
      update({
        sync_path_id: 12,
        sync_id: 2,
        sequence: 2,
        status: 2,
        ledger_status: 'pending',
        event_time: 99,
      })
      await flushPromises()
      expect(wrapper.get('.directory-card').classes()).not.toContain('is-running')
    },
  )

  it('删除旧记录通过当前目录状态收敛，不清除新任务运行展示', async () => {
    const { wrapper, getReply } = await mountDirectories(2)
    getReply.mockResolvedValueOnce(relationSuccess({ list: [{ id: 12, is_running: 2 }], total: 1 }))
    events.get('sync_task_deleted')!.update({
      sync_path_id: 12,
      sync_id: 1,
      sequence: 1,
      deleted: true,
      ledger_status: 'completed',
    })
    await flushPromises()
    expect(wrapper.get('.directory-card').classes()).toContain('is-running')
  })

  it.each([4, 5, 6])('新终态 %s 恢复目录的可启动状态', async (status) => {
    const { wrapper } = await mountDirectories(2)
    events.get('sync_task_updated')!.update({ sync_path_id: 12, sync_id: 1, sequence: 1, status })
    await flushPromises()
    expect(wrapper.get('.directory-card').classes()).not.toContain('is-running')
    expect(wrapper.find('button[aria-label="停止同步"]').exists()).toBe(false)
  })
})

describe('同步目录关联窗口请求归属', () => {
  it.each(['候选目录', '已有绑定'])(
    '%s 读取失败后禁止保存，重试必须等待两份数据都成功',
    async (failedRead) => {
      const { wrapper, getReply, reply, adapter, act } = await mountDirectories()
      getReply.mockImplementation(async (url) =>
        url.endsWith('/scrape/pathes')
          ? failedRead === '候选目录'
            ? relationFailure()
            : relationCandidates()
          : failedRead === '已有绑定'
            ? relationFailure()
            : relationSuccess([5]),
      )
      await act('关联刮削目录')
      expect(dialogButton(wrapper, '确定').attributes('disabled')).toBeDefined()
      expect(wrapper.getComponent(ElSelect).props('disabled')).toBe(true)
      expect(wrapper.get('[role="dialog"]').text()).toContain('同步目录操作失败，请稍后重试')
      expect(wrapper.text()).not.toContain('private')
      await dialogButton(wrapper, '确定').trigger('click')
      expect(reply).not.toHaveBeenCalled()

      const candidates = createDeferred<APIResponse<unknown>>()
      const bindings = createDeferred<APIResponse<unknown>>()
      getReply.mockImplementation((url) =>
        url.endsWith('/scrape/pathes') ? candidates.promise : bindings.promise,
      )
      await dialogButton(wrapper, '重新加载').trigger('click')
      candidates.resolve(relationCandidates())
      await flushPromises()
      expect(wrapper.getComponent(ElSelect).props('loading')).toBe(true)
      expect(dialogButton(wrapper, '确定').attributes('disabled')).toBeDefined()
      bindings.resolve(relationSuccess([6]))
      await flushPromises()
      expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([6])
      expect(wrapper.getComponent(ElSelect).props('loading')).toBe(false)
      expect(dialogButton(wrapper, '确定').attributes('disabled')).toBeUndefined()
      await dialogButton(wrapper, '确定').trigger('click')
      await flushPromises()
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      const write = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
      expect(write.url).toMatch(/\/sync\/path\/scrape-paths$/)
      expect(JSON.parse(write.data)).toEqual({ id: 12, scrape_path_id: [6] })
      expect(ElMessage.success).toHaveBeenCalledOnce()
    },
  )

  it.each([
    ['成功', '加载中'],
    ['失败', '加载中'],
    ['成功', '已加载'],
    ['失败', '已加载'],
    ['成功', '加载失败'],
    ['失败', '加载失败'],
  ])('A 绑定读取%s不能改变 B 的%s状态', async (oldResult, currentState) => {
    const { wrapper, getReply, reply, act } = await mountDirectories(0, [12, 13])
    const oldBindings = createDeferred<APIResponse<unknown>>()
    const newBindings = createDeferred<APIResponse<unknown>>()
    getReply.mockImplementation(async (url) => {
      if (url.endsWith('/scrape/pathes')) return relationCandidates()
      return url.includes('/12/') ? oldBindings.promise : newBindings.promise
    })
    await act('关联刮削目录')
    await dialogButton(wrapper, '取消').trigger('click')
    await act('关联刮削目录', 1)
    if (currentState !== '加载中') {
      newBindings.resolve(currentState === '已加载' ? relationSuccess([6]) : relationFailure())
      await flushPromises()
    }
    if (currentState === '已加载') {
      wrapper.getComponent(ElSelect).vm.$emit('update:modelValue', [7])
      await flushPromises()
    }
    const currentText = wrapper.get('[role="dialog"]').text()
    vi.mocked(ElMessage.error).mockClear()
    vi.mocked(console.error).mockClear()
    oldBindings.resolve(oldResult === '成功' ? relationSuccess([5]) : relationFailure())
    await flushPromises()
    expect(wrapper.get('[role="dialog"]').text()).toBe(currentText)
    expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual(
      currentState === '已加载' ? [7] : [],
    )
    expect(wrapper.getComponent(ElSelect).props('loading')).toBe(currentState === '加载中')
    expect(dialogButton(wrapper, '确定').attributes('disabled') !== undefined).toBe(
      currentState !== '已加载',
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
    if (currentState !== '已加载') {
      await dialogButton(wrapper, '确定').trigger('click')
      expect(reply).not.toHaveBeenCalled()
    }
    if (currentState === '加载中') {
      newBindings.resolve(relationSuccess([6]))
      await flushPromises()
      expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([6])
      expect(dialogButton(wrapper, '确定').attributes('disabled')).toBeUndefined()
    }
  })

  it.each(['成功', '失败'])('A 候选读取%s不能覆盖 B 的候选和选择', async (oldResult) => {
    const { wrapper, getReply, act } = await mountDirectories(0, [12, 13])
    const oldCandidates = createDeferred<APIResponse<unknown>>()
    let candidateReads = 0
    getReply.mockImplementation(async (url) => {
      if (url.endsWith('/scrape/pathes')) {
        candidateReads += 1
        return candidateReads === 1 ? oldCandidates.promise : relationCandidates([6, 7])
      }
      return relationSuccess(url.includes('/12/') ? [5] : [6])
    })
    await act('关联刮削目录')
    await dialogButton(wrapper, '取消').trigger('click')
    await act('关联刮削目录', 1)
    const reads = getReply.mock.calls.length
    oldCandidates.resolve(oldResult === '成功' ? relationCandidates([5]) : relationFailure())
    await flushPromises()
    expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([6])
    expect(wrapper.findAllComponents(ElOption).map((option) => option.props('value'))).toEqual([
      6, 7,
    ])
    expect(getReply).toHaveBeenCalledTimes(reads)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each(['同一目录重开', '直接切换目录'])('%s使旧绑定读取失效', async (transition) => {
    const { wrapper, getReply, act } = await mountDirectories(0, [12, 13])
    const oldBindings = createDeferred<APIResponse<unknown>>()
    let bindingReads = 0
    getReply.mockImplementation(async (url) => {
      if (url.endsWith('/scrape/pathes')) return relationCandidates()
      bindingReads += 1
      return bindingReads === 1 ? oldBindings.promise : relationSuccess([6])
    })
    await act('关联刮削目录')
    if (transition === '同一目录重开') {
      wrapper.getComponent(DialogStub).vm.$emit('update:modelValue', false)
    }
    await act('关联刮削目录', transition === '同一目录重开' ? 0 : 1)
    expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([6])
    oldBindings.resolve(relationSuccess([5]))
    await flushPromises()
    expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([6])
    expect(dialogButton(wrapper, '确定').attributes('disabled')).toBeUndefined()
  })

  it.each([
    ['关闭', '成功'],
    ['关闭', '失败'],
    ['卸载', '成功'],
    ['卸载', '失败'],
  ])('%s后旧候选读取%s不再读取绑定或提示', async (lifecycle, oldResult) => {
    const { wrapper, getReply, act } = await mountDirectories()
    const candidates = createDeferred<APIResponse<unknown>>()
    getReply.mockImplementation(async (url) =>
      url.endsWith('/scrape/pathes') ? candidates.promise : relationSuccess([5]),
    )
    await act('关联刮削目录')
    if (lifecycle === '关闭') await dialogButton(wrapper, '取消').trigger('click')
    else wrapper.unmount()
    const reads = getReply.mock.calls.length
    candidates.resolve(oldResult === '成功' ? relationCandidates() : relationFailure())
    await flushPromises()
    expect(getReply).toHaveBeenCalledTimes(reads)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each([
    ['成功', false],
    ['失败', false],
    ['成功', true],
    ['失败', true],
  ])('A 保存%s不影响 B 窗口或新保存（新保存在途：%s）', async (oldResult, newSaving) => {
    const { wrapper, getReply, reply, adapter, act } = await mountDirectories(0, [12, 13])
    getReply.mockImplementation(async (url) =>
      url.endsWith('/scrape/pathes')
        ? relationCandidates()
        : relationSuccess(url.includes('/12/') ? [5] : [6]),
    )
    const oldSave = createDeferred<APIResponse<unknown>>()
    const newSave = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValueOnce(oldSave.promise).mockReturnValueOnce(newSave.promise)
    await act('关联刮削目录')
    await dialogButton(wrapper, '确定').trigger('click')
    await flushPromises()
    expect(reply).toHaveBeenCalledOnce()
    await dialogButton(wrapper, '取消').trigger('click')
    await act('关联刮削目录', 1)
    expect(dialogButton(wrapper, '确定').classes()).not.toContain('is-loading')
    wrapper.getComponent(ElSelect).vm.$emit('update:modelValue', [7])
    await flushPromises()
    if (newSaving) {
      await dialogButton(wrapper, '确定').trigger('click')
      await flushPromises()
      expect(reply).toHaveBeenCalledTimes(2)
    }
    oldSave.resolve(oldResult === '成功' ? relationSuccess() : relationFailure())
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(wrapper.getComponent(ElSelect).props('modelValue')).toEqual([7])
    expect(dialogButton(wrapper, '确定').classes().includes('is-loading')).toBe(newSaving)
    expect(wrapper.getComponent(ElSelect).props('disabled')).toBe(newSaving)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
    if (!newSaving) {
      await dialogButton(wrapper, '确定').trigger('click')
      await flushPromises()
    }
    const writes = adapter.mock.calls
      .filter(([config]) => config.method === 'post')
      .map(([config]) => JSON.parse(config.data))
    expect(writes).toEqual([
      { id: 12, scrape_path_id: [5] },
      { id: 13, scrape_path_id: [7] },
    ])
    newSave.resolve(relationSuccess())
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(ElMessage.success).toHaveBeenCalledOnce()
  })

  it('同一窗口连续确认只提交一次', async () => {
    const { wrapper, reply, act } = await mountDirectories()
    const save = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValue(save.promise)
    await act('关联刮削目录')
    const confirm = dialogButton(wrapper, '确定')
    await Promise.all([confirm.trigger('click'), confirm.trigger('click')])
    await flushPromises()
    expect(reply).toHaveBeenCalledOnce()
    save.resolve(relationSuccess())
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(ElMessage.success).toHaveBeenCalledOnce()
  })

  it.each(['成功', '失败'])('卸载后旧保存%s保持静默', async (oldResult) => {
    const { wrapper, reply, act } = await mountDirectories()
    const save = createDeferred<APIResponse<unknown>>()
    reply.mockReturnValueOnce(save.promise)
    await act('关联刮削目录')
    await dialogButton(wrapper, '确定').trigger('click')
    await flushPromises()
    wrapper.unmount()
    save.resolve(oldResult === '成功' ? relationSuccess() : relationFailure())
    await flushPromises()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
