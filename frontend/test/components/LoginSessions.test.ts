// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import LoginSessions from '../../src/components/user-settings/LoginSessions.vue'
import { httpKey } from '@/http/client'

describe('LoginSessions', () => {
  it('加载并展示当前登录设备', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: [
            {
              session_id: 'sid-current',
              current: true,
              ip_address: '127.0.0.1',
              user_agent: 'Chrome',
              last_seen_at: 1710000000,
              expires_at: 1710003600,
              created_at: 1710000000,
            },
          ],
        },
      }),
      delete: vi.fn(),
      post: vi.fn(),
    }
    const wrapper = mount(LoginSessions, {
      global: {
        provide: { [httpKey]: http },
        stubs: {
          PageHeader: true,
          'el-button': { template: '<button @click="$emit(\'click\')"><slot /></button>' },
          'el-table': {
            props: ['data'],
            template:
              '<div><span v-for="row in data" :key="row.session_id">{{ row.current ? "当前设备" : "其他设备" }}</span><slot /></div>',
          },
          'el-table-column': { props: ['label'], template: '<span>{{ label }}</span>' },
          'el-tag': { template: '<span><slot /></span>' },
          'el-alert': true,
        },
      },
    })

    await Promise.resolve()
    await Promise.resolve()

    expect(http.get).toHaveBeenCalledWith(expect.stringContaining('/user/sessions'))
    expect(wrapper.text()).toContain('当前设备')
    expect(wrapper.text()).toContain('登录 IP')
  })
})
