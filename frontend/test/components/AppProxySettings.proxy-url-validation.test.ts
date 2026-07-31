// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { flushPromises, mount } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AppProxySettings from '@/components/AppProxySettings.vue'
import {
  PROXY_CREDENTIALS_MASKED_HINT,
  PROXY_SCHEMES,
  PROXY_SCHEME_HINT,
  PROXY_URL_HELP,
  PROXY_URL_MESSAGES,
  PROXY_URL_PLACEHOLDER,
} from '@/constants/validation'
import { httpKey } from '@/http/client'

const messageError = vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)

const createHTTP = (httpProxy = '', credentialsMasked = '0') => ({
  get: vi.fn().mockResolvedValue({
    data: { code: 200, data: { http_proxy: httpProxy, credentials_masked: credentialsMasked } },
  }),
  post: vi.fn().mockResolvedValue({ data: { code: 200 } }),
})

const mountProxySettings = async (httpProxy = '', credentialsMasked = '0') => {
  const http = createHTTP(httpProxy, credentialsMasked)
  const wrapper = mount(AppProxySettings, {
    global: {
      provide: { [httpKey]: http },
      stubs: {
        ElForm: { template: '<form><slot /></form>' },
        ElFormItem: { template: '<div><slot /></div>' },
        ElInput: {
          props: ['modelValue', 'disabled', 'placeholder'],
          template:
            '<input :value="modelValue" :disabled="disabled" :placeholder="placeholder" @input="$emit(\'update:modelValue\', $event.target.value)" />',
        },
        ElButton: {
          props: ['disabled', 'loading'],
          template: '<button :disabled="disabled || loading"><slot /></button>',
        },
        ElAlert: {
          props: ['title', 'description'],
          template: '<section><strong>{{ title }}</strong><p>{{ description }}</p></section>',
        },
      },
    },
  })
  await flushPromises()

  // 通过保存按钮走完整校验路径，返回面向用户的错误提示；地址合法时返回 null
  const saveProxyUrl = async (raw: string): Promise<string | null> => {
    messageError.mockClear()
    http.post.mockClear()
    await wrapper.find('input').setValue(raw)
    const saveButton = wrapper.findAll('button').find((button) => button.text() === '保存')
    expect(saveButton, '保存按钮应存在').toBeDefined()
    await saveButton!.trigger('click')
    await wrapper.vm.$nextTick()

    if (messageError.mock.calls.length === 0) {
      return null
    }
    return String(messageError.mock.calls[0][0])
  }

  return { wrapper, http, saveProxyUrl }
}

beforeEach(() => {
  messageError.mockClear()
})

describe('AppProxySettings 代理地址校验', () => {
  it('漏写协议时提示格式问题，而不是误报缺少主机名', async () => {
    const { saveProxyUrl } = await mountProxySettings()

    expect(await saveProxyUrl('proxy.example.com:8080')).toBe(PROXY_URL_MESSAGES.format)
    expect(await saveProxyUrl('localhost:1080')).toBe(PROXY_URL_MESSAGES.format)
  })

  it('拒绝含制表符等控制字符的地址，不把它当合法端口放过去', async () => {
    const { saveProxyUrl, http } = await mountProxySettings()

    expect(await saveProxyUrl('socks5://127.0.0.1:10\t80')).toBe(PROXY_URL_MESSAGES.whitespace)
    expect(await saveProxyUrl('socks5://127.0.0.1:10 80')).toBe(PROXY_URL_MESSAGES.whitespace)
    expect(http.post).not.toHaveBeenCalled()
  })

  it('端口越界给出端口专属提示，而不是笼统的格式错误', async () => {
    const { saveProxyUrl } = await mountProxySettings()

    expect(await saveProxyUrl('socks5://127.0.0.1:99999')).toBe(PROXY_URL_MESSAGES.port)
    expect(await saveProxyUrl('socks5://127.0.0.1:65536')).toBe(PROXY_URL_MESSAGES.port)
    expect(await saveProxyUrl('socks5://127.0.0.1:0')).toBe(PROXY_URL_MESSAGES.port)
    expect(await saveProxyUrl('socks5://127.0.0.1:65535')).toBeNull()
  })

  it('省略主机名的本机代理简写是合法的，与后端一致地放行', async () => {
    const { saveProxyUrl, http } = await mountProxySettings()

    expect(await saveProxyUrl('http://:1080')).toBeNull()
    expect(http.post).toHaveBeenCalled()
    expect(await saveProxyUrl('socks5://:7890')).toBeNull()
    // 省略主机名也要照常校验端口范围
    expect(await saveProxyUrl('http://:99999')).toBe(PROXY_URL_MESSAGES.port)
  })

  it('只有协议或没有拨号目标的地址按格式错误拒绝', async () => {
    const { saveProxyUrl } = await mountProxySettings()

    expect(await saveProxyUrl('socks5://')).toBe(PROXY_URL_MESSAGES.format)
    expect(await saveProxyUrl('socks5://user:pass@')).toBe(PROXY_URL_MESSAGES.format)
  })

  it('白名单内的协议全部放行，白名单外的协议给出协议提示', async () => {
    const { saveProxyUrl } = await mountProxySettings()

    for (const scheme of PROXY_SCHEMES) {
      expect(await saveProxyUrl(`${scheme}://127.0.0.1:1080`), `${scheme} 应放行`).toBeNull()
    }
    for (const scheme of ['socks4', 'socks4a', 'ftp']) {
      expect(await saveProxyUrl(`${scheme}://127.0.0.1:1080`)).toBe(PROXY_URL_MESSAGES.scheme)
    }
  })

  it('留空表示清除代理，不做协议校验', async () => {
    const { saveProxyUrl, http } = await mountProxySettings()

    expect(await saveProxyUrl('   ')).toBeNull()
    expect(http.post).toHaveBeenCalled()
  })
})

