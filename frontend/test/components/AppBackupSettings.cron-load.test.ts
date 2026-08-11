// @vitest-environment happy-dom
import type { AxiosInstance } from 'axios'
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AppBackupSettings from '@/components/AppBackupSettings.vue'
import { httpKey } from '@/http/client'

describe('AppBackupSettings', () => {
  it('加载与默认值不同的 Cron 时只查询一次下次执行时间', async () => {
    const get = vi.fn(async (url: string, _config?: unknown) => {
      void _config
      if (url === '/api/backup/config') {
        return {
          data: {
            code: 200,
            data: {
              backup_enabled: 1,
              backup_cron: '0 4 * * *',
              backup_retention: 7,
              backup_max_count: 10,
              backup_compress: 1,
            },
          },
        }
      }
      return { data: { code: 200, data: ['2026-07-30 04:00:00'] } }
    })

    mount(AppBackupSettings, {
      global: {
        provide: { [httpKey]: { get } as unknown as AxiosInstance },
        stubs: {
          PageHeader: true,
          ElIcon: { template: '<span><slot /></span>' },
          ElForm: { template: '<form><slot /></form>' },
          ElFormItem: { template: '<div><slot /></div>' },
          ElSwitch: { template: '<input type="checkbox" />' },
          ElInputNumber: { template: '<input type="number" />' },
          ElButton: { template: '<button><slot /></button>' },
          ElTag: { template: '<span><slot /></span>' },
          CronSelector: { template: '<input />' },
        },
      },
    })

    await flushPromises()

    const cronRequests = get.mock.calls.filter(([url]) => url === '/api/setting/cron')
    expect(cronRequests).toHaveLength(1)
    expect(cronRequests[0]?.[1]).toEqual({ params: { cron: '0 4 * * *' } })
  })
})
