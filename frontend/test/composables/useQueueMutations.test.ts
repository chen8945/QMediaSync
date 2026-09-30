import { AxiosError, CanceledError } from 'axios'
import { ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  useQueueMutations,
  type QueueMutationOperationOptions,
  type QueueSnapshotResult,
} from '@/composables/useQueueMutations'
import { HttpResponseError, markAuthInvalidationHandled, parseHttpError } from '@/http/errors'

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
const loaded = (): QueueSnapshotResult => ({ status: 'loaded' })
const failed = (error: unknown): QueueSnapshotResult => ({
  status: 'failed',
  error: parseHttpError(error),
})
const operation = (overrides: Partial<QueueMutationOperationOptions> = {}) => ({
  execute: vi.fn(async () => {}),
  successMessage: '成功',
  requestErrorMessage: '请求失败',
  ...overrides,
})

const setup = (overrides: Partial<Parameters<typeof useQueueMutations>[0]> = {}) => {
  let current = true
  let version = 0
  const options = {
    reloadQueue: vi.fn(async () => loaded()),
    reloadQueueStatus: vi.fn(async () => loaded()),
    startContext: vi.fn(() => ({ contextVersion: ++version })),
    isContextCurrent: vi.fn(() => current),
    finishContext: vi.fn(),
    onClearPendingSuccess: vi.fn(),
    ...overrides,
  }
  return {
    ...useQueueMutations(options),
    options,
    invalidate: () => {
      current = false
    },
  }
}

beforeEach(() => {
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.restoreAllMocks()
})

