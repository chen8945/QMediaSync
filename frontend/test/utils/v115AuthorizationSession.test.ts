import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  clearPendingV115Authorization,
  loadPendingV115Authorization,
  savePendingV115Authorization,
  type PendingV115Authorization,
} from '@/utils/v115AuthorizationSession'

const createStorage = () => {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
    removeItem: (key: string) => values.delete(key),
  } as unknown as Storage
}

describe('v115AuthorizationSession', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('保存并读取直接跳转 OAuth 的待处理会话', () => {
    const storage = createStorage()
    const pending: PendingV115Authorization = { accountId: 12, authorizationId: 'session-1' }

    savePendingV115Authorization(pending, storage)

    expect(loadPendingV115Authorization(storage)).toEqual(pending)
  })

  it('只清理匹配的会话，避免误删新流程', () => {
    const storage = createStorage()
    const pending = { accountId: 12, authorizationId: 'session-2' }
    savePendingV115Authorization(pending, storage)

    clearPendingV115Authorization('other-session', storage)
    expect(loadPendingV115Authorization(storage)).toEqual(pending)

    clearPendingV115Authorization('session-2', storage)
    expect(loadPendingV115Authorization(storage)).toBeNull()
  })

  it('忽略损坏或不完整的存储值', () => {
    const storage = createStorage()
    storage.setItem('qmediasync.pending-v115-authorization', '{"accountId":0}')

    expect(loadPendingV115Authorization(storage)).toBeNull()
  })
})
