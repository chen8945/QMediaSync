import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

const pages = [
  {
    file: 'AppSyncDirectories.vue',
    rootClass: 'sync-directories-page',
    contentClass: 'directories-content',
    gridClass: 'directories-grid',
    cardClass: 'directory-card',
  },
  {
    file: 'AppScrapePathes.vue',
    rootClass: 'scrape-pathes-page',
    contentClass: 'pathes-content',
    gridClass: 'pathes-grid',
    cardClass: 'path-card',
  },
] as const

const managementPages = [
  'AppCloudAccounts.vue',
  'AppSyncDirectories.vue',
  'AppScrapePathes.vue',
  'AppApiKeys.vue',
  'user-settings/LoginSessions.vue',
] as const

const getPageHeaderOpenTag = (source: string) => {
  const start = source.indexOf('<PageHeader')
  const end = source.indexOf('>', start)
  return start >= 0 && end >= 0 ? source.slice(start, end + 1) : ''
}

describe('管理列表页布局', () => {
  for (const page of pages) {
    it(`${page.file} 将统计信息放在内容左侧并在移动端显示`, () => {
      const source = readFileSync(resolve(`src/components/${page.file}`), 'utf8')
      expect(source).not.toContain('management-page-header')
      expect(source).toContain('<template #actions>')
      expect(source).not.toContain('mobile-hidden')
      const pageHeaderEnd = source.indexOf('</PageHeader>')
      const contentStart = source.indexOf(`<div class="${page.contentClass}">`)
      const actionsSlotStart = source.indexOf('<template #actions>')
      const statsSlotStart = source.indexOf('<template #stats>')
      const statsStart = source.indexOf('<PageStats :items="stats" />')
      const statsSlotEnd = source.indexOf('</template>', statsSlotStart)
      const gridStart = source.indexOf(`<div class="${page.gridClass}"`)

      expect(pageHeaderEnd).toBeGreaterThan(-1)
      expect(contentStart).toBeGreaterThan(pageHeaderEnd)
      expect(actionsSlotStart).toBeGreaterThan(-1)
      expect(statsSlotStart).toBeGreaterThan(actionsSlotStart)
      expect(statsStart).toBeGreaterThan(-1)
      expect(statsStart).toBeGreaterThan(statsSlotStart)
      expect(statsStart).toBeLessThan(statsSlotEnd)
      expect(source.slice(statsStart, statsSlotEnd)).not.toContain('mobile-hidden')
      expect(gridStart).toBeGreaterThan(contentStart)
      expect(source).not.toContain('class="page-title"')
      expect(source).not.toContain('<h1')

      expect(source).toContain("import PageStats from '@/components/common/PageStats.vue'")

      const statLabels = [
        '总目录数',
        '运行中',
        '等待中',
        page.file === 'AppSyncDirectories.vue' ? '定时同步' : '定时任务',
      ]
      const statPositions = statLabels.map((label) => source.indexOf(`label: '${label}'`))
      expect(statPositions.every((position) => position >= 0)).toBe(true)
      expect(statPositions).toEqual([...statPositions].sort((a, b) => a - b))

      expect(extractRule(source, `.${page.cardClass}`)).toContain('border: 1px solid #dcdfe6;')
      expect(extractRule(source, '.card-header')).toContain('border-bottom: 1px solid #dcdfe6;')
      expect(extractRule(source, '.card-footer')).toContain('border-top: 1px solid #dcdfe6;')

      const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
      expect(extractRule(mobileStyles, `.${page.rootClass}`)).toContain('padding: 0 8px 8px;')
      expect(extractRule(mobileStyles, '.header-actions')).toContain('width: max-content;')
      expect(extractRule(mobileStyles, '.header-actions .add-btn')).toContain('width: auto;')
    })
  }

  it('AppCloudAccounts.vue 在移动端保留统计信息并使用共享头部 actions', () => {
    const source = readFileSync(resolve('src/components/AppCloudAccounts.vue'), 'utf8')
    expect(source).not.toContain('management-page-header')
    expect(source).toContain('<template #actions>')
    expect(source).toContain('<template #stats>')
    expect(source).toContain('<PageStats :items="stats" />')
    expect(source).toContain("import PageStats from '@/components/common/PageStats.vue'")

    const pageHeaderEnd = source.indexOf('</PageHeader>')
    const contentStart = source.indexOf('<div class="accounts-content">')
    const statsSlotStart = source.indexOf('<template #stats>')
    const statsSlotEnd = source.indexOf('</template>', statsSlotStart)
    const statsStart = source.indexOf('<PageStats :items="stats" />')
    expect(pageHeaderEnd).toBeGreaterThan(-1)
    expect(statsSlotStart).toBeGreaterThan(-1)
    expect(statsSlotEnd).toBeGreaterThan(statsSlotStart)
    expect(statsStart).toBeGreaterThan(-1)
    expect(statsStart).toBeGreaterThan(statsSlotStart)
    expect(statsStart).toBeLessThan(statsSlotEnd)
    expect(statsStart).toBeLessThan(pageHeaderEnd)
    expect(source.slice(statsStart, statsSlotEnd)).not.toContain('mobile-hidden')
    expect(contentStart).toBeGreaterThan(pageHeaderEnd)

    for (const label of ['总账号数', '已授权', '未授权', '授权失败']) {
      expect(source).toContain(`label: '${label}'`)
    }

    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
    expect(extractRule(mobileStyles, '.cloud-accounts-page')).toContain('padding: 0 8px 8px;')
  })

  it('共享 PageStats 在桌面端保持原尺寸并在移动端使用紧凑两列布局', () => {
    const source = readFileSync(resolve('src/components/common/PageStats.vue'), 'utf8')
    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')

    expect(source).toContain('items: readonly PageStatItem[]')
    expect(extractRule(source, '.qms-page-stats__item')).toContain('min-width: 140px;')
    expect(extractRule(source, '.qms-page-stats__icon')).toContain('flex: 0 0 40px;')
    expect(extractRule(source, '.qms-page-stats__value')).toContain('font-size: 20px;')
    expect(extractRule(mobileStyles, '.qms-page-stats')).toContain(
      'grid-template-columns: repeat(2, minmax(0, 1fr));',
    )
    expect(extractRule(mobileStyles, '.qms-page-stats__item')).toContain('padding: 8px 10px;')
    expect(extractRule(mobileStyles, '.qms-page-stats__icon')).toContain('width: 32px;')
    expect(extractRule(mobileStyles, '.qms-page-stats__value')).toContain('font-size: 16px;')
  })

  it('五个目标页移除 management-page-header 并保留共享 actions', () => {
    for (const file of managementPages) {
      const source = readFileSync(resolve(`src/components/${file}`), 'utf8')
      const pageHeaderOpenTag = getPageHeaderOpenTag(source)
      expect(source, file).not.toContain('management-page-header')
      expect(source, file).toContain('<template #actions>')
      expect(source, file).not.toContain('mobile-hidden')
      expect(pageHeaderOpenTag, file).not.toContain('actions-position="start"')
    }
  })

  it('共享 PageHeader 保持 actions 的桌面垂直居中并按 actions-position 控制移动端间距', () => {
    const pageHeaderSource = readFileSync(resolve('src/components/common/PageHeader.vue'), 'utf8')
    expect(pageHeaderSource).not.toContain('management-page-header')
    expect(extractRule(pageHeaderSource, '.qms-page-header__top')).toContain('align-items: center;')
    expect(
      extractRule(pageHeaderSource, '.qms-page-header--actions-start .qms-page-header__top'),
    ).toContain('align-items: flex-start;')
    expect(pageHeaderSource).not.toMatch(
      /\.qms-page-header--actions-end \.qms-page-header__actions\s*\{\s*padding-top:\s*16px;/,
    )
    expect(
      extractRule(pageHeaderSource, '.qms-page-header--actions-start .qms-page-header__actions'),
    ).not.toContain('padding-top: 16px;')

    const mobileStyles = extractMediaBlock(pageHeaderSource, '@media (max-width: 768px)')
    expect(
      extractRule(mobileStyles, '.qms-page-header--actions-end .qms-page-header__actions'),
    ).toContain('padding-top: 0;')
    expect(
      extractRule(mobileStyles, '.qms-page-header--actions-start .qms-page-header__actions'),
    ).toContain('padding-top: 0;')
  })

  it('AppApiKeys 和 LoginSessions 的 action-bar 保留 gap 并清除相邻按钮默认左边距', () => {
    for (const file of ['AppApiKeys.vue', 'user-settings/LoginSessions.vue']) {
      const source = readFileSync(resolve(`src/components/${file}`), 'utf8')

      expect(extractRule(source, '.action-bar'), file).toMatch(/gap:\s*\d+px;/)
      expect(extractRule(source, '.action-bar :deep(.el-button + .el-button)'), file).toContain(
        'margin-left: 0;',
      )
    }
  })
})