describe('useQueueMutations', () => {
  it('holds pending from confirmation through mutation and both required snapshots', async () => {
    const confirmation = deferred<MessageBoxData>()
    const mutation = deferred<void>()
    const queueReload = deferred<QueueSnapshotResult>()
    const statusReload = deferred<QueueSnapshotResult>()
    vi.mocked(ElMessageBox.confirm).mockReturnValue(confirmation.promise)
    const state = setup({
      reloadQueue: vi.fn(() => queueReload.promise),
      reloadQueueStatus: vi.fn(() => statusReload.promise),
    })
    const action = operation({
      execute: vi.fn(() => mutation.promise),
      confirm: { message: '确认' },
    })
    const run = state.clearQueue(action)
    expect(state.isQueueMutationPending.value).toBe(true)
    expect(state.isReloadingQueueSnapshot.value).toBe(false)
    const ignored = operation()
    await state.runMutation(ignored)
    expect(ignored.execute).not.toHaveBeenCalled()
    confirmation.resolve('confirm' as MessageBoxData)
    await Promise.resolve()
    expect(action.execute).toHaveBeenCalledOnce()
    mutation.resolve()
    await Promise.resolve()
    expect(state.isReloadingQueueSnapshot.value).toBe(true)
    expect(state.options.onClearPendingSuccess).toHaveBeenCalledOnce()
    expect(state.options.reloadQueueStatus).not.toHaveBeenCalled()
    queueReload.resolve(loaded())
    await Promise.resolve()
    expect(state.options.reloadQueueStatus).toHaveBeenCalledOnce()
    expect(state.isQueueMutationPending.value).toBe(true)
    statusReload.resolve(loaded())
    await run
    expect(state.isQueueMutationPending.value).toBe(false)
    expect(state.isReloadingQueueSnapshot.value).toBe(false)
    expect(state.options.finishContext).toHaveBeenCalledOnce()
  })

  it.each(['cancel', 'close'])(
    'confirmation %s leaves rows and requests untouched',
    async (reason) => {
      vi.mocked(ElMessageBox.confirm).mockRejectedValue(reason)
      const state = setup()
      const action = operation({ confirm: { message: '确认' } })
      await state.clearQueue(action)
      expect(action.execute).not.toHaveBeenCalled()
      expect(state.options.reloadQueue).not.toHaveBeenCalled()
      expect(state.options.onClearPendingSuccess).not.toHaveBeenCalled()
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(state.isQueueMutationPending.value).toBe(false)
    },
  )

  it('shows the server business message and does not refresh a failed operation', async () => {
    const state = setup()
    await state.clearQueue(
      operation({
        execute: vi.fn().mockRejectedValue(
          new HttpResponseError({
            status: 200,
            data: { code: 400, message: '拒绝', data: { token: 'secret' } },
            config: { method: 'post', url: '/api/queue/action?token=secret#private' },
          }),
        ),
      }),
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('请求失败：拒绝')
    expect(console.error).toHaveBeenCalledWith(expect.any(String), {
      method: 'POST',
      path: '/api/queue/action',
      status: 200,
    })
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
    expect(state.options.reloadQueue).not.toHaveBeenCalled()
    expect(state.options.onClearPendingSuccess).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each([
    ['network', new AxiosError('secret', 'ERR_NETWORK'), '无法获取服务器响应'],
    [
      'timeout',
      new AxiosError('secret', 'ETIMEDOUT', { method: 'post', url: '/x', headers: {} } as never),
      '操作结果尚未确认',
    ],
    ['program', new Error('secret'), '请求失败'],
  ])('classifies %s without exposing the exception', async (_, error, message) => {
    const state = setup()
    await state.runMutation(operation({ execute: vi.fn().mockRejectedValue(error) }))
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(expect.stringContaining(message))
    expect(state.options.reloadQueue).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  const silentErrors = () => {
    const auth = new HttpResponseError({ status: 401, data: { code: 401 } })
    markAuthInvalidationHandled(auth)
    return [new CanceledError(), auth]
  }
  it('silences cancelled and handled authentication failures at every request stage', async () => {
    for (const error of silentErrors()) {
      for (const stage of ['mutation', 'queue', 'status'] as const) {
        const state = setup({
          reloadQueue: vi.fn(async () => (stage === 'queue' ? failed(error) : loaded())),
          reloadQueueStatus: vi.fn(async () => (stage === 'status' ? failed(error) : loaded())),
        })
        await state.clearQueue(
          operation({
            execute:
              stage === 'mutation' ? vi.fn().mockRejectedValue(error) : vi.fn(async () => {}),
          }),
        )
        expect(ElMessage.error).not.toHaveBeenCalled()
        expect(state.isQueueMutationPending.value).toBe(false)
      }
    }
    expect(console.error).not.toHaveBeenCalled()
  })

  it.each(['queue', 'status'])(
    'reports %s snapshot failure as refresh failure after success',
    async (stage) => {
      const state = setup({
        reloadQueue: vi.fn(async () =>
          stage === 'queue' ? failed(new Error('secret')) : loaded(),
        ),
        reloadQueueStatus: vi.fn(async () =>
          stage === 'status' ? failed(new Error('secret')) : loaded(),
        ),
      })
      await state.clearQueue(operation())
      expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('成功')
      expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
        '操作已成功，但刷新队列快照失败，请手动刷新。',
      )
      expect(state.isQueueMutationPending.value).toBe(false)
    },
  )

  it.each(['resolve', 'reject'])(
    'ignores a stale confirmation %s and releases pending',
    async (outcome) => {
      const confirmation = deferred<MessageBoxData>()
      vi.mocked(ElMessageBox.confirm).mockReturnValue(confirmation.promise)
      const state = setup()
      const action = operation({ confirm: { message: '确认' } })
      const run = state.clearQueue(action)
      state.invalidate()
      if (outcome === 'resolve') confirmation.resolve('confirm' as MessageBoxData)
      else confirmation.reject(new Error('secret'))
      await run
      expect(action.execute).not.toHaveBeenCalled()
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(state.options.finishContext).not.toHaveBeenCalled()
      expect(state.isQueueMutationPending.value).toBe(false)
    },
  )

  it.each(['mutation', 'queue', 'status'])(
    'ignores a stale %s response without later callbacks',
    async (stage) => {
      const response = deferred<void>()
      const snapshot = deferred<QueueSnapshotResult>()
      const state = setup({
        reloadQueue: vi.fn(async () => (stage === 'queue' ? snapshot.promise : loaded())),
        reloadQueueStatus: vi.fn(async () => (stage === 'status' ? snapshot.promise : loaded())),
      })
      const run = state.clearQueue(
        operation({
          execute: vi.fn(() => (stage === 'mutation' ? response.promise : Promise.resolve())),
        }),
      )
      if (stage !== 'mutation')
        await vi.waitFor(() => expect(state.isReloadingQueueSnapshot.value).toBe(true))
      state.invalidate()
      response.resolve()
      snapshot.resolve(failed(new Error('secret')))
      await run
      expect(ElMessage.error).not.toHaveBeenCalled()
      expect(state.options.finishContext).not.toHaveBeenCalled()
      if (stage === 'mutation') {
        expect(state.options.onClearPendingSuccess).not.toHaveBeenCalled()
        expect(ElMessage.success).not.toHaveBeenCalled()
      }
      if (stage !== 'status') expect(state.options.reloadQueueStatus).not.toHaveBeenCalled()
      expect(state.isQueueMutationPending.value).toBe(false)
    },
  )

  it('keeps separate instances independently operable', async () => {
    const first = setup()
    const second = setup()
    const one = operation()
    const two = operation()
    await Promise.all([first.runMutation(one), second.runMutation(two)])
    expect(one.execute).toHaveBeenCalledOnce()
    expect(two.execute).toHaveBeenCalledOnce()
  })
})
