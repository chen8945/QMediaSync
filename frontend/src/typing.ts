type defType = '本地路径' | 'WebDAV' | 'alist302'
type CopyMetaFileType = '关闭' | '复制' | '软链接'
type SyncType = '手动' | '定时' | '监控变更'
type CloudType = '115' | 'other'
type DeleteType = '是' | '否'

interface oo5Account {
  key: string
  name: string
  cookie: string
  created_at: string
  updated_at: string
  status: 0 | 1
}

interface LibForm {
  id_of_115: string
  key: string
  cloud_type: CloudType
  name: string
  path: string
  type: defType
  strm_root_path: string
  mount_path: string
  alist_server: string
  alist_115_path: string
  path_of_115: string
  copy_meta_file: CopyMetaFileType
  copy_delay: number
  webdav_url: string
  webdav_username: string
  webdav_password: string
  sync_type: SyncType
  cron_str: string
  meta_ext: Array<string>
  strm_ext: Array<string>
  delete: DeleteType
}
interface Lib extends LibForm {
  extra: {
    status: 1 | 2 | 3
    pid: string
    last_sync_at: string
    last_sync_result: {
      strm: Array<number>
      meta: Array<number>
      delete: Array<number>
    }
  }
}

interface Setting {
  username: string
  password: string
  telegram_bot_token?: string
  telegram_user_id?: string
}

interface BasicSetting {
  username: string
  password: string
}

interface TelegramSetting {
  enabled: boolean
  telegram_bot_token: string
  telegram_user_id: string
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
  token: string
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

// 备份状态接口
interface BackupStatusInfo {
  is_running: boolean
  backup_dir: string
  config: BackupConfig
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

// 备份记录接口
interface BackupRecord {
  id: number
  created_at: number
  updated_at: number
  task_id: number
  status: BackupStatus
  file_path: string
  file_size: number
  database_size: number
  table_count: number
  backup_duration: number
  backup_type: BackupType
  created_reason: string
  failure_reason: string
  compression_ratio: number
  is_compressed: 0 | 1
  completed_at: number
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

// 文件列表响应接口
interface FileListResponse {
  total: number
  items: FileSystemItem[]
  current_path: string
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
  oo5Account,
  LibForm,
  Lib,
  defType,
  CopyMetaFileType,
  SyncType,
  CloudType,
  Setting,
  BasicSetting,
  TelegramSetting,
  DirInfo,
  CloudAccount,
  BackupTaskType,
  BackupStatus,
  BackupType,
  BackupConfig,
  BackupStatusInfo,
  BackupProgress,
  BackupRecord,
  BackupRecordListItem,
  BackupRecordsResponse,
  FileType,
  FileOperationType,
  FileSystemItem,
  FileListResponse,
  DirectoryUploadWatchMode,
  DirectoryUploadOverwriteMode,
  DirectoryUploadRule,
}
