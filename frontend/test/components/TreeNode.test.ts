// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import TreeNode from '@/components/TreeNode.vue'

const node = {
  id: 'movies',
  name: '电影',
  path: '/movies',
  expanded: false,
  loadState: 'unloaded' as const,
  latestChildLoadId: 0,
  children: [],
  isLeaf: false,
}

describe('TreeNode', () => {
  it('点击整行目录时同时选中并展开', async () => {
    const wrapper = mount(TreeNode, {
      props: {
        node,
        selectedId: undefined,
        sourceType: 'local',
        accountId: 0,
      },
    })

    await wrapper.get('.node-content').trigger('click')

    expect(wrapper.emitted('select') || []).toHaveLength(1)
    expect(wrapper.emitted('toggle') || []).toHaveLength(1)
  })

  it('可通过键盘激活目录行，并公开树形可访问性状态', async () => {
    const wrapper = mount(TreeNode, {
      props: {
        node,
        selectedId: 'movies',
        sourceType: 'local',
        accountId: 0,
      },
    })

    const treeItem = wrapper.get('[role="treeitem"]')
    await treeItem.trigger('keydown', { key: 'Enter' })

    expect(treeItem.attributes('tabindex')).toBe('0')
    expect(treeItem.attributes('aria-selected')).toBe('true')
    expect(treeItem.attributes('aria-expanded')).toBe('false')
    expect(wrapper.emitted('select') || []).toHaveLength(1)
    expect(wrapper.emitted('toggle') || []).toHaveLength(1)
  })

  it('嵌套控件的键盘操作不会激活目录行', async () => {
    const wrapper = mount(TreeNode, {
      props: {
        node: { ...node, loadState: 'error' as const },
        selectedId: undefined,
        sourceType: 'local',
        accountId: 0,
      },
    })

    await wrapper.get('[aria-label="展开目录"]').trigger('keydown', { key: 'Enter' })
    await wrapper.get('[aria-label="重试加载目录"]').trigger('keydown', { key: ' ' })

    expect(wrapper.emitted('select')).toBeUndefined()
    expect(wrapper.emitted('toggle')).toBeUndefined()
    expect(wrapper.emitted('retry')).toBeUndefined()
  })

  it('叶子目录不暴露可展开状态', () => {
    const wrapper = mount(TreeNode, {
      props: {
        node: { ...node, expanded: true, loadState: 'loaded' as const, isLeaf: true },
        selectedId: undefined,
        sourceType: 'local',
        accountId: 0,
      },
    })

    const treeItem = wrapper.get('[role="treeitem"]')
    expect(treeItem.attributes('aria-expanded')).toBeUndefined()
    expect(wrapper.find('[aria-label="收起目录"]').exists()).toBe(false)
  })

  it('将子目录组嵌套在拥有展开状态的树节点内', () => {
    const wrapper = mount(TreeNode, {
      props: {
        node: {
          ...node,
          expanded: true,
          children: [
            {
              ...node,
              id: 'shows',
              name: '剧集',
              path: '/shows',
            },
          ],
        },
        selectedId: undefined,
        sourceType: 'local',
        accountId: 0,
      },
    })

    const treeItem = wrapper.get('[role="treeitem"]')
    const group = treeItem.get('[role="group"]')

    expect(group.get('[role="treeitem"]').attributes('aria-selected')).toBe('false')
  })

  it('子目录键盘激活不会同时激活父目录', async () => {
    const wrapper = mount(TreeNode, {
      props: {
        node: {
          ...node,
          expanded: true,
          children: [
            {
              ...node,
              id: 'shows',
              name: '剧集',
              path: '/shows',
            },
          ],
        },
        selectedId: undefined,
        sourceType: 'local',
        accountId: 0,
      },
    })

    await wrapper.get('[role="group"]').get('[role="treeitem"]').trigger('keydown', {
      key: 'Enter',
    })

    expect(wrapper.emitted('select') || []).toHaveLength(1)
    expect(wrapper.emitted('toggle') || []).toHaveLength(1)
  })
})
