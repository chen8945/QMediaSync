import { HttpResponseError } from '@/http/errors'
import type { SyncTask, SyncScanResult, SyncScanFailure } from '@/types/syncTaskStream'
import { fetchJSONResponse } from './fetchResponse'
import { unwrapResponse } from './response'

// 首屏与 HTTP 降级共用轻量查询，不等待日志和结果明细。
export async function fetchSyncTask(syncID: number, signal?: AbortSignal): Promise<SyncTask> {
  const response = await fetchJSONResponse(`/api/sync/task?sync_id=${syncID}`, {
    credentials: 'include',
    signal,
  })
  const data = unwrapResponse<SyncTask | null>(response).data
  if (!data || data.id !== syncID) {
    throw new HttpResponseError(response)
  }
  return data
}

export async function fetchSyncTaskResult(
  syncID: number,
  signal?: AbortSignal,
): Promise<SyncScanResult | null> {
  const response = await fetchJSONResponse(`/api/sync/tasks/${syncID}/result`, {
    credentials: 'include',
    signal,
  })
  return unwrapResponse<SyncScanResult | null>(response).data
}

export interface SyncFailurePage {
  entries: SyncScanFailure[]
  total: number
  recorded: boolean
}
export async function fetchSyncTaskFailures(
  syncID: number,
  offset: number,
  signal?: AbortSignal,
): Promise<SyncFailurePage> {
  const response = await fetchJSONResponse(
    `/api/sync/tasks/${syncID}/failures?offset=${offset}&limit=100`,
    { credentials: 'include', signal },
  )
  const data = unwrapResponse<SyncFailurePage | null>(response).data
  if (!data || !Array.isArray(data.entries) || typeof data.total !== 'number')
    throw new HttpResponseError(response)
  return data
}
