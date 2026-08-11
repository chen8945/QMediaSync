// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

describe('AppBackupRecords responsive layout', () => {
  it('uses the shared responsive record table without a fixed table height', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    expect(source).toContain(
      "import ResponsiveRecordTable from '@/components/records/ResponsiveRecordTable.vue'",
    )
    expect(source).toContain('<ResponsiveRecordTable')
    expect(source).toContain(':is-mobile="isMobile"')
    expect(source).not.toContain(':height="isMobile ? \'auto\' : 400"')
  })

  it('keeps the manual-backup action visually unboxed in the shared header', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    expect(source).toContain('<template #actions>')
    expect(source).toContain('<div class="action-section">')
    expect(source).not.toMatch(/\.action-section\s*\{[^}]*margin-bottom:/)
    expect(source).not.toMatch(/\.action-section\s*\{[^}]*background:/)
  })

  it('formats a zero-second backup duration instead of treating it as missing', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    expect(source).toContain('value: (row) => formatDuration(row.backup_duration)')
  })

  it('orders expanded details with six compact fields before full-width path and reason', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    const keys = [
      "key: 'id'",
      "key: 'status'",
      "key: 'backup_type'",
      "key: 'created_at'",
      "key: 'backup_duration'",
      "key: 'file_size'",
      "key: 'file_path'",
      "key: 'created_reason'",
    ]

    const positions = keys.map((key) => source.indexOf(key))

    expect(positions.every((position) => position >= 0)).toBe(true)
    expect(positions).toEqual([...positions].sort((left, right) => left - right))
    expect(source).toMatch(/key: 'file_path',[\s\S]{0,280}span: 3/)
    expect(source).toMatch(/key: 'created_reason',[\s\S]{0,280}span: 3/)
  })

  it('keeps the file path stretchable on desktop while mobile retains full record columns', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    // 不钉缩进和属性顺序：只要求这几列的优先级和宽度约束还在，格式化改动不应该让回归测试变红
    expect(source).toMatch(
      /key: 'file_path',[\s\S]{0,280}priority: 'secondary',[\s\S]{0,80}minWidth: 320/,
    )
    expect(source).toMatch(/key: 'id',[\s\S]{0,80}priority: 'primary'/)
    expect(source).toMatch(/key: 'backup_type',[\s\S]{0,80}priority: 'primary'/)
    expect(source).toMatch(
      /key: 'created_at',[\s\S]{0,120}priority: 'primary',[\s\S]{0,80}width: 180/,
    )
  })
})
