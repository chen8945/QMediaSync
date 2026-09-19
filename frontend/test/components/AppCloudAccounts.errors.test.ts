import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import axios, { AxiosError, CanceledError, type InternalAxiosRequestConfig } from 'axios'
import { ElDialog, ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppCloudAccounts from '@/components/AppCloudAccounts.vue'
import V115AuthorizationChangeDialog from '@/components/cloud-auth/V115AuthorizationChangeDialog.vue'
import V115AuthorizationDialog from '@/components/cloud-auth/V115AuthorizationDialog.vue'
import type { CloudAccount } from '@/api/accounts'
import { httpKey } from '@/http/client'
import { markAuthInvalidationHandled } from '@/http/errors'
import {
  loadPendingV115Authorization,
  savePendingV115Authorization,
} from '@/utils/v115AuthorizationSession'

interface Reply {
  status: number
  data: { code: number; data: unknown; message?: string; error_code?: string }
}
const success = (data: unknown = null): Reply => ({ status: 200, data: { code: 200, data } })
const failure = (message = 'internal token=private', status = 200, error_code?: string): Reply => ({
  status,
  data: { code: 500, data: null, message, error_code },
})
const account: CloudAccount = {
  id: 42,
  source_type: '115',
  name: '我的网盘',
  user_id: 'user-id',
  username: '用户',
  base_url: '',
  created_at: 1,
  authorized: false,
  auth_source_type: 'third_party_service',
  auth_provider: 'moviepilot',
  app_id: 'moviepilot',
}
const replacement = {
  auth_source_type: 'third_party_service',
  auth_provider: 'moviepilot',
  app_id: 'moviepilot',
  app_id_name: 'MoviePilot',
}
const oauthURL = success({
  auth_url: 'https://authorize.example/login',
  state: 'private-state',
  polling: true,
})
const wrappers: VueWrapper[] = []

function createHTTP(
  respond: (config: InternalAxiosRequestConfig) => Reply | Promise<Reply> = () => success(),
  rows: CloudAccount[] = [account],
) {
  // 保留 Axios、领域 API 和错误解析，仅替换网络传输。
  const request = vi.fn(async (config: InternalAxiosRequestConfig) => {
    const reply = config.url === '/api/account/list' ? success(rows) : await respond(config)
    const response = { ...reply, config, headers: {}, statusText: '' }
    if (reply.status >= 400) {
      throw new AxiosError('Request failed', 'ERR_BAD_RESPONSE', config, {}, response)
    }
    return response
  })
  const http = axios.create({ adapter: request })
  const calls = (url: string) =>
    request.mock.calls.map(([config]) => config).filter((config) => config.url === url)
  return { http, request, calls }
}

const mountPage = async (transport = createHTTP()) => {
  const wrapper = mount(AppCloudAccounts, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: transport.http },
      stubs: {
        PageHeader: { template: '<header><slot name="actions"/><slot name="stats"/></header>' },
        PageStats: true,
        V115AppSelector: true,
        V115AuthorizationDialog: true,
        V115AuthorizationChangeDialog: true,
        ElSelect: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template:
            '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>',
        },
        ElOption: {
          props: ['value', 'label'],
          template: '<option :value="value">{{ label }}</option>',
        },
        teleport: true,
        Warning: true,
        Cloudy: true,
      },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
const click = async (wrapper: VueWrapper, text: string) => {
  const button = wrapper.findAll('button').find((item) => item.text() === text)
  expect(button, text).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}
const dialog = (wrapper: VueWrapper, title: string) =>
  wrapper.findAllComponents(ElDialog).find((item) => item.props('title') === title)!
const openAddForm = async (wrapper: VueWrapper, source = '115') => {
  await click(wrapper, '添加账号')
  const form = dialog(wrapper, '添加账号')
  await form.get('select').setValue(source)
  if (source !== 'openlist')
    await form.get('input[placeholder="请输入账号备注"]').setValue('保留备注')
  return form
}
const prepareReplacement = async (wrapper: VueWrapper, payload = replacement) => {
  await click(wrapper, '更换授权')
  wrapper.getComponent(V115AuthorizationChangeDialog).vm.$emit('confirmed', payload)
  wrapper.getComponent(V115AuthorizationChangeDialog).vm.$emit('update:visible', false)
  await flushPromises()
}
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.spyOn(window, 'open').mockReturnValue(null)
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  window.history.replaceState({}, '', '/')
  window.sessionStorage.clear()
})
afterEach(async () => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  await flushPromises()
  vi.useRealTimers()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('云盘账号请求失败', () => {
  it('无响应时保留添加输入且只显示安全提示，不再次抛出 TypeError', async () => {
    const transport = createHTTP((config) => {
      throw new AxiosError('credential-private', 'ERR_NETWORK', config)
    })
    const wrapper = await mountPage(transport)
    const form = await openAddForm(wrapper)
    await click(form, '确定')

    expect(form.props('modelValue')).toBe(true)
    expect(form.get<HTMLInputElement>('input[placeholder="请输入账号备注"]').element.value).toBe(
      '保留备注',
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '无法获取服务器响应，请检查网络连接、服务状态或访问配置',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(transport.calls('/api/account/list')).toHaveLength(1)
    expect(console.error).toHaveBeenCalledExactlyOnceWith('添加账号失败', {
      method: 'POST',
      path: '/api/account/add',
    })
  })

  it.each([
    [failure('创建开放平台账号失败：账号备注已存在，请换一个'), '账号备注已存在，请换一个'],
    [failure(), '添加账号失败'],
    [
      failure('private', 403, 'REQUEST_ORIGIN_INVALID'),
      '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
    ],
    [failure('CSRF 校验失败', 403), '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录'],
  ])('添加失败保留输入且不刷新列表：%s', async (reply, message) => {
    const transport = createHTTP(() => reply)
    const wrapper = await mountPage(transport)
    const form = await openAddForm(wrapper)
    await click(form, '确定')
    expect(form.props('modelValue')).toBe(true)
    expect(form.get<HTMLInputElement>('input[placeholder="请输入账号备注"]').element.value).toBe(
      '保留备注',
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(message)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(transport.calls('/api/account/list')).toHaveLength(1)
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it.each(['cancelled', 'handled'])('添加请求 %s 时静默且保留表单', async (kind) => {
    const transport = createHTTP((config) => {
      const error =
        kind === 'cancelled'
          ? new CanceledError()
          : new AxiosError(
              'private',
              '',
              config,
              {},
              { ...failure('', 401), headers: {}, statusText: '', config },
            )
      if (kind === 'handled') markAuthInvalidationHandled(error)
      throw error
    })
    const wrapper = await mountPage(transport)
    const form = await openAddForm(wrapper)
    await click(form, '确定')
    expect(form.props('modelValue')).toBe(true)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('成功后才关闭、清空表单并刷新列表，保留 115 授权来源字段', async () => {
    const transport = createHTTP()
    const wrapper = await mountPage(transport)
    await click(await openAddForm(wrapper), '确定')
    expect(JSON.parse(transport.calls('/api/account/add')[0]!.data)).toEqual({
      source_type: '115',
      name: '保留备注',
      app_id: '100197849',
      app_id_name: 'QMediaSync',
      auth_source_type: 'built_in_appid',
      auth_provider: 'official_pkce',
    })
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('添加账号成功')
    expect(transport.calls('/api/account/list')).toHaveLength(2)
    await click(wrapper, '添加账号')
    expect(
      dialog(wrapper, '添加账号').get<HTMLInputElement>('input[placeholder="请输入账号备注"]')
        .element.value,
    ).toBe('')
  })

  it('OpenList 创建失败保留凭据，日志中不包含凭据', async () => {
    const transport = createHTTP(() => failure('password-private token-private'))
    const wrapper = await mountPage(transport)
    const form = await openAddForm(wrapper, 'openlist')
    await form
      .get('input[placeholder="请输入 OpenList 地址：http://ip:5244"]')
      .setValue('https://openlist.example')
    await form.get('input[placeholder="请输入用户名"]').setValue('account-user')
    await form.get('input[placeholder="请输入密码"]').setValue('password-private')
    await click(form, '确定')
    expect(JSON.parse(transport.calls('/api/account/openlist')[0]!.data)).toEqual({
      source_type: 'openlist',
      name: '',
      base_url: 'https://openlist.example',
      auth_type: 'password',
      username: 'account-user',
      password: 'password-private',
    })
    expect(form.get<HTMLInputElement>('input[placeholder="请输入密码"]').element.value).toBe(
      'password-private',
    )
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('添加账号失败')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it('OpenList 编辑凭据失败不继续保存资料，保留修改输入', async () => {
    const transport = createHTTP(
      () => failure(),
      [
        {
          ...account,
          source_type: 'openlist',
          auth_type: 'token',
          base_url: 'https://old.example',
        },
      ],
    )
    const wrapper = await mountPage(transport)
    await click(wrapper, '编辑')
    const form = dialog(wrapper, '编辑 OpenList 账号')
    await form.get('input[placeholder="请输入令牌"]').setValue('token-private')
    await click(form, '确定')
    expect(JSON.parse(transport.calls('/api/account/openlist')[0]!.data)).toEqual({
      id: 42,
      base_url: 'https://old.example',
      auth_type: 'token',
      token: 'token-private',
    })
    expect(transport.calls('/api/account/update')).toHaveLength(0)
    expect(form.props('modelValue')).toBe(true)
    expect(form.get<HTMLInputElement>('input[placeholder="请输入令牌"]').element.value).toBe(
      'token-private',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['编辑', '删除'])('%s 的 HTTP 200 业务失败不成功、不刷新', async (action) => {
    const transport = createHTTP(() => failure())
    const wrapper = await mountPage(transport)
    await click(wrapper, action)
    if (action === '编辑') {
      const form = dialog(wrapper, '编辑账号')
      await form.get('input[placeholder="请输入账号备注"]').setValue('修改保留')
      await click(form, '确定')
      expect(form.props('modelValue')).toBe(true)
      expect(form.get<HTMLInputElement>('input[placeholder="请输入账号备注"]').element.value).toBe(
        '修改保留',
      )
    }
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      action === '编辑' ? '更新账号失败' : '删除账号失败',
    )
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(transport.calls('/api/account/list')).toHaveLength(1)
  })
})

describe('云盘 OAuth 请求和生命周期', () => {
  it.each(['115', 'baidupan'] as const)('%s 获取地址失败不跳转且安全提示', async (source_type) => {
    const transport = createHTTP(
      () => failure('private', 403, 'CSRF_TOKEN_INVALID'),
      [{ ...account, source_type }],
    )
    const wrapper = await mountPage(transport)
    await click(wrapper, '授权')
    expect(transport.calls(`/api/${source_type}/oauth-url`)[0]!.params).toEqual({
      account_id: 42,
      redirect_url: window.location.href,
    })
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(
      '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
    )
    expect(window.open).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'close'])('取消授权确认 %s 不请求、不提示', async (reason) => {
    vi.mocked(ElMessageBox.confirm).mockRejectedValue(reason)
    const transport = createHTTP()
    const wrapper = await mountPage(transport)
    await click(wrapper, '授权')
    expect(transport.calls('/api/115/oauth-url')).toHaveLength(0)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('更换授权准备业务失败不启动 OAuth、不改变账号', async () => {
    const transport = createHTTP(() => failure())
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper)
    expect(transport.calls('/api/115/oauth-url')).toHaveLength(0)
    expect(loadPendingV115Authorization()).toBeNull()
    expect(wrapper.get('.card-name').text()).toBe('我的网盘')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('准备更换授权失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('更换授权 ID 贯穿准备、地址、状态；业务失败取消会话并停止轮询', async () => {
    vi.useFakeTimers()
    const transport = createHTTP((config) => {
      if (config.url?.endsWith('/prepare'))
        return success({ authorization_id: 'change-id', expires_in: 600 })
      if (config.url?.endsWith('/oauth-url')) return oauthURL
      if (config.url?.endsWith('/oauth-status')) return failure()
      return success()
    })
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper)
    expect(JSON.parse(transport.calls('/api/account/authorization/prepare')[0]!.data)).toEqual({
      account_id: 42,
      source_type: '115',
      confirmed: true,
      ...replacement,
    })
    expect(transport.calls('/api/115/oauth-url')[0]!.params).toMatchObject({
      account_id: 42,
      authorization_id: 'change-id',
    })
    await vi.advanceTimersByTimeAsync(3000)
    expect(transport.calls('/api/115/oauth-status')[0]!.params).toEqual({
      account_id: 42,
      state: 'private-state',
      authorization_id: 'change-id',
    })
    expect(JSON.parse(transport.calls('/api/account/authorization/cancel')[0]!.data)).toEqual({
      account_id: 42,
      authorization_id: 'change-id',
    })
    expect(loadPendingV115Authorization()).toBeNull()
    await vi.advanceTimersByTimeAsync(12000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(1)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('授权状态查询失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it.each(['cancelled', 'handled'])('OAuth 状态请求 %s 时静默结束并取消会话', async (kind) => {
    vi.useFakeTimers()
    const transport = createHTTP((config) => {
      if (config.url?.endsWith('/prepare'))
        return success({ authorization_id: 'change-id', expires_in: 600 })
      if (config.url?.endsWith('/oauth-url')) return oauthURL
      if (config.url?.endsWith('/oauth-status')) {
        const error =
          kind === 'cancelled'
            ? new CanceledError()
            : new AxiosError(
                'private',
                '',
                config,
                {},
                { ...failure('', 401), headers: {}, statusText: '', config },
              )
        if (kind === 'handled') markAuthInvalidationHandled(error)
        throw error
      }
      return success()
    })
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper)
    await vi.advanceTimersByTimeAsync(12000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(1)
    expect(transport.calls('/api/account/authorization/cancel')).toHaveLength(1)
    expect(loadPendingV115Authorization()).toBeNull()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('OAuth 未完成继续等待，成功后才刷新账号并消费暂存会话', async () => {
    vi.useFakeTimers()
    let done = false
    const transport = createHTTP((config) => {
      if (config.url?.endsWith('/prepare'))
        return success({ authorization_id: 'change-id', expires_in: 600 })
      if (config.url?.endsWith('/oauth-url')) return oauthURL
      return success({ done })
    })
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper)
    await vi.advanceTimersByTimeAsync(3000)
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(transport.calls('/api/account/list')).toHaveLength(1)
    expect(loadPendingV115Authorization()?.authorizationId).toBe('change-id')
    done = true
    await vi.advanceTimersByTimeAsync(12000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(2)
    expect(transport.calls('/api/account/list')).toHaveLength(2)
    expect(transport.calls('/api/account/authorization/cancel')).toHaveLength(0)
    expect(loadPendingV115Authorization()).toBeNull()
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('授权成功')
  })

  it('直接 OAuth 跳转时保留更换会话，页面卸载不提前取消', async () => {
    const transport = createHTTP((config) =>
      config.url?.endsWith('/prepare')
        ? success({ authorization_id: 'redirect-id', expires_in: 600 })
        : success('https://authorize.example/login'),
    )
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper)
    expect(window.location.href).toBe('https://authorize.example/login')
    wrapper.unmount()
    await flushPromises()
    expect(transport.calls('/api/account/authorization/cancel')).toHaveLength(0)
    expect(loadPendingV115Authorization()).toEqual({
      accountId: 42,
      authorizationId: 'redirect-id',
    })
  })

  it('隐藏时暂停、恢复可见继续轮询，请求飞行中不重叠且卸载丢弃旧成功', async () => {
    vi.useFakeTimers()
    const pending = deferred<Reply>()
    const transport = createHTTP((config) =>
      config.url?.endsWith('/oauth-url') ? oauthURL : pending.promise,
    )
    const wrapper = await mountPage(transport)
    await click(wrapper, '授权')
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(6000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(0)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(12000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(1)
    wrapper.unmount()
    pending.resolve(success({ done: true }))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(12000)
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(1)
    expect(transport.calls('/api/account/list')).toHaveLength(1)
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('卸载后的旧授权地址不能打开页面或重启轮询', async () => {
    vi.useFakeTimers()
    const pending = deferred<Reply>()
    const transport = createHTTP(() => pending.promise)
    const wrapper = await mountPage(transport)
    await click(wrapper, '授权')
    wrapper.unmount()
    pending.resolve(oauthURL)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(window.open).not.toHaveBeenCalled()
    expect(transport.calls('/api/115/oauth-status')).toHaveLength(0)
  })

  it('卸载期间完成准备的会话被回收，不打开二维码', async () => {
    const pending = deferred<Reply>()
    const transport = createHTTP((config) =>
      config.url?.endsWith('/prepare') ? pending.promise : success(),
    )
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper, { ...replacement, auth_provider: 'official_pkce' })
    wrapper.unmount()
    pending.resolve(success({ authorization_id: 'late-change', expires_in: 600 }))
    await flushPromises()
    expect(JSON.parse(transport.calls('/api/account/authorization/cancel')[0]!.data)).toEqual({
      account_id: 42,
      authorization_id: 'late-change',
    })
    expect(loadPendingV115Authorization()).toBeNull()
  })

  it('二维码更换收到授权 ID，关闭时取消并仅在业务成功后清理暂存', async () => {
    const transport = createHTTP((config) =>
      config.url?.endsWith('/prepare')
        ? success({ authorization_id: 'qr-change', expires_in: 600 })
        : failure(),
    )
    const wrapper = await mountPage(transport)
    await prepareReplacement(wrapper, { ...replacement, auth_provider: 'official_pkce' })
    const qr = wrapper.getComponent(V115AuthorizationDialog)
    expect(qr.props()).toMatchObject({ visible: true, accountId: 42, authorizationId: 'qr-change' })
    qr.vm.$emit('update:visible', false)
    await flushPromises()
    expect(transport.calls('/api/account/authorization/cancel')).toHaveLength(1)
    expect(loadPendingV115Authorization()?.authorizationId).toBe('qr-change')
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it.each(['115', 'baidupan'])('%s 回调业务失败不成功、不刷新跳转', async (source) => {
    vi.useFakeTimers()
    window.history.replaceState(
      {},
      '',
      `/?account_id=42&source=${source}&token_data=token-private&authorization_id=change-id`,
    )
    savePendingV115Authorization({ accountId: 42, authorizationId: 'change-id' })
    const transport = createHTTP((config) =>
      config.url?.endsWith('/oauth-confirm') ? failure() : success(),
    )
    await mountPage(transport)
    expect(JSON.parse(transport.calls(`/api/${source}/oauth-confirm`)[0]!.data)).toEqual({
      account_id: 42,
      data: 'token-private',
      authorization_id: 'change-id',
    })
    expect(transport.calls('/api/account/authorization/cancel')).toHaveLength(1)
    expect(loadPendingV115Authorization()).toBeNull()
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('授权确认失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(3000)
    expect(window.location.search).toContain('token_data=token-private')
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it('回调 ID 不匹配时仅取消原会话，不确认其他会话', async () => {
    window.history.replaceState(
      {},
      '',
      '/?account_id=42&token_data=private&authorization_id=other-id',
    )
    savePendingV115Authorization({ accountId: 42, authorizationId: 'original-id' })
    const transport = createHTTP()
    await mountPage(transport)
    expect(transport.calls('/api/115/oauth-confirm')).toHaveLength(0)
    expect(JSON.parse(transport.calls('/api/account/authorization/cancel')[0]!.data)).toEqual({
      account_id: 42,
      authorization_id: 'original-id',
    })
    expect(loadPendingV115Authorization()).toBeNull()
  })
})
