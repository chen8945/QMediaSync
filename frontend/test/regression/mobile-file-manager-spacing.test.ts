import { describe, expect, test } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { extractMediaBlock, extractRule } from '../support/css'

const appVue = readFileSync(resolve(__dirname, '../../src/App.vue'), 'utf8')
const fileManagerVue = readFileSync(
  resolve(__dirname, '../../src/components/AppFileManager.vue'),
  'utf8',
)

function getMarginBottom(block: string) {
  const margin = block.match(/margin:\s*([^;]+);/)
  expect(margin).toBeTruthy()

  const parts = margin?.[1].trim().split(/\s+/) ?? []
  expect(parts).toHaveLength(4)

  const bottom = parts[2].match(/^(-?\d+)px$/)
  expect(bottom).toBeTruthy()

  return Number(bottom?.[1] ?? 0)
}

function getPaddingTop(block: string) {
  const paddingTop = block.match(/padding-top:\s*(-?\d+)(?:px)?(?:\s*!important)?;/)
  if (paddingTop) {
    return Number(paddingTop[1])
  }

  const padding = block.match(/padding:\s*([^;]+);/)
  expect(padding).toBeTruthy()

  const parts = padding?.[1].trim().split(/\s+/) ?? []
  const top = parts[0]?.match(/^(-?\d+)(?:px)?$/)
  expect(top).toBeTruthy()

  return Number(top?.[1] ?? 0)
}

function getBoxTop(block: string, property: 'margin' | 'padding') {
  const longhand = block.match(
    new RegExp(`${property}-top:\\s*(-?\\d+)(?:px)?(?:\\s*!important)?;`),
  )
  if (longhand) {
    return Number(longhand[1])
  }

  const declaration = block.match(new RegExp(`${property}:\\s*([^;!]+)(?:\\s*!important)?;`))
  expect(declaration).toBeTruthy()

  const parts = declaration?.[1].trim().split(/\s+/) ?? []
  const top = parts[0]?.match(/^(-?\d+)(?:px)?$/)
  expect(top).toBeTruthy()

  return Number(top?.[1] ?? 0)
}

function getBoxBottom(block: string, property: 'margin' | 'padding') {
  const longhand = block.match(
    new RegExp(`${property}-bottom:\\s*(-?\\d+)(?:px)?(?:\\s*!important)?;`),
  )
  if (longhand) {
    return Number(longhand[1])
  }

  const declaration = block.match(new RegExp(`${property}:\\s*([^;!]+)(?:\\s*!important)?;`))
  expect(declaration).toBeTruthy()

  const parts = declaration?.[1].trim().split(/\s+/) ?? []
  const bottom = (parts[2] ?? parts[0])?.match(/^(-?\d+)(?:px)?$/)
  expect(bottom).toBeTruthy()

  return Number(bottom?.[1] ?? 0)
}

describe('移动端文件管理器顶部间距', () => {
  test('移动端顶部栏和首个内容之间不保留大块空白', () => {
    const mobileBlock = extractMediaBlock(appVue, '@media (max-width: 768px)')
    const mobileHeaderBlock = extractRule(mobileBlock, '.mobile-header')

    expect(getMarginBottom(mobileHeaderBlock)).toBeLessThanOrEqual(8)
  })

  test('小屏移动端顶部栏和首个内容之间不保留大块空白', () => {
    const smallMobileBlock = extractMediaBlock(appVue, '@media (max-width: 480px)')
    const mobileHeaderBlock = extractRule(smallMobileBlock, '.mobile-header')

    expect(getMarginBottom(mobileHeaderBlock)).toBeLessThanOrEqual(8)
  })

  test('移动端隐藏卡片头后卡片内容顶部 padding 不继续撑开空白', () => {
    const mobileBlock = extractMediaBlock(fileManagerVue, '@media (max-width: 768px)')
    const cardBodyMatch = mobileBlock.match(
      /\.file-manager-container\s+:deep\(\.el-card__body\)\s*\{([^}]+)\}/,
    )

    expect(cardBodyMatch).toBeTruthy()
    expect(getPaddingTop(cardBodyMatch?.[1] ?? '')).toBeLessThanOrEqual(4)
  })

  test('移动端文件管理容器抵消全宽容器的默认顶部留白', () => {
    const mobileBlock = extractMediaBlock(fileManagerVue, '@media (max-width: 768px)')
    const containerMatch = mobileBlock.match(
      /\.file-manager-container\.full-width-container\s*\{([^}]+)\}/,
    )

    expect(containerMatch).toBeTruthy()

    const block = containerMatch?.[1] ?? ''
    expect(getBoxTop(block, 'margin') + getBoxTop(block, 'padding')).toBeLessThanOrEqual(-8)
  })

  test('移动端文件管理容器底部留白跟随顶部收敛', () => {
    const mobileBlock = extractMediaBlock(fileManagerVue, '@media (max-width: 768px)')
    const containerMatch = mobileBlock.match(
      /\.file-manager-container\.full-width-container\s*\{([^}]+)\}/,
    )

    expect(containerMatch).toBeTruthy()

    const block = containerMatch?.[1] ?? ''
    expect(getBoxBottom(block, 'padding')).toBeLessThanOrEqual(12)
  })

  test('移动端通过账号标题旁的信息入口查看页面说明', () => {
    expect(fileManagerVue).toContain('class="mobile-file-manager-info show-on-mobile"')
    expect(fileManagerVue).toContain('浏览和管理媒体文件，支持 STRM 生成、刮削整理和 ED2K 生成操作')
    expect(fileManagerVue).toContain('InfoFilled')
  })
})
