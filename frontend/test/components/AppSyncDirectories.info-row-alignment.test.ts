import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

const source = readFileSync(resolve('src/components/AppSyncDirectories.vue'), 'utf8')

describe('AppSyncDirectories 信息行对齐', () => {
  test('桌面端信息行内容按 32px 图标高度垂直居中', () => {
    expect(extractRule(source, '.info-row')).toContain('align-items: center;')
    expect(extractRule(source, '.info-content')).toContain('min-height: 32px;')
  })

  test('移动端信息行内容按 28px 图标高度垂直居中', () => {
    const mobileBlock = extractMediaBlock(source, '@media (max-width: 768px)')

    expect(extractRule(mobileBlock, '.info-content')).toContain('min-height: 28px;')
  })
})
