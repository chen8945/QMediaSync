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
type BackupType = 'manual' | 'auto' | 'legacy' | 'imported' | 'temporary_upload'
type BackupFormat = 'v1' | 'legacy'
type BackupVerificationState = 'verified' | 'pending_password' | 'invalid' | ''

// 备份与恢复操作的状态机取值，与后端协调器保持一致
type BackupOperationKind = 'backup' | 'restore'
type BackupOperationState =
  | 'queued'
  | 'waiting_for_tasks'
  | 'validating'
  | 'running'
  | 'rolling_back'
  | 'completed'
  | 'failed'
  | 'cancelled'
type BackupRollbackState = 'not_started' | 'succeeded' | 'failed'

interface BackupOperationProgress {
  message?: string
  completed: number
  total: number
}

// 状态查询返回的操作视图，只能通过 operation ID 与一次性令牌读取
interface BackupOperationView {
  operation_id: string
  kind: BackupOperationKind
  state: BackupOperationState
  progress: BackupOperationProgress
  error_code?: string
  rollback_state?: BackupRollbackState
  started_at: number
  updated_at: number
  completed_at?: number
}

// 受理响应；token 只出现一次，仅可保存在操作页内存中
interface BackupOperationAccepted {
  operation_id: string
  token: string
}

// 运行中的任务摘要，冲突响应用它说明为什么不能立即备份或恢复
interface BackupRunningTask {
  kind: string
  name: string
  running: number
}

// 备份配置接口
interface BackupConfig {
  id: number
  backup_enabled: 0 | 1
  backup_cron: string
  backup_retention: number
  backup_max_count: number
  backup_encryption_enabled: boolean
  created_at: number
  updated_at: number
}

// 备份记录列表项接口（列表用）
interface BackupRecordListItem {
  id: number
  created_at: number
  status: BackupStatus
  file_path: string
  file_size: number
  backup_type: BackupType
  format: BackupFormat
  verification_state: BackupVerificationState
  verification_error_code: string
  backup_duration: number
  created_reason: string
}

// 备份记录分页响应接口
interface BackupRecordsResponse {
  list: BackupRecordListItem[]
  total: number
  page: number
  page_size: number
  inventory_status: 'ready' | 'scanning'
  latest_operation?: BackupTerminalOperation
}

interface BackupTerminalOperation {
  kind: BackupOperationKind
  state: Extract<BackupOperationState, 'completed' | 'failed' | 'cancelled'>
  completed_at: number
  error_code?: string
  rollback_state?: BackupRollbackState
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
  BackupOperationKind,
  BackupOperationState,
  BackupRollbackState,
  BackupOperationProgress,
  BackupOperationView,
  BackupOperationAccepted,
  BackupRunningTask,
  BackupRecordListItem,
  BackupRecordsResponse,
  BackupTerminalOperation,
  FileType,
  FileOperationType,
  FileSystemItem,
  FileListResponse,
  DirectoryUploadWatchMode,
  DirectoryUploadOverwriteMode,
  DirectoryUploadRule,
}
