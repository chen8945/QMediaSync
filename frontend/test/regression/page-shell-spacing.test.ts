import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

const readSource = (path: string) => readFileSync(resolve(path), 'utf8')

const sharedRootSelectors = [
  '.main-content > .main-content-container:not(.full-width-container)',
  '.main-content > .cloud-accounts-page',
  '.main-content > .sync-directories-page',
  '.main-content > .scrape-pathes-page',
] as const

const targetRootRule = (styles: string) => {
  const selectorPattern = sharedRootSelectors.map((selector) =>
    selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'),
  )
  const match = styles.match(new RegExp(`${selectorPattern.join('\\s*,\\s*')}\\s*\\{([^}]+)\\}`))
  expect(match).toBeTruthy()
  return match?.[1] ?? ''
}

function getBoxTop(block: string, property: 'margin' | 'padding') {
  const longhand = block.match(new RegExp(`${property}-top:\\s*(-?\\d+)px(?:\\s*!important)?;`))
  if (longhand) {
    return Number(longhand[1])
  }

  const shorthand = block.match(new RegExp(property + ':\\s*([^;]+);'))
  expect(shorthand).toBeTruthy()

  const top = shorthand?.[1]
    .trim()
    .split(/\s+/)[0]
    ?.match(/^(-?\d+)px$/)
  expect(top).toBeTruthy()

  return Number(top?.[1] ?? 0)
}

describe('页面 shell 与用户管理页间距契约', () => {
  it('共享页面根容器统一保留标题顶部留白', () => {
    const source = readSource('src/assets/main.css')

    expect(targetRootRule(source)).toContain('padding-top: 20px !important;')

    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
    expect(targetRootRule(mobileStyles)).toContain('padding-top: 10px !important;')
  })

  it('二级分类设置和版本更新页面跟随共享页面标题顶部留白', () => {
    const categorySource = readSource('src/components/AppCategoryStrategy.vue')
    const updateSource = readSource('src/components/AppUpdate.vue')

    expect(
      extractRule(categorySource, '.category-strategy-container.full-width-container'),
    ).toContain('margin-top: 0 !important;')
    expect(extractRule(updateSource, '.update-page')).toContain('padding: 20px 0 0;')

    const categoryMobileStyles = extractMediaBlock(categorySource, '@media (max-width: 768px)')
    const updateMobileStyles = extractMediaBlock(updateSource, '@media (max-width: 768px)')
    expect(
      extractRule(categoryMobileStyles, '.category-strategy-container.full-width-container'),
    ).toContain('padding-top: 10px !important;')
    expect(extractRule(updateMobileStyles, '.update-page')).toContain('padding-top: 10px;')
  })

  it('锁定桌面、平板和移动端 shell 的顶部与水平 padding', () => {
    const source = readSource('src/App.vue')

    expect(extractRule(source, '.main-content')).toContain('padding: 20px;')

    const tabletStyles = extractMediaBlock(
      source,
      '@media (min-width: 769px) and (max-width: 1024px)',
    )
    expect(extractRule(tabletStyles, '.main-content')).toContain('padding: 15px;')

    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
    const mobileContent = extractRule(mobileStyles, '.main-content')
    const mobileHeader = extractRule(mobileStyles, '.mobile-header')
    const mobileShellPaddingTop = getBoxTop(mobileContent, 'padding')
    const mobileHeaderMarginTop = getBoxTop(mobileHeader, 'margin')

    expect(mobileContent).toContain('padding: 10px;')
    expect(mobileShellPaddingTop).toBe(10)
    expect(mobileHeaderMarginTop).toBe(-mobileShellPaddingTop)

    const narrowStyles = extractMediaBlock(source, '@media (max-width: 480px)')
    const narrowContent = extractRule(narrowStyles, '.main-content')
    const narrowHeader = extractRule(narrowStyles, '.mobile-header')
    const narrowShellPaddingTop = getBoxTop(narrowContent, 'padding')
    const narrowHeaderMarginTop = getBoxTop(narrowHeader, 'margin')

    expect(narrowContent).toContain('padding: 8px;')
    expect(narrowShellPaddingTop).toBe(8)
    expect(narrowHeaderMarginTop).toBe(-narrowShellPaddingTop)
  })

  it('短屏横屏保留紧凑垂直空间并精确对齐顶部栏', () => {
    const source = readSource('src/assets/main.css')
    const shortLandscapeStyles = extractMediaBlock(
      source,
      '@media (max-width: 768px) and (max-height: 500px) and (orientation: landscape)',
    )
    const mainContent = extractRule(shortLandscapeStyles, '.main-content')
    const mobileHeader = extractRule(shortLandscapeStyles, '.mobile-header')

    const mainContentPaddingTop = getBoxTop(mainContent, 'padding')
    const mobileHeaderMarginTop = getBoxTop(mobileHeader, 'margin')

    expect(mainContentPaddingTop).toBe(8)
    expect(mobileHeaderMarginTop).toBe(-mainContentPaddingTop)
  })

  it('仅在桌面端移除用户管理表单上方的额外间距并保留操作区位置规则', () => {
    const source = readSource('src/components/AppUserSettings.vue')

    expect(extractRule(source, '.user-form')).toContain('margin-top: 0;')
    expect(extractRule(source, '.form-actions')).toContain('margin-top: 20px;')

    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')
    expect(extractRule(mobileStyles, '.user-form')).toContain('margin-top: 20px;')
  })
})
