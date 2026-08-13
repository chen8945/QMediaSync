// @vitest-environment happy-dom
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppFileManager from '@/components/AppFileManager.vue'
import { httpKey } from '@/http/client'

const storageKey = 'qmediasync-page-state'

function primeFileManagerState() {
  sessionStorage.setItem(
    storageKey,
    JSON.stringify({
      'file-manager': {
        currentPage: 1,
        pageSize: 50,
        filters: {
          currentPath: '',
          pathItems: '[]',
          selectedAccountId: 1,
          sortBy: 'size',
          sortOrder: 'desc',
        },
        expandedRowKeys: [],
        scrollTop: 0,
      },
    }),
  )
}

describe('AppFileManager 排序入口临时隐藏', () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.restoreAllMocks()
  })

  it('隐藏排序控件，并且不把历史排序条件提交给文件列表接口', async () => {
    primeFileManagerState()

    const http = {
      get: vi.fn((url: string, config?: { params?: Record<string, unknown> }) => {
        void config

        if (url.endsWith('/account/list')) {
          return Promise.resolve({
            data: {
              code: 200,
              data: [
                {
                  id: 1,
                  name: 'media',
                  username: 'media',
                  user_id: 'u1',
                  source_type: '115',
                  authorized: true,
                  created_at: 1,
                },
              ],
            },
          })
        }

        if (url.endsWith('/path/files')) {
          return Promise.resolve({
            data: {
              code: 200,
              data: {
                list: [],
                total: 0,
                page: 1,
                page_size: 50,
              },
            },
          })
        }

        return Promise.reject(new Error(`unexpected url: ${url}`))
      }),
    }

    const wrapper = shallowMount(AppFileManager, {
      global: {
        plugins: [createPinia()],
        provide: {
          [httpKey]: http,
        },
        stubs: {
          ElCard: {
            template: '<section><slot name="header" /><slot /></section>',
          },
          ElSelect: {
            template: '<select><slot /></select>',
          },
          ResponsivePagination: true,
        },
      },
    })

    await flushPromises()
    await flushPromises()

    const fileListCall = http.get.mock.calls.find(([url]) => url.endsWith('/path/files'))
    expect(fileListCall?.[1]?.params).not.toHaveProperty('sort_by')
    expect(fileListCall?.[1]?.params).not.toHaveProperty('sort_order')
    expect(wrapper.find('.file-manager-sort-field').exists()).toBe(false)
    expect(wrapper.find('.file-manager-sort-order').exists()).toBe(false)
  })
})
