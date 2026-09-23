import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParsedHttpError } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface UpdateInfo {
  version: string
  published_at?: number
  date: string
  note: string
  url: string
  latest?: boolean
  current?: boolean
}

export type UpdateChannel = 'github' | 'gitee'
export type UpdateStatus = 'downloading' | 'install' | 'completed' | 'failed' | 'cancelled' | ''

export interface UpdateProgress {
  progress: number
  total_size: number
  downloaded: number
  status: UpdateStatus
  version?: string
  error_message?: string
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const updatePublicMessages: Readonly<Record<string, string>> = {
  'version：不能为空': '请选择更新版本',
  'version：版本号格式不正确': '版本号格式不正确',
  'channel：不是允许的取值': '更新渠道无效',
}

export function isUpdateNotStarted(error: ParsedHttpError): boolean {
  const response = error.response
  const body = response?.data
  return (
    error.kind === 'application' &&
    response?.status === 200 &&
    body !== null &&
    typeof body === 'object' &&
    'code' in body &&
    body.code === 500 &&
    'message' in body &&
    body.message === '未开始更新' &&
    'data' in body &&
    body.data === null
  )
}

export async function fetchUpdateList(
  http: AxiosInstance,
  channel: UpdateChannel,
  force = false,
): Promise<UpdateInfo[]> {
  return unwrapResponse(
    await http.get<APIResponse<UpdateInfo[]>>(
      `${SERVER_URL}/update/last?channel=${channel}${force ? '&force=1' : ''}`,
    ),
  )
}

export async function fetchUpdateProgress(http: AxiosInstance): Promise<UpdateProgress | null> {
  return unwrapResponse(
    await http.get<APIResponse<UpdateProgress | null>>(`${SERVER_URL}/update/progress`),
  )
}

export async function startUpdate(
  http: AxiosInstance,
  version: string,
  channel: UpdateChannel,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<UpdateProgress>>(`${SERVER_URL}/update/to-version`, {
      version,
      channel,
    }),
  )
}

export async function cancelUpdate(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<UpdateProgress>>(`${SERVER_URL}/update/cancel`))
}

export async function fetchIsFnOS(http: AxiosInstance): Promise<boolean> {
  return unwrapResponse(await http.get<APIResponse<boolean>>(`${SERVER_URL}/path/is-fn-os`))
}
