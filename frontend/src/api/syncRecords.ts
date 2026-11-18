import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParsedHttpError } from '@/http/errors'
import { isAPIResponse } from '@/http/envelope'
import { unwrapResponse } from './response'
import type { APIResponse, SettingsSaveResult } from './types'
import type { SyncLedgerState, SyncScanResult, SyncTaskDurations } from '@/types/syncTaskStream'

export interface ApiSyncRecord extends SyncLedgerState, SyncTaskDurations {
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
  scan_result?: SyncScanResult | null
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

export function syncRecordDeleteResult(
  error: ParsedHttpError,
): DeleteSyncRecordsResult | undefined {
  const response = error.response
  const body = response?.data
  if (
    !response ||
    response.status < 400 ||
    !isAPIResponse(response) ||
    !body ||
    typeof body !== 'object' ||
    !('data' in body)
  )
    return undefined
  const data = body.data
  if (
    !data ||
    typeof data !== 'object' ||
    !('deleted_ids' in data) ||
    !('failures' in data) ||
    !Array.isArray(data.deleted_ids) ||
    !Array.isArray(data.failures) ||
    !data.deleted_ids.every((id) => Number.isSafeInteger(id) && id > 0) ||
    !data.failures.every(
      (item) =>
        item &&
        typeof item === 'object' &&
        Number.isSafeInteger(item.id) &&
        item.id > 0 &&
        typeof item.reason === 'string',
    )
  )
    return undefined
  return { deleted_ids: data.deleted_ids, failures: data.failures }
}

// 对应 sync 控制器和 IDListRequest 的静态错误；失败明细中的内部原因不直接展示。
export const syncRecordPublicMessages: Readonly<Record<string, string>> = {
  'ids：不能为空': '请选择同步记录',
  'ids：必须大于 0': '同步记录 ID 必须大于 0',
}

export async function fetchSyncRecords(http: AxiosInstance, params: SyncRecordsQuery) {
  return unwrapResponse(
    await http.get<APIResponse<SyncRecordsPage>>(`${SERVER_URL}/sync/records`, { params }),
  ).data
}

export async function deleteSyncRecords(
  http: AxiosInstance,
  ids: number[],
  options: { batch?: boolean } = {},
): Promise<SettingsSaveResult & { data: DeleteSyncRecordsResult | null }> {
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
