import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppThreadSettings from '@/components/AppThreadSettings.vue'
import { SERVER_URL } from '@/const'
import { httpKey } from '@/http/client'

const existingSettings = {
  download_threads: 2,
  file_detail_threads: 4,
  openlist_qps: 3,
  openlist_retry: 2,
  openlist_retry_delay: 45,
  file_list_page_size: 1000,
  url_validity_check_enabled: 0,
  url_validity_check_timeout_seconds: 8,
  upload_rapid_wait_enabled: 1,
  upload_rapid_wait_timeout_seconds: 120,
  upload_rapid_wait_interval_seconds: 30,
  upload_rapid_wait_min_size: 100 * 1024 * 1024,
  upload_rapid_wait_force_size: 500 * 1024 * 1024,
  upload_rapid_wait_skip_upload: 1,
}

const mountSettings = async (uploadSettings: Record<string, unknown> = {}) => {
  const http = {
    get: vi.fn().mockResolvedValue({
      data: { code: 200, data: { ...existingSettings, ...uploadSettings } },
    }),
    post: vi.fn().mockResolvedValue({ data: { code: 200 } }),
  }
  const wrapper = mount(AppThreadSettings, {
    global: {
      provide: { [httpKey]: http },
      stubs: {
        PageHeader: true,
        ElForm: { template: '<form><slot /></form>' },
        ElFormItem: {
          props: ['label', 'prop'],
          template: '<label :data-field="prop"><span>{{ label }}</span><slot /></label>',
        },
        ElInputNumber: {
          name: 'ElInputNumber',
          props: ['modelValue', 'min', 'max', 'step', 'precision', 'disabled'],
          template: `<input type="number" :value="modelValue" :min="min" :max="max" :step="step" :disabled="disabled"
            @input="$emit('update:modelValue', $event.target.value === '' ? undefined : Number($event.target.value))" />`,
        },
        ElSwitch: true,
        ElDivider: true,
        ElButton: {
          props: ['loading'],
          template: '<button type="button" :disabled="loading"><slot /></button>',
        },
        ElAlert: {
          props: ['title', 'description'],
          template:
            '<section role="alert"><strong>{{ title }}</strong><p>{{ description }}</p></section>',
        },
      },
    },
  })
  await flushPromises()
  const input = wrapper.get('[data-field="uploadThreads"] input')
  const save = async () => {
    await wrapper.get('button').trigger('click')
    await flushPromises()
  }
  return { wrapper, http, input, save }
}

enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.useFakeTimers()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('AppThreadSettings 同时上传任务数', () => {
  it.each([
    { settings: { upload_threads: 3 }, expected: '3' },
    { settings: {}, expected: '1' },
  ])('加载配置 $settings 时显示 $expected', async ({ settings, expected }) => {
    const { http, input } = await mountSettings(settings)

    expect(http.get).toHaveBeenCalledExactlyOnceWith(`${SERVER_URL}/setting/threads`)
    expect((input.element as HTMLInputElement).value).toBe(expected)
    expect(input.attributes('min')).toBe('1')
    expect(input.attributes('max')).toBe('10')
    expect(input.attributes('step')).toBe('1')
  })

  it.each([1, 10])('保存边界值 %s，同时保留其他配置', async (value) => {
    const { wrapper, http, input, save } = await mountSettings({ upload_threads: 3 })

    await input.setValue(String(value))
    await save()

    expect(http.post).toHaveBeenCalledExactlyOnceWith(`${SERVER_URL}/setting/threads`, {
      ...existingSettings,
      upload_threads: value,
    })
    expect(wrapper.get('.save-status').text()).toContain('保存成功')
  })

  it.each(['', '0', '11', '1.5'])('保存前拒绝输入 "%s"', async (value) => {
    const { wrapper, http, input, save } = await mountSettings()

    await input.setValue(value)
    await save()

    expect(http.post).not.toHaveBeenCalled()
    expect(wrapper.get('.save-status').text()).toContain('同时上传任务数必须是 1 到 10 的整数')
  })

  it.each([null, undefined])('保存前拒绝空值 %s', async (value) => {
    const { wrapper, http, save } = await mountSettings()
    wrapper
      .get('[data-field="uploadThreads"]')
      .getComponent({ name: 'ElInputNumber' })
      .vm.$emit('update:modelValue', value)
    await save()

    expect(http.post).not.toHaveBeenCalled()
    expect(wrapper.get('.save-status').text()).toContain('同时上传任务数必须是 1 到 10 的整数')
  })

  it('显示后端保存失败的原因，并允许重新保存', async () => {
    const { wrapper, http, save } = await mountSettings({ upload_threads: 3 })
    http.post.mockResolvedValueOnce({ data: { code: 400, message: '设置写入失败' } })

    await save()

    expect(wrapper.get('.save-status').text()).toContain('保存失败')
    expect(wrapper.get('.save-status').text()).toContain('设置写入失败')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
    await save()
    expect(http.post).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.save-status').text()).toContain('保存成功')
  })
})
