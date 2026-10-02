import { describe, expect, it } from 'vitest'

import { getEmbyRefreshDecision } from '@/utils/syncRefreshDecision'

describe('getEmbyRefreshDecision', () => {
  it.each([4, 5])('终态 %s 保留成功范围的刷新相关变更', (status) => {
    expect(getEmbyRefreshDecision({ createdStrm: 1, downloadedMeta: 0, status }).label).toBe(
      '有刷新相关变更',
    )
  })

  it('取消不显示任务完成后仍会刷新', () => {
    expect(getEmbyRefreshDecision({ createdStrm: 1, downloadedMeta: 0, status: 6 }).label).toBe(
      '未提交媒体库刷新',
    )
  })

  it('新增 STRM 和元数据下载皆为 0 时展示无需刷新', () => {
    expect(getEmbyRefreshDecision({ createdStrm: 0, downloadedMeta: 0 })).toEqual({
      hasRefreshRelatedChanges: false,
      label: '无需刷新媒体库',
      type: 'info',
      reason: '无新增 STRM 或下载元数据',
    })
  })

  it('已完成任务有新增 STRM 或元数据下载时只展示刷新相关变更', () => {
    expect(getEmbyRefreshDecision({ createdStrm: 1, downloadedMeta: 0, status: 2 })).toEqual({
      hasRefreshRelatedChanges: true,
      label: '有刷新相关变更',
      type: 'warning',
      reason: '实际刷新需满足 Emby 已启用且同步目录已关联媒体库',
    })
  })

  it('任务运行中有新增 STRM 时只展示检测到刷新相关变更', () => {
    expect(getEmbyRefreshDecision({ createdStrm: 1, downloadedMeta: 0, status: 1 })).toEqual({
      hasRefreshRelatedChanges: true,
      label: '检测到刷新相关变更',
      type: 'warning',
      reason: '任务完成后会由后端根据 Emby 配置和媒体库关联判断是否刷新',
    })
  })

  it('失败任务有新增 STRM 时不展示已提交刷新', () => {
    expect(getEmbyRefreshDecision({ createdStrm: 1, downloadedMeta: 0, status: 3 })).toEqual({
      hasRefreshRelatedChanges: true,
      label: '未提交媒体库刷新',
      type: 'info',
      reason: '同步任务未成功完成',
    })
  })
})
