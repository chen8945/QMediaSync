// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const readSource = (path: string) => readFileSync(resolve(process.cwd(), path), 'utf8')

describe('刮削记录页面布局与 Tooltip', () => {
  it('沿用队列页面的标题层级，并把临时文件说明放在标题下方', () => {
    const source = readSource('src/components/AppScrapeRecords.vue')

    expect(source.indexOf('class="card-header"')).toBeGreaterThan(-1)
    expect(source.indexOf('class="card-header"')).toBeLessThan(
      source.indexOf('class="top-actions"'),
    )
    expect(source).toMatch(
      /<div class="card-header">\s*<div>\s*<h2 class="hide-on-mobile">刮削记录<\/h2>\s*<p class="queue-description">/,
    )
    expect(source).toContain('刮削时会把临时文件放在')
    expect(source).toContain('刮削完成后会自动删除')
    expect(source).not.toContain('class="page-title hide-on-mobile"')
    expect(source).not.toMatch(/\.page-title\s*\{/)
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
