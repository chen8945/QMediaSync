// @vitest-environment happy-dom
import axios from 'axios'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { ElDialog, ElMessage } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppNotificationChannels from '@/components/AppNotificationChannels.vue'
import { httpKey } from '@/http/client'

enableAutoUnmount(afterEach)
afterEach(() => vi.restoreAllMocks())

describe('AppNotificationChannels Webhook headers', () => {
  it('创建 Webhook 渠道时提交多个自定义请求头', async () => {
    vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: '',
      headers: {},
      data: { code: 0, data: config.method === 'get' ? [] : null },
    }))
    const wrapper = mount(AppNotificationChannels, {
      global: {
        provide: { [httpKey]: axios.create({ adapter }) },
        stubs: {
          PageHeader: true,
          teleport: true,
          ElSelect: {
            props: ['modelValue'],
            template: '<select :value="modelValue"><slot /></select>',
          },
          ElOption: {
            props: ['value', 'label'],
            template: '<option :value="value">{{ label }}</option>',
          },
        },
      },
    })
    await flushPromises()
    await wrapper.get('.empty-state button').trigger('click')
    const form = wrapper
      .findAllComponents(ElDialog)
      .find((item) => item.props('title') === '添加通知渠道')!
    await form.get('.channel-type-card.type-webhook').trigger('click')
    await form.get('input[placeholder="请输入渠道显示名称"]').setValue('Webhook')
    await form
      .get('input[placeholder="https://example.com/webhook"]')
      .setValue('https://example.com/webhook')
    await form.get('textarea').setValue('{"title":"{{title}}"}')
    const headers = [
      { key: 'X-Trace-ID', value: 'trace-1' },
      { key: 'X-Webhook-Source', value: 'qmediasync' },
    ]
    for (const header of headers) {
      const add = form.findAll('button').find((item) => item.text() === '添加 Header')!
      await add.trigger('click')
      const row = form.findAll('.webhook-header-row').at(-1)!
      await row.get('input[placeholder="Header 名称"]').setValue(header.key)
      await row.get('input[placeholder="Header 值"]').setValue(header.value)
    }
    const create = form.findAll('button').find((item) => item.text() === '创建')!
    await create.trigger('click')
    await flushPromises()

    const request = adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(request.url).toBe('/api/setting/notification/channels/webhook')
    expect(JSON.parse(request.data)).toMatchObject({
      headers: { 'X-Trace-ID': 'trace-1', 'X-Webhook-Source': 'qmediasync' },
    })
  })
})
