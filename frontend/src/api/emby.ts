import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface EmbyConfig {
  emby_url: string
  emby_api_key: string
  sync_enabled: number
  sync_cron: string
  enable_refresh_library: number
  enable_extract_media_info: number
  enable_delete_netdisk: number
  enable_auth: number
  sync_all_libraries: number
  selected_libraries: string
  enable_daily_first_full_sync: number
  enable_playback_overview: number
  enable_playback_progress: number
}

export interface EmbyConfigResult {
  exists: boolean
  config?: Partial<EmbyConfig> | null
}

export interface EmbyLibraryOption {
  library_id: string
  name: string
}

export type EmbySyncMode = 'idle' | 'full' | 'incremental' | 'webhook' | 'refresh_library' | ''

export interface EmbySyncInfo {
  exists?: boolean
  sync_enabled?: number
  sync_cron?: string
  total_items?: number
  last_sync_time?: number | null
  last_full_sync_at?: number | null
  last_incremental_sync_at?: number | null
  last_saved_cursor_at?: number | null
  last_processed_count?: number | null
  last_success_sync_mode?: EmbySyncMode
  last_error?: string
  is_running?: boolean
  sync_mode?: EmbySyncMode
  started_at?: number | null
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const embyPublicMessages: Readonly<Record<string, string>> = {
  '尚未配置 Emby': '尚未配置 Emby，请先保存服务器配置',
  'Emby URL 或 API Key 为空': '请先填写并保存 Emby 服务器地址和 API Key',
  '请先填写 Emby URL 和 Emby API Key，才能提取媒体信息':
    '请先填写并保存 Emby 服务器地址和 API Key，才能提取媒体信息',
  'emby_url：必须是有效的 HTTP URL': 'Emby 服务器地址必须是有效的 HTTP URL',
  'emby_url：只支持 http 或 https': 'Emby 服务器地址只支持 HTTP 或 HTTPS',
  'emby_url：端口必须在 1-65535 之间': 'Emby 服务器地址的端口必须在 1-65535 之间',
  'sync_cron：仅支持 5 位 cron 表达式或 robfig 描述符':
    '同步时间仅支持 5 位 Cron 表达式或 robfig 描述符',
  'selected_libraries：必须是有效的 JSON 字符串': '媒体库选择格式无效，请重新选择',
}

export async function fetchEmbyConfig(http: AxiosInstance): Promise<EmbyConfigResult> {
  const response = await http.get<APIResponse<EmbyConfigResult>>(
    `${SERVER_URL}/setting/emby-config`,
  )
  const data = unwrapResponse(response)
  if (
    !data ||
    typeof data.exists !== 'boolean' ||
    (data.exists && (!data.config || typeof data.config !== 'object' || Array.isArray(data.config)))
  ) {
    throw new HttpResponseError(response)
  }
  return data
}

export async function saveEmbyConfig(http: AxiosInstance, payload: EmbyConfig): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/setting/emby-config`, payload, {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

export async function fetchEmbyLibraries(http: AxiosInstance): Promise<EmbyLibraryOption[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<EmbyLibraryOption[] | null>>(`${SERVER_URL}/emby/libraries`),
    ) ?? []
  )
}

export async function extractEmbyMediaInfo(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/setting/emby/parse`))
}

export async function startEmbySync(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/emby/sync/start`))
}

export async function fetchEmbySyncStatus(http: AxiosInstance): Promise<EmbySyncInfo | null> {
  return unwrapResponse(
    await http.get<APIResponse<EmbySyncInfo | null>>(`${SERVER_URL}/emby/sync/status`),
  )
}
