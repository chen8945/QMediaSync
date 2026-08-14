// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import V115AuthorizationChangeDialog from '@/components/cloud-auth/V115AuthorizationChangeDialog.vue'

describe('V115AuthorizationChangeDialog', () => {
  it('显示账号关联风险并在勾选确认后提交目标来源', async () => {
    const wrapper = mount(V115AuthorizationChangeDialog, {
      props: {
        visible: true,
        account: {
          id: 42,
          name: '原账号',
          user_id: 'old-user',
          source_type: '115',
          auth_source_type: 'built_in_appid',
          auth_provider: 'official_pkce',
          app_id: '100197665',
          app_id_name: 'Q115-STRM',
        },
      },
      global: {
        stubs: {
          V115AppSelector: { template: '<div data-test="selector" />' },
          ElDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          ElAlert: { template: '<div><slot name="title" /><slot /></div>' },
          ElForm: { template: '<form><slot /></form>' },
          ElCheckbox: {
            props: ['modelValue'],
            emits: ['update:modelValue'],
            template:
              '<label><input data-test="risk-checkbox" type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', true)" /><slot /></label>',
          },
          ElButton: {
            props: ['disabled'],
            emits: ['click'],
            template: '<button :disabled="disabled" @click="$emit(\'click\')"><slot /></button>',
          },
        },
      },
    })

    expect(wrapper.text()).toContain('账号关联会保留，但云盘用户可能发生变化')
    expect(wrapper.text()).toContain('当前账号 ID：42')
    expect(wrapper.text()).toContain('旧路径或文件 ID 可能不再对应')
    expect(wrapper.text()).toContain('我已确认保留本地账号关联')

    await wrapper.get('[data-test="risk-checkbox"]').setValue(true)
    await wrapper.findAll('button').at(-1)!.trigger('click')

    expect(wrapper.emitted('confirmed')).toEqual([
      [
        {
          auth_source_type: 'built_in_appid',
          auth_provider: 'official_pkce',
          app_id: '100197849',
          app_id_name: 'QMediaSync',
        },
      ],
    ])
    expect(wrapper.emitted('update:visible')).toEqual([[false]])
  })
})
