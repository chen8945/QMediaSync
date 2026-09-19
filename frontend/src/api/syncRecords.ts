import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface ApiSyncRecord {
  id: number
  created_at: number
  finish_at: number | null
  status: number
  sub_status: number
  total: number
  new_strm: number
  new_meta: number
  new_upload: number
  local_path: string
  remote_path: string
  fail_reason: string
}

export interface SyncRecordsQuery {
  page: number
  page_size: number
}

export interface SyncRecordsPage {
  records: ApiSyncRecord[] | null
  total: number
}

export interface DeleteSyncRecordsResult {
  deleted_ids: number[]
  failures: { id: number; reason: string }[]
}

// 对应 sync 控制器和 IDListRequest 的静态错误；失败明细中的内部原因不直接展示。
export const syncRecordPublicMessages: Readonly<Record<string, string>> = {
  'ids：不能为空': '请选择同步记录',
  'ids：必须大于 0': '同步记录 ID 必须大于 0',
}

export async function fetchSyncRecords(http: AxiosInstance, params: SyncRecordsQuery) {
  return unwrapResponse(
    await http.get<APIResponse<SyncRecordsPage>>(`${SERVER_URL}/sync/records`, { params }),
  )
}

export async function deleteSyncRecords(
  http: AxiosInstance,
  ids: number[],
  options: { batch?: boolean } = {},
) {
  return unwrapResponse(
    await http.post<APIResponse<DeleteSyncRecordsResult | null>>(
      `${SERVER_URL}/sync/delete-records`,
      { ids },
      {
        headers: { 'Content-Type': 'application/json' },
        ...(options.batch ? { timeout: 60000 } : {}),
      },
    ),
  )
}
