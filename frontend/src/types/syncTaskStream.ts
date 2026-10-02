export type SyncTaskStatus = 0 | 1 | 2 | 3 | 4 | 5 | 6

export interface SyncLedgerState {
  ledger_status?:
    'not_required' | 'pending' | 'running' | 'completed' | 'failed' | 'interrupted' | null
  ledger_finished_at?: number | null
  ledger_error?: string
}

export interface SyncScanResult {
  succeeded_files: number
  failed_files: number
  skipped_files: number
  failures: { kind: 'file' | 'directory'; path: string; file_id?: string; reason: string }[]
  cleanup_status: 'completed' | 'partial' | 'skipped'
  cleanup_reason: string
}

export interface SyncTask extends SyncLedgerState {
  id: number
  sync_path_id: number
  created_at: number
  updated_at: number
  finish_at: number
  status: SyncTaskStatus
  sub_status: 0 | 1 | 2
  total: number
  new_strm: number
  new_meta: number
  new_upload: number
  net_file_start_at: number
  net_file_finish_at: number
  local_file_start_at: number
  local_file_finish_at: number
  local_path: string
  remote_path: string
  fail_reason: string
  scan_result?: SyncScanResult | null
}

export interface SyncTaskEventPayload extends SyncLedgerState {
  sync_id: number
  sync_path_id: number
  status: number
  sub_status: number
  total: number
  new_strm: number
  new_meta: number
  new_upload: number
  finish_at: number
  net_file_start_at: number
  net_file_finish_at: number
  local_file_start_at: number
  local_file_finish_at: number
  log_path: string
  sequence: number
  event_time: number
  created_at?: number
  updated_at?: number
  local_path?: string
  remote_path?: string
  fail_reason?: string
  scan_result?: SyncScanResult | null
  deleted?: boolean
  resync_reason?: string
}

export interface SyncTaskLogEntry {
  level: 'info' | 'warn' | 'error' | 'debug'
  message: string
  timestamp: string
  cursor?: number
  id?: string
}

export interface SyncTaskStreamMessage<T = unknown> {
  type: 'snapshot' | 'task_patch' | 'log_append' | 'complete' | 'error' | 'resync_required'
  version: number
  sync_id: number
  sequence?: number
  server_time: number
  data?: T
}

export interface SyncTaskSnapshot {
  task: SyncTask
  logs: SyncTaskLogEntry[]
  log_cursor: number
  log_path: string
}
