import type { AxiosInstance } from 'axios'
import { unwrapResponse } from './response'

export interface StrmResult {
  id: number
  parent_task_id: number
  upload_task_id: number
  sync_path_id: number
  source: string
  task_type: string
  file_name: string
  directory_path: string
  status: string
  skip_reason?: string | null
  total_items?: number | null
  accepted_items?: number | null
  failed_items?: number | null
  skipped_items?: number | null
}
export interface StrmResultQuery {
  page: number
  page_size: number
  id?: number
  parent_task_id?: number
  upload_task_id?: number
  sync_path_id?: number
}
export async function fetchStrmResults(http: AxiosInstance, params: StrmResultQuery) {
  return unwrapResponse<{ items: StrmResult[]; total: number }>(
    await http.get('/api/strm/tasks', { params }),
  )
}
export function strmResultStatus(status: string): string {
  return (
    (
      {
        pending: '等待处理',
        running: '处理中',
        finalizing: '正在完成处理',
        waiting_children: '等待子项',
        completed: '已完成',
        failed: '失败',
        cancelled: '已取消',
        skipped: '已跳过',
      } as Record<string, string>
    )[status] ?? `未知状态（${status || '未记录'}）`
  )
}
