import { describe, expect, it } from 'vitest'
import {
  getDownloadSourceName,
  getSyncTaskTypeName,
  getTaskSourceTypeName,
  getUploadSourceName,
} from '@/utils/taskSourceUtils'

describe('taskSourceUtils', () => {
  it('formats download source storage values', () => {
    expect(getDownloadSourceName('strm_sync')).toBe('STRM 同步')
    expect(getDownloadSourceName('local_file')).toBe('本地文件')
    expect(getDownloadSourceName('emby_media')).toBe('Emby 媒体信息提取')
    expect(getDownloadSourceName('jellyfin_media')).toBe('Jellyfin 媒体信息提取')
  })

  it('formats upload source storage values', () => {
    expect(getUploadSourceName('strm_sync')).toBe('STRM 同步')
    expect(getUploadSourceName('scrape_organize')).toBe('刮削整理')
    expect(getUploadSourceName('directory_monitor')).toBe('目录监控上传')
  })

  it('formats sync queue task type storage values', () => {
    expect(getSyncTaskTypeName('strm_sync')).toBe('STRM 同步')
    expect(getSyncTaskTypeName('scrape_organize')).toBe('刮削整理')
  })

  it('formats task source type storage values', () => {
    expect(getTaskSourceTypeName('115')).toBe('115 网盘')
    expect(getTaskSourceTypeName('baidupan')).toBe('百度网盘')
    expect(getTaskSourceTypeName('openlist')).toBe('OpenList')
    expect(getTaskSourceTypeName('local')).toBe('本地文件')
    expect(getTaskSourceTypeName('emby_media')).toBe('Emby 媒体信息提取')
    expect(getTaskSourceTypeName('jellyfin_media')).toBe('Jellyfin 媒体信息提取')
  })

  it('keeps unknown task source values visible', () => {
    expect(getDownloadSourceName('future_download_source')).toBe('future_download_source')
    expect(getUploadSourceName('future_upload_source')).toBe('future_upload_source')
    expect(getSyncTaskTypeName('future_sync_task')).toBe('future_sync_task')
    expect(getTaskSourceTypeName('future_source_type')).toBe('其他')
  })
})
