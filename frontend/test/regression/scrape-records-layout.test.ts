// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const readSource = (path: string) => readFileSync(resolve(process.cwd(), path), 'utf8')

describe('刮削记录页面布局与 Tooltip', () => {
  it('使用共享页面头部，并把批量操作放在页面头部之后', () => {
    const source = readSource('src/components/AppScrapeRecords.vue')
    const pageHeaderStart = source.indexOf('<PageHeader')
    const topActionsStart = source.indexOf('class="top-actions"')

    expect(pageHeaderStart).toBeGreaterThan(-1)
    expect(pageHeaderStart).toBeLessThan(topActionsStart)
    expect(source).toContain('<PageHeader class="scrape-records-page-header" />')
    expect(source).not.toContain('class="hide-on-mobile"')
    expect(source).toContain("import PageHeader from '@/components/common/PageHeader.vue'")

    expect(source).toMatch(
      /\.top-actions\s*\{[\s\S]*?padding: 16px;[\s\S]*?background: linear-gradient\([\s\S]*?border-radius: 8px;/,
    )
    expect(source).toMatch(
      /\.scrape-records-container :deep\(\.scrape-records-page-header\)\s*\{[\s\S]*?margin-bottom: 0;/,
    )
  })

  it('路径 Tooltip 显式保留“到”关系词，并为缺失路径使用占位符', () => {
    const source = readSource('src/components/AppScrapeRecords.vue')
    const pathTooltipStart = source.indexOf('const getPathTooltip')
    const nextHelperStart = source.indexOf('// 获取类型标签类型', pathTooltipStart)
    const pathTooltipSource = source.slice(pathTooltipStart, nextHelperStart)

    expect(source).toContain(':content="getPathTooltip(row)"')
    expect(pathTooltipSource).toContain("'到'")
    expect(pathTooltipSource).toContain("join('\\n')")
    expect(pathTooltipSource).toContain("row.source_full_path || '-'")
    expect(pathTooltipSource).toContain("row.dest_full_path || '-'")
    expect(pathTooltipSource).not.toContain('getRenameTypeName')
    expect(source).toMatch(/key:\s*['"]path['"][\s\S]*?showOverflowTooltip:\s*false/)
  })

  it('文件状态列使用固定宽度并统一居中，所有状态复用同一个渲染分支', () => {
    const source = readSource('src/components/AppScrapeRecords.vue')
    const statusTemplateStart = source.indexOf('<template #cell-status')
    const nextTemplateStart = source.indexOf('<template #cell-type', statusTemplateStart)
    const statusTemplateSource = source.slice(statusTemplateStart, nextTemplateStart)

    expect(source).toMatch(
      /key:\s*['"]status['"][\s\S]*?width:\s*132[\s\S]*?align:\s*['"]center['"][\s\S]*?detailField:/,
    )
    expect(source).toMatch(
      /<template #cell-status="\{ row \}">[\s\S]*?getStatusName\(row\.status\)/,
    )
    expect(source).toMatch(
      /\.scrape-status-tag\s*\{[\s\S]*?width:\s*84px;[\s\S]*?justify-content:\s*center;/,
    )
    expect(statusTemplateSource).not.toContain('<Warning />')
    expect(source).not.toMatch(/v-if="row\.status\s*===|v-else-if="row\.status\s*===/)
  })
})
