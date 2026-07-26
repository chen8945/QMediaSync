import { describe, expect, it } from 'vitest'

import {
  buildDownloadTaskDetailGroups,
  buildUploadTaskDetailGroups,
  getUploadTransportDetailSummary,
  type DownloadQueueTaskDetailInput,
  type UploadQueueTaskDetailInput,
} from '@/utils/queueTaskDetailUtils'

const getFields = (groups: ReturnType<typeof buildDownloadTaskDetailGroups>) =>
  groups.flatMap((group) => group.fields)

const getGroup = (groups: ReturnType<typeof buildDownloadTaskDetailGroups>, key: string) => {
  const group = groups.find((candidate) => candidate.key === key)
  if (!group) {
    throw new Error(`未找到 ${key} 分组`)
  }
  return group
}

const createDownloadTask = (
  overrides: Partial<DownloadQueueTaskDetailInput> = {},
): DownloadQueueTaskDetailInput => ({
  id: 'download-task',
  source: 'strm_sync',
  source_type: '115',
  status: 2,
  ...overrides,
})

const createUploadTask = (
  overrides: Partial<UploadQueueTaskDetailInput> = {},
): UploadQueueTaskDetailInput => ({
  id: 'upload-task',
  source: 'strm_sync',
  source_type: '115',
  status: 2,
  ...overrides,
})

