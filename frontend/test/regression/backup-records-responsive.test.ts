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

  it('keeps the manual-backup action visually unboxed', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRecords.vue'),
      'utf8',
    )

    expect(source).toMatch(/\.action-section\s*\{\s*margin-bottom:\s*20px;/)
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

    expect(source).toContain("priority: 'secondary',\n    minWidth: 320")
    expect(source).toContain("key: 'id',\n    label: 'ID',\n    priority: 'primary'")
    expect(source).toContain("key: 'backup_type',\n    label: '类型',\n    priority: 'primary'")
    expect(source).toContain(
      "key: 'created_at',\n    label: '创建时间',\n    priority: 'primary',\n    width: 180",
    )
  })
})
