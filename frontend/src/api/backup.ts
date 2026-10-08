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
  // 旧版本仅提供 is_running；停止状态不足以证明任务成功。
  status?: 'idle' | 'running' | 'completed' | 'failed'
  desc: string
  total: number
  count: number
  error_msg: string
  error_code?: string
  restore_outcome?: '' | 'not_started' | 'rolled_back' | 'committed' | 'uncertain'
  is_running: boolean
  elapsed: number
  restart_required?: boolean
  restart_supported?: boolean
  restart_requested?: boolean
}

export interface RestoreStartResponse {
  restore_receipt: string
}

export interface BackupFile {
  file_name: string
  file_size: number
  modified_at: number
}

export interface BackupFilesResponse {
  directory: string
  files: BackupFile[]
}

export function getBackupTaskStatus(
  data: BackupStatusResponse,
): 'running' | 'completed' | 'failed' | 'unknown' {
  if (data.is_running === true) return 'running'
  if (data.status === 'failed' || data.error_msg?.trim()) return 'failed'
  if (data.status === 'completed') return 'completed'
  return 'unknown'
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const backupPublicMessages: Readonly<Record<string, string>> = {
  备份文件路径为空: '备份文件不可用',
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

export async function fetchBackupFiles(http: AxiosInstance): Promise<BackupFilesResponse> {
  return unwrapResponse(
    await http.get<APIResponse<BackupFilesResponse>>(`${SERVER_URL}/backup/files`),
  )
}

export async function deleteBackup(http: AxiosInstance, recordId: number): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/backup/records/${recordId}`))
}

export async function restoreBackup(
  http: AxiosInstance,
  source: number | { file_name: string },
): Promise<RestoreStartResponse | null> {
  return unwrapResponse(
    await http.post<APIResponse<RestoreStartResponse | null>>(
      `${SERVER_URL}/backup/restore`,
      typeof source === 'number' ? { record_id: source } : { file_name: source.file_name },
    ),
  )
}

export async function uploadAndRestoreBackup(
  http: AxiosInstance,
  file: File,
): Promise<RestoreStartResponse | null> {
  const form = new FormData()
  form.append('file', file)
  return unwrapResponse(
    await http.post<APIResponse<RestoreStartResponse | null>>(
      `${SERVER_URL}/backup/upload-restore`,
      form,
      {
        headers: { 'Content-Type': 'multipart/form-data' },
        timeout: 600000,
      },
    ),
  )
}

export async function fetchBackupStatus(
  http: AxiosInstance,
  restoreReceipt?: string,
): Promise<BackupStatusResponse> {
  return unwrapResponse(
    await http.get<APIResponse<BackupStatusResponse>>(
      `${SERVER_URL}/backup/status`,
      restoreReceipt
        ? { headers: { 'X-Restore-Receipt': restoreReceipt }, skipAuthInvalidation: true }
        : undefined,
    ),
  )
}

export async function restartAfterRestore(
  http: AxiosInstance,
  restoreReceipt: string,
): Promise<BackupStatusResponse> {
  return unwrapResponse(
    await http.post<APIResponse<BackupStatusResponse>>(`${SERVER_URL}/backup/restart`, null, {
      headers: { 'X-Restore-Receipt': restoreReceipt },
      skipAuthInvalidation: true,
    }),
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
  if (isErrorBody || response.status < 200 || response.status >= 300) {
    throw new HttpResponseError(response)
  }
  return response.data
}