describe('queueTaskDetailUtils', () => {
  it('按来源类型标注下载标识符，并按 Emby 媒体提取语义组织位置', () => {
    const regularGroups = buildDownloadTaskDetailGroups(
      createDownloadTask({
        size: 1024,
        remote_file_id: 'pick-code',
        remote_path: '/remote/movie.mkv',
        local_full_path: '/library/movie.mkv',
        retry_count: 2,
        last_retry_time: 1_700_000_000,
        error: '下载失败',
        start_time: 1_700_000_001,
        end_time: 1_700_000_002,
      }),
    )
    const regularFields = getFields(regularGroups)

    expect(regularFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '任务 ID', value: 'download-task' }),
        expect.objectContaining({ label: '来源', value: 'STRM 同步' }),
        expect.objectContaining({ label: '来源类型', value: '115 网盘' }),
        expect.objectContaining({ label: '状态', value: '下载完成' }),
        expect.objectContaining({ label: '文件大小', value: '1 KB' }),
        expect.objectContaining({ label: 'PickCode', value: 'pick-code' }),
        expect.objectContaining({ label: '远端路径', value: '/remote/movie.mkv', fullWidth: true }),
        expect.objectContaining({
          label: '本地目标路径',
          value: '/library/movie.mkv',
          fullWidth: true,
        }),
        expect.objectContaining({ label: '失败原因', value: '下载失败', fullWidth: true }),
        expect.objectContaining({ label: '重试次数', value: '2 次' }),
        expect.objectContaining({ label: '最近重试时间' }),
        expect.objectContaining({ label: '耗时', value: '1 秒' }),
      ]),
    )

    expect(getGroup(regularGroups, 'basic')).toMatchObject({ label: '基本信息', columns: 5 })
    expect(getGroup(regularGroups, 'basic').fields.map((field) => field.label)).toEqual(
      expect.arrayContaining(['任务 ID', '来源', '来源类型', '状态', '文件大小']),
    )
    expect(getGroup(regularGroups, 'transfer')).toMatchObject({ label: '传输信息', columns: 2 })
    expect(getGroup(regularGroups, 'file')).toMatchObject({ label: '文件信息' })
    expect(getGroup(regularGroups, 'time')).toMatchObject({ label: '执行时间', columns: 3 })

    const sizeOnlyGroups = buildDownloadTaskDetailGroups(createDownloadTask({ size: 1024 }))
    expect(getGroup(sizeOnlyGroups, 'basic').fields).toEqual(
      expect.arrayContaining([expect.objectContaining({ label: '文件大小', value: '1 KB' })]),
    )
    expect(sizeOnlyGroups.map((group) => group.key)).not.toContain('transfer')

    const localFields = getFields(
      buildDownloadTaskDetailGroups(
        createDownloadTask({
          source: 'local_file',
          source_type: 'local',
          remote_file_id: '/source/movie.mkv',
        }),
      ),
    )
    expect(localFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '本地源文件路径', value: '/source/movie.mkv' }),
      ]),
    )

    const embyFields = getFields(
      buildDownloadTaskDetailGroups(
        createDownloadTask({
          source_type: 'emby_media',
          remote_file_id: 'https://emby.example/extract',
          remote_path: 'emby-item-123',
          local_full_path: '',
        }),
      ),
    )

    expect(embyFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '提取地址', value: 'https://emby.example/extract' }),
        expect.objectContaining({ label: 'Emby 条目 ID', value: 'emby-item-123' }),
      ]),
    )
    expect(embyFields.map((field) => field.label)).not.toContain('本地目标路径')
  })

  it('按来源和阶段显示上传专属详情，不生成空值占位', () => {
    const groups = buildUploadTaskDetailGroups(
      createUploadTask({
        source: 'directory_monitor',
        status: 2,
        file_size: 2 * 1024,
        upload_phase: 'completed',
        upload_result: 'multipart_uploaded',
        remote_file_id: '/remote/movie.mkv',
        remote_path_id: '115-parent-id',
        completed_remote_file_id: 'completed-file-id',
        completed_pick_code: 'pick-code',
        relative_path: 'Season 1/movie.mkv',
        source_deleted_at: 1_700_000_000,
        resume_state: 'resumed_session',
        total_parts: 10,
        uploaded_parts: 10,
        source_cleanup_status: 'completed',
        source_cleanup_error: '旧清理错误',
        error: '上传后回调失败',
        start_time: 1_700_000_001,
        end_time: 1_700_000_002,
      }),
    )
    const fields = getFields(groups)

    expect(fields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '上传结果', value: '上传完成' }),
        expect.objectContaining({ label: '失败原因', value: '上传后回调失败', fullWidth: true }),
        expect.objectContaining({ label: '远端目标', value: '/remote/movie.mkv' }),
        expect.objectContaining({ label: '远端父目录 ID', value: '115-parent-id' }),
        expect.objectContaining({ label: '远端文件 ID', value: 'completed-file-id' }),
        expect.objectContaining({ label: 'PickCode', value: 'pick-code' }),
        expect.objectContaining({ label: '断点续传', value: '已恢复上传' }),
        expect.objectContaining({ label: '分片进度', value: '10/10' }),
        expect.objectContaining({ label: '源文件清理', value: '清理成功' }),
        expect.objectContaining({ label: '清理失败原因', value: '旧清理错误' }),
        expect.objectContaining({ label: '相对监控根目录', value: 'Season 1/movie.mkv' }),
        expect.objectContaining({ label: '源文件删除时间' }),
        expect.objectContaining({ label: '耗时', value: '1 秒' }),
      ]),
    )

    expect(getGroup(groups, 'basic')).toMatchObject({ label: '基本信息', columns: 4 })
    expect(getGroup(groups, 'transfer')).toMatchObject({ label: '传输信息', columns: 3 })
    expect(getGroup(groups, 'file')).toMatchObject({ label: '文件信息', columns: 3 })
    expect(getGroup(groups, 'upload')).toMatchObject({ label: '上传信息', columns: 3 })
    expect(
      getGroup(groups, 'upload')
        .fields.slice(0, 3)
        .map((field) => field.label),
    ).toEqual(['断点续传', '源文件清理', '相对监控根目录'])
    expect(getGroup(groups, 'time')).toMatchObject({ label: '执行时间', columns: 3 })

    const internalFields = getFields(
      buildUploadTaskDetailGroups({
        ...createUploadTask(),
        account_id: 1,
        sync_path_id: 2,
        sync_file_id: 3,
        scrape_media_file_id: 4,
        local_mtime: 5,
        local_mtime_ns: 6,
        is_season_or_tvshow_file: true,
        source_fingerprint: 'v1:1:2',
      } as UploadQueueTaskDetailInput & Record<string, unknown>),
    )
    expect(
      internalFields.filter((field) =>
        [
          'account_id',
          'sync_path_id',
          'sync_file_id',
          'scrape_media_file_id',
          'local_mtime',
          'local_mtime_ns',
          'is_season_or_tvshow_file',
          'source_fingerprint',
        ].includes(field.key),
      ),
    ).toHaveLength(0)
  })

  it('仅在正确来源和秒传等待阶段显示受限字段', () => {
    const openListFields = getFields(
      buildUploadTaskDetailGroups(
        createUploadTask({
          source_type: 'openlist',
          remote_path_id: '/media',
          completed_pick_code: 'not-for-openlist',
          upload_phase: 'rapid_waiting',
          rapid_wait_attempts: 2,
          rapid_wait_until: 1_700_000_000,
        }),
      ),
    )
    expect(openListFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '远端父目录路径', value: '/media' }),
        expect.objectContaining({ label: '秒传等待尝试次数', value: '2 次' }),
        expect.objectContaining({ label: '秒传等待截止时间' }),
      ]),
    )
    expect(openListFields.map((field) => field.label)).not.toContain('PickCode')

    const otherFields = getFields(
      buildUploadTaskDetailGroups(
        createUploadTask({
          source_type: 'baidupan',
          remote_path_id: 'parent',
          upload_phase: 'multipart_uploading',
          rapid_wait_attempts: 3,
          rapid_wait_until: 1_700_000_000,
          upload_result: 'multipart_uploaded',
          status: 1,
        }),
      ),
    )
    expect(otherFields).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: '远端父目录', value: 'parent' }),
        expect.objectContaining({ label: '上传进度', value: '0%' }),
      ]),
    )
    expect(otherFields.map((field) => field.label)).not.toEqual(
      expect.arrayContaining(['秒传等待尝试次数', '秒传等待截止时间', '上传结果']),
    )
  })

  it('上传传输 Tooltip 同时保留完整错误和已有附加信息', () => {
    const error =
      '调用 115 上传 API 失败：本地文件已变化，不能复用断点续传 session：本地文件修改时间不匹配'
    const summary = getUploadTransportDetailSummary(
      createUploadTask({
        source: 'directory_monitor',
        resume_state: 'new_session',
        total_parts: 3,
        uploaded_parts: 1,
        source_cleanup_status: 'failed',
        source_cleanup_error: 'permission denied',
        error,
      }),
    )

    expect(summary).toContain('断点续传：新上传')
    expect(summary).toContain('分片进度：1/3')
    expect(summary).toContain('源文件清理：清理失败')
    expect(summary).toContain('清理失败原因：permission denied')
    expect(summary).toContain(`失败原因：${error}`)
  })
})
