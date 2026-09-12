// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
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

  it('清空前确认，取消保留草稿，确认后只清空当前列表并恢复添加入口', async () => {
    const original = ['.mp4']
    const wrapper = mount(MetadataExtInput, {
      attachTo: document.body,
      props: { modelValue: original, clearable: true },
      slots: { actions: '<button type="button">导入全局设置</button>' },
      global: { stubs: { teleport: true } },
    })
    const controls = wrapper
      .get('.tag-input-tags')
      .findAll('button')
      .filter((button) => button.text())
    expect(controls.map((button) => button.text())).toEqual(['+ 添加', '导入全局设置', '清空'])
    await controls[0]!.trigger('click')
    await wrapper.get('input').setValue('mkv')
    expect(controls[1]!.isVisible()).toBe(true)
    expect(controls[2]!.isVisible()).toBe(true)
    await controls[2]!.trigger('click')
    await vi.waitFor(() => expect(wrapper.find('.el-popconfirm').exists()).toBe(true))
    expect(wrapper.get('.el-popconfirm').text()).toContain('清空当前列表？保存后生效。')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper
      .get('.el-popconfirm')
      .findAll('button')
      .find((button) => button.text() === '取消')!
      .trigger('click')
    await vi.waitFor(() =>
      expect(wrapper.findAll('.el-popconfirm').some((popup) => popup.isVisible())).toBe(false),
    )
    expect(wrapper.get('input').element.value).toBe('mkv')
    expect(wrapper.get('.el-tag__content').text()).toBe('.mp4')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await controls[2]!.trigger('click')
    await vi.waitFor(() =>
      expect(wrapper.findAll('.el-popconfirm').some((popup) => popup.isVisible())).toBe(true),
    )
    await wrapper
      .get('.el-popconfirm')
      .findAll('button')
      .find((button) => button.text() === '清空')!
      .trigger('click')
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toEqual([[[]]])
    expect(original).toEqual(['.mp4'])
    expect(wrapper.find('.el-tag').exists()).toBe(false)
    expect(wrapper.find('input').exists()).toBe(false)
    expect(document.activeElement?.textContent?.trim()).toBe('+ 添加')
    expect(wrapper.element.contains(document.activeElement)).toBe(true)
    expect(controls[2]!.element.disabled).toBe(true)
    await wrapper.get('button').trigger('click')
    expect(wrapper.get('input').element.value).toBe('')
  })

  it('清空默认隐藏；空列表或禁用时不能清空，禁用时保留草稿且禁止添加和删除', async () => {
    const wrapper = mount(MetadataExtInput, { props: { modelValue: [] } })
    expect(wrapper.findAll('button').map((button) => button.text())).toEqual(['+ 添加'])
    await wrapper.setProps({ clearable: true })
    const clear = wrapper.findAll('button').find((button) => button.text() === '清空')!
    expect(clear.element.disabled).toBe(true)
    await clear.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()

    await wrapper.setProps({ modelValue: ['.mp4'], disabled: true })
    expect(clear.element.disabled).toBe(true)
    const add = wrapper.findAll('button').find((button) => button.text() === '+ 添加')!
    expect(add.element.disabled).toBe(true)
    await add.trigger('click')
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('.el-tag__close').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.setProps({ disabled: false })
    await add.trigger('click')
    await wrapper.get('input').setValue('mkv')
    await wrapper.setProps({ disabled: true })
    expect(wrapper.get('input').element.disabled).toBe(true)
    expect(wrapper.get('.el-input-group__append button').attributes('disabled')).toBeDefined()
    await wrapper.get('input').trigger('keydown', { key: 'Enter' })
    await wrapper.get('.el-tag__close').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('input').element.value).toBe('mkv')
  })
})
