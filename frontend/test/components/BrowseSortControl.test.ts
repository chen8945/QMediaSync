import { shallowMount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import BrowseSortControl from '@/components/BrowseSortControl.vue'
import { browseSortOptions } from '../support/browseSort'

describe('共享排序控件', () => {
  it('只展示上游能力支持的选项，default 隐藏方向和置顶', async () => {
    const options = browseSortOptions()
    const wrapper = shallowMount(BrowseSortControl, {
      props: { options, modelValue: options.default },
    })
    expect(wrapper.findComponent({ name: 'ElCheckbox' }).exists()).toBe(true)
    const field = wrapper.findComponent({ name: 'ElSelect' })
    field.vm.$emit('change', 'time')
    expect(wrapper.emitted('change')?.[0]).toEqual([
      { sort_by: 'time', sort_order: 'desc', folders_first: true },
    ])
    await wrapper.setProps({ modelValue: { sort_by: 'default', sort_order: 'asc' } })
    expect(wrapper.find('.browse-sort-order').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'ElCheckbox' }).exists()).toBe(false)
    await wrapper.setProps({
      options: browseSortOptions('115', 'directories'),
      modelValue: { sort_by: 'name', sort_order: 'asc' },
    })
    expect(wrapper.findComponent({ name: 'ElCheckbox' }).exists()).toBe(false)
    expect(
      wrapper.findAllComponents({ name: 'ElOption' }).map((item) => item.attributes('value')),
    ).not.toContain('size')
    await wrapper.setProps({
      options: browseSortOptions('openlist'),
      modelValue: { sort_by: 'default', sort_order: 'asc' },
    })
    expect(wrapper.find('.browse-sort-control').exists()).toBe(false)
    expect(wrapper.text()).toContain('跟随服务端顺序')
  })
})
