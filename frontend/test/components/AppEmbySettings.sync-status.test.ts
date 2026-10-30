import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('AppEmbySettings 同步状态展示', () => {
  const source = readFileSync(resolve('src/views/settings/AppEmbySettings.vue'), 'utf8')

  it('区分 Emby 刷新媒体库和条目同步状态文案', () => {
    expect(source).toContain("refresh_library: '刷新 Emby 媒体库'")
    expect(source).toContain("incremental: '增量同步 Emby 条目'")
    expect(source).toContain("full: '全量同步 Emby 条目'")
    expect(source).toContain("webhook: 'Webhook 单条同步 Emby 条目'")
  })

  it('业务时间通过统一工具格式化且不直接展示原始秒值', () => {
    expect(source).toContain('formatSyncRelativeTime(syncInfo.last_sync_time)')
    expect(source).toContain('{{ lastSuccessSyncHelper }}')
    expect(source).toContain('formatSyncAbsoluteTime(syncInfo.value.last_sync_time)')
    expect(source).not.toContain('{{ syncInfo.last_sync_time }}')
    expect(source).not.toContain('{{ syncInfo.started_at }}')
  })

  it('运行中禁用手动同步按钮', () => {
    expect(source).toContain(':disabled="isStartSyncDisabled"')
    expect(source).toContain('const isStartSyncDisabled = computed')
  })
})
