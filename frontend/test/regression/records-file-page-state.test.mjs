import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const readSource = (path) => readFileSync(resolve(frontendRoot, path), 'utf8')

const expectRecordTableContract = (source, pageKey, rowKey, columns, actions) => {
  expect(source).toMatch(new RegExp(`getPageState\\s*\\(\\s*['"]${pageKey}['"]`))
  expect(source).toContain('ResponsiveRecordTable')
  expect(source).toMatch(new RegExp(`:columns\\s*=\\s*["']${columns}["']`))
  expect(source).toMatch(new RegExp(`:actions\\s*=\\s*["']${actions}["']`))
  expect(source).toMatch(new RegExp(`:row-key\\s*=\\s*["']${rowKey}["']`))
  expect(source).toMatch(/:loading\s*=\s*["']initialLoading\s*\|\|\s*queryLoading["']/)
  expect(source).toMatch(/:expanded-row-keys\s*=\s*["']pageState\.expandedRowKeys["']/)
  expect(source).toMatch(/@expand-change\s*=\s*["']handleExpandChange["']/)
  expect(source).toContain('createActiveRequestGate')
}

describe('记录与文件页的公共状态契约', () => {
  it('同步记录通过响应式记录表公开稳定行键、展开状态和失败详情', () => {
    const source = readSource('src/views/sync/AppSyncRecords.vue')

    expectRecordTableContract(
      source,
      'sync-records',
      'getSyncRecordRowKey',
      'syncRecordColumns',
      'syncRecordActions',
    )
    expect(source).toMatch(/const\s+getSyncRecordRowKey\s*=\s*\(row:\s*SyncRecord\)\s*=>\s*row\.id/)
    expect(source).toMatch(
      /key:\s*['"]fail_reason['"][\s\S]*?value:\s*\(row\)\s*=>\s*row\.fail_reason\s*\|\|\s*['"]-['"]/,
    )
    expect(source).toMatch(
      /setExpandedRowKeys\s*\(\s*['"]sync-records['"][\s\S]*?String\s*\(\s*item\.id\s*\)/,
    )
  })

  it('刮削记录通过响应式记录表保留筛选、展开和请求上下文', () => {
    const source = readSource('src/views/scrape/AppScrapeRecords.vue')

    expectRecordTableContract(
      source,
      'scrape-records',
      'getScrapeRecordRowKey',
      'scrapeRecordColumns',
      'scrapeRecordActions',
    )
    expect(source).toMatch(/setFilter\s*\(\s*['"]scrape-records['"]\s*,\s*['"]status['"]/)
    expect(source).toMatch(
      /setExpandedRowKeys\s*\(\s*['"]scrape-records['"][\s\S]*?String\s*\(\s*item\.id\s*\)/,
    )
  })

  it('文件管理器保留受控展开、页面状态和失活时的请求取消', () => {
    const source = readSource('src/views/files/AppFileManager.vue')

    expect(source).toMatch(/getPageState\s*\(\s*['"]file-manager['"]/)
    expect(source).toMatch(/createActiveRequestGate/)
    expect(source).toMatch(
      /:expand-row-keys\s*=\s*["']pageState\.expandedRowKeys["'][\s\S]*?@expand-change\s*=\s*["']handleExpandChange["']/,
    )
    expect(source).toMatch(
      /const\s+handleExpandChange\s*=\s*\(\s*row:\s*FileSystemItem\s*,\s*expandedRows:\s*FileSystemItem\[\]\s*\)[\s\S]*?setExpandedRowKeys\s*\(\s*['"]file-manager['"][\s\S]*?String\s*\(\s*item\.id\s*\|\|\s*item\.path\s*\)/,
    )
    expect(source).toMatch(
      /function\s+deactivateFileManagerPage\s*\(\s*\)[\s\S]*?accountListRequestGate\.invalidate\s*\(\s*\)[\s\S]*?fileListRequestGate\.invalidate\s*\(\s*\)/,
    )
  })
})
