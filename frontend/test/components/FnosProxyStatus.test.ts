import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import FnosProxyStatus from '@/components/fnos-proxy/FnosProxyStatus.vue'
import type { FnosProxyRuntime } from '@/api/fnosProxy'

const stopped: FnosProxyRuntime = {
  running: false,
  listen_port: 0,
  https_enabled: false,
  https_port: 0,
  restart_required: false,
  last_error: '',
}

describe('飞牛影视运行状态展示', () => {
  it('未读取时显示尚未读取，不提供默认保存状态或说明', () => {
    const wrapper = mount(FnosProxyStatus, { props: { runtime: null, savedEnabled: false } })
    expect(wrapper.get('.runtime-status').text()).toBe('尚未读取')
    expect(wrapper.text()).toBe('运行状态 尚未读取')
    expect(wrapper.get('[role="status"]').attributes('aria-live')).toBe('polite')
    expect(wrapper.find('.el-card').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'ElAlert' }).exists()).toBe(false)
  })

  it('关闭状态不显示零端口地址或不必要的未应用提示', () => {
    const wrapper = mount(FnosProxyStatus, { props: { runtime: stopped, savedEnabled: false } })
    expect(wrapper.get('.runtime-status').text()).toBe('未启用')
    expect(wrapper.find('.restart-notice').exists()).toBe(false)
    expect(wrapper.text()).toBe('运行状态 未启用')
  })

  it('已保存关闭而旧实例仍运行时，显示实际运行状态和未应用提示', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: {
        savedEnabled: false,
        runtime: { ...stopped, running: true, listen_port: 9005, restart_required: true },
      },
    })
    expect(wrapper.get('.runtime-status').text()).toBe('运行中')
    expect(wrapper.get('.restart-notice').text()).toContain('设置尚未完全应用')
    expect(wrapper.get('.restart-notice').text()).toContain('重新保存')
  })

  it('保存关闭但尚未应用时，只显示关闭状态和重新保存提示', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: { savedEnabled: false, runtime: { ...stopped, restart_required: true } },
    })
    expect(wrapper.get('.restart-notice').text()).toContain('设置尚未完全应用')
    expect(wrapper.get('.runtime-status').text()).toBe('未启用')
    expect(wrapper.text()).not.toContain('重启')
  })

  it('正常运行只显示紧凑状态，不再显示连接示例、已保存配置或地址帮助', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: {
        savedEnabled: true,
        runtime: {
          ...stopped,
          running: true,
          listen_port: 9005,
          https_enabled: true,
          https_port: 9006,
        },
      },
    })
    expect(wrapper.text()).toBe('运行状态 运行中')
    expect(wrapper.findComponent({ name: 'ElTag' }).props('type')).toBe('success')
    expect(wrapper.findComponent({ name: 'ElAlert' }).exists()).toBe(false)
    expect(wrapper.find('.el-card').exists()).toBe(false)
  })

  it('启用失败突出显示实际原因', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: {
        savedEnabled: true,
        runtime: {
          ...stopped,
          restart_required: true,
          last_error: 'HTTP 端口 9005 已被占用',
        },
      },
    })
    expect(wrapper.get('.runtime-status').text()).toBe('未能启用')
    expect(wrapper.get('.runtime-error').text()).toContain('HTTP 端口 9005 已被占用')
    expect(wrapper.findAllComponents({ name: 'ElAlert' })).toHaveLength(1)
    expect(wrapper.find('.restart-notice').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'ElAlert' }).props('type')).toBe('error')
  })

  it('HTTPS 未能启用但 HTTP 仍运行时，显示部分服务警告', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: {
        savedEnabled: true,
        runtime: {
          ...stopped,
          running: true,
          listen_port: 9005,
          restart_required: true,
          last_error: 'HTTPS 证书无效',
        },
      },
    })
    expect(wrapper.get('.runtime-status').text()).toBe('运行异常')
    expect(wrapper.findComponent({ name: 'ElAlert' }).props('type')).toBe('warning')
    expect(wrapper.get('.runtime-error').text()).toContain('HTTPS 证书无效')
    expect(wrapper.get('.runtime-error').text()).toContain('部分服务未能启用')
    expect(wrapper.text()).not.toContain('重启')
  })

  it('已保存启用但尚未运行时提示检查并重新保存', () => {
    const wrapper = mount(FnosProxyStatus, {
      props: { savedEnabled: true, runtime: { ...stopped, restart_required: true } },
    })
    expect(wrapper.get('.runtime-status').text()).toBe('未运行')
    expect(wrapper.get('.restart-notice').text()).toContain('处理后重新保存设置')
  })
})