describe('AppProxySettings 脱敏凭据意图', () => {
  const maskedProxy = 'socks5://xxxxx:xxxxx@10.0.0.5:1080'

  const findButton = (wrapper: ReturnType<typeof mount>, label: string) => {
    const button = wrapper.findAll('button').find((candidate) => candidate.text() === label)
    expect(button, `${label} 按钮应存在`).toBeDefined()
    return button!
  }

  it('未编辑脱敏地址时自动请求保留当前凭据', async () => {
    const { wrapper, http } = await mountProxySettings(maskedProxy, '1')

    await findButton(wrapper, '保存').trigger('click')
    await flushPromises()

    expect(http.post).toHaveBeenCalledWith(
      expect.stringContaining('/setting/http-proxy'),
      {
        http_proxy: maskedProxy,
        preserve_proxy_credentials: true,
      },
      expect.any(Object),
    )
  })

  it('编辑后的 xxxxx 按真实凭据提交，而不是自动保留旧凭据', async () => {
    const { wrapper, http } = await mountProxySettings(maskedProxy, '1')

    expect(wrapper.text()).toContain(PROXY_CREDENTIALS_MASKED_HINT)
    await wrapper.find('input').setValue(maskedProxy)
    expect(wrapper.text()).not.toContain(PROXY_CREDENTIALS_MASKED_HINT)
    await findButton(wrapper, '保存').trigger('click')
    await flushPromises()

    expect(http.post).toHaveBeenCalledWith(
      expect.stringContaining('/setting/http-proxy'),
      {
        http_proxy: maskedProxy,
        preserve_proxy_credentials: false,
      },
      expect.any(Object),
    )
  })

  it('未编辑脱敏地址时测试请求也自动保留当前凭据', async () => {
    const { wrapper, http } = await mountProxySettings(maskedProxy, '1')

    await findButton(wrapper, '测试').trigger('click')
    await flushPromises()

    expect(http.post).toHaveBeenCalledWith(
      expect.stringContaining('/setting/test-http-proxy'),
      {
        http_proxy: maskedProxy,
        preserve_proxy_credentials: true,
      },
      expect.any(Object),
    )
  })
})

describe('代理协议白名单单一来源', () => {
  const componentSource = readFileSync(
    resolve(__dirname, '../../src/components/AppProxySettings.vue'),
    'utf8',
  )

  it('协议提示、表单帮助和占位符都覆盖白名单里的每个协议', () => {
    for (const scheme of PROXY_SCHEMES) {
      expect(PROXY_SCHEME_HINT).toContain(scheme)
      expect(PROXY_URL_HELP).toContain(scheme)
    }
    expect(PROXY_URL_MESSAGES.scheme).toBe(PROXY_SCHEME_HINT)
    expect(PROXY_URL_PLACEHOLDER).toContain(PROXY_SCHEMES[0])
  })

  it('组件代码不再手写协议名，全部从常量派生', () => {
    // 只看代码，注释里举例说明 socks5:// 不算重复维护白名单
    const codeWithoutComments = componentSource.replace(/\/\/[^\n]*/g, '')
    // http 和 https 会作为主机名探测的占位协议出现，这里只钉住因白名单才存在的协议名
    const whitelistOnlySchemes = PROXY_SCHEMES.filter((scheme) => !scheme.startsWith('http'))

    expect(whitelistOnlySchemes.length).toBeGreaterThan(0)
    for (const scheme of whitelistOnlySchemes) {
      expect(codeWithoutComments, `${scheme} 不应在组件内硬编码`).not.toContain(scheme)
    }
    expect(componentSource).toContain("from '@/constants/validation'")
    expect(componentSource).toContain('PROXY_SCHEMES')
    expect(componentSource).toContain('PROXY_URL_PLACEHOLDER')
    expect(componentSource).toContain('PROXY_URL_HELP')
  })

  it('渲染出的占位符和帮助文案来自常量', async () => {
    const { wrapper } = await mountProxySettings()

    expect(wrapper.find('input').attributes('placeholder')).toBe(PROXY_URL_PLACEHOLDER)
    expect(wrapper.text()).toContain(PROXY_URL_HELP)
  })
})
