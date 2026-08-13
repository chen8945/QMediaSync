import { SERVER_URL } from '@/const'
import type { AxiosInstance } from 'axios'
import {
  computed,
  readonly,
  shallowRef,
  toValue,
  unref,
  type MaybeRef,
  type MaybeRefOrGetter,
} from 'vue'

const V115_APPID_PAGE_SIZE = 50
const V115_APPID_SEARCH_FALLBACK_SERVER_URL = '/api'

export interface V115AppIDOption {
  app_id: string
  app_name: string
  display_name: string
  deprecated?: boolean
}

export interface UseV115AppIdSearchOptions {
  http: MaybeRef<AxiosInstance>
  pageSize?: MaybeRefOrGetter<number>
}

export function resolveV115AppIdSearchBaseURL(serverURL?: unknown): string {
  const value = arguments.length === 0 ? SERVER_URL : serverURL
  const normalized = typeof value === 'string' ? value.trim() : ''
  if (!normalized || normalized === 'undefined' || normalized === 'null') {
    return V115_APPID_SEARCH_FALLBACK_SERVER_URL
  }
  return normalized.replace(/\/+$/, '')
}

export function useV115AppIdSearch(options: UseV115AppIdSearchOptions) {
  const keyword = shallowRef('')
  const items = shallowRef<V115AppIDOption[]>([])
  const total = shallowRef(0)
  const loading = shallowRef(false)
  const requestRunId = shallowRef(0)
  const offset = computed(() => items.value.length)
  const hasMore = computed(() => items.value.length < total.value)
  const pageSize = computed(() => {
    const raw = toValue(options.pageSize) || V115_APPID_PAGE_SIZE
    return Math.min(Math.max(raw, 1), 100)
  })

  const search = async () => {
    const http = unref(options.http)
    const runId = requestRunId.value + 1
    requestRunId.value = runId
    loading.value = true
    try {
      const response = await http.get(`${resolveV115AppIdSearchBaseURL()}/115/appids`, {
        params: { keyword: keyword.value, offset: 0, limit: pageSize.value },
      })
      if (runId !== requestRunId.value) return
      const data = response.data?.data
      items.value = data?.items || []
      total.value = data?.total || 0
    } finally {
      if (runId === requestRunId.value) loading.value = false
    }
  }

  const loadMore = async () => {
    const http = unref(options.http)
    if (!hasMore.value) return
    const runId = requestRunId.value + 1
    requestRunId.value = runId
    loading.value = true
    try {
      const response = await http.get(`${resolveV115AppIdSearchBaseURL()}/115/appids`, {
        params: { keyword: keyword.value, offset: offset.value, limit: pageSize.value },
      })
      if (runId !== requestRunId.value) return
      const data = response.data?.data
      items.value = [...items.value, ...(data?.items || [])]
      total.value = data?.total || total.value
    } finally {
      if (runId === requestRunId.value) loading.value = false
    }
  }

  const reset = () => {
    requestRunId.value += 1
    keyword.value = ''
    items.value = []
    total.value = 0
    loading.value = false
  }

  return {
    keyword,
    items: readonly(items),
    total: readonly(total),
    loading: readonly(loading),
    hasMore,
    search,
    loadMore,
    reset,
  }
}
