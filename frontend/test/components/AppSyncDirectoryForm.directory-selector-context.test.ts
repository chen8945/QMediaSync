import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(__dirname, '../../src/components/AppSyncDirectoryForm.vue'),
  'utf-8',
)

describe('AppSyncDirectoryForm 目录选择器上下文', () => {
  it('按选择目标显示对应的目录对话框标题', () => {
    expect(source).toContain(':title="directoryDialogTitle"')
    expect(source).toContain("source: '选择网盘来源目录'")
    expect(source).toContain("strmLocal: '选择 STRM 存放目录'")
    expect(source).toContain("uploadMonitor: '选择监控目录'")
    expect(source).toContain("uploadRemote: '选择上传目标目录'")
  })

  it('以动态视口高度限制选择器，移动端操作区仍可访问', () => {
    expect(source).toMatch(/\.dir-selector\s*{[\s\S]*max-height:\s*calc\(100dvh - 32px\)/)
  })

  it('115 上传目标从已选来源目录的真实 ID 开始加载', () => {
    expect(source).toContain(':root-id="initialRootId"')
    expect(source).toContain("const initialRootId = ref('')")
    expect(source).toContain("initialRootId.value = form.base_cid || ''")
  })
})
