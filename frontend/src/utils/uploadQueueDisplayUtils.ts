import { formatFileSize } from '@/utils/fileSizeUtils'

export interface UploadQueueDisplayTask {
  id: string | number
  status?: number
  source?: string
  file_size?: number
  uploaded_bytes?: number
  progress_percent?: number
  upload_speed_bytes?: number
  upload_phase?: string
  upload_result?: string
  resume_state?: string
  rapid_wait_until?: number
  total_parts?: number
  uploaded_parts?: number
  source_cleanup_status?: string
  source_cleanup_error?: string
  source_deleted_at?: number
}

export interface UploadQueuePatch {
  task_id?: string | number
  id?: string | number
  status?: number
  uploaded_bytes?: number
  file_size?: number
  progress_percent?: number
  upload_speed_bytes?: number
  upload_phase?: string
  upload_result?: string
  resume_state?: string
  rapid_wait_until?: number
  total_parts?: number
  uploaded_parts?: number
  source_cleanup_status?: string
  source_cleanup_error?: string
  source_deleted_at?: number
}

export interface UploadTaskDetailRow {
  label: string
  value: string
}

const phaseLabelMap: Record<string, string> = {
  pending: '等待上传',
  checking_remote: '检查远端文件',
  rapid_waiting: '等待秒传',
  rapid_init: '等待秒传',
  resume_uploading: '恢复上传',
  resumed_uploading: '恢复上传',
  multipart_uploading: '正在上传',
  uploading: '正在上传',
  completing: '正在完成处理',
  remote_completed_pending_finalize: '等待完成处理',
  remote_completed_finalizing: '正在完成处理',
  complete_callback: '正在完成处理',
  completed: '上传完成',
  rapid_uploaded: '秒传成功',
  remote_exists: '远端已存在',
  skipped: '已跳过上传',
  failed: '上传失败',
  cancelled: '已取消',
}

const resultLabelMap: Record<string, string> = {
  rapid_upload: '秒传成功',
  multipart_uploaded: '上传完成',
  remote_exists: '远端已存在',
  skipped_after_rapid_wait: '秒传等待超时，已跳过上传',
  unknown: '',
}

const resumeStateLabelMap: Record<string, string> = {
  none: '',
  new_session: '新上传',
  resumed_session: '已恢复上传',
  session_expired_restarted: '续传会话已失效，已重新上传',
  resumed: '已恢复上传',
  resume_available: '可断点续传',
  no_session: '未发现可续传会话',
  new_upload: '新上传',
  stale_session: '续传会话已失效',
}

const cleanupStatusLabelMap: Record<string, string> = {
  none: '未清理',
  pending: '等待清理',
  completed: '清理成功',
  skipped: '未清理',
  success: '清理成功',
  deleted: '清理成功',
  failed: '清理失败',
}

const roundPercent = (value: number): number => Math.round(value * 10) / 10

const isSkippedAfterRapidWait = (task: Pick<UploadQueueDisplayTask, 'upload_result'>): boolean =>
  task.upload_result === 'skipped_after_rapid_wait'

const isCompletedUpload = (task: Pick<UploadQueueDisplayTask, 'status'>): boolean =>
  task.status === 2 || task.status === 5 || task.status === 6

const getEffectiveUploadedBytes = (task: UploadQueueDisplayTask, fileSize: number): number => {
  if (isSkippedAfterRapidWait(task)) {
    return 0
  }
  if (task.uploaded_bytes && Number.isFinite(task.uploaded_bytes)) {
    return Math.max(0, task.uploaded_bytes)
  }
  if (isCompletedUpload(task) && fileSize > 0) {
    return fileSize
  }
  return 0
}

export const getUploadProgressPercent = (task: UploadQueueDisplayTask): number => {
  if (isSkippedAfterRapidWait(task)) {
    return 0
  }
  const fileSize = Math.max(0, task.file_size || 0)
  if (
    task.progress_percent === 0 &&
    fileSize > 0 &&
    isCompletedUpload(task) &&
    getEffectiveUploadedBytes(task, fileSize) === fileSize
  ) {
    return 100
  }
  if (typeof task.progress_percent === 'number' && Number.isFinite(task.progress_percent)) {
    return Math.min(100, Math.max(0, roundPercent(task.progress_percent)))
  }
  if (fileSize > 0) {
    return Math.min(
      100,
      Math.max(0, roundPercent((getEffectiveUploadedBytes(task, fileSize) / fileSize) * 100)),
    )
  }
  if (isCompletedUpload(task)) {
    return 100
  }
  return 0
}

export const formatByteRate = (bytesPerSecond?: number): string => {
  if (!bytesPerSecond || bytesPerSecond <= 0) {
    return '-'
  }
  return `${formatFileSize(bytesPerSecond)}/s`
}

