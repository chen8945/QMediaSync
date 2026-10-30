import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('AppNotificationChannels 移动端 header 操作', () => {
  it('使用响应式图标按钮避免隐藏文字后图标偏移', () => {
    const source = readFileSync(resolve('src/views/settings/AppNotificationChannels.vue'), 'utf8')

    expect(source).toContain(
      "import ResponsiveIconButton from '@/components/common/ResponsiveIconButton.vue'",
    )
    expect(source).toContain('<ResponsiveIconButton')
    expect(source).not.toContain('.header-actions .btn-text')
  })
})
