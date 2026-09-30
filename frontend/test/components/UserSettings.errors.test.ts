// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import axios, { AxiosError, CanceledError, type InternalAxiosRequestConfig } from 'axios'
import { ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppUserSettings from '@/components/AppUserSettings.vue'
import LoginSessions from '@/components/user-settings/LoginSessions.vue'
import TwoFactorSettings from '@/components/user-settings/TwoFactorSettings.vue'
import { httpKey } from '@/http/client'
import { markAuthInvalidationHandled } from '@/http/errors'
import { useAuthStore } from '@/stores/auth'

const { router } = vi.hoisted(() => ({ router: { replace: vi.fn() } }))
vi.mock('vue-router', () => ({ useRouter: () => router }))
vi.mock('qrcode', () => ({
  default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,AA==') },
}))

interface Reply {
  status: number
  data: { code: number; data: unknown; message?: string; error_code?: string }
}
const ok = (data: unknown = null): Reply => ({ status: 200, data: { code: 200, data } })
const fail = (status = 200, error_code?: string): Reply => ({
  status,
  data: { code: 500, data: null, message: '服务暂不可用', error_code },
})
const sessions = [
  {
    session_id: 'sid',
    current: false,
    ip_address: '127.0.0.2',
    user_agent: 'Test browser',
    created_at: 1,
    last_seen_at: 1,
    expires_at: 2,
  },
]
const wrappers: VueWrapper[] = []

function mountPage(
  component: typeof AppUserSettings | typeof TwoFactorSettings | typeof LoginSessions,
  respond: (config: InternalAxiosRequestConfig) => Reply | Promise<Reply>,
) {
  const request = vi.fn(async (config: InternalAxiosRequestConfig) => {
    const reply = await respond(config)
    const response = { ...reply, statusText: '', headers: {}, config }
    if (reply.status >= 400)
      throw new AxiosError('Request failed', 'ERR_BAD_RESPONSE', config, {}, response)
    return response
  })
  const http = axios.create({ adapter: request })
  const pinia = createPinia()
  const auth = useAuthStore(pinia)
  auth.login({ user: { id: '1', username: 'admin' }, csrfToken: 'csrf' })
  const wrapper = mount(component, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      provide: { [httpKey]: http },
      stubs: {
        PageHeader: { template: '<header><slot name="actions" /></header>' },
        ...(component === AppUserSettings ? { TwoFactorSettings: true } : {}),
      },
    },
  })
  wrappers.push(wrapper)
  return { wrapper, request, auth }
}
async function click(wrapper: VueWrapper, label: string) {
  const button = wrapper.findAll('button').find((button) => button.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('用户凭据设置', () => {
  it.each([
    [fail(), '服务暂不可用'],
    [fail(403, 'REQUEST_ORIGIN_INVALID'), '访问地址校验失败'],
    [fail(403, 'CSRF_TOKEN_INVALID'), '请求安全校验失败'],
  ])('写入失败只显示提示条并保留输入和会话', async (reply, message) => {
    const { wrapper, auth } = mountPage(AppUserSettings, () => reply)
    await wrapper.get('input[placeholder="请输入管理员密码"]').setValue('NewPass123')
    await wrapper.get('input[placeholder="请再次输入密码"]').setValue('NewPass123')
    await click(wrapper, '保存设置')
    expect(wrapper.text()).toContain(message)
    expect(
      wrapper.get<HTMLInputElement>('input[placeholder="请输入管理员密码"]').element.value,
    ).toBe('NewPass123')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(auth.isAuthenticated).toBe(true)
    expect(router.replace).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toMatch(/NewPass123|private/)
  })

  it.each([true, false])('成功响应 data=%s 决定是否重新登录', async (requiresLogin) => {
    const { wrapper, auth } = mountPage(AppUserSettings, () => ok(requiresLogin))
    await wrapper.get('input[placeholder="请输入新的管理员用户名"]').setValue('newadmin')
    await click(wrapper, '保存设置')
    expect(auth.isAuthenticated).toBe(!requiresLogin)
    expect(router.replace).toHaveBeenCalledTimes(requiresLogin ? 1 : 0)
  })

  it.each(['cancel', 'handled'] as const)('%s 不重复通知', async (kind) => {
    const error =
      kind === 'cancel' ? new CanceledError() : new AxiosError('Unauthorized', 'ERR_BAD_RESPONSE')
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const { wrapper } = mountPage(AppUserSettings, () => {
      throw error
    })
    await wrapper.get('input[placeholder="请输入新的管理员用户名"]').setValue('newadmin')
    await click(wrapper, '保存设置')
    expect(wrapper.text()).not.toContain('保存用户设置失败')
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})

describe('两步验证设置', () => {
  it('启用业务失败保留密钥和验证码，不刷新成功状态', async () => {
    const { wrapper, request } = mountPage(TwoFactorSettings, (config) => {
      if (config.url?.endsWith('/status')) return ok({ enabled: false })
      if (config.url?.endsWith('/setup'))
        return ok({ secret: 'TOTPSECRET', otpauth_url: 'otpauth://totp/test?secret=TOTPSECRET' })
      return fail()
    })
    await flushPromises()
    await click(wrapper, '生成配置')
    await wrapper.get('input[placeholder="输入动态验证码确认启用"]').setValue('123456')
    await click(wrapper, '启用两步验证')
    expect(wrapper.find('img').exists()).toBe(true)
    expect(
      wrapper.get<HTMLInputElement>('input[placeholder="输入动态验证码确认启用"]').element.value,
    ).toBe('123456')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('服务暂不可用')
    expect(request.mock.calls.filter(([config]) => config.url?.endsWith('/status'))).toHaveLength(1)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toMatch(
      /TOTPSECRET|123456|private/,
    )
  })

  it('关闭两步验证来源失败保留当前密码和验证码', async () => {
    const { wrapper } = mountPage(TwoFactorSettings, (config) =>
      config.url?.endsWith('/status') ? ok({ enabled: true }) : fail(403, 'REQUEST_ORIGIN_INVALID'),
    )
    await flushPromises()
    await wrapper.get('input[placeholder="当前密码"]').setValue('Pass123')
    await wrapper.get('input[placeholder="当前动态验证码"]').setValue('123456')
    await click(wrapper, '关闭两步验证')
    expect(wrapper.get<HTMLInputElement>('input[placeholder="当前密码"]').element.value).toBe(
      'Pass123',
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('生成配置取消不提示错误并结束loading', async () => {
    const { wrapper } = mountPage(TwoFactorSettings, (config) => {
      if (config.url?.endsWith('/status')) return ok({ enabled: false })
      throw new CanceledError()
    })
    await flushPromises()
    await click(wrapper, '生成配置')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.find('button').classes()).not.toContain('is-loading')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})

describe('登录设备撤销', () => {
  beforeEach(() => {
    // Element Plus 的声明为交叉类型，confirm 运行时返回动作字符串。
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  })

  it('撤销失败保留列表且不提示成功', async () => {
    const { wrapper, request } = mountPage(LoginSessions, (config) =>
      config.method === 'get' ? ok(sessions) : fail(403, 'CSRF_TOKEN_INVALID'),
    )
    await flushPromises()
    await click(wrapper, '撤销')
    expect(wrapper.text()).toContain('Test browser')
    expect(request.mock.calls.filter(([config]) => config.method === 'get')).toHaveLength(1)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('确认框取消不发请求也不显示错误', async () => {
    vi.mocked(ElMessageBox.confirm).mockRejectedValue('cancel')
    const { wrapper, request } = mountPage(LoginSessions, () => ok(sessions))
    await flushPromises()
    await click(wrapper, '撤销其他设备')
    expect(request).toHaveBeenCalledTimes(1)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('撤销已成功但回读失败时准确提示加载失败', async () => {
    let reads = 0
    const { wrapper } = mountPage(LoginSessions, (config) =>
      config.method === 'get' ? (++reads === 1 ? ok(sessions) : fail()) : ok(),
    )
    await flushPromises()
    await click(wrapper, '撤销其他设备')
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('其他登录设备已撤销')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('服务暂不可用')
    expect(wrapper.text()).toContain('Test browser')
  })
})