export const getUploadPhaseLabel = (task: Pick<UploadQueueDisplayTask, 'upload_phase'>): string => {
  if (!task.upload_phase) {
    return '-'
  }
  return phaseLabelMap[task.upload_phase] ?? task.upload_phase
}

export const getUploadResultLabel = (
  task: Pick<UploadQueueDisplayTask, 'upload_result' | 'status'>,
): string => {
  if (!task.upload_result || task.upload_result === 'unknown') {
    if (task.status === 2) {
      return '上传完成'
    }
    if (task.status === 5) {
      return '等待完成处理'
    }
    if (task.status === 6) {
      return '正在完成处理'
    }
    return '-'
  }
  return resultLabelMap[task.upload_result] ?? task.upload_result
}

export const getUploadStageOrResultLabel = (task: UploadQueueDisplayTask): string => {
  if (task.status === 5 || task.status === 6) {
    if (task.upload_phase) {
      return getUploadPhaseLabel(task)
    }
    return task.status === 6 ? '正在完成处理' : '等待完成处理'
  }
  if (task.status === 2 || (task.upload_result && task.upload_result !== 'unknown')) {
    return getUploadResultLabel(task)
  }
  return getUploadPhaseLabel(task)
}

export const getUploadedSizeLabel = (task: UploadQueueDisplayTask): string => {
  const fileSize = Math.max(0, task.file_size || 0)
  const uploadedBytes = getEffectiveUploadedBytes(task, fileSize)
  if (fileSize > 0) {
    return `${formatFileSize(uploadedBytes)} / ${formatFileSize(fileSize)}`
  }
  if (uploadedBytes > 0) {
    return formatFileSize(uploadedBytes)
  }
  return '-'
}

export const getResumeStateLabel = (resumeState?: string): string => {
  if (!resumeState) {
    return ''
  }
  return resumeStateLabelMap[resumeState] ?? resumeState
}

export const getSourceCleanupStatusLabel = (cleanupStatus?: string): string => {
  if (!cleanupStatus) {
    return ''
  }
  return cleanupStatusLabelMap[cleanupStatus] ?? cleanupStatus
}

export const getUploadTaskDetailRows = (task: UploadQueueDisplayTask): UploadTaskDetailRow[] => {
  const rows: UploadTaskDetailRow[] = []
  const resumeState = getResumeStateLabel(task.resume_state)
  if (resumeState) {
    rows.push({ label: '断点续传', value: resumeState })
  }
  if (task.total_parts && task.total_parts > 0) {
    rows.push({ label: '分片进度', value: `${task.uploaded_parts || 0}/${task.total_parts}` })
  }
  if (task.source === 'directory_monitor') {
    const cleanupStatus = getSourceCleanupStatusLabel(task.source_cleanup_status)
    if (cleanupStatus) {
      rows.push({ label: '源文件清理', value: cleanupStatus })
    }
    if (task.source_cleanup_error) {
      rows.push({ label: '清理失败原因', value: String(task.source_cleanup_error) })
    }
  }
  return rows
}

export const applyUploadQueuePatch = <T extends UploadQueueDisplayTask>(
  rows: T[],
  patch: UploadQueuePatch,
): boolean => {
  const taskId = patch.task_id ?? patch.id
  if (taskId === undefined || taskId === null) {
    return false
  }
  const row = rows.find((item) => String(item.id) === String(taskId))
  if (!row) {
    return false
  }
  if (patch.status !== undefined) {
    row.status = patch.status
  }
  if (patch.uploaded_bytes !== undefined) {
    row.uploaded_bytes = patch.uploaded_bytes
  }
  if (patch.file_size !== undefined) {
    row.file_size = patch.file_size
  }
  if (patch.progress_percent !== undefined) {
    row.progress_percent = patch.progress_percent
  }
  if (patch.upload_speed_bytes !== undefined) {
    row.upload_speed_bytes = patch.upload_speed_bytes
  }
  if (patch.upload_phase !== undefined) {
    row.upload_phase = patch.upload_phase
  }
  if (patch.upload_result !== undefined) {
    row.upload_result = patch.upload_result
  }
  if (patch.resume_state !== undefined) {
    row.resume_state = patch.resume_state
  }
  if (patch.rapid_wait_until !== undefined) {
    row.rapid_wait_until = patch.rapid_wait_until
  }
  if (patch.total_parts !== undefined) {
    row.total_parts = patch.total_parts
  }
  if (patch.uploaded_parts !== undefined) {
    row.uploaded_parts = patch.uploaded_parts
  }
  if (patch.source_cleanup_status !== undefined) {
    row.source_cleanup_status = patch.source_cleanup_status
  }
  if (patch.source_cleanup_error !== undefined) {
    row.source_cleanup_error = patch.source_cleanup_error
  }
  if (patch.source_deleted_at !== undefined) {
    row.source_deleted_at = patch.source_deleted_at
  }
  return true
}
