// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { ElMessage } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useBackupStore } from '@/stores/backup'
import type { BackupOperationState, BackupOperationView } from '@/typing'

const OPERATION_TOKEN_HEADER = 'X-Backup-Operation-Token'

function operationView(state: BackupOperationState): BackupOperationView {
  return {
    operation_id: 'operation-1',
    kind: 'backup',
    state,
    progress: { message: '正在导出', completed: 3, total: 6 },
    started_at: 100,
    updated_at: 130,
  }
}

// 状态轮询的契约：受理后立即首查、固定 2 秒节奏、终态停止，
// 且一次性令牌只走请求头，不进入 URL 或持久化浏览器存储。
describe('useBackupStore 状态轮询', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    localStorage.clear()
    sessionStorage.clear()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('受理后立即首查并按 2 秒轮询，令牌只出现在请求头', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: operationView('running') } })
    const http = { get } as never
    const store = useBackupStore()

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    expect(get).toHaveBeenCalledTimes(1)

    const [url, config] = get.mock.calls[0]
    expect(url).toContain('/backup/status')
    expect(url).not.toContain('secret')
    expect(config.params).toEqual({ operation_id: 'operation-1' })
    expect(config.headers[OPERATION_TOKEN_HEADER]).toBe('secret')

    await vi.advanceTimersByTimeAsync(2000)
    expect(get).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(2000)
    expect(get).toHaveBeenCalledTimes(3)

    expect(store.isRunning).toBe(true)
    expect(store.progressPercent).toBe(50)
    // setup store 必须返回全部状态，DevTools 与插件才看得到；令牌仍只驻留内存。
    expect(Object.keys(store.$state)).toEqual(
      expect.arrayContaining([
        'operation',
        'taskType',
        'showProgressDialog',
        'errorRetryCount',
        '_operationId',
        '_operationToken',
      ]),
    )
    expect(localStorage.getItem(OPERATION_TOKEN_HEADER)).toBeNull()
    expect(JSON.stringify(localStorage)).not.toContain('secret')
    expect(JSON.stringify(sessionStorage)).not.toContain('secret')
    expect(document.cookie).not.toContain('secret')

    store.stopProgressPolling()
  })

  it('进入终态后停止轮询并复位状态', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: operationView('completed') } })
    const http = { get } as never
    const store = useBackupStore()

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    await vi.advanceTimersByTimeAsync(0)
    expect(store.isRunning).toBe(false)

    await vi.advanceTimersByTimeAsync(4000)
    expect(get).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(2000)
    expect(store.showProgressDialog).toBe(false)
    expect(store.operation).toBeNull()
  })

  it('停止轮询后不再发起状态查询', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: operationView('running') } })
    const http = { get } as never
    const store = useBackupStore()

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    store.stopProgressPolling()
    await vi.advanceTimersByTimeAsync(6000)
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('慢响应不会重叠轮询，停止后也不会写回旧状态', async () => {
    let resolveFirstPoll!: (value: { data: { code: number; data: BackupOperationView } }) => void
    const firstPoll = new Promise<{ data: { code: number; data: BackupOperationView } }>(
      (resolve) => {
        resolveFirstPoll = resolve
      },
    )
    const get = vi.fn().mockReturnValue(firstPoll)
    const http = { get } as never
    const store = useBackupStore()

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    await vi.advanceTimersByTimeAsync(6000)
    expect(get).toHaveBeenCalledTimes(1)

    store.stopProgressPolling()
    resolveFirstPoll({ data: { code: 200, data: operationView('running') } })
    await Promise.resolve()

    expect(store.operation).toBeNull()
    expect(store.isRunning).toBe(true)
  })

  it('页面隐藏时暂停，重新可见后在飞行请求结束时恢复且不重叠', async () => {
    let hidden = false
    const originalHidden = Object.getOwnPropertyDescriptor(document, 'hidden')
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })

    let resolveFirstPoll!: (value: { data: { code: number; data: BackupOperationView } }) => void
    const firstPoll = new Promise<{ data: { code: number; data: BackupOperationView } }>(
      (resolve) => {
        resolveFirstPoll = resolve
      },
    )
    const get = vi.fn().mockReturnValueOnce(firstPoll).mockResolvedValue({
      data: { code: 200, data: operationView('running') },
    })
    const http = { get } as never
    const store = useBackupStore()

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    expect(get).toHaveBeenCalledTimes(1)

    hidden = true
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(6000)
    expect(get).toHaveBeenCalledTimes(1)

    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    expect(get).toHaveBeenCalledTimes(1)

    resolveFirstPoll({ data: { code: 200, data: operationView('running') } })
    await Promise.resolve()
    expect(get).toHaveBeenCalledTimes(2)

    store.stopProgressPolling()
    if (originalHidden) Object.defineProperty(document, 'hidden', originalHidden)
  })

  it('状态令牌失效时关闭进度弹窗，避免留下无法关闭的遮罩', async () => {
    const get = vi.fn().mockResolvedValue({
      data: { code: 400, message: '备份操作不存在或状态令牌无效' },
    })
    const http = { get } as never
    const store = useBackupStore()
    const error = vi.spyOn(ElMessage, 'error').mockReturnValue({ close: vi.fn() } as never)

    store.startOperationPolling('backup', { operation_id: 'operation-1', token: 'secret' }, http)
    await Promise.resolve()

    expect(error).toHaveBeenCalledWith('备份操作不存在或状态令牌无效')
    expect(store.showProgressDialog).toBe(false)
    expect(store.operation).toBeNull()
    expect(store.isRunning).toBe(false)
  })
})
