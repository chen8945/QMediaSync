// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import MetadataExtInput from '@/components/MetadataExtInput.vue'

enableAutoUnmount(afterEach)

describe('名称和扩展名标签输入', () => {
  it.each([
    {
      autoAddDot: true,
      before: ['.jpg'],
      draft: ' JPG, .jpg ; png;; ',
      after: ['.jpg', '.JPG', '.png'],
    },
    {
      autoAddDot: false,
      before: ['sample'],
      draft: ' Sample, trailer; sample ',
      after: ['sample', 'Sample', 'trailer'],
    },
  ])(
    '保留裁剪、分隔和去重规则（autoAddDot=$autoAddDot）',
    async ({ autoAddDot, before, draft, after }) => {
      const wrapper = mount(MetadataExtInput, { props: { modelValue: before, autoAddDot } })
      expect(wrapper.find('input').exists()).toBe(false)
      expect(wrapper.findAll('.el-tag__content').map((tag) => tag.text())).toEqual(before)
      await wrapper
        .findAll('button')
        .find((button) => button.text() === '+ 添加')!
        .trigger('click')
      const input = wrapper.get('input')
      await input.setValue(draft)
      const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
      input.element.dispatchEvent(enter)
      await flushPromises()

      expect(enter.defaultPrevented).toBe(true)
      expect(wrapper.emitted('update:modelValue')).toEqual([[after]])
      expect(wrapper.find('input').exists()).toBe(false)
      expect(wrapper.findAll('.el-tag__content').map((tag) => tag.text())).toEqual(after)
      await wrapper
        .findAll('button')
        .find((button) => button.text() === '+ 添加')!
        .trigger('click')
      expect(wrapper.get('input').element.value).toBe('')
    },
  )

  it('多个控件展开时分别聚焦，按钮添加后焦点返回当前控件入口', async () => {
    const first = mount(MetadataExtInput, { attachTo: document.body, props: { modelValue: [] } })
    const second = mount(MetadataExtInput, { attachTo: document.body, props: { modelValue: [] } })
    await first.get('button').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(first.get('input').element)
    await first.get('input').setValue('jpg')
    await second.get('button').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(second.get('input').element)
    await second.get('input').setValue('png')
    await second.get('.el-input-group__append button').trigger('click')
    await flushPromises()

    expect(second.emitted('update:modelValue')).toEqual([[['.png']]])
    expect(second.find('input').exists()).toBe(false)
    expect(document.activeElement?.textContent?.trim()).toBe('+ 添加')
    expect(second.element.contains(document.activeElement)).toBe(true)
    expect(first.get('input').element.value).toBe('jpg')
    expect(first.emitted('update:modelValue')).toBeUndefined()
  })

  it('输入法组合回车不添加，空白回车保留原有收起行为', async () => {
    const wrapper = mount(MetadataExtInput, { props: { modelValue: [], autoAddDot: false } })
    await wrapper.get('button').trigger('click')
    const input = wrapper.get('input')
    await input.setValue('预告')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    await input.trigger('keyup', { key: 'Enter', isComposing: true })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('input').element.value).toBe('预告')
    await input.setValue('   ')
    expect(wrapper.get('.el-input-group__append button').attributes('disabled')).toBeDefined()
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.find('input').exists()).toBe(false)
  })
})
