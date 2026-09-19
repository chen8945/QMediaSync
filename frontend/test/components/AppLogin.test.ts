// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { AxiosError, CanceledError } from 'axios'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppLogin from '@/components/AppLogin.vue'
import LoginForm from '@/components/auth/LoginForm.vue'
import { httpKey } from '@/http/client'
import { ElMessage } from 'element-plus'

const __dirname = dirname(fileURLToPath(import.meta.url))
const messageError = vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
const messageSuccess = vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)

const createHTTP = () => ({
  get: vi.fn().mockResolvedValue({ data: { code: 200, data: { required: false } } }),
  post: vi.fn().mockResolvedValue({ data: { code: 500, message: '登录失败' } }),
})

const createWrapper = async (http = createHTTP()) => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/login', component: AppLogin },
    ],
  })
  await router.push('/login')
  await router.isReady()

  const wrapper = mount(AppLogin, {
    global: {
      plugins: [createPinia(), router],
      provide: {
        [httpKey]: http,
      },
      stubs: {
        ElForm: {
          name: 'ElForm',
          props: ['model', 'rules'],
          methods: { validate: async () => true },
          template: '<form v-bind="$attrs" @submit="$emit(\'submit\', $event)"><slot /></form>',
        },
        ElFormItem: { props: ['prop'], template: '<div><slot /></div>' },
        ElInput: {
          props: ['modelValue', 'type', 'name', 'autocomplete', 'placeholder', 'disabled', 'id'],
          template:
            '<input :id="id" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" :type="type || \'text\'" :name="name" :autocomplete="autocomplete" :placeholder="placeholder" :disabled="disabled" />',
        },
        ElCheckbox: {
          props: ['modelValue', 'disabled'],
          template:
            '<label><input type="checkbox" :checked="modelValue" :disabled="disabled" /> <slot /></label>',
        },
        ElButton: {
          props: ['type', 'nativeType', 'loading'],
          template:
            '<button :type="nativeType || \'button\'" :disabled="loading"><slot /></button>',
        },
      },
    },
  })

  await flushPromises()
  return { http, router, wrapper }
}

