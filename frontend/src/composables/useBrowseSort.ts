import { computed, shallowRef, toValue, unref, type MaybeRef, type MaybeRefOrGetter } from 'vue'
import type { AxiosInstance } from 'axios'
import { useAuthStore } from '@/stores/auth'
import {
  fetchBrowseSortOptions,
  type BrowseScope,
  type BrowseSortOptions,
  type BrowseSortValue,
} from '@/api/files'
import type { DirInfo } from '@/typing'

const capabilityRequests = new WeakMap<AxiosInstance, Map<string, Promise<BrowseSortOptions>>>()
const memoryPreferences = new Map<string, BrowseSortValue>()

function readPreference(key: string): unknown {
  if (memoryPreferences.has(key)) return memoryPreferences.get(key)
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return undefined
    try {
      return JSON.parse(raw)
    } catch {
      return undefined
    }
  } catch {
    return memoryPreferences.get(key)
  }
}

function normalizePreference(value: unknown, options: BrowseSortOptions): BrowseSortValue {
  if (!value || typeof value !== 'object') return { ...options.default }
  const stored = value as Partial<BrowseSortValue>
  if (
    !stored.sort_by ||
    !options.fields.includes(stored.sort_by) ||
    (stored.sort_order !== 'asc' && stored.sort_order !== 'desc') ||
    (stored.folders_first !== undefined && typeof stored.folders_first !== 'boolean')
  )
    return { ...options.default }
  return {
    sort_by: stored.sort_by,
    sort_order: stored.sort_by === 'default' ? 'asc' : stored.sort_order,
    ...(options.folders_first && stored.sort_by !== 'default'
      ? { folders_first: stored.folders_first ?? options.default.folders_first ?? true }
      : {}),
  }
}

export function browseSortQuery(value: BrowseSortValue) {
  return value.sort_by === 'default' ? { sort_by: value.sort_by } : { ...value }
}

export function useBrowseSort(
  http: MaybeRef<AxiosInstance>,
  source: MaybeRefOrGetter<string>,
  account: MaybeRefOrGetter<number | null | undefined>,
  scope: BrowseScope,
) {
  const auth = useAuthStore()
  const contextKey = computed(
    () =>
      `qmediasync-browse-sort:v1:${JSON.stringify([
        auth.user?.id ?? 'anonymous',
        scope,
        toValue(source),
        toValue(source) === 'local' ? 0 : (toValue(account) ?? 0),
      ])}`,
  )
  const capabilities = shallowRef<BrowseSortOptions | null>(null)
  const selection = shallowRef<BrowseSortValue>({ sort_by: 'default', sort_order: 'asc' })
  const applied = shallowRef<BrowseSortValue | null>(null)
  const loadedKey = shallowRef('')
  const ready = computed(() => loadedKey.value === contextKey.value && !!capabilities.value)

  async function prepare() {
    if (ready.value) return true
    const key = contextKey.value
    const client = unref(http)
    let requests = capabilityRequests.get(client)
    if (!requests) {
      requests = new Map()
      capabilityRequests.set(client, requests)
    }
    const capabilityKey = `${toValue(source)}:${scope}`
    let request = requests.get(capabilityKey)
    if (!request) {
      request = fetchBrowseSortOptions(client, toValue(source), scope)
      requests.set(capabilityKey, request)
      request.catch(() => requests.delete(capabilityKey))
    }
    const options = await request
    if (key !== contextKey.value) return false
    if (!ready.value) {
      capabilities.value = options
      selection.value = normalizePreference(readPreference(key), options)
      applied.value = selection.value
      loadedKey.value = key
    }
    return true
  }

  function choose(value: BrowseSortValue) {
    if (ready.value && capabilities.value) {
      selection.value = normalizePreference(value, capabilities.value)
    }
  }

  function commit(value: BrowseSortValue) {
    if (!ready.value) return
    applied.value = { ...value }
    try {
      localStorage.setItem(contextKey.value, JSON.stringify(applied.value))
      memoryPreferences.delete(contextKey.value)
    } catch {
      memoryPreferences.set(contextKey.value, applied.value)
      // 偏好存储不可用时，当前会话继续使用内存中的成功选择。
    }
  }

  function rollback() {
    if (ready.value && applied.value) selection.value = { ...applied.value }
  }

  return { capabilities, selection, ready, contextKey, prepare, choose, commit, rollback }
}

const names = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

// 仅本地完整目录数组使用；远程列表必须保留服务端顺序。
export function sortLocalDirectories(directories: DirInfo[], sort: BrowseSortValue): DirInfo[] {
  const byName = (a: DirInfo, b: DirInfo) =>
    names.compare(a.name, b.name) || a.path.localeCompare(b.path)
  const direction = sort.sort_order === 'asc' ? 1 : -1
  return [...directories].sort((a, b) => {
    if (sort.sort_by !== 'time') return direction * byName(a, b)
    const aTime = a.modified_time
    const bTime = b.modified_time
    const aKnown = typeof aTime === 'number' && Number.isFinite(aTime)
    const bKnown = typeof bTime === 'number' && Number.isFinite(bTime)
    if (aKnown !== bKnown) return aKnown ? -1 : 1
    return (aKnown && bKnown ? direction * (aTime - bTime) : 0) || byName(a, b)
  })
}
