// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

describe('AppBackupRestore', () => {
  it('仅接受 ZIP 备份，并提示恢复后重启服务', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRestore.vue'),
      'utf8',
    )

    expect(source).toContain('提示：恢复成功后请重启服务，让所有数据和配置生效')
    expect(source).toContain('恢复成功后请重启服务让所有数据和配置生效')
    expect(source).toContain('accept=".zip"')
    expect(source).toContain("endsWith('.zip')")
    expect(source).not.toContain('.sql')
    expect(source).toContain('dangerouslyUseHTMLString: true')
    expect(source.indexOf('提示：恢复成功后请重启服务，让所有数据和配置生效')).toBeLessThan(
      source.indexOf('重要提示：当前备份与恢复功能仍在完善'),
    )
  })
})
