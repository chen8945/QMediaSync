import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import StrmResultsDialog from '@/components/StrmResultsDialog.vue'

const get = vi.fn()
vi.mock('@/http/client', () => ({ useHttpClient: () => ({ get }) }))
vi.mock('@/utils/httpErrorNotification', () => ({ notifyHttpError: vi.fn() }))
const rows = [
  {
    id: 5,
    task_type: 'directory_scan',
    directory_path: '/movies',
    status: 'completed',
    total_items: 2,
    accepted_items: 1,
    failed_items: 0,
    skipped_items: 1,
  },
]

describe('STRM 后处理结果', () => {
  it('按目录读取结果，展示独立计数并能查看跳过子项', async () => {
    get.mockResolvedValueOnce({ status: 200, data: { code: 200, data: { items: rows, total: 1 } } })
    const wrapper = mount(StrmResultsDialog, {
      props: { modelValue: true, syncPathId: 8 },
      global: {
        directives: { loading: () => {} },
        stubs: {
          ElDialog: { template: '<div><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElTable: defineComponent({
            props: ['data'],
            setup(props, { slots }) {
              return () =>
                h(
                  'div',
                  (props.data as unknown[]).flatMap(
                    (row) =>
                      slots.default?.().map((vnode) =>
                        h(
                          vnode.type as object,
                          {},
                          {
                            default: () =>
                              vnode.children &&
                              typeof vnode.children === 'object' &&
                              'default' in vnode.children
                                ? (vnode.children.default as (p: unknown) => unknown)({ row })
                                : '',
                          },
                        ),
                      ) ?? [],
                  ),
                )
            },
          }),
          ElTableColumn: { template: '<div><slot /></div>' },
          ResponsivePagination: true,
        },
      },
    })
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/api/strm/tasks', {
      params: expect.objectContaining({ sync_path_id: 8, page_size: 20 }),
    })
    expect(wrapper.text()).toContain('完成 1，失败 0，跳过 1')
    get.mockResolvedValueOnce({
      status: 200,
      data: {
        code: 200,
        data: {
          items: [
            {
              id: 6,
              task_type: 'file',
              file_name: 'sample.mkv',
              status: 'skipped',
              skip_reason: '视频文件小于最小大小要求',
            },
          ],
          total: 1,
        },
      },
    })
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '查看子项')!
      .trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/api/strm/tasks', {
      params: expect.objectContaining({ parent_task_id: 5 }),
    })
    expect(wrapper.text()).toContain('已跳过')
    expect(wrapper.text()).toContain('视频文件小于最小大小要求')
    get.mockResolvedValueOnce({
      status: 200,
      data: {
        code: 200,
        data: { items: [{ id: 6, task_type: 'directory_scan', status: 'future' }], total: 1 },
      },
    })
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '刷新结果')!
      .trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('未知状态（future）')
    expect(wrapper.text()).toContain('总计 未记录，完成 未记录，失败 未记录，跳过 未记录')
    get.mockResolvedValueOnce({ status: 200, data: { code: 200, data: { items: [], total: 0 } } })
    await wrapper
      .findAll('button')
      .find((button) => button.text() === '返回任务列表')!
      .trigger('click')
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith('/api/strm/tasks', {
      params: expect.objectContaining({ sync_path_id: 8, parent_task_id: undefined }),
    })
    wrapper.unmount()
  })
})
