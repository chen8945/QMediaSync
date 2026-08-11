import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

const readSource = (path: string) => readFileSync(resolve(path), 'utf8')

describe('首页移动端头部布局', () => {
  it('AppHome 只在首页移动端恢复页面身份并保留运行日志操作', () => {
    const source = readSource('src/components/AppHome.vue')
    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
    const routerSource = readSource('src/router/index.ts')

    expect(source).toContain(
      '<PageHeader class="home-page-header" icon="" :show-identity-on-mobile="true">',
    )
    expect(source).toContain('运行日志')

    expect(mobileStyles).not.toContain('qms-page-header__main')

    const actionsRule = extractRule(mobileStyles, '.home-header__actions')
    expect(actionsRule).toContain('justify-content: center;')
    expect(actionsRule).toContain('width: 100%;')
    expect(
      extractRule(mobileStyles, '.home-page-header :deep(.qms-page-header__identity)'),
    ).toContain('justify-content: center;')
    expect(extractRule(mobileStyles, '.home-page-header :deep(.qms-page-header__copy)')).toContain(
      'text-align: center;',
    )
    expect(extractRule(source, '.header-section :deep(.qms-page-header__top)')).toContain(
      'align-items: center;',
    )
    expect(routerSource).toMatch(
      /name:\s*['"]home['"][\s\S]*?page:\s*\{[\s\S]*?title:\s*['"]控制台['"][\s\S]*?description:\s*['"]系统运行状态监控与管理['"]/,
    )
  })
})
