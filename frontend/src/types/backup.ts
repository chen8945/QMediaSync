// 备份相关类型定义
export type BackupTaskType = 'backup' | 'restore' | null
export type BackupStatus =
  'pending' | 'running' | 'completed' | 'cancelled' | 'timeout' | 'failed' | 'unconfirmed'
export type BackupType = 'manual' | 'auto'

// 备份配置接口
export interface BackupConfig {
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
export interface BackupProgress {
  running: boolean
  status?: BackupStatus | 'unknown'
  progress?: number
  elapsed_seconds?: number
  estimated_seconds?: number
  current_step?: string
  processed_tables?: number
  total_tables?: number
}

// 备份记录列表项接口（列表用）
export interface BackupRecordListItem {
  id: number
  created_at: number
  status: BackupStatus
  file_path: string
  file_size: number
  file_status: 'available' | 'missing' | 'unavailable'
  backup_type: BackupType
  backup_duration: number
  created_reason: string
}

// 备份记录分页响应接口
export interface BackupRecordsResponse {
  list: BackupRecordListItem[]
  total: number
  page: number
  page_size: number
}
