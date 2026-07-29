// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const backupNotice =
  '当前备份与恢复功能仍在完善，建议同时使用外部方式备份重要数据；后续将持续完善。'

describe('备份页面风险提示', () => {
  it('在设置、记录和恢复页面显示同一提示', () => {
    for (const component of [
      'AppBackupSettings.vue',
      'AppBackupRecords.vue',
      'AppBackupRestore.vue',
    ]) {
      const source = readFileSync(resolve(process.cwd(), `src/components/${component}`), 'utf8')
      expect(source).toContain(backupNotice)
    }
  })
})
