// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

describe('AppBackupRestore', () => {
  it('上传恢复使用同一 URL 的预检和确认，并说明完整覆盖与重启边界', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRestore.vue'),
      'utf8',
    )

    expect(source).toContain('提示：恢复成功后请重启服务，让所有数据和配置生效')
    expect(source).toContain("formData.append('phase', 'preflight')")
    expect(source).toContain("confirmData.append('phase', 'confirm')")
    expect(source).toContain('配置和全部数据都会被覆盖。恢复目标：')
    expect(source).toContain('TLS 证书/私钥和日志')
    expect(source).toContain('accept=".zip"')
  })
})
