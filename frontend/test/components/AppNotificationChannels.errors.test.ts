import axios, { CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElDialog, ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppNotificationChannels from '@/components/AppNotificationChannels.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { ChannelType, NotificationConfig } from '@/utils/notificationUtils'

const success = (data: unknown = null) => ({ status: 200, data: { code: 0, message: '', data } })
const failure = (
  message = 'private token=https://user:password@notify.example',
  status = 200,
  error_code?: string,
) => ({ status, data: { code: 1, message, data: null, error_code } })
const initialConfig: NotificationConfig = {
  bot_token: 'stored-token',
  chat_id: '123',
  nickname: '昵称',
  device_key: '设备',
  sc_key: 'SCKEY',
  endpoint: 'https://notify.example',
  server_url: 'https://bark.example',
  method: 'POST',
  format: 'json',
  template: '{{content}}',
  headers: {},
}
const createHTTP = (type: ChannelType = 'telegram') => {
  const channel = {
    id: 7,
    channel_type: type,
    channel_name: '通知渠道',
    is_enabled: true,
    created_at: 1,
    updated_at: 1,
  }
  const reply = vi.fn(async () => success())
  const read = vi.fn(async (url: string) => {
    if (url.endsWith('/channels')) return success([channel])
    if (url.endsWith('/rules'))
      return success([{ id: 5, channel_id: 7, event_type: 'sync_finish', is_enabled: true }])
    return success({ channel, config: initialConfig })
  })
  const adapter = vi.fn(async (config) => ({
    config,
    statusText: '',
    headers: {},
    ...(config.method === 'get' ? await read(config.url) : await reply()),
  }))
  return { http: axios.create({ adapter }), adapter, reply, read }
}
const mountPage = async (transport = createHTTP()) => {
  const wrapper = mount(AppNotificationChannels, {
    attachTo: document.body,
    global: {
      provide: { [httpKey]: transport.http },
      stubs: {
        PageHeader: { template: '<header><slot name="actions"/></header>' },
        teleport: true,
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
      },
    },
  })
  await flushPromises()
  return wrapper
}
const click = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find((item) => item.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}
const dialog = (wrapper: VueWrapper, title: string) =>
  wrapper.findAllComponents(ElDialog).find((item) => item.props('title') === title)!
const field = (wrapper: VueWrapper, label: string) => {
  const item = wrapper
    .findAll('.el-form-item')
    .find(
      (item) =>
        item.find('.el-form-item__label').exists() &&
        item.find('.el-form-item__label').text().trim() === label,
    )
  expect(item, label).toBeDefined()
  return item!.get('input,textarea')
}
const createForm = async (wrapper: VueWrapper, type: ChannelType = 'telegram') => {
  await click(wrapper, '添加渠道')
  const form = dialog(wrapper, '添加通知渠道')
  await form.get(`.channel-type-card.type-${type}`).trigger('click')
  await field(form, '渠道名称').setValue('保留渠道名称')
  return form
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => vi.restoreAllMocks())

describe('通知渠道失败反馈', () => {
  it.each([
    [failure('private'), '创建渠道失败'],
    [failure('bot_token：不能为空', 400), '请填写 Bot Token'],
    [failure('private', 403, 'REQUEST_ORIGIN_INVALID'), '访问地址校验失败'],
    [failure('CSRF 校验失败', 403), '请求安全校验失败'],
  ])('创建失败保留输入，不关闭或刷新：%s', async (reply, message) => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(reply)
    const wrapper = await mountPage(transport)
    const form = await createForm(wrapper)
    await field(form, 'Bot Token').setValue('private-token')
    await field(form, 'Chat ID').setValue('456')
    await click(form, '创建')
    expect(form.props('modelValue')).toBe(true)
    expect((field(form, 'Bot Token').element as HTMLInputElement).value).toBe('private-token')
    expect((field(form, '渠道名称').element as HTMLInputElement).value).toBe('保留渠道名称')
    expect(transport.read).toHaveBeenCalledTimes(1)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(expect.stringContaining(message))
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it.each(['cancel', 'handled'])('创建请求 %s 时保留表单并静默', async (kind) => {
    const error =
      kind === 'cancel'
        ? new CanceledError()
        : new HttpResponseError({ status: 401, data: { code: 500 } })
    if (kind === 'handled') markAuthInvalidationHandled(error)
    const transport = createHTTP()
    transport.reply.mockRejectedValue(error)
    const wrapper = await mountPage(transport)
    const form = await createForm(wrapper)
    await field(form, 'Bot Token').setValue('private-token')
    await field(form, 'Chat ID').setValue('456')
    await click(form, '创建')
    expect(form.props('modelValue')).toBe(true)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('编辑失败保留输入和配置，不执行列表刷新', async () => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(failure())
    const wrapper = await mountPage(transport)
    await click(wrapper, '编辑')
    const form = dialog(wrapper, '编辑渠道 - 通知渠道')
    await field(form, 'Bot Token').setValue('changed-private')
    await click(form, '保存')
    expect(form.props('modelValue')).toBe(true)
    expect((field(form, 'Bot Token').element as HTMLInputElement).value).toBe('changed-private')
    expect(transport.read).toHaveBeenCalledTimes(2)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('更新渠道失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('切换渠道失败恢复旧开关状态，不提示成功', async () => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(failure())
    const wrapper = await mountPage(transport)
    await wrapper.get('.channel-card-header .el-switch').trigger('click')
    await flushPromises()
    expect(JSON.parse(transport.adapter.mock.calls.at(-1)![0].data)).toEqual({
      channel_id: 7,
      is_enabled: false,
    })
    expect(wrapper.get('.channel-card-header .el-switch').classes()).toContain('is-checked')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('切换渠道状态失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('规则保存失败恢复旧开关状态，保留事件和渠道 ID', async () => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(failure())
    const wrapper = await mountPage(transport)
    await click(wrapper, '规则')
    const form = dialog(wrapper, '通知渠道 - 通知规则')
    await form.get('.el-switch').trigger('click')
    await flushPromises()
    expect(transport.adapter.mock.calls.at(-1)![0].url).toBe('/api/setting/notification/rules')
    expect(JSON.parse(transport.adapter.mock.calls.at(-1)![0].data)).toEqual({
      channel_id: 7,
      event_type: 'sync_finish',
      is_enabled: false,
    })
    expect(form.get('.el-switch').classes()).toContain('is-checked')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('更新通知规则失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each([
    [
      failure('测试失败：https://api.telegram.org/botprivate/sendMessage'),
      '发送通知测试消息失败，请检查渠道配置或查看服务日志',
    ],
    [failure('private', 403, 'REQUEST_ORIGIN_INVALID'), '访问地址校验失败'],
  ])('测试失败只提示一次安全原因：%s', async (reply, message) => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(reply)
    const wrapper = await mountPage(transport)
    await click(wrapper, '测试')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith(expect.stringContaining(message))
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it('删除业务失败不移除渠道也不刷新列表', async () => {
    const transport = createHTTP()
    transport.reply.mockResolvedValue(failure())
    const wrapper = await mountPage(transport)
    await click(wrapper, '删除')
    expect(wrapper.get('.channel-name').text()).toBe('通知渠道')
    expect(transport.read).toHaveBeenCalledTimes(1)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('删除渠道失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'close'])('取消删除 %s 不发送删除请求或提示', async (reason) => {
    vi.mocked(ElMessageBox.confirm).mockRejectedValue(reason)
    const transport = createHTTP()
    const wrapper = await mountPage(transport)
    await click(wrapper, '删除')
    expect(transport.reply).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('读取配置失败不打开编辑框或显示原始错误', async () => {
    const transport = createHTTP()
    const wrapper = await mountPage(transport)
    transport.read.mockResolvedValue(failure())
    await click(wrapper, '编辑')
    expect(dialog(wrapper, '编辑渠道 - 通知渠道').props('modelValue')).toBe(false)
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('获取渠道配置失败')
  })

  it('保存成功后的刷新失败仍报告刷新失败，并保留已有列表', async () => {
    const transport = createHTTP()
    const wrapper = await mountPage(transport)
    await click(wrapper, '编辑')
    const form = dialog(wrapper, '编辑渠道 - 通知渠道')
    transport.read.mockResolvedValue(failure())
    await click(form, '保存')
    expect(form.props('modelValue')).toBe(false)
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('更新成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('加载通知渠道失败')
    expect(wrapper.get('.channel-name').text()).toBe('通知渠道')
  })
})

describe('通知渠道创建字段兼容', () => {
  it('Webhook GET 保留查询参数鉴权，不发送 POST 数据格式', async () => {
    const transport = createHTTP('webhook')
    const wrapper = await mountPage(transport)
    const form = await createForm(wrapper, 'webhook')
    await field(form, '请求地址').setValue('https://hook.example')
    await field(form, '消息模板').setValue('{{content}}')
    await form.get('select[placeholder="选择请求方法"]').setValue('GET')
    await field(form, '查询参数名').setValue('message')
    await form.get('select[placeholder="选择鉴权方式"]').setValue('query')
    await field(form, '参数值').setValue('private-token')
    await field(form, '参数名').setValue('access_key')
    await click(form, '创建')
    const request = transport.adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(JSON.parse(request.data)).toEqual({
      channel_name: '保留渠道名称',
      endpoint: 'https://hook.example',
      method: 'GET',
      template: '{{content}}',
      query_param: 'message',
      auth_type: 'query',
      auth_token: 'private-token',
      auth_query_key: 'access_key',
    })
  })

  it('Webhook 编辑清空请求头和关闭鉴权时显式提交空头与 none', async () => {
    const transport = createHTTP('webhook')
    const wrapper = await mountPage(transport)
    transport.read.mockResolvedValueOnce(
      success({
        channel: { channel_name: '通知渠道' },
        config: {
          ...initialConfig,
          auth_type: 'bearer',
          auth_token: 'private-token',
          headers: { 'X-Api-Key': 'private-key' },
        },
      }),
    )
    await click(wrapper, '编辑')
    const form = dialog(wrapper, '编辑渠道 - 通知渠道')
    await form.get('.webhook-header-row button').trigger('click')
    await form.get('select[placeholder="选择鉴权方式"]').setValue('none')
    await click(form, '保存')
    const request = transport.adapter.mock.calls.find(([config]) => config.method === 'put')![0]
    const payload = JSON.parse(request.data)
    expect(payload).toMatchObject({
      channel_id: 7,
      channel_name: '通知渠道',
      headers: {},
      auth_type: 'none',
    })
    expect(payload).not.toHaveProperty('auth_token')
    expect(form.props('modelValue')).toBe(false)
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('更新成功')
  })

  const cases: Array<[ChannelType, Record<string, string>, Record<string, string>]> = [
    [
      'telegram',
      { 'Bot Token': 'token-value', 'Chat ID': '456' },
      { bot_token: 'token-value', chat_id: '456' },
    ],
    [
      'meow',
      { 昵称: 'meow-name', 'API 地址': 'https://meow.example' },
      { nickname: 'meow-name', endpoint: 'https://meow.example' },
    ],
    [
      'bark',
      {
        设备密钥: 'bark-key',
        服务器地址: 'https://bark.example',
        通知声音: 'alert',
        通知图标: 'https://bark.example/icon.png',
      },
      {
        device_key: 'bark-key',
        server_url: 'https://bark.example',
        sound: 'alert',
        icon: 'https://bark.example/icon.png',
      },
    ],
    [
      'serverchan',
      { SCKEY: 'sc-key', 'API 地址': 'https://server.example' },
      { sc_key: 'sc-key', endpoint: 'https://server.example' },
    ],
    [
      'webhook',
      { 请求地址: 'https://hook.example', 消息模板: '{"title":"{{title}}"}' },
      {
        endpoint: 'https://hook.example',
        method: 'POST',
        format: 'json',
        template: '{"title":"{{title}}"}',
      },
    ],
  ]
  it.each(cases)('%s 创建保留字段，code=0 成功后关闭并刷新', async (type, inputs, expected) => {
    const transport = createHTTP(type)
    const wrapper = await mountPage(transport)
    const form = await createForm(wrapper, type)
    for (const [label, value] of Object.entries(inputs)) await field(form, label).setValue(value)
    await click(form, '创建')
    const request = transport.adapter.mock.calls.find(([config]) => config.method === 'post')![0]
    expect(request.url).toBe(`/api/setting/notification/channels/${type}`)
    expect(JSON.parse(request.data)).toEqual({ channel_name: '保留渠道名称', ...expected })
    expect(form.props('modelValue')).toBe(false)
    expect(transport.read).toHaveBeenCalledTimes(2)
    expect(ElMessage.success).toHaveBeenCalledExactlyOnceWith('创建成功')
  })
})
