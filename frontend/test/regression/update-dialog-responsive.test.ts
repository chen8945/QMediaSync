import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('AppUpdate 更新完成弹窗响应式样式', () => {
  const source = readFileSync(resolve(__dirname, '../../src/views/system/AppUpdate.vue'), 'utf-8')

  it('通过 Element Plus width prop 控制倒计时弹窗宽度', () => {
    const dialogStyleBlock = source.match(
      /:deep\(\.update-complete-dialog\.el-dialog\),\s*:deep\(\.update-complete-dialog \.el-dialog\) \{[\s\S]*?\}/,
    )?.[0]

    expect(source).toContain('width="min(500px, calc(100vw - 32px))"')
    expect(dialogStyleBlock).toBeDefined()
    expect(dialogStyleBlock).not.toMatch(/^\s*(?:width|max-width):/m)
  })
})
