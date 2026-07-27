import { formatFileSize } from '@/utils/fileSizeUtils'
import {
  getDownloadSourceName,
  getTaskSourceTypeName,
  getUploadSourceName,
} from '@/utils/taskSourceUtils'
import { formatDateTime, formatDuration } from '@/utils/timeUtils'
import {
  formatByteRate,
  getUploadedSizeLabel,
  getUploadPhaseLabel,
  getUploadProgressPercent,
  getUploadResultLabel,
  getUploadStageOrResultLabel,
  getUploadTaskDetailRows,
  type UploadQueueDisplayTask,
} from '@/utils/uploadQueueDisplayUtils'

export type QueueTagType = 'primary' | 'success' | 'warning' | 'danger' | 'info'

export interface QueueTaskDetailField {
  key: string
  label: string
  value: string
  tagType?: QueueTagType
  fullWidth?: boolean
}

export type QueueTaskDetailColumns = 1 | 2 | 3 | 4 | 5

export interface QueueTaskDetailGroup {
  key: string
  label: string
  columns: QueueTaskDetailColumns
  fields: QueueTaskDetailField[]
}

interface QueueTaskDetailBaseInput {
  id: string | number
  source: string
  source_type: string
  status: number
  start_time?: number
  end_time?: number
  remote_file_id?: string
  remote_path?: string
  remote_full_path?: string
  remote_pick_code?: string
  remote_sha1?: string
  remote_md5?: string
  local_full_path?: string
  error?: string
  retry_count?: number
  last_retry_time?: number
}

export interface DownloadQueueTaskDetailInput extends QueueTaskDetailBaseInput {
  size?: number
}

export type UploadQueueTaskDetailInput = QueueTaskDetailBaseInput &
  UploadQueueDisplayTask & {
    file_size?: number
    rapid_wait_attempts?: number
    relative_path?: string
    source_deleted_at?: number
    remote_path_id?: string
    replaced_remote_file_id?: string
  }

export interface UploadTransportDetailRow {
  label: string
  value: string
}

const hasText = (value: unknown): value is string =>
  typeof value === 'string' && value.trim().length > 0

const hasNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value)

const hasPositiveNumber = (value: unknown): value is number => hasNumber(value) && value > 0

// getQueueRemotePathSummary 只以持久化的远端完整路径生成队列摘要；历史任务不把 ID 回退为路径。
export const getQueueRemotePathSummary = (remoteFullPath: string | undefined): string =>
  hasText(remoteFullPath) ? remoteFullPath : '远端路径未知（历史记录未保存）'

export interface DownloadQueueLocationSummaryInput {
  source_type?: string
  remote_full_path?: string
  local_full_path?: string
}

export const getDownloadQueueLocationSummary = (
  task: DownloadQueueLocationSummaryInput,
): string => {
  const targetPath = hasText(task.local_full_path) ? task.local_full_path : ''
  if (task.source_type === 'local') {
    return targetPath ? `下载至 ${targetPath}` : ''
  }
  if (task.source_type === 'emby_media') {
    return ''
  }

  const sourcePath = getQueueRemotePathSummary(task.remote_full_path)
  return targetPath ? `${sourcePath}\n下载至 ${targetPath}` : sourcePath
}

export interface UploadQueueLocationSummaryInput {
  source_type?: string
  remote_full_path?: string
  local_full_path?: string
}

export const getUploadQueueLocationSummary = (task: UploadQueueLocationSummaryInput): string => {
  const sourcePath = hasText(task.local_full_path) ? task.local_full_path : ''
  if (task.source_type === 'local') {
    const targetPath = hasText(task.remote_full_path) ? task.remote_full_path : ''
    if (sourcePath && targetPath) {
      return `${sourcePath}\n复制到 ${targetPath}`
    }
    return sourcePath || targetPath
  }
  if (task.source_type === 'emby_media') {
    return ''
  }

  const targetPath = getQueueRemotePathSummary(task.remote_full_path)
  return sourcePath ? `${sourcePath}\n上传至 ${targetPath}` : targetPath
}

const supportsRemoteSHA1 = (sourceType: string): boolean =>
  sourceType === '115' || sourceType === 'openlist'

const supportsRemoteMD5 = (sourceType: string): boolean =>
  sourceType === 'baidupan' || sourceType === 'openlist'

const createGroup = (
  key: string,
  label: string,
  preferredColumns: QueueTaskDetailColumns,
  fields: QueueTaskDetailField[],
): QueueTaskDetailGroup | null => {
  if (fields.length === 0) {
    return null
  }

  const compactFieldCount = fields.filter((field) => !field.fullWidth).length
  const columns = Math.max(1, Math.min(preferredColumns, compactFieldCount || 1))

  return { key, label, columns: columns as QueueTaskDetailColumns, fields }
}

