import { HttpResponseError } from '@/http/errors'
import type { SyncTask } from '@/types/syncTaskStream'
import type { APIResponse } from './types'
import { fetchJSONResponse } from './fetchResponse'

// 仅用于不支持 EventSource 时的现有 HTTP 降级读取，不探测 SSE 认证状态。
export async function fetchSyncTask(syncID: number): Promise<SyncTask> {
  const response = await fetchJSONResponse(`/api/sync/task?sync_id=${syncID}`, {
    credentials: 'include',
  })
  const body = response.data as APIResponse<SyncTask> | null
  if (body?.code !== 200 || !body.data || body.data.id !== syncID) {
    throw new HttpResponseError(response)
  }
  return body.data
}
