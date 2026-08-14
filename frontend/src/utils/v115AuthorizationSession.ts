export interface PendingV115Authorization {
  accountId: number
  authorizationId: string
}

export const V115_PENDING_AUTHORIZATION_STORAGE_KEY = 'qmediasync.pending-v115-authorization'

const getSessionStorage = (): Storage | null => {
  if (typeof window === 'undefined') return null
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

export const savePendingV115Authorization = (
  pending: PendingV115Authorization,
  storage: Storage | null = getSessionStorage(),
) => {
  if (!storage || pending.accountId <= 0 || !pending.authorizationId.trim()) return
  try {
    storage.setItem(
      V115_PENDING_AUTHORIZATION_STORAGE_KEY,
      JSON.stringify({
        accountId: pending.accountId,
        authorizationId: pending.authorizationId.trim(),
      }),
    )
  } catch {
    // 隐私受限的浏览器环境可能无法使用 Storage。
  }
}

export const loadPendingV115Authorization = (
  storage: Storage | null = getSessionStorage(),
): PendingV115Authorization | null => {
  if (!storage) return null
  try {
    const raw = storage.getItem(V115_PENDING_AUTHORIZATION_STORAGE_KEY)
    if (!raw) return null
    const value = JSON.parse(raw) as Partial<PendingV115Authorization>
    if (
      typeof value.accountId !== 'number' ||
      !Number.isSafeInteger(value.accountId) ||
      value.accountId <= 0 ||
      typeof value.authorizationId !== 'string' ||
      !value.authorizationId.trim()
    ) {
      return null
    }
    return { accountId: value.accountId, authorizationId: value.authorizationId.trim() }
  } catch {
    return null
  }
}

export const clearPendingV115Authorization = (
  authorizationId?: string,
  storage: Storage | null = getSessionStorage(),
) => {
  if (!storage) return
  const pending = loadPendingV115Authorization(storage)
  if (!pending || (authorizationId && pending.authorizationId !== authorizationId)) return
  try {
    storage.removeItem(V115_PENDING_AUTHORIZATION_STORAGE_KEY)
  } catch {
    // 隐私受限的浏览器环境可能无法使用 Storage。
  }
}
