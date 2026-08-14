// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { nextTick, shallowRef } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import type { V115AuthStatus } from '@/types/v115Auth'
import V115AuthorizationDialog from '@/components/cloud-auth/V115AuthorizationDialog.vue'
import { httpKey } from '@/http/client'

const authorizationState = {
  qrCode: shallowRef<null>(null),
  status: shallowRef<V115AuthStatus>('waiting'),
  tip: shallowRef('等待扫码'),
  loading: shallowRef(false),
  isPolling: shallowRef(true),
}

const startAuthorization = vi.fn()
const stopPolling = vi.fn()
const resetAuthorization = vi.fn()

vi.mock('@/composables/useV115DeviceAuthorization', () => ({
  useV115DeviceAuthorization: () => ({
    ...authorizationState,
    startAuthorization,
    stopPolling,
    resetAuthorization,
  }),
}))

describe('V115AuthorizationDialog', () => {
  it('二维码授权成功后关闭弹窗并通知父组件刷新', async () => {
    authorizationState.status.value = 'waiting'
    const wrapper = mount(V115AuthorizationDialog, {
      props: {
        visible: true,
        accountId: 1,
        accountName: '测试账号',
        authorizationId: 'change-session',
      },
      global: {
        provide: {
          [httpKey]: {},
        },
        stubs: {
          ElDialog: { template: '<div><slot /></div>' },
          ElSkeleton: { template: '<div />' },
          ElTag: { template: '<div><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElIcon: { template: '<span><slot /></span>' },
          V115QrCode: { template: '<canvas />' },
        },
      },
    })

    expect(startAuthorization).toHaveBeenCalledWith(1, 'change-session')

    authorizationState.status.value = 'confirmed'
    await nextTick()

    expect(wrapper.emitted('confirmed')).toHaveLength(1)
    expect(wrapper.emitted('update:visible')).toEqual([[false]])
  })

  it('关闭弹窗时停止当前二维码轮询', async () => {
    authorizationState.status.value = 'waiting'
    startAuthorization.mockClear()
    stopPolling.mockClear()
    const wrapper = mount(V115AuthorizationDialog, {
      props: {
        visible: true,
        accountId: 1,
        accountName: '测试账号',
      },
      global: {
        provide: {
          [httpKey]: {},
        },
        stubs: {
          ElDialog: { template: '<div><slot /></div>' },
          ElSkeleton: { template: '<div />' },
          ElTag: { template: '<div><slot /></div>' },
          ElButton: { template: '<button><slot /></button>' },
          ElIcon: { template: '<span><slot /></span>' },
          V115QrCode: { template: '<canvas />' },
        },
      },
    })

    await wrapper.setProps({ visible: false })

    expect(stopPolling).toHaveBeenCalled()
  })
})
