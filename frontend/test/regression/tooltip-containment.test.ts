// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { extractRule } from '../support/css'

const readSource = (path: string) => readFileSync(resolve(process.cwd(), path), 'utf8')

describe('长文本 Tooltip 容器约束', () => {
  it('共享记录表统一配置容器内的溢出 Tooltip', () => {
    const source = readSource('src/components/records/ResponsiveRecordTable.vue')

    expect(source).toContain(
      ":tooltip-options=\"{ popperClass: 'qms-contained-tooltip', strategy: 'fixed' }\"",
    )
  })

  it('共享长文本样式保持换行并移除 Element Plus 的旧全局覆盖', () => {
    const source = readSource('src/assets/main.css')
    const tooltipRule = extractRule(source, '.qms-contained-tooltip')

    expect(tooltipRule).toContain('max-width: 90%;')
    expect(tooltipRule).toContain('white-space: pre-line;')
    expect(tooltipRule).toContain('overflow-wrap: anywhere;')
    expect(tooltipRule).not.toContain('box-sizing:')
    expect(source).not.toMatch(
      /\.queue-(?:upload-detail-tooltip|summary-tooltip|status-error-tooltip)/,
    )
  })

  it('页面动态说明使用稳定的 Tooltip 挂载目标', () => {
    const cloudSource = readSource('src/components/AppCloudAccounts.vue')
    const downloadSource = readSource('src/components/AppDownloadQueue.vue')
    const uploadSource = readSource('src/components/AppUploadQueue.vue')
    const scrapeSource = readSource('src/components/AppScrapeRecords.vue')
    const syncSource = readSource('src/components/AppSyncDirectories.vue')

    for (const source of [cloudSource, downloadSource, uploadSource, syncSource]) {
      expect(source).toContain('popper-class="qms-contained-tooltip"')
      expect(source).toContain('append-to="body"')
      expect(source).not.toMatch(/append-to="\.[^"]+"/)
    }

    expect(cloudSource).toMatch(/\.cloud-accounts-page\s*\{[\s\S]*position:\s*relative;/)

    expect(scrapeSource).toContain('ref="pageContainerRef"')
    expect(scrapeSource).toContain(':append-to="pageContainerRef ?? undefined"')
    expect(scrapeSource).not.toContain('append-to=".scrape-records-container"')
    expect(scrapeSource).toContain('popper-class="qms-contained-tooltip"')
    expect(scrapeSource).toMatch(/\.scrape-records-container\s*\{[\s\S]*position:\s*relative;/)

    expect(syncSource).toMatch(/\.sync-directories-page\s*\{[\s\S]*position:\s*relative;/)
  })

  it('登录设备 UA 复用容器内的表格 Tooltip', () => {
    const source = readSource('src/components/user-settings/LoginSessions.vue')

    expect(source).toContain(
      ":show-overflow-tooltip=\"{ popperClass: 'qms-contained-tooltip', strategy: 'fixed' }\"",
    )
  })

  it('目录选择器和文件管理选择不添加路径 Tooltip', () => {
    const treeSource = readSource('src/components/TreeNode.vue')
    const selectorSource = readSource('src/components/DirectorySelector.vue')

    expect(treeSource).not.toContain('<el-tooltip')
    expect(treeSource).not.toContain('qms-contained-tooltip')
    expect(treeSource).not.toContain('tooltipAppendTo')
    expect(treeSource).not.toContain(':title="node.path || node.name"')
    expect(selectorSource).not.toContain('tooltip-append-to')
    expect(selectorSource).not.toContain('selectorContainer')
  })
})
