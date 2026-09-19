// @vitest-environment happy-dom
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CanceledError } from 'axios'
import { ElMessage } from 'element-plus'

import AppLogin from '@/components/AppLogin.vue'
import { httpKey } from '@/http/client'

const router = {
  push: vi.fn(),
  replace: vi.fn(),
  currentRoute: {
    value: {
      query: {},
    },
  },
}

const messageError = vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
const messageSuccess = vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
vi.spyOn(console, 'error').mockImplementation(() => undefined)

vi.mock('vue-router', () => ({
  useRouter: () => router,
}))

const mountLogin = (http: { get: ReturnType<typeof vi.fn>; post: ReturnType<typeof vi.fn> }) =>
  mount(AppLogin, {
    global: {
      plugins: [createPinia()],
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

describe('AppLogin 初始化模式', () => {
  beforeEach(() => vi.clearAllMocks())

  it('用户表为空时显示创建管理员表单', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: { required: true },
        },
      }),
      post: vi.fn(),
    }
    const wrapper = mountLogin(http)

    await flushPromises()

    expect(wrapper.text()).toContain('创建管理员')
    expect(wrapper.text()).toContain('初始化码')
  })

  it('用户表已有用户时显示登录表单', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({
        data: {
          code: 200,
          data: { required: false },
        },
      }),
      post: vi.fn(),
    }
    const wrapper = mountLogin(http)

    await flushPromises()

    expect(wrapper.text()).toContain('系统登录')
    expect(wrapper.text()).toContain('登录')
  })

  it.each([
    ['初始化码无效', '初始化码无效'],
    ['创建管理员失败：username：不能为空', '创建管理员失败：用户名：不能为空'],
    [
      '创建管理员失败：username：长度必须在 3 到 20 个字符之间',
      '创建管理员失败：用户名：长度必须在 3 到 20 个字符之间',
    ],
    ['创建管理员失败：username：只能包含英文和数字', '创建管理员失败：用户名：只能包含英文和数字'],
    ['创建管理员失败：password：长度不能小于 6', '创建管理员失败：密码：长度不能小于 6'],
    [
      '创建管理员失败：password：不能是纯数字或纯字母',
      '创建管理员失败：密码：不能是纯数字或纯字母',
    ],
    ['创建管理员失败：数据库繁忙', '创建管理员失败：数据库繁忙'],
    ['创建管理员失败：username：不能为空', '创建管理员失败：用户名：不能为空'],
  ])('创建失败 %s 保留输入且不切换登录表单', async (message, expected) => {
    const http = {
      get: vi.fn().mockResolvedValue({ data: { code: 200, data: { required: true } } }),
      post: vi.fn().mockResolvedValue({ data: { code: 500, message, data: null } }),
    }
    const wrapper = mountLogin(http)
    await flushPromises()
    await wrapper.get('input[name="setup-token"]').setValue('setup-secret')
    await wrapper.get('input[name="new-password"]').setValue('secret123')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageError).toHaveBeenCalledExactlyOnceWith(expected)
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('创建管理员')
    expect(wrapper.get<HTMLInputElement>('input[name="setup-token"]').element.value).toBe(
      'setup-secret',
    )
    expect(wrapper.get<HTMLInputElement>('input[name="new-password"]').element.value).toBe(
      'secret123',
    )
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('创建成功接受空 data 并切换登录表单', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({ data: { code: 200, data: { required: true } } }),
      post: vi.fn().mockResolvedValue({ data: { code: 200, data: null } }),
    }
    const wrapper = mountLogin(http)
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageSuccess).toHaveBeenCalledExactlyOnceWith('管理员创建成功，请登录')
    expect(messageError).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('系统登录')
    expect(wrapper.find('input[name="setup-token"]').exists()).toBe(false)
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('创建请求取消时静默保留初始化表单', async () => {
    const http = {
      get: vi.fn().mockResolvedValue({ data: { code: 200, data: { required: true } } }),
      post: vi.fn().mockRejectedValue(new CanceledError()),
    }
    const wrapper = mountLogin(http)
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(messageError).not.toHaveBeenCalled()
    expect(messageSuccess).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('创建管理员')
    expect(wrapper.get('button').attributes('disabled')).toBeUndefined()
  })
})
