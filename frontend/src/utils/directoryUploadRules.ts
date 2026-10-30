import type { DirectoryUploadRule } from '@/types/directoryUpload'

export const groupDirectoryUploadRulesBySyncPath = <T extends DirectoryUploadRule>(
  rules: T[],
): Record<number, T[]> => {
  const grouped: Record<number, T[]> = {}
  for (const rule of rules) {
    if (!grouped[rule.sync_path_id]) {
      grouped[rule.sync_path_id] = []
    }
    grouped[rule.sync_path_id].push(rule)
  }
  return grouped
}

export const getEnabledDirectoryUploadRules = <T extends DirectoryUploadRule>(rules: T[]): T[] => {
  return rules.filter((rule) => rule.enabled)
}

export const formatDirectoryUploadStatus = (
  rules: DirectoryUploadRule[],
  masterEnabled = true,
  loadFailed = false,
): string => {
  if (loadFailed) {
    return '加载失败'
  }
  if (rules.length === 0) {
    return '未配置'
  }
  if (!masterEnabled) {
    return `已关闭 / 共 ${rules.length} 个`
  }
  const enabledCount = getEnabledDirectoryUploadRules(rules).length
  if (enabledCount === 0) {
    return `已停用 / 共 ${rules.length} 个`
  }
  return `已启用 ${enabledCount} 个 / 共 ${rules.length} 个`
}

export const formatDirectoryUploadPathSummary = (rules: DirectoryUploadRule[]): string => {
  if (rules.length === 0) {
    return ''
  }
  const firstPath = rules[0]?.monitor_path || ''
  if (rules.length === 1) {
    return firstPath
  }
  return `${firstPath} 等 ${rules.length} 个目录`
}
