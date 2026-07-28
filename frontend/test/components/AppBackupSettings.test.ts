// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { ElMessage, ElMessageBox } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppBackupSettings from '@/components/AppBackupSettings.vue'
import { httpKey } from '@/http/client'

const backupConfig = {
  id: 1,
  backup_enabled: 0 as const,
  backup_cron: '0 3 * * *',
  backup_retention: 7,
  backup_max_count: 10,
  backup_encryption_enabled: false,
  created_at: 0,
  updated_at: 0,
}

const globalStubs = {
  ElButton: {
    props: ['disabled', 'loading'],
    emits: ['click'],
    template:
      '<button type="button" role="button" :disabled="disabled || loading" @click="$emit(\'click\')"><slot /></button>',
  },
  ElForm: { template: '<form><slot /></form>' },
  ElFormItem: { template: '<div><slot /></div>' },
  ElIcon: { template: '<span><slot /></span>' },
  ElInput: {
    props: ['modelValue'],
    emits: ['update:modelValue'],
    template:
      '<input :aria-label="$attrs[\'aria-label\']" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
  ElInputNumber: { template: '<input type="number" />' },
  ElSwitch: {
    props: ['modelValue', 'activeValue', 'inactiveValue'],
    emits: ['update:modelValue'],
    template:
      '<input role="switch" aria-label="启用自动备份" type="checkbox" :checked="modelValue === activeValue" @change="$emit(\'update:modelValue\', $event.target.checked ? activeValue : inactiveValue)" />',
  },
  ElTag: { template: '<span><slot /></span>' },
  CronSelector: { template: '<div />' },
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AppBackupSettings', () => {
  it('维护屏障返回 503 时展示服务端消息，而不是使用默认配置', async () => {
    const message = vi.spyOn(ElMessage, 'error').mockReturnValue({ close: vi.fn() } as never)
    const http = {
      get: vi.fn().mockResolvedValue({
        status: 503,
        data: { code: 400, message: '系统正在备份或恢复，暂时无法处理请求' },
      }),
    }

    mount(AppBackupSettings, {
      global: {
        directives: { loading: {} },
        provide: { [httpKey]: http },
        stubs: globalStubs,
      },
    })
    await flushPromises()

    expect(message).toHaveBeenCalledWith('系统正在备份或恢复，暂时无法处理请求')
  })

  it('保存启用的无密码定时备份前确认风险，并提交一次性确认字段', async () => {
    const http = {
      get: vi.fn().mockImplementation((url: string) => {
        if (url === '/api/backup/config') {
          return Promise.resolve({ data: { code: 200, data: backupConfig } })
        }

        return Promise.resolve({ data: { code: 200, data: [] } })
      }),
      put: vi.fn().mockResolvedValue({ data: { code: 200 } }),
    }
    const confirm = vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue(undefined as never)
    const wrapper = mount(AppBackupSettings, {
      global: {
        directives: { loading: {} },
        provide: { [httpKey]: http },
        stubs: globalStubs,
      },
    })

    await flushPromises()

    const automaticBackupSwitch = wrapper.get('[role="switch"][aria-label="启用自动备份"]')
    expect((automaticBackupSwitch.element as HTMLInputElement).checked).toBe(false)

    await automaticBackupSwitch.setValue(true)
    expect((automaticBackupSwitch.element as HTMLInputElement).checked).toBe(true)

    const saveButton = wrapper.get('[role="button"]')
    expect(saveButton.text()).toBe('保存配置')
    await saveButton.trigger('click')
    await flushPromises()

    expect(confirm).toHaveBeenCalledWith(
      '无密码定时备份不会加密，备份可能包含 TLS 私钥。是否继续保存？',
      '确认未加密定时备份',
      expect.objectContaining({
        confirmButtonText: '继续保存',
        cancelButtonText: '取消',
        type: 'warning',
      }),
    )
    expect(http.put).toHaveBeenCalledWith(
      '/api/backup/config',
      expect.objectContaining({
        backup_enabled: 1,
        confirm_unencrypted: true,
      }),
      expect.objectContaining({
        validateStatus: expect.any(Function),
      }),
    )
  })
})
