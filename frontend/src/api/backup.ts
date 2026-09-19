import { isAxiosError, type AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError, type HttpErrorResponse } from '@/http/errors'
import type { BackupConfig, BackupRecordsResponse } from '@/typing'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export type BackupConfigInput = Pick<
  BackupConfig,
  'backup_enabled' | 'backup_cron' | 'backup_retention' | 'backup_max_count' | 'backup_compress'
>

export interface BackupStatusResponse {
  type: string
  desc: string
  total: number
  count: number
  error_msg: string
  is_running: boolean
  elapsed: number
}

// 固定文案来自 controllers/backup.go 与 requests/backup.go；不公开文件路径和底层错误。
export const backupPublicMessages: Readonly<Record<string, string>> = {
  '备份任务正在运行，请稍后再试': '备份任务正在运行，请稍后再试',
  '备份或恢复任务正在运行，请稍后再试': '备份或恢复任务正在运行，请稍后再试',
  '无效的备份记录 ID': '无效的备份记录 ID',
  备份记录不存在: '备份记录不存在',
  备份文件路径为空: '备份文件不可用',
  请求参数不正确: '请求参数不正确',
  '请指定要恢复的备份记录 ID': '请指定要恢复的备份记录 ID',
  请上传备份文件: '请上传备份文件',
  '仅支持 .zip 格式的备份文件': '仅支持 .zip 格式的备份文件',
  保存上传文件失败: '保存上传文件失败',
  'backup_enabled：不是允许的取值': '自动备份设置无效',
  'backup_cron：仅支持 5 位 cron 表达式或 robfig 描述符': '仅支持 5 位 Cron 表达式或 robfig 描述符',
  'backup_retention：取值超出允许范围': '备份保留天数必须在 1 到 365 之间',
  'backup_max_count：取值超出允许范围': '最大备份数必须在 0 到 1000 之间',
  'backup_compress：不是允许的取值': '备份压缩设置无效',
}

export async function fetchBackupConfig(http: AxiosInstance): Promise<BackupConfig> {
  return unwrapResponse(await http.get<APIResponse<BackupConfig>>(`${SERVER_URL}/backup/config`))
}

export async function saveBackupConfig(
  http: AxiosInstance,
  config: BackupConfigInput,
): Promise<void> {
  unwrapResponse(await http.put<APIResponse<null>>(`${SERVER_URL}/backup/config`, config))
}

export async function fetchBackupRecords(
  http: AxiosInstance,
  params: { page: number; page_size: number; type: string },
): Promise<BackupRecordsResponse> {
  return unwrapResponse(
    await http.get<APIResponse<BackupRecordsResponse>>(`${SERVER_URL}/backup/list`, { params }),
  )
}

export async function createBackup(http: AxiosInstance, reason: string): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/backup/create`, { reason }))
}

export async function deleteBackup(http: AxiosInstance, recordId: number): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/backup/records/${recordId}`))
}

export async function restoreBackup(http: AxiosInstance, recordId: number): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/backup/restore`, { record_id: recordId }),
  )
}

export async function uploadAndRestoreBackup(http: AxiosInstance, file: File): Promise<void> {
  const form = new FormData()
  form.append('file', file)
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/backup/upload-restore`, form, {
      headers: { 'Content-Type': 'multipart/form-data' },
      timeout: 600000,
    }),
  )
}

export async function fetchBackupStatus(http: AxiosInstance): Promise<BackupStatusResponse> {
  return unwrapResponse(
    await http.get<APIResponse<BackupStatusResponse>>(`${SERVER_URL}/backup/status`),
  )
}

async function decodeDownloadError(response: HttpErrorResponse): Promise<boolean> {
  const body = response.data
  if (!(body instanceof Blob)) return true
  // 正常 ZIP 只读取前缀；JSON 错误即使缺少 MIME 类型也不能被下载为备份。
  const prefix = await body.slice(0, 64).text()
  if (!/json|text\//i.test(body.type) && !/^\s*[\[{<]/.test(prefix)) return false
  const raw = await body.text()
  try {
    response.data = JSON.parse(raw)
  } catch {
    response.data = raw
  }
  return true
}

export async function downloadBackup(http: AxiosInstance, recordId: number): Promise<Blob> {
  let response
  try {
    response = await http.get<Blob>(`${SERVER_URL}/backup/download/${recordId}`, {
      responseType: 'blob',
    })
  } catch (error) {
    if (isAxiosError(error) && error.response) {
      // 保留拦截器已处理认证失效的原异常身份，只解码用于错误分类的响应体。
      await decodeDownloadError(error.response)
    }
    throw error
  }
  const isErrorBody = await decodeDownloadError(response)
  if (
    isErrorBody ||
    (response.status !== undefined && (response.status < 200 || response.status >= 300))
  ) {
    throw new HttpResponseError(response)
  }
  return response.data
}
