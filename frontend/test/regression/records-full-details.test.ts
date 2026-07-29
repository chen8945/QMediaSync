// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

describe('记录页完整详情', () => {
  it('备份、刮削和同步记录都启用共享表格的完整详情', () => {
    for (const component of [
      'AppBackupRecords.vue',
      'AppScrapeRecords.vue',
      'AppSyncRecords.vue',
    ]) {
      const source = readFileSync(resolve(process.cwd(), `src/components/${component}`), 'utf8')

      expect(source).toContain('show-all-details')
      expect(source).toMatch(/key: 'status',[\s\S]{0,240}detailField:/)
    }
  })

  it('同步和备份记录在桌面端使用三列详情布局', () => {
    for (const component of ['AppBackupRecords.vue', 'AppSyncRecords.vue']) {
      const source = readFileSync(resolve(process.cwd(), `src/components/${component}`), 'utf8')

      expect(source).toContain(':detail-columns="3"')
    }

    const syncSource = readFileSync(
      resolve(process.cwd(), 'src/components/AppSyncRecords.vue'),
      'utf8',
    )

    for (const key of ['local_path', 'remote_path', 'stats', 'fail_reason']) {
      expect(syncSource).toMatch(new RegExp(`key: '${key}',[\\s\\S]{0,320}span: 3`))
    }

    const detailsSource = readFileSync(
      resolve(process.cwd(), 'src/components/records/RecordDetailDescriptions.vue'),
      'utf8',
    )

    expect(detailsSource).toContain("'record-detail--three-columns': columns === 3")
    expect(detailsSource).toMatch(/\.record-detail--three-columns[\s\S]*table-layout:\s*fixed/)
  })

  it('同步记录移动端字号作用于共享表格本身', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/components/AppSyncRecords.vue'), 'utf8')

    expect(source).toMatch(/\.sync-table\s+:deep\(\.record-table\)\s*\{\s*font-size:\s*12px/)
  })

  it('同步路径单元格不重复显示移动端任务 ID', () => {
    const source = readFileSync(resolve(process.cwd(), 'src/components/AppSyncRecords.vue'), 'utf8')

    expect(source).not.toContain('sync-path-cell__id')
  })
})
