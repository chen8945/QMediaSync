// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import QueueTaskDetails from '@/components/queue/QueueTaskDetails.vue'

describe('QueueTaskDetails', () => {
  it('在整行字段前补齐当前行，避免长错误文本挤入剩余列', () => {
    const wrapper = mount(QueueTaskDetails, {
      props: {
        maxColumns: 5,
        groups: [
          {
            key: 'transfer',
            label: '传输信息',
            columns: 3,
            fields: [
              { key: 'size', label: '文件大小', value: '1 KB' },
              { key: 'phase', label: '上传阶段', value: '上传失败' },
              { key: 'retry-count', label: '重试次数', value: '1 次' },
              { key: 'last-retry-time', label: '最近重试时间', value: '2026-07-26 12:00:00' },
              { key: 'error', label: '失败原因', value: '远端服务不可用', fullWidth: true },
            ],
          },
        ],
      },
      global: {
        stubs: {
          'el-descriptions': {
            props: ['column', 'direction'],
            template:
              '<div class="descriptions" :data-columns="column" :data-direction="direction"><slot /></div>',
          },
          'el-descriptions-item': {
            props: ['label', 'span'],
            template:
              '<div class="description-item" :data-label="label" :data-span="span"><slot /></div>',
          },
          'el-tag': { template: '<span><slot /></span>' },
        },
      },
    })

    expect(wrapper.get('.descriptions').attributes('data-columns')).toBe('3')
    expect(wrapper.get('.descriptions').attributes('data-direction')).toBe('vertical')
    expect(
      wrapper.findAll('.description-item').map((item) => item.attributes('data-span')),
    ).toEqual(['1', '1', '1', '3', '3'])

    wrapper.unmount()
  })
})
