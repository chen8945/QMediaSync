// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import RecordDetailDescriptions from '@/components/records/RecordDetailDescriptions.vue'

describe('RecordDetailDescriptions', () => {
  it('将空字符串详情显示为占位符', () => {
    const wrapper = mount(RecordDetailDescriptions, {
      props: {
        row: { file_path: '' },
        fields: [
          {
            key: 'file_path',
            label: '文件路径',
            value: (row) => (row as { file_path: string }).file_path,
          },
        ],
      },
      global: {
        stubs: {
          ElDescriptions: { template: '<section><slot /></section>' },
          ElDescriptionsItem: { template: '<div><slot /></div>' },
        },
      },
    })

    expect(wrapper.find('.record-detail__value').text()).toBe('-')
  })
})
