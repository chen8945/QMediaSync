export interface SyncRefreshDecisionInput {
  createdStrm: number
  downloadedMeta: number
  status?: number
}

export type SyncRefreshDecisionTagType = 'warning' | 'info'

export interface SyncRefreshDecision {
  hasRefreshRelatedChanges: boolean
  label: string
  type: SyncRefreshDecisionTagType
  reason: string
}

export const getEmbyRefreshDecision = ({
  createdStrm,
  downloadedMeta,
  status = 2,
}: SyncRefreshDecisionInput): SyncRefreshDecision => {
  const hasRefreshRelatedChanges = createdStrm > 0 || downloadedMeta > 0

  if (!hasRefreshRelatedChanges) {
    return {
      hasRefreshRelatedChanges: false,
      label: status === 2 ? '无需刷新媒体库' : '暂无媒体库刷新变更',
      type: 'info',
      reason: '无新增 STRM 或下载元数据',
    }
  }

  if (status === 3 || status === 6) {
    return {
      hasRefreshRelatedChanges: true,
      label: '未提交媒体库刷新',
      type: 'info',
      reason: '同步任务未成功完成',
    }
  }

  if (status === 2 || status === 4 || status === 5) {
    return {
      hasRefreshRelatedChanges: true,
      label: '有刷新相关变更',
      type: 'warning',
      reason: '实际刷新需满足 Emby 已启用且同步目录已关联媒体库',
    }
  }

  return {
    hasRefreshRelatedChanges: true,
    label: '检测到刷新相关变更',
    type: 'warning',
    reason: '任务完成后会由后端根据 Emby 配置和媒体库关联判断是否刷新',
  }
}
