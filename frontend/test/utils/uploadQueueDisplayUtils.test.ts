import { describe, expect, it } from 'vitest'

import {
  applyUploadQueuePatch,
  formatByteRate,
  getResumeStateLabel,
  getSourceCleanupStatusLabel,
  getUploadedSizeLabel,
  getUploadPhaseLabel,
  getUploadProgressPercent,
  getUploadResultLabel,
  getUploadStageOrResultLabel,
  getUploadTaskDetailRows,
  type UploadQueueDisplayTask,
} from '@/utils/uploadQueueDisplayUtils'

const createTask = (task: Omit<UploadQueueDisplayTask, 'id'>): UploadQueueDisplayTask => ({
  id: 'test-task',
  ...task,
})

describe('uploadQueueDisplayUtils', () => {
  it('展示用户可理解的上传阶段和结果', () => {
    expect(getUploadPhaseLabel({ upload_phase: 'pending' })).toBe('等待上传')
    expect(getUploadPhaseLabel({ upload_phase: 'checking_remote' })).toBe('检查远端文件')
    expect(getUploadPhaseLabel({ upload_phase: 'rapid_waiting' })).toBe('等待秒传')
    expect(getUploadPhaseLabel({ upload_phase: 'resume_uploading' })).toBe('恢复上传')
    expect(getUploadPhaseLabel({ upload_phase: 'multipart_uploading' })).toBe('正在上传')
    expect(getUploadPhaseLabel({ upload_phase: 'uploading' })).toBe('正在上传')
    expect(getUploadPhaseLabel({ upload_phase: 'completing' })).toBe('正在完成处理')
    expect(getUploadPhaseLabel({ upload_phase: 'remote_completed_pending_finalize' })).toBe(
      '等待完成处理',
    )
    expect(getUploadPhaseLabel({ upload_phase: 'remote_completed_finalizing' })).toBe(
      '正在完成处理',
    )
    expect(getUploadPhaseLabel({ upload_phase: 'complete_callback' })).toBe('正在完成处理')
    expect(getUploadPhaseLabel({ upload_phase: 'completed' })).toBe('上传完成')
    expect(getUploadPhaseLabel({ upload_phase: 'remote_exists' })).toBe('远端已存在')
    expect(getUploadPhaseLabel({ upload_phase: 'skipped' })).toBe('已跳过上传')
    expect(getUploadPhaseLabel({ upload_phase: 'failed' })).toBe('上传失败')
    expect(getUploadPhaseLabel({ upload_phase: 'cancelled' })).toBe('已取消')

    expect(getUploadResultLabel({ upload_result: 'rapid_upload' })).toBe('秒传成功')
    expect(getUploadResultLabel({ upload_result: 'remote_exists' })).toBe('远端已存在')
    expect(getUploadResultLabel({ upload_result: 'multipart_uploaded' })).toBe('上传完成')
    expect(getUploadResultLabel({ upload_result: 'skipped_after_rapid_wait' })).toBe(
      '秒传等待超时，已跳过上传',
    )
  })

  it('上传结果未知时继续展示当前阶段', () => {
    expect(
      getUploadStageOrResultLabel(
        createTask({
          status: 1,
          upload_phase: 'rapid_waiting',
          upload_result: 'unknown',
        }),
      ),
    ).toBe('等待秒传')

    expect(
      getUploadStageOrResultLabel(
        createTask({
          status: 2,
          upload_phase: 'multipart_uploading',
          upload_result: 'unknown',
        }),
      ),
    ).toBe('上传完成')

    expect(getUploadResultLabel({ status: 5, upload_result: 'unknown' })).toBe('等待完成处理')
    expect(getUploadResultLabel({ status: 6, upload_result: 'unknown' })).toBe('正在完成处理')
    expect(getUploadStageOrResultLabel(createTask({ status: 5 }))).toBe('等待完成处理')
    expect(getUploadStageOrResultLabel(createTask({ status: 6 }))).toBe('正在完成处理')
  })

  it('按后端进度字段展示百分比和速度', () => {
    expect(
      getUploadProgressPercent(
        createTask({ progress_percent: 43.26, uploaded_bytes: 10, file_size: 100 }),
      ),
    ).toBe(43.3)
    expect(getUploadProgressPercent(createTask({ uploaded_bytes: 256, file_size: 1024 }))).toBe(25)
    expect(getUploadedSizeLabel(createTask({ status: 2, file_size: 1024 }))).toBe('1 KB / 1 KB')
    expect(formatByteRate(2 * 1024 * 1024)).toBe('2 MB/s')
  })

  it('秒传等待超时跳过时保留未传输的进度和大小', () => {
    const skippedTask = createTask({
      status: 2,
      file_size: 1024,
      uploaded_bytes: 0,
      progress_percent: 0,
      upload_result: 'skipped_after_rapid_wait',
    })

    expect(getUploadProgressPercent(skippedTask)).toBe(0)
    expect(getUploadedSizeLabel(skippedTask)).toBe('0 Bytes / 1 KB')
    expect(
      getUploadProgressPercent(
        createTask({
          status: 2,
          file_size: 1024,
          upload_result: 'skipped_after_rapid_wait',
        }),
      ),
    ).toBe(0)
    expect(
      getUploadedSizeLabel(
        createTask({
          status: 2,
          file_size: 1024,
          upload_result: 'skipped_after_rapid_wait',
        }),
      ),
    ).toBe('0 Bytes / 1 KB')
  })

  it('完成态快照的零进度和未跟踪上传字节回退为完整大小', () => {
    const zeroByteTask = createTask({
      status: 2,
      file_size: 1024,
      uploaded_bytes: 0,
      progress_percent: 0,
      upload_result: 'unknown',
    })

    expect(getUploadProgressPercent(zeroByteTask)).toBe(100)
    expect(getUploadedSizeLabel(zeroByteTask)).toBe('1 KB / 1 KB')
  })

  it('秒传成功时展示完整进度和大小', () => {
    const rapidUploadTask = createTask({
      status: 2,
      file_size: 1024,
      uploaded_bytes: 1024,
      upload_result: 'rapid_upload',
    })

    expect(getUploadProgressPercent(rapidUploadTask)).toBe(100)
    expect(getUploadedSizeLabel(rapidUploadTask)).toBe('1 KB / 1 KB')
  })

  it('展示后端真实续传和源文件清理枚举', () => {
    expect(getResumeStateLabel('new_session')).toBe('新上传')
    expect(getResumeStateLabel('resumed_session')).toBe('已恢复上传')
    expect(getResumeStateLabel('session_expired_restarted')).toBe('续传会话已失效，已重新上传')

    expect(getSourceCleanupStatusLabel('none')).toBe('未清理')
    expect(getSourceCleanupStatusLabel('pending')).toBe('等待清理')
    expect(getSourceCleanupStatusLabel('completed')).toBe('清理成功')
    expect(getSourceCleanupStatusLabel('failed')).toBe('清理失败')
  })

  it('详情中展示断点续传、分片和源文件清理状态', () => {
    const rows = getUploadTaskDetailRows(
      createTask({
        resume_state: 'resumed_session',
        uploaded_parts: 3,
        total_parts: 10,
        source: 'directory_monitor',
        source_cleanup_status: 'completed',
        source_cleanup_error: 'permission denied',
      }),
    )

    expect(rows).toContainEqual({ label: '断点续传', value: '已恢复上传' })
    expect(rows).toContainEqual({ label: '分片进度', value: '3/10' })
    expect(rows).toContainEqual({ label: '源文件清理', value: '清理成功' })
    expect(rows).toContainEqual({ label: '清理失败原因', value: 'permission denied' })
  })

  it('局部合并 upload_queue_changed 进度 patch', () => {
    const rows: UploadQueueDisplayTask[] = [
      { id: '7', status: 1, uploaded_bytes: 0, file_size: 100 },
      { id: '8', status: 0, uploaded_bytes: 0, file_size: 200 },
    ]

    const patched = applyUploadQueuePatch(rows, {
      task_id: 7,
      uploaded_bytes: 50,
      progress_percent: 50,
      upload_speed_bytes: 1024,
      upload_phase: 'multipart_uploading',
    })

    expect(patched).toBe(true)
    expect(rows[0]).toMatchObject({
      uploaded_bytes: 50,
      progress_percent: 50,
      upload_speed_bytes: 1024,
      upload_phase: 'multipart_uploading',
    })
    expect(rows[1].uploaded_bytes).toBe(0)
  })

  it('局部合并目录监控源文件清理状态 patch 并清空旧错误', () => {
    const rows: UploadQueueDisplayTask[] = [
      {
        id: '7',
        status: 2,
        source: 'directory_monitor',
        source_cleanup_status: 'failed',
        source_cleanup_error: 'permission denied',
      },
    ]

    const patched = applyUploadQueuePatch(rows, {
      task_id: 7,
      source_cleanup_status: 'completed',
      source_cleanup_error: '',
      source_deleted_at: 1_700_000_000,
    })

    expect(patched).toBe(true)
    expect(rows[0]).toMatchObject({
      source_cleanup_status: 'completed',
      source_cleanup_error: '',
      source_deleted_at: 1_700_000_000,
    })
  })
})