const compactGroups = (groups: Array<QueueTaskDetailGroup | null>): QueueTaskDetailGroup[] =>
  groups.filter((group): group is QueueTaskDetailGroup => group !== null)

const buildRetryFields = (task: QueueTaskDetailBaseInput): QueueTaskDetailField[] => {
  if (!hasPositiveNumber(task.retry_count)) {
    return []
  }

  const fields: QueueTaskDetailField[] = [
    { key: 'retry-count', label: '重试次数', value: `${task.retry_count} 次` },
  ]
  if (hasPositiveNumber(task.last_retry_time)) {
    fields.push({
      key: 'last-retry-time',
      label: '最近重试时间',
      value: formatDateTime(task.last_retry_time),
    })
  }
  return fields
}

const buildTimeFields = (
  task: QueueTaskDetailBaseInput,
  endTimeLabel: string,
): QueueTaskDetailField[] => {
  const fields: QueueTaskDetailField[] = []
  if (hasPositiveNumber(task.start_time)) {
    fields.push({ key: 'start-time', label: '开始时间', value: formatDateTime(task.start_time) })
  }
  if (hasPositiveNumber(task.end_time)) {
    fields.push({ key: 'end-time', label: endTimeLabel, value: formatDateTime(task.end_time) })
  }
  if (
    hasPositiveNumber(task.start_time) &&
    hasPositiveNumber(task.end_time) &&
    task.end_time >= task.start_time
  ) {
    fields.push({
      key: 'duration',
      label: '耗时',
      value: formatDuration(task.end_time - task.start_time),
    })
  }
  return fields
}

export const getDownloadStatusText = (status: number): string => {
  switch (status) {
    case 0:
      return '等待下载'
    case 1:
      return '正在下载'
    case 2:
      return '下载完成'
    case 3:
      return '下载失败'
    case 4:
      return '已取消'
    default:
      return '未知'
  }
}

export const getDownloadStatusTagType = (status: number): QueueTagType => {
  switch (status) {
    case 1:
      return 'primary'
    case 2:
      return 'success'
    case 3:
      return 'danger'
    case 4:
      return 'warning'
    default:
      return 'info'
  }
}

export const getUploadStatusText = (status: number): string => {
  switch (status) {
    case 0:
      return '等待上传'
    case 1:
      return '正在上传'
    case 2:
      return '上传完成'
    case 3:
      return '上传失败'
    case 4:
      return '已取消'
    case 5:
      return '等待完成处理'
    case 6:
      return '正在完成处理'
    default:
      return '未知'
  }
}

export const getUploadStatusTagType = (status: number): QueueTagType => {
  switch (status) {
    case 1:
    case 5:
    case 6:
      return 'primary'
    case 2:
      return 'success'
    case 3:
      return 'danger'
    case 4:
      return 'warning'
    default:
      return 'info'
  }
}

export const getUploadStageResultTagType = (
  task: Pick<UploadQueueDisplayTask, 'status' | 'upload_phase' | 'upload_result'>,
): QueueTagType => {
  if (task.upload_result === 'rapid_upload' || task.upload_result === 'multipart_uploaded') {
    return 'success'
  }
  if (task.upload_result === 'remote_exists') {
    return 'info'
  }
  if (task.upload_result === 'skipped_after_rapid_wait' || task.upload_phase === 'rapid_waiting') {
    return 'warning'
  }
  if (
    task.status === 5 ||
    task.status === 6 ||
    task.upload_phase === 'remote_completed_pending_finalize' ||
    task.upload_phase === 'remote_completed_finalizing'
  ) {
    return 'primary'
  }
  if (task.status === 2) {
    return 'success'
  }
  if (task.status === 3) {
    return 'danger'
  }
  if (task.status === 1) {
    return 'primary'
  }
  return 'info'
}

export const getUploadStageSummaryLabel = (task: UploadQueueDisplayTask): string => {
  const label = getUploadStageOrResultLabel(task)
  return label === '-' ? '' : label
}

export const getUploadTransportDetailRows = (
  task: UploadQueueTaskDetailInput,
): UploadTransportDetailRow[] => {
  const rows = getUploadTaskDetailRows(task)
  if (hasText(task.error)) {
    rows.push({ label: '失败原因', value: task.error })
  }
  return rows
}

export const getUploadTransportDetailSummary = (task: UploadQueueTaskDetailInput): string =>
  getUploadTransportDetailRows(task)
    .map((item) => `${item.label}：${item.value}`)
    .join('\n')

