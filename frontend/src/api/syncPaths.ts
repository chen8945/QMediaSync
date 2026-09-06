import { SERVER_URL } from '@/const'
import type { AxiosInstance } from 'axios'

export interface SyncPathSettingPayload {
  local_proxy: number
  strm_base_url: string
  cron: string
  min_video_size: number
  video_ext_arr: string[]
  meta_ext_arr: string[]
  exclude_name_arr: string[]
  exclude_name_regex_arr: string[]
  upload_meta: number
  download_meta: number
  delete_dir: number
  add_path: number
  check_meta_mtime: number
}

export interface SyncPathPayload {
  source_type: string
  account_id: number
  base_cid: string
  local_path: string
  remote_path: string
  enable_cron: boolean
  custom_config: boolean
  setting: SyncPathSettingPayload
}

export interface DirectoryUploadRulePayload {
  client_id: string
  id: number
  enabled: boolean
  monitor_path: string
  remote_root_path: string
  remote_root_id: string
  recursive: boolean
  watch_mode: string
  upload_metadata: boolean
  startup_scan_enabled: boolean
  processed_cache_ttl_seconds: number
  delete_source_after_success: boolean
  ignore_patterns: string[]
  overwrite_mode: string
}

export interface SaveSyncPathPayload {
  sync_path: SyncPathPayload
  directory_upload: null | {
    enabled: boolean
    rules: DirectoryUploadRulePayload[]
  }
}

export interface SyncPathFieldError {
  client_id?: string
  field: string
  message: string
}

export interface SaveSyncPathResponseData {
  sync_path: { id: number }
  directory_upload: { enabled: boolean; rules: unknown[] }
  warnings: string[]
}

export async function saveSyncPathAggregate(
  http: AxiosInstance,
  id: number,
  payload: SaveSyncPathPayload,
  idempotencyKey: string,
) {
  const options = id === 0 ? { headers: { 'Idempotency-Key': idempotencyKey } } : undefined
  if (id > 0) {
    return http.put(`${SERVER_URL}/sync/paths/${id}`, payload)
  }
  return http.post(`${SERVER_URL}/sync/paths`, payload, options)
}
