import { defineComponent, ref } from 'vue'
import { ElForm } from 'element-plus'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import FnosPathMappings from '@/components/fnos-proxy/FnosPathMappings.vue'
import type { FnosPathMapping } from '@/api/fnosProxy'
import type { DirInfo } from '@/typing'

const DirectorySelectorStub = defineComponent({
  name: 'DirectorySelector',
  props: ['modelValue', 'sourceType', 'resetOnSelect'],
  emits: ['update:modelValue', 'select', 'cancel'],
  template: '<div data-testid="directory-selector" />',
})
const directory: DirInfo = { id: '/selected', path: '/selected', name: 'selected' }
const initialRows: FnosPathMapping[] = [
  { fnos_root: '/fnos/first', local_root: '/local/first' },
  { fnos_root: '/fnos/second', local_root: '/local/second' },
]
const mountMappings = async () => {
  const harness = defineComponent({
    components: { FnosPathMappings, ElForm },
    setup() {
      const rows = ref(structuredClone(initialRows))
      const disabled = ref(false)
      return { rows, disabled }
    },
    template:
      '<el-form :model="{path_mappings:rows}" label-position="top"><FnosPathMappings v-model="rows" :disabled="disabled" /></el-form>',
  })
  const wrapper = mount(harness, {
    attachTo: document.body,
    global: { stubs: { DirectorySelector: DirectorySelectorStub, teleport: true } },
  })
  await flushPromises()
  return wrapper
}
const buttons = (wrapper: VueWrapper, label: string) =>
  wrapper.findAll('button').filter((entry) => entry.text() === label)
const inputValue = (wrapper: VueWrapper, row: number) =>
  (
    wrapper.get(`input[aria-label="映射 ${row} QMediaSync 中的 STRM 文件夹"]`)
      .element as HTMLInputElement
  ).value

enableAutoUnmount(afterEach)
afterEach(() => {
  document.body.innerHTML = ''
})

describe('飞牛影视 STRM 路径映射', () => {
  it('复用本地选择器，临时选中不修改输入，确认只填写对应行', async () => {
    const wrapper = await mountMappings()
    await buttons(wrapper, '选择文件夹')[1]!.trigger('click')
    await flushPromises()
    const selector = wrapper.findComponent(DirectorySelectorStub)
    expect(selector.props('sourceType')).toBe('local')
    expect(selector.props('resetOnSelect')).toBe(false)
    selector.vm.$emit('update:modelValue', directory)
    await flushPromises()
    expect(inputValue(wrapper, 2)).toBe('/local/second')
    selector.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/first')
    expect(inputValue(wrapper, 2)).toBe('/selected')
    expect(wrapper.findComponent(DirectorySelectorStub).exists()).toBe(false)
  })

  it('取消、无选择和旧确认事件均保留输入', async () => {
    const wrapper = await mountMappings()
    await buttons(wrapper, '选择文件夹')[0]!.trigger('click')
    await flushPromises()
    const selector = wrapper.findComponent(DirectorySelectorStub)
    selector.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/first')
    selector.vm.$emit('update:modelValue', directory)
    selector.vm.$emit('cancel')
    await flushPromises()
    selector.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/first')
  })

  it('切换行后上一选择器的迟到事件不能填写新行', async () => {
    const wrapper = await mountMappings()
    await buttons(wrapper, '选择文件夹')[0]!.trigger('click')
    await flushPromises()
    const previous = wrapper.findComponent(DirectorySelectorStub)
    previous.vm.$emit('update:modelValue', directory)
    await buttons(wrapper, '选择文件夹')[1]!.trigger('click')
    await flushPromises()
    previous.vm.$emit('update:modelValue', { ...directory, path: '/stale' })
    previous.vm.$emit('select')
    previous.vm.$emit('cancel')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/first')
    expect(inputValue(wrapper, 2)).toBe('/local/second')
    const current = wrapper.findComponent(DirectorySelectorStub)
    expect(current.exists()).toBe(true)
    current.vm.$emit('update:modelValue', directory)
    current.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 2)).toBe('/selected')
  })

  it('删除目标行后旧事件不能填写索引相同的其他行', async () => {
    const wrapper = await mountMappings()
    await buttons(wrapper, '选择文件夹')[0]!.trigger('click')
    await flushPromises()
    const previous = wrapper.findComponent(DirectorySelectorStub)
    previous.vm.$emit('update:modelValue', directory)
    await buttons(wrapper, '删除此映射')[0]!.trigger('click')
    previous.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/second')
    expect(buttons(wrapper, '删除此映射')).toHaveLength(1)
  })

  it('手动输入不会让当前行失去焦点，添加和删除保留其他行', async () => {
    const wrapper = await mountMappings()
    const input = wrapper.get('input[aria-label="映射 1 QMediaSync 中的 STRM 文件夹"]')
    await input.setValue('/changed')
    expect(wrapper.get('input[aria-label="映射 1 QMediaSync 中的 STRM 文件夹"]').element).toBe(
      input.element,
    )
    expect(inputValue(wrapper, 1)).toBe('/changed')
    await buttons(wrapper, '添加路径映射')[0]!.trigger('click')
    expect(buttons(wrapper, '删除此映射')).toHaveLength(3)
    await buttons(wrapper, '删除此映射')[1]!.trigger('click')
    expect(inputValue(wrapper, 1)).toBe('/changed')
    expect(inputValue(wrapper, 2)).toBe('')
  })

  it('提交期间关闭选择器并忽略旧事件，所有编辑按钮禁用', async () => {
    const wrapper = await mountMappings()
    await buttons(wrapper, '选择文件夹')[0]!.trigger('click')
    await flushPromises()
    const previous = wrapper.findComponent(DirectorySelectorStub)
    previous.vm.$emit('update:modelValue', directory)
    wrapper.vm.disabled = true
    await flushPromises()
    previous.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, 1)).toBe('/local/first')
    expect(wrapper.findComponent(DirectorySelectorStub).exists()).toBe(false)
    for (const label of ['选择文件夹', '删除此映射', '添加路径映射']) {
      expect(
        buttons(wrapper, label).every((button) => button.attributes('disabled') !== undefined),
      ).toBe(true)
    }
  })
})
