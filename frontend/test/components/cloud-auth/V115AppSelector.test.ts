// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import V115AppSelector from '@/components/cloud-auth/V115AppSelector.vue'
import { SERVER_URL } from '@/const'
import { httpKey } from '@/http/client'

describe('V115AppSelector', () => {
  it('展示所有内置 115 开放平台应用', () => {
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: {
            get: vi.fn().mockResolvedValue({ data: { data: { items: [], total: 0 } } }),
          },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: { template: '<div><slot /></div>' },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: { template: '<button><slot /></button>' },
        },
      },
    })

    expect(wrapper.text()).toContain('扫码授权')
    expect(wrapper.text()).toContain('网页授权')
    expect(wrapper.text()).toContain('QMediaSync')
    expect(wrapper.text()).not.toContain('Q115-STRM')
    expect(wrapper.text()).not.toContain('MQ的媒体库')
    expect(wrapper.text()).toContain('自定义 APP ID')
  })

  it('扫码授权把 APP ID 搜索内置在下拉框里', async () => {
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: {
            get: vi.fn().mockResolvedValue({ data: { data: { items: [], total: 0 } } }),
          },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            props: ['placeholder'],
            emits: ['visible-change'],
            template:
              '<div><input name="v115-appid-search" :placeholder="placeholder" @focus="$emit(\'visible-change\', true)" /><slot name="header" /><slot /></div>',
          },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: { template: '<button><slot /></button>' },
        },
      },
    })

    const searchInput = wrapper.find('input[name="v115-appid-search"]')
    await searchInput.trigger('focus')

    expect(wrapper.text()).not.toContain('搜索 APP ID')
    expect(searchInput.exists()).toBe(true)
    expect(searchInput.attributes('placeholder')).toBe('选择或搜索 115 开放平台 APP ID')
    expect(wrapper.text()).toContain('输入应用名或 APP ID 搜索更多内置应用')
  })

  it('网页授权不展示已移除的 MQ 授权入口', () => {
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'oauth',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSelect: { template: '<div><slot /></div>' },
          ElOption: { props: ['label'], template: '<div>{{ label }}</div>' },
        },
      },
    })

    expect(wrapper.text()).toContain('QMediaSync')
    expect(wrapper.text()).toContain('MoviePilot')
    expect(wrapper.text()).toContain('CloudDrive')
    expect(wrapper.text()).not.toContain('Q115-STRM')
    expect(wrapper.text()).not.toContain('MQ的媒体库')
  })

  it('扫码授权默认只显示置顶和精选 APP ID，不自动合并远程第一页', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        data: {
          items: [{ app_id: '1001', app_name: '测试应用', display_name: '测试应用' }],
          total: 2,
        },
      },
    })
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: { get },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            template: '<div><slot /><slot name="footer" /></div>',
          },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: { template: '<button><slot /></button>' },
        },
      },
    })

    await flushPromises()

    expect(get).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('QMediaSync')
    expect(wrapper.text()).toContain('MoviePilot-115')
    expect(wrapper.text()).not.toContain('测试应用')
  })

  it('扫码授权搜索结果存在下一页时显示加载更多入口', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        data: {
          items: [{ app_id: '1001', app_name: '测试应用', display_name: '测试应用' }],
          total: 2,
        },
      },
    })
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: { get },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            props: ['placeholder', 'remoteMethod'],
            template:
              '<div><input name="v115-appid-search" :placeholder="placeholder" @input="remoteMethod?.($event.target.value)" /><slot /><slot name="footer" /></div>',
          },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: { template: '<button><slot /></button>' },
        },
      },
    })

    await wrapper.find('input[name="v115-appid-search"]').setValue('测试')
    await flushPromises()

    expect(get).toHaveBeenCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '测试', offset: 0, limit: 50 },
    })
    expect(wrapper.text()).toContain('加载更多')
  })

  it('扫码授权可以搜索置顶的 QMediaSync', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        data: {
          items: [{ app_id: '100197849', app_name: 'QMediaSync', display_name: 'QMediaSync' }],
          total: 1,
        },
      },
    })
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: { get },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            props: ['remoteMethod'],
            template:
              '<div><input name="v115-appid-search" @input="remoteMethod?.($event.target.value)" /><slot /><slot name="footer" /></div>',
          },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: { template: '<button><slot /></button>' },
        },
      },
    })

    await wrapper.find('input[name="v115-appid-search"]').setValue('QMediaSync')
    await flushPromises()

    expect(get).toHaveBeenCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: 'QMediaSync', offset: 0, limit: 50 },
    })
    expect(wrapper.text()).toContain('QMediaSync')
  })

  it('扫码授权未输入搜索词时显示加载更多，点击后再展示远程结果', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        data: {
          items: [{ app_id: '1001', app_name: '测试应用', display_name: '测试应用' }],
          total: 2,
        },
      },
    })
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: { get },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            props: ['placeholder'],
            emits: ['visible-change'],
            template:
              '<div><input name="v115-appid-search" :placeholder="placeholder" @focus="$emit(\'visible-change\', true)" /><slot /><slot name="footer" /></div>',
          },
          ElOption: { props: ['label'], template: '<div>{{ label }}<slot /></div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: {
            emits: ['click'],
            template: '<button @click="$emit(\'click\', $event)"><slot /></button>',
          },
        },
      },
    })

    await wrapper.find('input[name="v115-appid-search"]').trigger('focus')
    await flushPromises()

    expect(get).toHaveBeenCalledWith(`${SERVER_URL}/115/appids`, {
      params: { keyword: '', offset: 0, limit: 50 },
    })
    expect(wrapper.text()).toContain('加载更多')
    expect(wrapper.text()).not.toContain('测试应用')

    await wrapper.find('.v115-load-more-button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('测试应用')
    expect(wrapper.text().indexOf('测试应用')).toBeLessThan(wrapper.text().indexOf('自定义 APP ID'))
  })

  it('扫码授权加载更多时不重复显示固定的 QMediaSync', async () => {
    const get = vi.fn().mockResolvedValue({
      data: {
        data: {
          items: [
            { app_id: '100197849', app_name: 'QMediaSync', display_name: 'QMediaSync' },
            { app_id: '1001', app_name: '测试应用', display_name: '测试应用' },
          ],
          total: 2,
        },
      },
    })
    const wrapper = mount(V115AppSelector, {
      props: {
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
        'onUpdate:authMode': vi.fn(),
        'onUpdate:selectedQrApp': vi.fn(),
        'onUpdate:selectedWebProvider': vi.fn(),
        'onUpdate:customAppId': vi.fn(),
        'onUpdate:customAppName': vi.fn(),
      },
      global: {
        provide: {
          [httpKey]: { get },
        },
        stubs: {
          ElFormItem: { props: ['label'], template: '<div><span>{{ label }}</span><slot /></div>' },
          ElSegmented: {
            props: ['options'],
            template:
              '<div><button v-for="option in options" :key="option.value">{{ option.label }}</button></div>',
          },
          ElSelect: {
            emits: ['visible-change'],
            template:
              '<div><input name="v115-appid-search" @focus="$emit(\'visible-change\', true)" /><slot /><slot name="footer" /></div>',
          },
          ElOption: { props: ['label'], template: '<div class="v115-option">{{ label }}</div>' },
          ElInput: {
            props: ['name', 'placeholder', 'modelValue'],
            template:
              '<input :name="name" :placeholder="placeholder" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value); $emit(\'input\', $event.target.value)" />',
          },
          ElButton: {
            emits: ['click'],
            template: '<button @click="$emit(\'click\', $event)"><slot /></button>',
          },
        },
      },
    })

    await wrapper.find('input[name="v115-appid-search"]').trigger('focus')
    await flushPromises()
    await wrapper.find('.v115-load-more-button').trigger('click')
    await flushPromises()

    expect(
      wrapper.findAll('.v115-option').filter((item) => item.text() === 'QMediaSync'),
    ).toHaveLength(1)
    expect(wrapper.text()).toContain('测试应用')
  })
})
