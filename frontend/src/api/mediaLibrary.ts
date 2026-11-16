import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import { getSettingsSaveResult, unwrapResponse } from './response'
import type { APIResponse, SettingsSaveResult } from './types'

export interface MediaLibraryPathMapping {
  emby_root: string
  local_root: string
  sync_path_id?: number
}

export type MediaServerType = 'emby' | 'jellyfin'

export interface MediaLibraryConfig {
  enabled: boolean
  enable_media_notification: number
  server_url: string
  api_key: string
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
  path_mappings: MediaLibraryPathMapping[]
}

export interface MediaLibraryConfigResult {
  exists: boolean
  config?: Partial<MediaLibraryConfig> | null
}

export interface MediaLibraryLibraryOption {
  library_id: string
  name: string
}

export type MediaLibrarySyncMode =
  'idle' | 'full' | 'incremental' | 'webhook' | 'refresh_library' | ''

export interface MediaLibrarySyncInfo {
  exists?: boolean
  sync_enabled?: number
  sync_cron?: string
  total_items?: number
  last_sync_time?: number | null
  last_full_sync_at?: number | null
  last_incremental_sync_at?: number | null
  last_saved_cursor_at?: number | null
  last_processed_count?: number | null
  last_success_sync_mode?: MediaLibrarySyncMode
  last_error?: string
  is_running?: boolean
  sync_mode?: MediaLibrarySyncMode
  started_at?: number | null
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const mediaLibraryPublicMessages: Readonly<Record<string, string>> = {
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

export async function fetchMediaLibraryConfig(
  http: AxiosInstance,
  serverType: MediaServerType,
): Promise<MediaLibraryConfigResult> {
  const response = await http.get<APIResponse<MediaLibraryConfigResult>>(
    `${SERVER_URL}/setting/${serverType}-config`,
  )
  const data = unwrapResponse(response).data
  if (
    !data ||
    typeof data.exists !== 'boolean' ||
    (data.exists && (!data.config || typeof data.config !== 'object' || Array.isArray(data.config)))
  ) {
    throw new HttpResponseError(response)
  }
  const mappings = data.config?.path_mappings
  if (
    mappings !== undefined &&
    (!Array.isArray(mappings) ||
      !mappings.every(
        (mapping) =>
          mapping !== null &&
          typeof mapping === 'object' &&
          typeof mapping.emby_root === 'string' &&
          typeof mapping.local_root === 'string' &&
          (mapping.sync_path_id === undefined ||
            (Number.isSafeInteger(mapping.sync_path_id) && mapping.sync_path_id >= 0)),
      ))
  ) {
    throw new HttpResponseError(response)
  }
  if (!data.config) return data
  const { emby_url, emby_api_key, jellyfin_url, jellyfin_api_key, ...config } =
    data.config as Partial<MediaLibraryConfig> & {
      emby_url?: string
      emby_api_key?: string
      jellyfin_url?: string
      jellyfin_api_key?: string
    }
  return {
    ...data,
    config: {
      ...config,
      server_url: serverType === 'emby' ? emby_url : jellyfin_url,
      api_key: serverType === 'emby' ? emby_api_key : jellyfin_api_key,
    },
  }
}

export async function saveMediaLibraryConfig(
  http: AxiosInstance,
  serverType: MediaServerType,
  payload: Omit<MediaLibraryConfig, 'path_mappings'> & {
    path_mappings?: MediaLibraryPathMapping[]
  },
): Promise<SettingsSaveResult> {
  const { server_url, api_key, ...fields } = payload
  const response = await http.post<APIResponse<unknown>>(
    `${SERVER_URL}/setting/${serverType}-config`,
    { ...fields, [`${serverType}_url`]: server_url, [`${serverType}_api_key`]: api_key },
    { headers: { 'Content-Type': 'application/json' } },
  )
  return getSettingsSaveResult(response)
}

export async function fetchMediaLibraryLibraries(
  http: AxiosInstance,
  serverType: MediaServerType,
): Promise<{ libraries: MediaLibraryLibraryOption[]; warnings: string[] }> {
  const result = unwrapResponse(
    await http.get<APIResponse<MediaLibraryLibraryOption[] | null>>(
      `${SERVER_URL}/${serverType}/libraries`,
    ),
  )
  return { libraries: result.data ?? [], warnings: result.warnings }
}

export async function extractMediaLibraryMediaInfo(
  http: AxiosInstance,
  serverType: MediaServerType,
): Promise<SettingsSaveResult> {
  return getSettingsSaveResult(
    await http.post<APIResponse<null>>(`${SERVER_URL}/setting/${serverType}/parse`),
  )
}

export async function startMediaLibrarySync(
  http: AxiosInstance,
  serverType: MediaServerType,
): Promise<SettingsSaveResult> {
  return getSettingsSaveResult(
    await http.post<APIResponse<unknown>>(`${SERVER_URL}/${serverType}/sync/start`),
  )
}

export async function fetchMediaLibrarySyncStatus(
  http: AxiosInstance,
  serverType: MediaServerType,
): Promise<MediaLibrarySyncInfo | null> {
  return unwrapResponse(
    await http.get<APIResponse<MediaLibrarySyncInfo | null>>(
      `${SERVER_URL}/${serverType}/sync/status`,
    ),
  ).data
}

export function mediaLibraryMessages(
  serverType: MediaServerType,
): Readonly<Record<string, string>> {
  if (serverType === 'emby') return mediaLibraryPublicMessages
  return Object.fromEntries(
    Object.entries(mediaLibraryPublicMessages).map(([key, value]) => [
      key.replaceAll('Emby', 'Jellyfin').replaceAll('emby_', 'jellyfin_'),
      value.replaceAll('Emby', 'Jellyfin'),
    ]),
  )
}