export const buildDownloadTaskDetailGroups = (
  task: DownloadQueueTaskDetailInput,
): QueueTaskDetailGroup[] => {
  const basicFields: QueueTaskDetailField[] = [
    { key: 'task-id', label: '任务 ID', value: String(task.id) },
    ...(hasText(task.source)
      ? [{ key: 'source', label: '来源', value: getDownloadSourceName(task.source) }]
      : []),
    ...(hasText(task.source_type)
      ? [{ key: 'source-type', label: '来源类型', value: getTaskSourceTypeName(task.source_type) }]
      : []),
    {
      key: 'status',
      label: '状态',
      value: getDownloadStatusText(task.status),
      tagType: getDownloadStatusTagType(task.status),
    },
  ]

  if (hasNumber(task.size)) {
    basicFields.push({ key: 'size', label: '文件大小', value: formatFileSize(task.size) })
  }

  const transmissionFields: QueueTaskDetailField[] = [...buildRetryFields(task)]
  if (hasText(task.error)) {
    transmissionFields.push({
      key: 'error',
      label: '失败原因',
      value: task.error,
      fullWidth: true,
    })
  }

  const locationFields: QueueTaskDetailField[] = []
  const hasRemoteIdentity = task.source_type !== 'local' && task.source_type !== 'emby_media'
  if (hasRemoteIdentity) {
    if (hasText(task.remote_file_id)) {
      locationFields.push({
        key: 'remote-file-id',
        label: '远端文件 ID',
        value: task.remote_file_id,
      })
    }
    if (hasText(task.remote_path)) {
      locationFields.push({
        key: 'remote-path',
        label: '远端目录路径',
        value: task.remote_path,
        fullWidth: true,
      })
    }
    if (hasText(task.remote_full_path)) {
      locationFields.push({
        key: 'remote-full-path',
        label: '远端完整路径',
        value: task.remote_full_path,
        fullWidth: true,
      })
    }
    if (task.source_type === '115' && hasText(task.remote_pick_code)) {
      locationFields.push({
        key: 'remote-pick-code',
        label: 'PickCode',
        value: task.remote_pick_code,
      })
    }
    if (supportsRemoteSHA1(task.source_type) && hasText(task.remote_sha1)) {
      locationFields.push({
        key: 'remote-sha1',
        label: 'SHA1',
        value: task.remote_sha1,
      })
    }
    if (supportsRemoteMD5(task.source_type) && hasText(task.remote_md5)) {
      locationFields.push({
        key: 'remote-md5',
        label: 'MD5',
        value: task.remote_md5,
      })
    }
  }
  if (hasText(task.local_full_path)) {
    locationFields.push({
      key: 'local-path',
      label: '本地目标路径',
      value: task.local_full_path,
      fullWidth: true,
    })
  }

  return compactGroups([
    createGroup('basic', '基本信息', hasNumber(task.size) ? 5 : 4, basicFields),
    createGroup('transfer', '传输信息', 3, transmissionFields),
    createGroup('file', '文件信息', 3, locationFields),
    createGroup('time', '执行时间', 3, buildTimeFields(task, '结束时间')),
  ])
}

