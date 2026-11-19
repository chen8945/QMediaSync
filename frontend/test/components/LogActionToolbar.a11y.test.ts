import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import LogActionToolbar from '@/components/log/LogActionToolbar.vue'

enableAutoUnmount(afterEach)

describe('日志操作工具栏无障碍名称', () => {
  it('紧凑模式保留操作名称，主按钮查看、菜单下载', async () => {
    const wrapper = mount(LogActionToolbar, { props: { connected: false, compact: true } })
    for (const label of ['连接实时日志', '断开实时日志', '清空当前显示日志', '查看日志']) {
      expect(wrapper.find('button[aria-label="' + label + '"]').exists()).toBe(true)
    }
    await wrapper.get('button[aria-label="查看日志"]').trigger('click')
    expect(wrapper.emitted('view')).toHaveLength(1)
    const toggle = wrapper.get('button.el-dropdown__caret-button')
    expect(toggle.attributes('aria-label')).toBeTruthy()
    await toggle.trigger('click')
    await flushPromises()
    const download = wrapper.findComponent({ name: 'ElDropdownItem' }).get('li')
    expect(download.attributes('aria-label')).toBe('下载日志文件')
    await download.trigger('click')
    expect(wrapper.emitted('download')).toHaveLength(1)
  })
})
