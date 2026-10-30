// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import AppUserSettings from '@/views/settings/AppUserSettings.vue'
import { httpKey } from '@/http/client'
import { useAuthStore } from '@/stores/auth'

const createWrapper = async () => {
  const pinia = createPinia()
  const authStore = useAuthStore(pinia)
  authStore.login({
    user: { id: '1', username: 'admin', role: 'admin' },
    csrfToken: 'csrf-token',
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  await router.isReady()

  const wrapper = mount(AppUserSettings, {
    global: {
      plugins: [pinia, router],
      provide: {
        [httpKey]: { get: async () => ({ data: { code: 200 } }) },
      },
      stubs: {
        ElForm: { template: '<form><slot /></form>' },
        ElFormItem: { template: '<div><slot /></div>' },
        ElInput: {
          props: ['modelValue', 'disabled'],
          template:
            '<input :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)" />',
        },
        ElButton: {
          props: ['disabled', 'loading'],
          template: '<button :disabled="disabled || loading"><slot /></button>',
        },
        ElAlert: {
          props: ['title'],
          template: '<section><strong>{{ title }}</strong><slot /></section>',
        },
        TwoFactorSettings: { template: '<div data-test="two-factor">两步验证</div>' },
      },
    },
  })

  return wrapper
}

describe('AppUserSettings', () => {
  it('用户名和密码都未变化时禁用保存，修改用户名后允许保存', async () => {
    const wrapper = await createWrapper()
    const saveButton = wrapper.find('button')

    expect(saveButton.element.disabled).toBe(true)

    await wrapper.findAll('input')[0].setValue('newadmin')

    expect(saveButton.element.disabled).toBe(false)
  })

  it('凭据变更后的重新登录提示显示在两步验证之前', async () => {
    const wrapper = await createWrapper()
    const text = wrapper.text()

    expect(text).toContain('用户名或密码修改后，你需要重新登录')
    expect(text.indexOf('用户名或密码修改后，你需要重新登录')).toBeLessThan(
      text.indexOf('两步验证'),
    )
  })
})
