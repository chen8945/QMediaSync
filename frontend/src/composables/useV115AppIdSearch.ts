import { authorizationPublicMessages, fetchV115AppIds, type V115AppIDOption } from '@/api/accounts'
import { parseHttpError } from '@/http/errors'
import type { AxiosInstance } from 'axios'
import {
  computed,
  onScopeDispose,
  readonly,
  shallowRef,
  toValue,
  unref,
  type MaybeRef,
  type MaybeRefOrGetter,
} from 'vue'

const V115_APPID_PAGE_SIZE = 50

export interface UseV115AppIdSearchOptions {
  http: MaybeRef<AxiosInstance>
  pageSize?: MaybeRefOrGetter<number>
}

export function useV115AppIdSearch(options: UseV115AppIdSearchOptions) {
  const keyword = shallowRef('')
  const resultKeyword = shallowRef<string | null>(null)
  const resultItems = shallowRef<V115AppIDOption[]>([])
  const resultTotal = shallowRef(0)
  const items = computed(() => (resultKeyword.value === keyword.value ? resultItems.value : []))
  const total = computed(() => (resultKeyword.value === keyword.value ? resultTotal.value : 0))
  const loading = shallowRef(false)
  const errorMessage = shallowRef('')
  const requestRunId = shallowRef(0)
  const offset = computed(() => items.value.length)
  const hasMore = computed(() => items.value.length < total.value)
  const pageSize = computed(() => {
    const raw = toValue(options.pageSize) || V115_APPID_PAGE_SIZE
    return Math.min(Math.max(raw, 1), 100)
  })

  const search = async () => {
    const http = unref(options.http)
    const requestedKeyword = keyword.value
    const runId = requestRunId.value + 1
    requestRunId.value = runId
    loading.value = true
    errorMessage.value = ''
    try {
      const data = await fetchV115AppIds(http, {
        keyword: requestedKeyword,
        offset: 0,
        limit: pageSize.value,
      })
      if (runId !== requestRunId.value || requestedKeyword !== keyword.value) return
      resultKeyword.value = requestedKeyword
      resultItems.value = data.items || []
      resultTotal.value = data.total || 0
    } catch (error) {
      if (runId !== requestRunId.value || requestedKeyword !== keyword.value) return
      const failure = parseHttpError(error, {
        publicMessages: authorizationPublicMessages,
        fallbackMessage: '搜索 APP ID 失败',
      })
      if (failure.shouldNotify) errorMessage.value = failure.message
    } finally {
      if (runId === requestRunId.value) loading.value = false
    }
  }

  const loadMore = async () => {
    const http = unref(options.http)
    if (loading.value || !hasMore.value) return
    const requestedKeyword = keyword.value
    const runId = requestRunId.value + 1
    requestRunId.value = runId
    loading.value = true
    errorMessage.value = ''
    try {
      const data = await fetchV115AppIds(http, {
        keyword: requestedKeyword,
        offset: offset.value,
        limit: pageSize.value,
      })
      if (runId !== requestRunId.value || requestedKeyword !== keyword.value) return
      resultItems.value = [...resultItems.value, ...(data.items || [])]
      resultTotal.value = data.total || resultTotal.value
    } catch (error) {
      if (runId !== requestRunId.value || requestedKeyword !== keyword.value) return
      const failure = parseHttpError(error, {
        publicMessages: authorizationPublicMessages,
        fallbackMessage: '加载更多 APP ID 失败',
      })
      if (failure.shouldNotify) errorMessage.value = failure.message
    } finally {
      if (runId === requestRunId.value) loading.value = false
    }
  }

  const reset = () => {
    requestRunId.value += 1
    keyword.value = ''
    resultKeyword.value = null
    resultItems.value = []
    resultTotal.value = 0
    loading.value = false
    errorMessage.value = ''
  }

  onScopeDispose(() => {
    requestRunId.value += 1
  })

  return {
    keyword,
    items: readonly(items),
    total: readonly(total),
    loading: readonly(loading),
    errorMessage: readonly(errorMessage),
    hasMore,
    search,
    loadMore,
    reset,
  }
}
