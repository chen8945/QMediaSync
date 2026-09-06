// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import StrmRegexInput from '@/components/StrmRegexInput.vue'

describe('正则排除名称输入', () => {
  it('按回车整条添加，保留大小写、空格、分隔符和转义', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [] } })
    const pattern = String.raw`  (?i)Sample{1,3},Trailer;\D+  `
    const input = wrapper.get('input')

    await input.setValue(pattern)
    await input.trigger('keydown', { key: 'Enter' })

    expect(wrapper.emitted('update:modelValue')).toEqual([[[pattern]]])
    await wrapper.setProps({ modelValue: [pattern] })
    expect(wrapper.get('.el-tag code').element.textContent).toBe(pattern)
    expect(input.element.value).toBe('')
    wrapper.unmount()
  })

  it('不兼容表达式显示原因并保留输入供用户修改', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [] } })
    const input = wrapper.get('input')

    await input.setValue('(?=sample)')
    await input.trigger('keydown', { key: 'Enter' })

    expect(wrapper.get('[role="alert"]').text()).toContain('前后向断言')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(input.element.value).toBe('(?=sample)')
    wrapper.unmount()
  })

  it('Go 特有语法可以添加，并显示后端校验提示及常用示例', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [], inherit: true } })
    const pattern = String.raw`\p{Han}+`
    await wrapper.get('input').setValue(pattern)
    expect(wrapper.get('[role="status"]').text()).toContain('保存时由服务器校验')
    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[[pattern]]])
    expect(wrapper.text()).toContain('列表为空时使用 STRM 设置中的正则')
    expect(wrapper.get('details').text()).toContain('(?i)(sample|trailer)')
    wrapper.unmount()
  })
})
