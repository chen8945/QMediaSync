import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { getSettingsSaveResult, unwrapResponse } from './response'
import type { APIResponse, SettingsSaveResult } from './types'

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
export type UpdateStatus =
  'idle' | 'downloading' | 'install' | 'completed' | 'failed' | 'cancelled' | ''

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

export async function fetchUpdateList(
  http: AxiosInstance,
  channel: UpdateChannel,
  force = false,
): Promise<UpdateInfo[]> {
  return unwrapResponse(
    await http.get<APIResponse<UpdateInfo[]>>(
      `${SERVER_URL}/update/last?channel=${channel}${force ? '&force=1' : ''}`,
    ),
  ).data
}

export async function fetchUpdateProgress(http: AxiosInstance): Promise<UpdateProgress | null> {
  return unwrapResponse(
    await http.get<APIResponse<UpdateProgress | null>>(`${SERVER_URL}/update/progress`),
  ).data
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

export async function cancelUpdate(http: AxiosInstance): Promise<SettingsSaveResult> {
  return getSettingsSaveResult(
    await http.post<APIResponse<UpdateProgress>>(`${SERVER_URL}/update/cancel`),
  )
}

export async function fetchIsFnOS(http: AxiosInstance): Promise<boolean> {
  return unwrapResponse(await http.get<APIResponse<boolean>>(`${SERVER_URL}/path/is-fn-os`)).data
}
