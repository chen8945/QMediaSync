import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  useQueueMutations,
  type QueueMutationOperationOptions,
} from '@/composables/useQueueMutations'

const { confirmMock, messageErrorMock, messageSuccessMock } = vi.hoisted(() => ({
  confirmMock: vi.fn(),
  messageErrorMock: vi.fn(),
  messageSuccessMock: vi.fn(),
}))

vi.mock('element-plus', () => ({
  ElMessageBox: { confirm: confirmMock },
  ElMessage: { error: messageErrorMock, success: messageSuccessMock },
}))

vi.mock('@/utils/messageBoxUtils', () => ({
  isMessageBoxCancelError: (error: unknown) => error === 'cancel',
}))

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const operation = (overrides: Partial<QueueMutationOperationOptions> = {}) => ({
  endpoint: '/queue/action',
  successMessage: '成功',
  businessErrorMessage: (message: string | undefined) => `业务失败：${message || ''}`,
  requestErrorMessage: '请求失败',
  ...overrides,
})

const setup = (overrides: Partial<Parameters<typeof useQueueMutations>[0]> = {}) => {
  let current = true
  let version = 0
  const finishContext = vi.fn()
  const options = {
    post: vi.fn(async () => ({ data: { code: 200 } })),
    reloadQueue: vi.fn(async () => true),
    reloadQueueStatus: vi.fn(async () => true),
    startContext: vi.fn(() => ({ contextVersion: ++version })),
    isContextCurrent: vi.fn(() => current),
    finishContext,
    ...overrides,
  }
  return {
    ...useQueueMutations(options),
    options,
    invalidate: () => {
      current = false
    },
    makeCurrent: () => {
      current = true
    },
  }
}

afterEach(() => {
  vi.clearAllMocks()
  confirmMock.mockResolvedValue(true)
})

describe('useQueueMutations', () => {
  it('holds pending through mutation and snapshot reload and rejects a second operation', async () => {
    const reload = deferred<boolean>()
    const state = setup({ reloadQueue: vi.fn(() => reload.promise) })
    const first = state.runMutation(operation())
    expect(state.isQueueMutationPending.value).toBe(true)
    const second = state.runMutation(operation({ endpoint: '/other' }))
    expect(state.options.post).toHaveBeenCalledTimes(1)
    expect(state.isQueueMutationPending.value).toBe(true)
    reload.resolve(true)
    await first
    expect(state.isQueueMutationPending.value).toBe(false)
    expect(state.options.finishContext).toHaveBeenCalledTimes(1)
    await second
  })

  it('reports request, business, and snapshot errors separately', async () => {
    const requestError = setup({
      post: vi.fn().mockRejectedValue(new Error('network')),
      onMutationError: vi.fn(),
    })
    await requestError.runMutation(operation())
    expect(requestError.options.onMutationError).toHaveBeenCalledWith(expect.any(Error), '请求失败')
    expect(requestError.isQueueMutationPending.value).toBe(false)

    messageErrorMock.mockClear()
    const businessError = setup({
      post: vi.fn(async () => ({ data: { code: 400, message: '拒绝' } })),
      onMutationError: vi.fn(),
    })
    await businessError.runMutation(operation())
    expect(businessError.options.onMutationError).toHaveBeenCalledWith(
      { data: { code: 400, message: '拒绝' } },
      '业务失败：拒绝',
    )

    messageErrorMock.mockClear()
    const snapshotError = setup({
      reloadQueue: vi.fn(async () => false),
      onSnapshotError: vi.fn(),
    })
    await snapshotError.runMutation(operation())
    expect(snapshotError.options.onSnapshotError).toHaveBeenCalledWith(
      '操作已成功，但刷新队列快照失败，请手动刷新。',
    )
    expect(snapshotError.isQueueMutationPending.value).toBe(false)
  })

  it('waits for both queue snapshots when clearing pending tasks', async () => {
    const queueReload = deferred<boolean>()
    const statusReload = deferred<boolean>()
    const state = setup({
      reloadQueue: vi.fn(() => queueReload.promise),
      reloadQueueStatus: vi.fn(() => statusReload.promise),
    })
    const run = state.clearQueue(operation())
    expect(state.options.reloadQueueStatus).not.toHaveBeenCalled()
    queueReload.resolve(true)
    await vi.waitFor(() => expect(state.options.reloadQueueStatus).toHaveBeenCalledTimes(1))
    expect(state.isQueueMutationPending.value).toBe(true)
    statusReload.resolve(true)
    await run
    expect(state.isQueueMutationPending.value).toBe(false)
  })

  it('does not write stale errors or finish context after invalidation', async () => {
    const post = deferred<{ data: { code: number } }>()
    const state = setup({ post: vi.fn(() => post.promise) })
    const run = state.runMutation(operation())
    state.invalidate()
    post.reject(new Error('stale'))
    await run
    expect(messageErrorMock).not.toHaveBeenCalled()
    expect(state.options.finishContext).not.toHaveBeenCalled()
    expect(state.isQueueMutationPending.value).toBe(false)
  })

  it('keeps separate instances independently operable', async () => {
    const first = setup()
    const second = setup()
    const firstRun = first.runMutation(operation())
    const secondRun = second.runMutation(operation({ endpoint: '/second' }))
    await Promise.all([firstRun, secondRun])
    expect(first.options.post).toHaveBeenCalledWith('/queue/action')
    expect(second.options.post).toHaveBeenCalledWith('/second')
  })
})
