import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { QueueStatusSnapshot } from '@/utils/queueStatusUtils'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface DownloadTask {
  id: string
  source: string
  file_name: string
  local_full_path: string
  remote_path: string
  remote_full_path: string
  status: 0 | 1 | 2 | 3 | 4
  size: number
  start_time: number
  end_time: number
  remote_file_id: string
  remote_pick_code?: string
  remote_sha1?: string
  remote_md5?: string
  error: string
  source_type: string
  retry_count: number
  last_retry_time: number
}

export interface DownloadQueueQuery {
  page: number
  page_size: number
  status: number
}

export interface DownloadQueueSnapshot {
  list?: DownloadTask[] | null
  total: number
  downloading?: number
  queue_status?: QueueStatusSnapshot | boolean
}

const queueURL = `${SERVER_URL}/download/queue`

export async function fetchDownloadQueue(
  http: AxiosInstance,
  query: DownloadQueueQuery,
): Promise<DownloadQueueSnapshot> {
  return unwrapResponse(
    await http.get<APIResponse<DownloadQueueSnapshot>>(queueURL, { params: query }),
  )
}

export async function fetchDownloadQueueStatus(
  http: AxiosInstance,
): Promise<QueueStatusSnapshot | boolean> {
  return unwrapResponse(
    await http.get<APIResponse<QueueStatusSnapshot | boolean>>(`${queueURL}/status`),
  )
}

export async function clearPendingDownloadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/clear-pending`))
}

export async function clearCompletedDownloadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/clear-success-failed`))
}

export async function retryFailedDownloadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/retry-failed`))
}

export async function pauseDownloadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/stop`))
}

export async function resumeDownloadQueue(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${queueURL}/start`))
}
