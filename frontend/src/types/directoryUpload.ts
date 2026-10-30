export type DirectoryUploadWatchMode = 'auto' | 'fsnotify' | 'polling'
export type DirectoryUploadOverwriteMode = 'skip_same' | 'fail_conflict' | 'replace_conflict'

export interface DirectoryUploadRule {
  id: number
  sync_path_id: number
  account_id: number
  enabled: boolean
  monitor_path: string
  remote_root_path: string
  remote_root_id: string
  recursive: boolean
  watch_mode: DirectoryUploadWatchMode
  upload_metadata: boolean
  startup_scan_enabled: boolean
  processed_cache_ttl_seconds: number
  delete_source_after_success: boolean
  ignore_patterns?: string[]
  overwrite_mode: DirectoryUploadOverwriteMode
}
