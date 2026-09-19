import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { QueueStatusSnapshot } from '@/utils/queueStatusUtils'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface UploadTask {
  id: string
  source: string
  source_type: string
  file_name: string
  local_full_path: string
  remote_path_id?: string
  status: 0 | 1 | 2 | 3 | 4 | 5 | 6
  file_size: number
  start_time: number
  end_time: number
  remote_file_id: string
  remote_full_path: string
  remote_pick_code?: string
  remote_sha1?: string
  remote_md5?: string
  replaced_remote_file_id?: string
  error: string
  retry_count: number
  last_retry_time: number
  uploaded_bytes?: number
  upload_result?: string
  resume_state?: string
  rapid_wait_until?: number
  upload_phase?: string
  upload_speed_bytes?: number
  progress_percent?: number
  total_parts?: number
  uploaded_parts?: number
  source_cleanup_status?: string
  source_cleanup_error?: string
  rapid_wait_attempts?: number
  relative_path?: string
  source_deleted_at?: number
}

export interface UploadQueueQuery {
  page: number
  page_size: number
  status: number
}

export interface UploadQueueSnapshot {
  list?: UploadTask[] | null
  total: number
  uploading?: number
  queue_status?: QueueStatusSnapshot | boolean
}

const queueURL = `${SERVER_URL}/upload/queue`

export async function fetchUploadQueue(
  http: AxiosInstance,
  query: UploadQueueQuery,
): Promise<UploadQueueSnapshot> {
  return unwrapResponse(
    await http.get<APIResponse<UploadQueueSnapshot>>(queueURL, { params: query }),
  )
}

export async function fetchUploadQueueStatus(
  http: AxiosInstance,
): Promise<QueueStatusSnapshot | boolean> {
  return unwrapResponse(
    await http.get<APIResponse<QueueStatusSnapshot | boolean>>(`${queueURL}/status`),
  )
}

export async function clearPendingUploadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/clear-pending`))
}

export async function clearCompletedUploadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/clear-success-failed`))
}

export async function retryFailedUploadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/retry-failed`))
}

export async function pauseUploadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/stop`))
}

export async function resumeUploadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/start`))
}
