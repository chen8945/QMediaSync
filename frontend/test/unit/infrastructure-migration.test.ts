import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const readSource = (path: string) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')

const deviceTypeConsumerPaths = [
  '../../src/App.vue',
  '../../src/views/accounts/AppCloudAccounts.vue',
  '../../src/views/queues/AppDownloadQueue.vue',
  '../../src/views/scrape/AppScrapePathForm.vue',
  '../../src/views/scrape/AppScrapeRecords.vue',
  '../../src/views/sync/AppSyncDirectoryForm.vue',
  '../../src/views/sync/AppSyncRecords.vue',
  '../../src/views/queues/AppUploadQueue.vue',
]

describe('基础设施迁移', () => {
  it('应用与路由都通过 HTTP 客户端模块使用默认客户端', () => {
    for (const path of ['../../src/App.vue', '../../src/router/index.ts']) {
      const source = readSource(path)

      expect(source).toContain("from '@/http/client'")
      expect(source).not.toContain("from 'axios'")
    }
  })

  it('所有设备类型订阅者统一使用 useDeviceType', () => {
    for (const path of deviceTypeConsumerPaths) {
      const source = readSource(path)

      expect(source).toContain("from '@/composables/useDeviceType'")
      expect(source).toContain('useDeviceType()')
      expect(source).not.toContain('onDeviceTypeChange')
    }
  })
})
