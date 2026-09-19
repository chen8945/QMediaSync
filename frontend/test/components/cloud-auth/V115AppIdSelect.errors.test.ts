// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import axios, { CanceledError, type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import V115AppIdSelect from '@/components/cloud-auth/V115AppIdSelect.vue'
import { httpKey } from '@/http/client'
import { markAuthInvalidationHandled } from '@/http/errors'

const mountSelect = (get: AxiosInstance['get']) => {
  const http = axios.create()
  http.get = get
  return mount(V115AppIdSelect, {
    props: {
      selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
      customAppId: '',
      customAppName: '',
    },
    global: {
      provide: { [httpKey]: http },
      stubs: {
        ElFormItem: {
          props: ['label', 'error'],
          template:
            '<div><label>{{ label }}</label><slot /><p v-if="error" role="alert">{{ error }}</p></div>',
        },
        ElSelect: {
          props: ['remoteMethod'],
          template:
            '<div><input aria-label="搜索 APP ID" @input="remoteMethod($event.target.value)" /><slot /><slot name="footer" /></div>',
        },
        ElOption: { props: ['label'], template: '<span>{{ label }}</span>' },
        ElButton: { template: '<button><slot /></button>' },
      },
    },
  })
}

describe('APP ID 选择器请求错误', () => {
  it('业务失败就地显示安全文案，重试成功后恢复结果', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce({
        data: { code: 500, message: '搜索服务繁忙', data: null },
      })
      .mockResolvedValueOnce({
        data: {
          code: 200,
          data: {
            items: [{ app_id: '1001', app_name: '测试应用', display_name: '测试应用' }],
            total: 1,
          },
        },
      })
    const wrapper = mountSelect(get)
    await wrapper.get('input').setValue('测试')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('搜索服务繁忙')
    expect(wrapper.emitted('update:selectedQrApp')).toBeUndefined()
    await wrapper.get('input').setValue('测试应用')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('测试应用')
    wrapper.unmount()
  })

  it('取消和已经处理的 401 不再显示错误', async () => {
    const handled = { response: { status: 401, data: { code: 500, message: 'private-token' } } }
    markAuthInvalidationHandled(handled)
    const wrapper = mountSelect(
      vi.fn().mockRejectedValueOnce(new CanceledError()).mockRejectedValueOnce(handled),
    )
    await wrapper.get('input').setValue('第一次')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('input').setValue('第二次')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
