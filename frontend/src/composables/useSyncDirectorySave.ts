import {
  saveSyncPathAggregate,
  syncPathPublicMessages,
  syncPathFieldErrors,
  syncPathSaveWarning,
  type SaveSyncPathPayload,
  type SaveSyncPathResponseData,
  type SyncPathFieldError,
} from '@/api/syncPaths'
import type { AxiosInstance } from 'axios'
import { parseHttpError } from '@/http/errors'
import { readonly, ref } from 'vue'

export function useSyncDirectorySave(http: AxiosInstance) {
  const errorMessage = ref('')
  const fieldErrors = ref<SyncPathFieldError[]>([])

  async function save(
    id: number,
    payload: SaveSyncPathPayload,
    idempotencyKey: string,
  ): Promise<SaveSyncPathResponseData | null> {
    errorMessage.value = ''
    fieldErrors.value = []
    try {
      const data = await saveSyncPathAggregate(http, id, payload, idempotencyKey)
      return { ...data, warnings: data.warnings.map(syncPathSaveWarning) }
    } catch (error) {
      const failure = parseHttpError(error, {
        publicMessages: syncPathPublicMessages,
        fallbackMessage: '保存同步目录失败',
      })
      if (failure.shouldNotify) {
        errorMessage.value = failure.message
        fieldErrors.value = syncPathFieldErrors(failure)
        console.error('保存同步目录失败：', failure.diagnostics)
      }
      return null
    }
  }

  async function saveAndRun(
    id: number,
    payload: SaveSyncPathPayload,
    idempotencyKey: string,
    onSuccess: (data: SaveSyncPathResponseData) => void | Promise<void>,
  ): Promise<SaveSyncPathResponseData | null> {
    const result = await save(id, payload, idempotencyKey)
    if (!result) {
      return null
    }
    await onSuccess(result)
    return result
  }

  return {
    errorMessage: readonly(errorMessage),
    fieldErrors: readonly(fieldErrors),
    saveAndRun,
  }
}
