// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'
import StrmRegexInput from '@/components/StrmRegexInput.vue'

enableAutoUnmount(afterEach)

async function openEditor(wrapper: VueWrapper) {
  await wrapper
    .findAll('button')
    .find((button) => button.text() === '+ 添加')!
    .trigger('click')
  return wrapper.get('input')
}

describe('正则排除名称输入', () => {
  it('初始显示已有标签和添加入口，匹配与继承说明常驻，语法和示例默认折叠', () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: ['^Sample$'], inherit: true } })

    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.get('.el-tag code').text()).toBe('^Sample$')
    expect(wrapper.text()).toContain('+ 添加')
    const help = wrapper
      .findAll('p')
      .filter((paragraph) => !paragraph.element.closest('details'))
      .map((paragraph) => paragraph.text())
      .join(' ')
    expect(help).toContain('默认区分大小写、部分匹配')
    expect(help).toContain('目录命中时也排除其下内容')
    expect(help).toContain('列表为空时使用 STRM 设置中的正则')
    expect(wrapper.get('details').element.open).toBe(false)
    expect(wrapper.get('details').text()).toContain('^…$')
    expect(wrapper.get('details').text()).toContain('(?i)(sample|trailer)')
  })

  it('允许有意义的纯空格规则，并在输入法组合期间保留草稿', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [] } })
    const input = await openEditor(wrapper)
    expect(wrapper.get('.el-input-group__append button').attributes('disabled')).toBeDefined()
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.find('input').exists()).toBe(true)
    await input.setValue(' ')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('input').element.value).toBe(' ')
    await input.trigger('keydown', { key: 'Enter', isComposing: false })
    expect(wrapper.emitted('update:modelValue')).toEqual([[[' ']]])
    expect(wrapper.find('input').exists()).toBe(false)
  })

  it('展开后聚焦，按回车整条添加并收起，保留原文且阻止外层表单提交', async () => {
    const wrapper = mount(StrmRegexInput, { attachTo: document.body, props: { modelValue: [] } })
    const pattern = String.raw`  (?i)Sample{1,3},Trailer;\D+  `
    const input = await openEditor(wrapper)
    await flushPromises()
    expect(document.activeElement).toBe(input.element)
    expect(wrapper.text()).toContain('每次添加一条原始表达式')
    expect(wrapper.text()).toContain('逗号、分号和首尾空格均按原文保留')

    await input.setValue(pattern)
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.element.dispatchEvent(enter)
    await flushPromises()

    expect(enter.defaultPrevented).toBe(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([[[pattern]]])
    expect(wrapper.find('input').exists()).toBe(false)
    expect(document.activeElement).toBe(wrapper.get('button').element)
    await wrapper.setProps({ modelValue: [pattern] })
    expect(wrapper.get('.el-tag code').element.textContent).toBe(pattern)
    expect((await openEditor(wrapper)).element.value).toBe('')
  })

  it('不兼容表达式显示原因并保留输入供用户修改', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [] } })
    const input = await openEditor(wrapper)

    await input.setValue('(?=sample)')
    await input.trigger('keydown', { key: 'Enter' })
    await wrapper.get('.el-input-group__append button').trigger('click')

    expect(wrapper.get('[role="alert"]').text()).toContain('前后向断言')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('input').element.value).toBe('(?=sample)')

    await input.setValue('^Sample$')
    await wrapper.get('.el-input-group__append button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[['^Sample$']]])
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('Go 特有语法可以添加，并显示后端校验提示及常用示例', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: [], inherit: true } })
    const pattern = String.raw`\p{Han}+`
    await (await openEditor(wrapper)).setValue(pattern)
    expect(wrapper.get('[role="status"]').text()).toContain('保存时由服务器校验')
    await wrapper.get('.el-input-group__append button').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[[pattern]]])
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.setProps({ modelValue: [pattern] })
    expect(wrapper.get('[role="status"]').text()).toContain('保存时由服务器校验')
    expect(wrapper.text()).toContain('列表为空时使用 STRM 设置中的正则')
    expect(wrapper.get('details').text()).toContain('(?i)(sample|trailer)')
  })

  it('仅对完全相同的原文去重，重复添加也清空并收起', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: ['Sample'] } })
    await (await openEditor(wrapper)).setValue('Sample')
    await wrapper.get('.el-input-group__append button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.find('input').exists()).toBe(false)
    const input = await openEditor(wrapper)
    expect(input.element.value).toBe('')
    await input.setValue('sample')
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toEqual([[['Sample', 'sample']]])
  })

  it('禁用时不能展开、添加或删除，并保留已展开的草稿', async () => {
    const wrapper = mount(StrmRegexInput, { props: { modelValue: ['sample'], disabled: true } })
    const trigger = wrapper.findAll('button').find((button) => button.text() === '+ 添加')!
    expect(trigger.element.disabled).toBe(true)
    await trigger.trigger('click')
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('.el-tag__close').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await wrapper.setProps({ disabled: false })
    const input = await openEditor(wrapper)
    await input.setValue('trailer')
    await wrapper.setProps({ disabled: true })
    expect(input.element.disabled).toBe(true)
    expect(wrapper.get('.el-input-group__append button').attributes('disabled')).toBeDefined()
    const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.element.dispatchEvent(enter)
    await nextTick()
    await wrapper.get('.el-tag__close').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('input').element.value).toBe('trailer')
  })
})