describe('AppLogin', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('使用可被密码管理器识别的原生登录表单语义', async () => {
    const { wrapper } = await createWrapper()

    const form = wrapper.find('form')
    const usernameInput = wrapper.find('input[name="username"]')
    const passwordInput = wrapper.find('input[name="password"]')
    const loginButton = wrapper.find('button')

    expect(form.attributes('autocomplete')).toBe('on')
    expect(usernameInput.attributes('autocomplete')).toBe('username')
    expect(passwordInput.attributes('type')).toBe('password')
    expect(passwordInput.attributes('autocomplete')).toBe('current-password')
    expect(loginButton.attributes('type')).toBe('submit')
  })

  it('登录页校验用户名非空和长度上限，密码只校验非空', async () => {
    const { wrapper } = await createWrapper()
    const form = wrapper.findComponent({ name: 'ElForm' })

    const usernameRule = form.props('rules').username[0]
    const passwordRule = form.props('rules').password[0]
    let error: Error | undefined

    usernameRule.validator({}, 'ab', (nextError?: Error) => {
      error = nextError
    })
    expect(error).toBeUndefined()

    usernameRule.validator({}, 'abcdefghijklmnopqrstu', (nextError?: Error) => {
      error = nextError
    })
    expect(error).toMatchObject({ message: '用户名长度不能超过 20 个字符' })

    error = undefined
    usernameRule.validator({}, ' ', (nextError?: Error) => {
      error = nextError
    })
    expect(error).toMatchObject({ message: '请输入用户名' })

    error = undefined
    passwordRule.validator({}, '12345', (nextError?: Error) => {
      error = nextError
    })
    expect(error).toBeUndefined()

    passwordRule.validator({}, '', (nextError?: Error) => {
      error = nextError
    })
    expect(error).toMatchObject({ message: '请输入密码' })
  })

  it('不混用 click 和 keyup enter 触发登录', () => {
    const source = readFileSync(
      resolve(__dirname, '../../src/components/auth/LoginForm.vue'),
      'utf-8',
    )

    expect(source).not.toContain('@click="handleSubmit"')
    expect(source).not.toContain('@keyup.enter="handleSubmit"')
    expect(source).toContain('@submit.prevent="handleSubmit"')
    expect(source).toContain('native-type="submit"')
  })

  it('登录成功后确认会话 Cookie 可用才跳转首页', async () => {
    const http = {
      get: vi
        .fn()
        .mockResolvedValueOnce({ data: { code: 200, data: { required: false } } })
        .mockResolvedValueOnce({
          data: {
            code: 200,
            data: {
              authenticated: true,
              user: { id: '1', username: 'admin', role: 'admin' },
              csrf_token: 'csrf-token',
              session: { session_id: 'sid', expires_at: 1 },
            },
          },
        }),
      post: vi.fn().mockResolvedValue({ data: { code: 200, data: {} } }),
    }
    const { router, wrapper } = await createWrapper(http)

    wrapper.findComponent(LoginForm).vm.$emit('submit', {
      username: 'admin',
      password: 'admin123',
      rememberMe: false,
      totp_code: '',
    })
    await flushPromises()

    expect(http.post).toHaveBeenCalledWith(
      expect.stringContaining('/login'),
      {
        username: 'admin',
        password: 'admin123',
        totp_code: '',
        rememberMe: false,
      },
      expect.objectContaining({ skipAuthInvalidation: true }),
    )
    expect(http.get).toHaveBeenCalledTimes(2)
    expect(http.get).toHaveBeenLastCalledWith(expect.stringContaining('/session'), {
      skipAuthInvalidation: true,
      withCredentials: true,
    })
    expect(router.currentRoute.value.path).toBe('/')
  })

  it('登录后会话 Cookie 未建立时保持在登录页', async () => {
    const http = {
      get: vi
        .fn()
        .mockResolvedValueOnce({ data: { code: 200, data: { required: false } } })
        .mockResolvedValueOnce({ data: { code: 200, data: { authenticated: false } } }),
      post: vi.fn().mockResolvedValue({ data: { code: 200, data: {} } }),
    }
    const { router, wrapper } = await createWrapper(http)

    wrapper.findComponent(LoginForm).vm.$emit('submit', {
      username: 'admin',
      password: 'admin123',
      rememberMe: false,
      totp_code: '',
    })
    await flushPromises()

    expect(http.get).toHaveBeenCalledTimes(2)
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('登录后会话查询不可用时不提示 Cookie 设置问题', async () => {
    const http = {
      get: vi
        .fn()
        .mockResolvedValueOnce({ data: { code: 200, data: { required: false } } })
        .mockRejectedValueOnce(new Error('session service unavailable')),
      post: vi.fn().mockResolvedValue({ data: { code: 200, data: {} } }),
    }
    const { router, wrapper } = await createWrapper(http)

    wrapper.findComponent(LoginForm).vm.$emit('submit', {
      username: 'admin',
      password: 'admin123',
      rememberMe: false,
      totp_code: '',
    })
    await flushPromises()

    expect(messageError).toHaveBeenCalledWith('登录会话验证失败，请稍后重试')
    expect(messageError).not.toHaveBeenCalledWith(
      '登录会话未能建立，请允许本站 Cookie 后重试；若问题持续，请清除本站点数据或停用拦截扩展',
    )
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it.each(['用户不存在', '密码错误', '动态验证码错误'])(
    '凭据失败 %s 只提示登录失败并保留输入',
    async () => {
      const http = createHTTP()
      // 后端对各类凭据失败统一返回“登录失败”
      http.post.mockResolvedValue({
        status: 200,
        data: { code: 500, message: '登录失败', data: null },
      })
      const { router, wrapper } = await createWrapper(http)
      await wrapper.get('input[name="username"]').setValue('admin')
      await wrapper.get('input[name="password"]').setValue('secret123')
      await wrapper.get('input[name="one-time-code"]').setValue('654321')
      await wrapper.get('form').trigger('submit')
      await flushPromises()

      expect(messageError).toHaveBeenCalledExactlyOnceWith('登录失败')
      expect(messageSuccess).not.toHaveBeenCalled()
      expect(http.post).toHaveBeenCalledTimes(1)
      expect(http.get).toHaveBeenCalledTimes(1)
      expect(wrapper.get<HTMLInputElement>('input[name="username"]').element.value).toBe('admin')
      expect(wrapper.get<HTMLInputElement>('input[name="password"]').element.value).toBe(
        'secret123',
      )
      expect(wrapper.get<HTMLInputElement>('input[name="one-time-code"]').element.value).toBe(
        '654321',
      )
      expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
      expect(router.currentRoute.value.path).toBe('/login')
      expect(consoleError).toHaveBeenCalledWith('登录错误：', { status: 200 })
    },
  )

  it.each([
    [403, '请求来源无效', '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置'],
    [403, 'CSRF 校验失败', '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录'],
    [429, '请求过于频繁，请 83 秒后再试', '请求过于频繁，请 83 秒后再试'],
  ])('登录 HTTP %s 错误按原因展示一次', async (status, message, expected) => {
    const http = createHTTP()
    http.post.mockRejectedValue({ response: { status, data: { code: 500, message } } })
    const { router, wrapper } = await createWrapper(http)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageError).toHaveBeenCalledExactlyOnceWith(expected)
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('登录主动取消不提示、不验证会话或跳转', async () => {
    const http = createHTTP()
    http.post.mockRejectedValue(new CanceledError('cancelled'))
    const { router, wrapper } = await createWrapper(http)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageError).not.toHaveBeenCalled()
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
    expect(http.get).toHaveBeenCalledTimes(1)
    expect(router.currentRoute.value.path).toBe('/login')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
  })

  it('登录后会话无响应时展示连接提示，不误判 Cookie', async () => {
    const http = createHTTP()
    http.post.mockResolvedValue({ data: { code: 200, data: null } })
    http.get
      .mockResolvedValueOnce({ data: { code: 200, data: { required: false } } })
      .mockRejectedValueOnce(new AxiosError('Network Error', 'ERR_NETWORK'))
    const { router, wrapper } = await createWrapper(http)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageError).toHaveBeenCalledExactlyOnceWith(
      '无法获取服务器响应，请检查网络连接、服务状态或访问配置',
    )
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(router.currentRoute.value.path).toBe('/login')
  })
})
