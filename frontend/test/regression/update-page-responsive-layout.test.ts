import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

describe('版本更新页移动端布局', () => {
  it('当前版本图标与文字保持水平，版本标志不受标题换行影响', () => {
    const source = readFileSync(resolve('src/components/AppUpdate.vue'), 'utf8')
    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')

    expect(source).toContain('<div class="section-header section-header--current">')
    expect(source).toContain('<div class="section-header-left">')
    expect(extractRule(mobileStyles, '.section-header--current')).toContain('flex-direction: row;')
    expect(extractRule(mobileStyles, '.section-header--current .section-header-left')).toContain(
      'width: auto;',
    )
    expect(
      extractRule(mobileStyles, '.update-collapse :deep(.el-collapse-item__header)'),
    ).toContain('height: auto;')
    expect(
      extractRule(mobileStyles, '.update-collapse :deep(.el-collapse-item__header)'),
    ).toContain('min-height: 56px;')
    expect(extractRule(mobileStyles, '.update-title-row')).toContain('align-items: center;')
    expect(extractRule(source, '.update-tags')).toContain('flex: 0 0 auto;')
  })
})
