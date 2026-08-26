interface Setting {
  username: string
  password: string
  telegram_bot_token?: string
  telegram_user_id?: string
}

interface DirInfo {
  id: string
  name: string
  path: string
}

// 账户信息接口
interface CloudAccount {
  id: number
  name: string
  source_type: string
  user_id: string
  username: string
  created_at: number
  authorized: boolean
}

// 备份相关类型定义
type BackupTaskType = 'backup' | 'restore' | null
type BackupStatus = 'pending' | 'running' | 'completed' | 'cancelled' | 'timeout' | 'failed'
type BackupType = 'manual' | 'auto'

// 备份配置接口
interface BackupConfig {
  id: number
  backup_enabled: 0 | 1
  backup_cron: string
  backup_path: string
  backup_retention: number
  backup_max_count: number
  backup_compress: 0 | 1
  created_at: number
  updated_at: number
}

// 备份进度接口
interface BackupProgress {
  running: boolean
  status?: BackupStatus
  progress?: number
  elapsed_seconds?: number
  estimated_seconds?: number
  current_step?: string
  processed_tables?: number
  total_tables?: number
}

// 备份记录列表项接口（列表用）
interface BackupRecordListItem {
  id: number
  created_at: number
  status: BackupStatus
  file_path: string
  file_size: number
  backup_type: BackupType
  backup_duration: number
  created_reason: string
}

// 备份记录分页响应接口
interface BackupRecordsResponse {
  list: BackupRecordListItem[]
  total: number
  page: number
  page_size: number
}

// 文件管理器相关类型定义
type FileType = 'directory' | 'video' | 'image' | 'nfo' | 'other'
type FileOperationType = 'STRM_GENERATE' | 'SCRAPE_ORGANIZE' | 'GENERATE_ED2K' | 'DELETE'

// 文件系统项目接口
interface FileSystemItem {
  id: string
  name: string
  path: string
  type: FileType
  size: number
  modified_time: number
  is_directory: boolean
}

type DirectoryUploadWatchMode = 'auto' | 'fsnotify' | 'polling'
type DirectoryUploadOverwriteMode = 'skip_same' | 'fail_conflict' | 'replace_conflict'

interface DirectoryUploadRule {
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

export type {
  Setting,
  DirInfo,
  CloudAccount,
  BackupTaskType,
  BackupStatus,
  BackupType,
  BackupConfig,
  BackupProgress,
  BackupRecordListItem,
  BackupRecordsResponse,
  FileType,
  FileOperationType,
  FileSystemItem,
  DirectoryUploadWatchMode,
  DirectoryUploadOverwriteMode,
  DirectoryUploadRule,
}
