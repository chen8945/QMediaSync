import { flushPromises } from '@vue/test-utils'
import { createPinia, disposePinia, getActivePinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { AxiosError, CanceledError } from 'axios'
import { ElMessage } from 'element-plus'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { useBackupStore } from '@/stores/backup'
import { createDeferred } from '../support/deferred'

describe('backup store 进度轮询', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(console, 'error').mockImplementation(() => {})
    setActivePinia(createPinia())
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })

  it.each(['backup', 'restore'] as const)('%s 失败终态不能提示成功或泄露内部错误', async (type) => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: {
            type,
            status: 'failed',
            is_running: false,
            count: 1,
            total: 2,
            elapsed: 1,
            desc: '/private/secret/path',
            error_msg: 'password=secret',
          },
        },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling(type, undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.progress?.status).toBe('failed')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledTimes(1)
    expect(
      JSON.stringify([
        store.progress,
        vi.mocked(ElMessage.error).mock.calls,
        vi.mocked(console.error).mock.calls,
      ]),
    ).not.toContain('secret')
    await vi.advanceTimersByTimeAsync(6000)
    expect(http.get).toHaveBeenCalledTimes(1)
  })

  it('旧后端仅返回停止状态时不推断成功，空总数不产生 NaN', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: {
            type: 'backup',
            is_running: false,
            count: 0,
            total: 0,
            elapsed: 0,
            desc: '',
            error_msg: '',
          },
        },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.warning).toHaveBeenCalledTimes(1)
    expect(Number.isFinite(store.progress?.progress)).toBe(true)
    expect(store.progress?.status).not.toBe('completed')
    store.stopProgressPolling()
  })

  it.each([
    {
      outcome: 'not_started',
      code: 'RESTORE_KEY_MISMATCH',
      reason: '备份中的两步验证密钥无法解密，请确认 encryption.key 与原实例一致。',
      expected: '本次恢复未修改数据库',
      restart: false,
    },
    {
      outcome: 'rolled_back',
      code: 'RESTORE_FAILED',
      reason: '恢复任务失败，请查看服务日志。',
      expected: '数据库改动已确认回滚',
      restart: true,
    },
    {
      outcome: 'committed',
      code: 'RESTORE_FAILED',
      reason: '恢复任务失败，请查看服务日志。',
      expected: '数据库恢复已提交，后续处理未完成',
      restart: true,
    },
    {
      outcome: 'uncertain',
      code: 'RESTORE_FAILED',
      reason: '恢复任务失败，请查看服务日志。',
      expected: '数据库提交或回滚结果不确定',
      restart: true,
    },
    {
      outcome: undefined,
      code: 'BACKUP_ARCHIVE_INVALID',
      reason: '备份文件已损坏。',
      expected: '数据库结果尚未确认',
      restart: false,
    },
  ])('恢复失败 $outcome 展示安全原因和明确数据库结果', async (scenario) => {
    const { store } = await setupRestart({
      status: 'failed',
      error_code: scenario.code,
      error_msg: scenario.reason,
      restore_outcome: scenario.outcome,
      restart_required: scenario.restart,
    })
    expect(store.progress?.current_step).toContain(scenario.reason)
    expect(store.progress?.current_step).toContain(scenario.expected)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(store.progress?.current_step)
    expect(ElMessage.success).not.toHaveBeenCalled()
    if (scenario.outcome !== 'rolled_back')
      expect(store.progress?.current_step).not.toContain('已确认回滚')
    expect(store.canRestart).toBe(scenario.restart)
  })

  it.each(['BACKUP_ARCHIVE_UNSUPPORTED', 'BACKUP_ARCHIVE_LIMIT', 'BACKUP_FAILED'])(
    '备份失败 %s 使用安全原因',
    async (code) => {
      const http = {
        get: vi.fn().mockResolvedValue({
          data: {
            code: 200,
            data: {
              type: 'backup',
              status: 'failed',
              is_running: false,
              error_code: code,
              error_msg: '备份失败的安全原因。',
            },
          },
        }),
      }
      const store = useBackupStore()
      store.startProgressPolling('backup', undefined, http as never)
      await vi.advanceTimersByTimeAsync(3000)
      expect(store.progress?.current_step).toBe('备份失败的安全原因。')
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('备份失败的安全原因。')
    },
  )

  it('未知错误码不显示原始错误，但保留明确回滚结果', async () => {
    const { store } = await setupRestart({
      status: 'failed',
      error_code: 'UNKNOWN_CODE',
      error_msg: 'password=secret',
      restore_outcome: 'rolled_back',
    })
    expect(store.progress?.current_step).toContain('数据库改动已确认回滚')
    expect(store.progress?.current_step).not.toContain('secret')
  })

  it.each(['backup', 'restore'] as const)('%s 只对明确完成状态提示成功且停止轮询', async (type) => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: {
            type,
            status: 'completed',
            is_running: false,
            count: 1,
            total: 2,
            elapsed: 1,
            desc: '完成',
            error_msg: '',
          },
        },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling(type, undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.progress?.status).toBe('completed')
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith(
      type === 'backup' ? '备份任务完成！' : '恢复任务完成！',
    )
    expect(ElMessage.error).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(6000)
    expect(http.get).toHaveBeenCalledTimes(1)
  })

  it('旧后端的 error_msg 保留失败语义，不依赖进度计数', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: {
            type: 'backup',
            is_running: false,
            count: 2,
            total: 2,
            elapsed: 0,
            desc: 'private-secret',
            error_msg: 'private-secret',
          },
        },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.progress?.status).toBe('failed')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('备份任务失败，请查看服务日志。')
    expect(ElMessage.success).not.toHaveBeenCalled()
    store.stopProgressPolling()
  })

  afterEach(() => {
    disposePinia(getActivePinia()!)
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('恢复完成后保留结果与重启提示，恢复凭证在页面切换可见性后仍用于查询', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: {
            type: 'restore',
            status: 'completed',
            is_running: false,
            restart_required: true,
            count: 2,
            total: 2,
            elapsed: 1,
            desc: '',
            error_msg: '',
          },
        },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling('restore', undefined, http as never, 'test-restore-receipt')
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)

    expect(http.get).toHaveBeenCalledExactlyOnceWith('/api/backup/status', {
      headers: { 'X-Restore-Receipt': 'test-restore-receipt' },
      skipAuthInvalidation: true,
    })
    expect(store.restartRequired).toBe(true)
    expect(store.showProgressDialog).toBe(true)
    expect(store.progress?.status).toBe('completed')
    expect(store.progress?.current_step).toContain('数据库已恢复')
    expect(store.progress?.current_step).toContain('重启应用服务')
    expect(store.progress?.current_step).toContain('重新登录')
    expect(JSON.stringify(store.$state)).not.toContain('test-restore-receipt')
    store.closeProgressDialog()
    expect(store.showProgressDialog).toBe(true)
  })

  it.each([false, true])(
    '恢复失败保留终态，restart_required=%s 决定是否需要重启',
    async (restartRequired) => {
      const http = {
        get: vi.fn().mockResolvedValue({
          data: {
            code: 200,
            data: {
              type: 'restore',
              status: 'failed',
              is_running: false,
              restart_required: restartRequired,
              count: 1,
              total: 2,
              elapsed: 1,
              desc: '',
              error_msg: 'password=secret',
            },
          },
        }),
      }
      const store = useBackupStore()
      store.startProgressPolling('restore', undefined, http as never, 'test-restore-receipt')
      await vi.advanceTimersByTimeAsync(9000)
      expect(store.showProgressDialog).toBe(true)
      expect(store.progress?.status).toBe('failed')
      expect(store.restartRequired).toBe(restartRequired)
      expect(store.progress?.current_step).not.toContain('部分数据可能已恢复')
      expect(store.progress?.current_step).not.toContain('已回滚')
      expect(store.progress?.current_step).not.toContain('secret')
      if (restartRequired) expect(store.progress?.current_step).toContain('重启服务')
      expect(ElMessage.success).not.toHaveBeenCalled()
      store.closeProgressDialog()
      expect(store.showProgressDialog).toBe(restartRequired)
    },
  )

  it('恢复查询连续失败不自动刷新，手动重试在首次请求前切出再切回页面仍继续查询', async () => {
    const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})
    const http = {
      get: vi.fn().mockRejectedValue(new HttpResponseError({ status: 503, data: null })),
    }
    const store = useBackupStore()
    store.startProgressPolling('restore', undefined, http as never, 'test-restore-receipt')
    await vi.advanceTimersByTimeAsync(12000)
    expect(http.get).toHaveBeenCalledTimes(3)
    expect(reload).not.toHaveBeenCalled()
    expect(store.showProgressDialog).toBe(true)
    expect(store.progress?.status).toBe('unknown')
    expect(store.progressQueryError).toContain('不要重复提交恢复')
    expect(ElMessage.success).not.toHaveBeenCalled()

    http.get.mockResolvedValue({
      data: {
        code: 200,
        data: {
          type: 'restore',
          status: 'completed',
          is_running: false,
          restart_required: true,
          count: 2,
          total: 2,
          elapsed: 1,
          desc: '',
          error_msg: '',
        },
      },
    })
    const queryError = store.progressQueryError
    store.retryProgressPolling()
    expect(store.isPolling).toBe(true)
    expect(store.progressQueryError).toBe(queryError)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(http.get).toHaveBeenCalledTimes(3)
    expect(store.isPolling).toBe(true)
    expect(store.progressQueryError).toBe(queryError)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(http.get).toHaveBeenCalledTimes(4)
    expect(http.get).toHaveBeenLastCalledWith('/api/backup/status', {
      headers: { 'X-Restore-Receipt': 'test-restore-receipt' },
      skipAuthInvalidation: true,
    })
    expect(store.progress?.status).toBe('completed')
    expect(store.isPolling).toBe(false)
    expect(store.progressQueryError).toBe('')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain(
      'test-restore-receipt',
    )
  })

  it('初次延迟期间切回前台后重新启动轮询', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: { is_running: true, count: 1, total: 2, elapsed: 1, desc: '备份中' },
        },
      }),
    }
    const store = useBackupStore()

    store.startProgressPolling('backup', undefined, http as never)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(http.get).not.toHaveBeenCalled()

    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(http.get).toHaveBeenCalledTimes(1)
    store.stopProgressPolling()
  })

  it.each([
    { type: 'backup' as const, desc: '旧备份' },
    { type: 'restore' as const, desc: '旧恢复' },
  ])('$desc 请求在新一轮任务启动后不写回状态', async ({ type }) => {
    const oldRequest = createDeferred<{
      data: {
        code: number
        data: {
          is_running: boolean
          count: number
          total: number
          elapsed: number
          desc: string
        }
      }
    }>()
    const oldHttp = { get: vi.fn(() => oldRequest.promise) }
    const newHttp = { get: vi.fn() }
    const store = useBackupStore()

    store.startProgressPolling(type, undefined, oldHttp as never)
    await vi.advanceTimersByTimeAsync(3000)
    expect(oldHttp.get).toHaveBeenCalledTimes(1)

    store.startProgressPolling(
      type === 'backup' ? 'restore' : 'backup',
      undefined,
      newHttp as never,
    )
    oldRequest.resolve({
      data: {
        code: 200,
        data: { is_running: false, count: 1, total: 1, elapsed: 1, desc: '旧任务完成' },
      },
    })
    await flushPromises()

    expect(store.progress).toBeNull()
    expect(store.showProgressDialog).toBe(true)
    store.stopProgressPolling()
  })

  it('页面隐藏后忽略仍在途的进度响应', async () => {
    const request = createDeferred<{
      data: {
        code: number
        data: {
          is_running: boolean
          count: number
          total: number
          elapsed: number
          desc: string
        }
      }
    }>()
    const http = { get: vi.fn(() => request.promise) }
    const store = useBackupStore()

    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(3000)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    request.resolve({
      data: {
        code: 200,
        data: { is_running: false, count: 1, total: 1, elapsed: 1, desc: '备份完成' },
      },
    })
    await flushPromises()

    expect(store.progress).toBeNull()
    expect(store.showProgressDialog).toBe(true)
    store.stopProgressPolling()
  })
  it('业务失败计入连续失败次数且不写回成功进度', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 500, message: '进度文件不存在', data: null },
      }),
    }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(http.get).toHaveBeenCalledTimes(3)
    expect(store.errorRetryCount).toBe(3)
    expect(store.progress).toBeNull()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('进度文件不存在。页面即将刷新…')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    store.stopProgressPolling()
  })

  it.each(['cancel', 'handled'])('%s 不计入失败次数，不弹重复错误', async (kind) => {
    const error =
      kind === 'cancel'
        ? new CanceledError()
        : new HttpResponseError({ status: 401, data: { code: 401 } })
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const http = { get: vi.fn().mockRejectedValue(error) }
    const store = useBackupStore()
    store.startProgressPolling('restore', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(store.errorRetryCount).toBe(0)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
    store.stopProgressPolling()
  })

  it('慢请求不重叠，旧失败在停止后不增加重试次数', async () => {
    const request = createDeferred<{ data: { code: number; message: string; data: null } }>()
    const http = { get: vi.fn(() => request.promise) }
    const store = useBackupStore()
    store.startProgressPolling('backup', undefined, http as never)
    await vi.advanceTimersByTimeAsync(7000)
    expect(http.get).toHaveBeenCalledTimes(1)
    store.stopProgressPolling()
    request.resolve({ data: { code: 500, message: 'secret', data: null } })
    await flushPromises()
    expect(store.errorRetryCount).toBe(0)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  const restartStatus = (overrides = {}) => ({
    data: {
      code: 200,
      data: {
        type: 'restore',
        status: 'completed',
        is_running: false,
        restart_required: true,
        restart_supported: true,
        restart_requested: false,
        count: 2,
        total: 2,
        elapsed: 1,
        desc: '',
        error_msg: '',
        ...overrides,
      },
    },
  })

  const receiptExpired = () =>
    new HttpResponseError({
      status: 401,
      data: { code: 401, error_code: 'RESTORE_RECEIPT_INVALID' },
    })

  const setupRestart = async (overrides = {}) => {
    const http = {
      get: vi.fn().mockResolvedValue(restartStatus(overrides)),
      post: vi.fn().mockResolvedValue(restartStatus({ restart_requested: true, ...overrides })),
    }
    const store = useBackupStore()
    store.startProgressPolling('restore', undefined, http as never, 'test-restore-receipt')
    await vi.advanceTimersByTimeAsync(3000)
    return { store, http }
  }

  it.each([
    { status: 'completed', restore_outcome: 'committed', error_msg: '' },
    { status: 'failed', restore_outcome: 'rolled_back', error_msg: '恢复任务失败。' },
    { status: 'failed', restore_outcome: 'committed', error_msg: '恢复任务失败。' },
    { status: 'failed', restore_outcome: 'uncertain', error_msg: '恢复任务失败。' },
  ])(
    '重启等待保留恢复 $status/$restore_outcome 结果，只有应用明确拒绝旧回执才确认上线',
    async (result) => {
      const { store, http } = await setupRestart({ ...result, error_code: 'RESTORE_FAILED' })
      const originalProgress = { ...store.progress }
      vi.mocked(ElMessage.success).mockClear()
      http.get.mockResolvedValue(restartStatus({ ...result, restart_requested: true }))
      await store.requestRestart()
      expect(store.restartPhase).toBe('waiting')
      expect(store.canRestart).toBe(false)
      await vi.advanceTimersByTimeAsync(3000)
      expect(store.restartPhase).toBe('waiting')
      http.get.mockRejectedValueOnce(new HttpResponseError({ status: 401, data: 'proxy auth' }))
      await vi.advanceTimersByTimeAsync(2000)
      expect(store.restartPhase).toBe('waiting')
      http.get.mockRejectedValueOnce(receiptExpired())
      await vi.advanceTimersByTimeAsync(2000)
      expect(store.restartPhase).toBe('ready')
      expect(store.isPolling).toBe(false)
      expect(store.progress).toEqual(originalProgress)
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(store.showProgressDialog).toBe(true)
      expect(http.post).toHaveBeenCalledExactlyOnceWith('/api/backup/restart', null, {
        headers: { 'X-Restore-Receipt': 'test-restore-receipt' },
        skipAuthInvalidation: true,
      })
    },
  )

  it('重复点击不重发，页面隐藏期间收到重启响应仍处理，切回后继续查询', async () => {
    const { store, http } = await setupRestart()
    const request = createDeferred<ReturnType<typeof restartStatus>>()
    http.post.mockReturnValueOnce(request.promise)
    const pending = store.requestRestart()
    await store.requestRestart()
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(store.restartPhase).toBe('requesting')
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    document.dispatchEvent(new Event('visibilitychange'))
    request.resolve(restartStatus({ restart_requested: true }))
    await pending
    expect(store.restartPhase).toBe('waiting')
    http.get.mockRejectedValue(receiptExpired())
    await vi.advanceTimersByTimeAsync(5000)
    expect(http.get).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.restartPhase).toBe('ready')
    expect(http.post).toHaveBeenCalledTimes(1)
  })

  it.each([
    { status: 409, errorCode: 'RESTORE_RESTART_NOT_READY' },
    { status: 503, errorCode: 'RESTORE_RESTART_UNSUPPORTED' },
    { status: 503, errorCode: 'RESTORE_RESTART_PREPARATION_FAILED' },
  ])(
    '应用明确拒绝 $status $errorCode 时保留结果与凭证，允许再次手动重启',
    async ({ status, errorCode }) => {
      const { store, http } = await setupRestart({ status: 'failed' })
      http.post.mockRejectedValueOnce(
        new HttpResponseError({ status, data: { code: status, error_code: errorCode } }),
      )
      await store.requestRestart()
      expect(store.canRestart).toBe(true)
      expect(store.restartError).not.toBe('')
      expect(store.progress?.status).toBe('failed')
      await vi.advanceTimersByTimeAsync(10000)
      expect(http.post).toHaveBeenCalledTimes(1)
      expect(http.get).toHaveBeenCalledTimes(1)
      await store.requestRestart()
      expect(http.post).toHaveBeenCalledTimes(2)
      expect(store.restartPhase).toBe('waiting')
      expect(store.restartError).toBe('')
    },
  )

  it.each([
    { status: 502, data: '<html>Bad Gateway</html>' },
    { status: 504, data: '<html>Gateway Timeout</html>' },
    { status: 500, data: { code: 500, error_code: 'UNKNOWN_SERVER_ERROR', message: 'secret' } },
    { status: 503, data: { code: 503, message: 'restart failed' } },
  ])('未知 HTTP $status 不能证明重启未执行，仅查询直到确认上线', async ({ status, data }) => {
    const { store, http } = await setupRestart()
    http.post.mockRejectedValueOnce(new HttpResponseError({ status, data }))
    http.get.mockResolvedValue(restartStatus({ restart_requested: true }))
    await store.requestRestart()
    expect(store.restartPhase).toBe('waiting')
    expect(store.canRestart).toBe(false)
    expect(store.restartError).toContain('重启请求结果尚未确认')
    await store.requestRestart()
    await vi.advanceTimersByTimeAsync(9000)
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(store.restartPhase).toBe('waiting')
    expect(store.progress?.status).toBe('completed')
    http.get.mockRejectedValue(receiptExpired())
    await vi.advanceTimersByTimeAsync(2000)
    expect(store.restartPhase).toBe('ready')
    expect(http.post).toHaveBeenCalledTimes(1)
  })

  it.each(['ETIMEDOUT', 'ERR_NETWORK'])('重启响应 %s 后只查询，不自动重复提交', async (code) => {
    const { store, http } = await setupRestart()
    http.post.mockRejectedValueOnce(new AxiosError('secret', code))
    http.get.mockResolvedValue(restartStatus({ restart_requested: true }))
    await store.requestRestart()
    expect(store.restartError).toContain('重启请求结果尚未确认')
    await vi.advanceTimersByTimeAsync(9000)
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(store.restartPhase).toBe('waiting')
    expect(store.restartError).toBe('')
    http.get.mockRejectedValue(receiptExpired())
    await vi.advanceTimersByTimeAsync(2000)
    expect(store.restartPhase).toBe('ready')
    expect(store.progress?.status).toBe('completed')
    expect(JSON.stringify(store.$state)).not.toContain('secret')
  })

  it('请求丢失后读到未执行状态，提示手动重试且不丢失结果', async () => {
    const { store, http } = await setupRestart()
    http.post.mockRejectedValueOnce(new AxiosError('secret', 'ERR_NETWORK'))
    await store.requestRestart()
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.canRestart).toBe(true)
    expect(store.restartError).toContain('重启尚未执行')
    expect(store.progress?.status).toBe('completed')
    expect(http.post).toHaveBeenCalledTimes(1)
  })

  it('重启等待有上限，重新检查保留旧回执且不重发 POST', async () => {
    const { store, http } = await setupRestart({ status: 'failed' })
    http.get.mockRejectedValue(new AxiosError('secret', 'ERR_NETWORK'))
    await store.requestRestart()
    await vi.advanceTimersByTimeAsync(65000)
    expect(store.restartPhase).toBe('unknown')
    expect(store.isPolling).toBe(false)
    expect(store.restartError).toContain('尚未确认服务重新上线')
    expect(store.progress?.status).toBe('failed')
    http.get.mockRejectedValue(receiptExpired())
    store.checkRestartStatus()
    await vi.advanceTimersByTimeAsync(3000)
    expect(store.restartPhase).toBe('ready')
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(http.get).toHaveBeenLastCalledWith('/api/backup/status', {
      headers: { 'X-Restore-Receipt': 'test-restore-receipt' },
      skipAuthInvalidation: true,
    })
  })

  it.each([
    { restart_supported: false },
    { restart_required: false },
    { status: 'running', is_running: true },
  ])('不满足重启条件 %j 时不能发起重启', async (overrides) => {
    const { store, http } = await setupRestart(overrides)
    await store.requestRestart()
    expect(store.canRestart).toBe(false)
    expect(http.post).not.toHaveBeenCalled()
  })

  it('离开旧任务后忽略仍在途的重启响应，不启动新轮询', async () => {
    const { store, http } = await setupRestart()
    const request = createDeferred<ReturnType<typeof restartStatus>>()
    http.post.mockReturnValueOnce(request.promise)
    const pending = store.requestRestart()
    store.resetState()
    request.resolve(restartStatus({ restart_requested: true }))
    await pending
    await vi.advanceTimersByTimeAsync(9000)
    expect(store.restartPhase).toBe('idle')
    expect(store.progress).toBeNull()
    expect(store.isPolling).toBe(false)
    expect(http.get).toHaveBeenCalledTimes(1)
  })
})
