import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(__dirname, '../../src/components/AppSyncDirectories.vue'),
  'utf-8',
)

describe('AppSyncDirectories 目录监控规则加载状态', () => {
  it('规则加载失败时显示独立错误态，避免展示为未配置', () => {
    expect(source).toContain('directoryUploadRulesLoadFailed')
    expect(source).toMatch(/directoryUploadRulesLoadFailed\.value[\s\S]*?加载失败/)
    expect(source).toMatch(/directoryUploadRulesLoadFailed\.value[\s\S]*?return\s+['"]danger['"]/)
    expect(source).toMatch(
      /const\s+loadDirectoryUploadRules\s*=\s*async\s*\([^)]*\)\s*=>\s*{[\s\S]*?directoryUploadRulesLoadFailed\.value\s*=\s*true/,
    )
  })
})
