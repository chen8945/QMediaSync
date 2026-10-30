import { describe, expect, it } from 'vitest'

import {
  formatDirectoryUploadPathSummary,
  formatDirectoryUploadStatus,
  getEnabledDirectoryUploadRules,
  groupDirectoryUploadRulesBySyncPath,
} from '@/utils/directoryUploadRules'
import type { DirectoryUploadRule } from '@/types/directoryUpload'

const makeRule = (partial: Partial<DirectoryUploadRule>): DirectoryUploadRule => ({
  id: partial.id ?? 1,
  sync_path_id: partial.sync_path_id ?? 10,
  account_id: partial.account_id ?? 1,
  enabled: partial.enabled ?? true,
  monitor_path: partial.monitor_path ?? '/watch/a',
  remote_root_path: partial.remote_root_path ?? '/remote/a',
  remote_root_id: partial.remote_root_id ?? 'remote-a',
  recursive: partial.recursive ?? true,
  watch_mode: partial.watch_mode ?? 'auto',
  upload_metadata: partial.upload_metadata ?? false,
  startup_scan_enabled: partial.startup_scan_enabled ?? true,
  processed_cache_ttl_seconds: partial.processed_cache_ttl_seconds ?? 600,
  delete_source_after_success: partial.delete_source_after_success ?? false,
  ignore_patterns: partial.ignore_patterns ?? [],
  overwrite_mode: partial.overwrite_mode ?? 'skip_same',
})

describe('directoryUploadRules', () => {
  it('按同步目录 ID 分组并保留同一同步目录下的多条规则', () => {
    const grouped = groupDirectoryUploadRulesBySyncPath([
      makeRule({ id: 1, sync_path_id: 10, monitor_path: '/watch/a' }),
      makeRule({ id: 2, sync_path_id: 10, monitor_path: '/watch/b' }),
      makeRule({ id: 3, sync_path_id: 11, monitor_path: '/watch/c' }),
    ])

    expect(grouped[10].map((rule) => rule.monitor_path)).toEqual(['/watch/a', '/watch/b'])
    expect(grouped[11].map((rule) => rule.monitor_path)).toEqual(['/watch/c'])
  })

  it('只返回启用的目录监控规则用于批量扫描', () => {
    const enabled = getEnabledDirectoryUploadRules([
      makeRule({ id: 1, enabled: true }),
      makeRule({ id: 2, enabled: false }),
      makeRule({ id: 3, enabled: true }),
    ])

    expect(enabled.map((rule) => rule.id)).toEqual([1, 3])
  })

  it('格式化多规则状态和监控目录摘要', () => {
    const rules = [
      makeRule({ id: 1, enabled: true, monitor_path: '/watch/a' }),
      makeRule({ id: 2, enabled: false, monitor_path: '/watch/b' }),
      makeRule({ id: 3, enabled: true, monitor_path: '/watch/c' }),
    ]

    expect(formatDirectoryUploadStatus([])).toBe('未配置')
    expect(formatDirectoryUploadStatus(rules)).toBe('已启用 2 个 / 共 3 个')
    expect(formatDirectoryUploadStatus(rules, false)).toBe('已关闭 / 共 3 个')
    expect(formatDirectoryUploadPathSummary(rules)).toBe('/watch/a 等 3 个目录')
  })

  it('目录监控规则加载失败时展示独立错误态而不是未配置', () => {
    expect(formatDirectoryUploadStatus([], true, true)).toBe('加载失败')
  })
})