export const buildUploadTaskDetailGroups = (
  task: UploadQueueTaskDetailInput,
): QueueTaskDetailGroup[] => {
  const basicFields: QueueTaskDetailField[] = [
    { key: 'task-id', label: '任务 ID', value: String(task.id) },
    ...(hasText(task.source)
      ? [{ key: 'source', label: '来源', value: getUploadSourceName(task.source) }]
      : []),
    ...(hasText(task.source_type)
      ? [{ key: 'source-type', label: '来源类型', value: getTaskSourceTypeName(task.source_type) }]
      : []),
    {
      key: 'status',
      label: '状态',
      value: getUploadStatusText(task.status),
      tagType: getUploadStatusTagType(task.status),
    },
  ]

  const transmissionFields: QueueTaskDetailField[] = []
  if (hasNumber(task.file_size)) {
    transmissionFields.push({
      key: 'size',
      label: '文件大小',
      value: formatFileSize(task.file_size),
    })
  }
  if (task.status === 1) {
    if (hasNumber(task.uploaded_bytes) || hasPositiveNumber(task.file_size)) {
      transmissionFields.push({
        key: 'uploaded-size',
        label: '已上传',
        value: getUploadedSizeLabel(task),
      })
    }
    transmissionFields.push({
      key: 'progress',
      label: '上传进度',
      value: `${getUploadProgressPercent(task)}%`,
    })
    if (hasPositiveNumber(task.upload_speed_bytes)) {
      transmissionFields.push({
        key: 'speed',
        label: '上传速度',
        value: formatByteRate(task.upload_speed_bytes),
      })
    }
  }
  if (hasText(task.upload_phase)) {
    transmissionFields.push({
      key: 'phase',
      label: '上传阶段',
      value: getUploadPhaseLabel(task),
    })
  }
  if (
    task.status !== 0 &&
    task.status !== 1 &&
    hasText(task.upload_result) &&
    task.upload_result !== 'unknown'
  ) {
    transmissionFields.push({
      key: 'result',
      label: '上传结果',
      value: getUploadResultLabel(task),
    })
  }
  transmissionFields.push(...buildRetryFields(task))
  if (hasText(task.error)) {
    transmissionFields.push({
      key: 'error',
      label: '失败原因',
      value: task.error,
      fullWidth: true,
    })
  }

  const locationFields: QueueTaskDetailField[] = []
  if (hasText(task.local_full_path)) {
    locationFields.push({
      key: 'local-source-path',
      label: '本地源文件',
      value: task.local_full_path,
      fullWidth: true,
    })
  }
  const hasRemoteIdentity = task.source_type !== 'local' && task.source_type !== 'emby_media'
  if (hasRemoteIdentity && hasText(task.remote_full_path)) {
    locationFields.push({
      key: 'remote-full-path',
      label: '远端完整路径',
      value: task.remote_full_path,
      fullWidth: true,
    })
  }
  if (task.source_type === '115' && hasText(task.remote_path_id)) {
    locationFields.push({
      key: 'remote-path-id',
      label: '远端父目录 ID',
      value: task.remote_path_id,
    })
  }
  if (hasRemoteIdentity && hasText(task.remote_file_id)) {
    locationFields.push({
      key: 'remote-file-id',
      label: '远端文件 ID',
      value: task.remote_file_id,
    })
  }
  if (task.source_type === '115' && hasText(task.remote_pick_code)) {
    locationFields.push({
      key: 'remote-pick-code',
      label: 'PickCode',
      value: task.remote_pick_code,
    })
  }
  if (hasRemoteIdentity && supportsRemoteSHA1(task.source_type) && hasText(task.remote_sha1)) {
    locationFields.push({ key: 'remote-sha1', label: 'SHA1', value: task.remote_sha1 })
  }
  if (hasRemoteIdentity && supportsRemoteMD5(task.source_type) && hasText(task.remote_md5)) {
    locationFields.push({ key: 'remote-md5', label: 'MD5', value: task.remote_md5 })
  }
  if (hasRemoteIdentity && hasText(task.replaced_remote_file_id)) {
    locationFields.push({
      key: 'replaced-remote-file-id',
      label: '旧文件 ID',
      value: task.replaced_remote_file_id,
    })
  }

  const uploadRows = getUploadTaskDetailRows(task)
  const uploadFields: QueueTaskDetailField[] = []
  for (const label of ['断点续传', '源文件清理']) {
    const row = uploadRows.find((candidate) => candidate.label === label)
    if (row) {
      uploadFields.push({ key: `upload-${row.label}`, label: row.label, value: row.value })
    }
  }
  if (task.source === 'directory_monitor') {
    if (hasText(task.relative_path)) {
      uploadFields.push({
        key: 'relative-path',
        label: '相对监控根目录',
        value: task.relative_path,
      })
    }
  }
  for (const row of uploadRows) {
    if (row.label === '断点续传' || row.label === '源文件清理') {
      continue
    }
    uploadFields.push({
      key: `upload-${row.label}`,
      label: row.label,
      value: row.value,
      fullWidth: row.label === '清理失败原因',
    })
  }
  if (task.source === 'directory_monitor') {
    if (hasPositiveNumber(task.source_deleted_at)) {
      uploadFields.push({
        key: 'source-deleted-at',
        label: '源文件删除时间',
        value: formatDateTime(task.source_deleted_at),
      })
    }
  }
  if (task.upload_phase === 'rapid_waiting' && hasPositiveNumber(task.rapid_wait_attempts)) {
    uploadFields.push({
      key: 'rapid-wait-attempts',
      label: '秒传等待尝试次数',
      value: `${task.rapid_wait_attempts} 次`,
    })
  }
  if (task.upload_phase === 'rapid_waiting' && hasPositiveNumber(task.rapid_wait_until)) {
    uploadFields.push({
      key: 'rapid-wait-until',
      label: '秒传等待截止时间',
      value: formatDateTime(task.rapid_wait_until),
    })
  }

  return compactGroups([
    createGroup('basic', '基本信息', 4, basicFields),
    createGroup('transfer', '传输信息', 3, transmissionFields),
    createGroup('file', '文件信息', 3, locationFields),
    createGroup('upload', '上传信息', 3, uploadFields),
    createGroup('time', '执行时间', 3, buildTimeFields(task, '完成时间')),
  ])
}
