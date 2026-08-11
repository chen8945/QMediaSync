import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('AppEmbySettings 移动端操作和媒体库图标', () => {
  it('使用响应式操作栏并定义媒体库图标背景', () => {
    const source = readFileSync(resolve('src/components/AppEmbySettings.vue'), 'utf8')

    expect(source).toContain(
      "import ResponsiveActionBar from '@/components/common/ResponsiveActionBar.vue'",
    )
    expect(source).toContain('<ResponsiveActionBar')
    expect(source).toContain('<PageHeader />')
    expect(source).toContain('.library-icon')
    expect(source).toMatch(/\.library-icon\s*\{[\s\S]*background:/)
  })

  it('保留内容卡片标题层级并让静止状态的卡片边界可见', () => {
    const source = readFileSync(resolve('src/components/AppEmbySettings.vue'), 'utf8')

    expect(source).toContain('<h2 class="card-title">Emby 服务器配置</h2>')
    expect(source).not.toContain('<h1 class="card-title">')
    expect(source).toContain('<h3 class="card-title">通知链接配置</h3>')
    expect(source).toMatch(
      /\.settings-card,\s*\.sync-management-card\s*\{[\s\S]*border:\s*1px solid #dcdfe6;[\s\S]*box-shadow:/,
    )
    expect(source).toContain('.settings-card :deep(.el-card__header)')
    expect(source).toMatch(
      /\.settings-card :deep\(\.el-card__header\),\s*\.sync-management-card :deep\(\.el-card__header\)\s*\{[\s\S]*border-bottom:\s*1px solid #dcdfe6;/,
    )
    expect(source).toMatch(
      /\.settings-card,\s*\.sync-management-card\s*\{[\s\S]*border:\s*1px solid #dcdfe6;/,
    )
  })
})
