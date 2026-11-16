import { HttpResponseError } from '@/http/errors'
import type { SyncTask } from '@/types/syncTaskStream'
import { fetchJSONResponse } from './fetchResponse'
import { unwrapResponse } from './response'

// 仅用于不支持 EventSource 时的现有 HTTP 降级读取，不探测 SSE 认证状态。
export async function fetchSyncTask(syncID: number): Promise<SyncTask> {
  const response = await fetchJSONResponse(`/api/sync/task?sync_id=${syncID}`, {
    credentials: 'include',
  })
  const data = unwrapResponse<SyncTask | null>(response).data
  if (!data || data.id !== syncID) {
    throw new HttpResponseError(response)
  }
  return data
}
