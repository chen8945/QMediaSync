import { isSyncTaskTerminal } from '@/utils/syncTaskStatusUtils'
import type { SyncLedgerState, SyncScanResult, SyncTaskStatus } from '@/types/syncTaskStream'

export type SyncRecordEventType = 'sync_task_created' | 'sync_task_updated' | 'sync_task_deleted'

export interface SyncRecordRow extends SyncLedgerState {
  id: number
  start_time: number
  end_time: number | null
  status: SyncTaskStatus
  sub_status: 0 | 1 | 2 | 3 | 4
  processed_files: number
  created_strm: number
  downloaded_meta: number
  uploaded_meta: number
  local_path: string
  remote_path: string
  fail_reason: string
  scan_result?: SyncScanResult | null
}

export interface SyncTaskRecordEventPayload extends SyncLedgerState {
  sync_id: number
  sync_path_id: number
  status: number
  sub_status: number
  total: number
  new_strm: number
  new_meta: number
  new_upload: number
  finish_at: number
  sequence: number
  created_at?: number
  updated_at?: number
  local_path?: string
  remote_path?: string
  fail_reason?: string
  scan_result?: SyncScanResult | null
  deleted?: boolean
}

export interface ApplySyncRecordEventPatchOptions {
  records: SyncRecordRow[]
  total: number
  currentPage: number
  pageSize: number
  eventType: SyncRecordEventType
  payload: SyncTaskRecordEventPayload
  now?: number
}

export interface ApplySyncRecordEventPatchResult {
  records: SyncRecordRow[]
  total: number
  refreshNeeded: boolean
}

export const mapSyncTaskPayloadToRecord = (
  payload: SyncTaskRecordEventPayload,
  now = Math.floor(Date.now() / 1000),
): SyncRecordRow => ({
  id: payload.sync_id,
  start_time: payload.created_at || now,
  end_time: payload.finish_at || null,
  status: payload.status as SyncTaskStatus,
  sub_status: payload.sub_status as 0 | 1 | 2 | 3 | 4,
  processed_files: payload.total,
  created_strm: payload.new_strm,
  downloaded_meta: payload.new_meta || 0,
  uploaded_meta: payload.new_upload || 0,
  local_path: payload.local_path || '',
  remote_path: payload.remote_path || '',
  fail_reason: payload.fail_reason || '',
  scan_result: payload.scan_result,
  ledger_status: payload.ledger_status,
  ledger_finished_at: payload.ledger_finished_at,
  ledger_error: payload.ledger_error,
})

export function applySyncRecordEventPatch(
  options: ApplySyncRecordEventPatchOptions,
): ApplySyncRecordEventPatchResult {
  const { records, currentPage, eventType, pageSize, payload, now } = options
  const total = options.total
  const index = records.findIndex((record) => record.id === payload.sync_id)

  if (payload.deleted || eventType === 'sync_task_deleted') {
    const nextRecords =
      index >= 0 ? records.filter((record) => record.id !== payload.sync_id) : records
    return {
      records: nextRecords,
      total: Math.max(0, total - 1),
      refreshNeeded: false,
    }
  }

  if (index >= 0) {
    const nextRecords = [...records]
    const current = records[index]
    nextRecords[index] = isSyncTaskTerminal(current.status)
      ? {
          ...current,
          ...(payload.ledger_status !== undefined ? { ledger_status: payload.ledger_status } : {}),
          ...(payload.ledger_finished_at !== undefined
            ? { ledger_finished_at: payload.ledger_finished_at }
            : {}),
          ...(payload.ledger_error !== undefined ? { ledger_error: payload.ledger_error } : {}),
        }
      : mapSyncTaskPayloadToRecord(payload, now)
    return { records: nextRecords, total, refreshNeeded: false }
  }

  if (eventType === 'sync_task_created') {
    if (currentPage === 1) {
      return {
        records: [mapSyncTaskPayloadToRecord(payload, now), ...records].slice(0, pageSize),
        total: total + 1,
        refreshNeeded: false,
      }
    }
    return { records, total, refreshNeeded: true }
  }

  return { records, total, refreshNeeded: false }
}
