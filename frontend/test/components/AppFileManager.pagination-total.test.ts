// @vitest-environment happy-dom
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { nextTick } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppFileManager from '@/components/AppFileManager.vue'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const storageKey = 'qmediasync-page-state'

function primeFileManagerState() {
  sessionStorage.setItem(
    storageKey,
    JSON.stringify({
      'file-manager': {
        currentPage: 1,
        pageSize: 100,
        filters: {
          currentPath: '',
          pathItems: '[]',
          selectedAccountId: 1,
        },
        expandedRowKeys: [],
        scrollTop: 0,
      },
    }),
  )
}

function createFilesResponse(page: number, total = 305) {
  return {
    data: {
      code: 200,
      data: {
        list: Array.from({ length: 100 }, (_, index) => {
          const fileIndex = (page - 1) * 100 + index + 1
          return {
            id: String(fileIndex),
            name: `file-${fileIndex}.mkv`,
            size: 1024,
            modified_time: 1,
            is_directory: false,
          }
        }),
        total,
        page,
        page_size: 100,
      },
    },
  }
}

describe('AppFileManager 分页总数', () => {
  beforeEach(() => {
    sessionStorage.clear()
    vi.restoreAllMocks()
  })

  it('使用后端返回的总数渲染分页页码，而不是当前页条数', async () => {
    primeFileManagerState()

    const http = {
      get: vi.fn((url: string) => {
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
                  token: 'token',
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
                list: Array.from({ length: 100 }, (_, index) => ({
                  id: String(index + 1),
                  name: `file-${index + 1}.mkv`,
                  size: 1024,
                  modified_time: 1,
                  is_directory: false,
                })),
                total: 305,
                page: 1,
                page_size: 100,
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
          ResponsivePagination: {
            props: ['currentPage', 'pageSize', 'pageSizes', 'total', 'isMobile'],
            template:
              '<div data-testid="responsive-pagination" :data-total="total" :data-current-page="currentPage" :data-page-size="pageSize"></div>',
          },
        },
      },
    })

    await flushPromises()
    await flushPromises()

    const pagination = wrapper.find('[data-testid="responsive-pagination"]')
    expect(pagination.exists()).toBe(true)
    expect(pagination.attributes('data-total')).toBe('305')
  })

  it('切换页码时保留上一份总数，避免分页组件回退到第一页', async () => {
    primeFileManagerState()

    const requestedPages: number[] = []
    const pageTwoResponse = createDeferred<ReturnType<typeof createFilesResponse>>()
    const http = {
      get: vi.fn((url: string, config?: { params?: { page?: number } }) => {
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
                  token: 'token',
                  created_at: 1,
                },
              ],
            },
          })
        }

        if (url.endsWith('/path/files')) {
          const page = config?.params?.page ?? 1
          requestedPages.push(page)

          if (page === 2) {
            return pageTwoResponse.promise
          }

          return Promise.resolve(createFilesResponse(page))
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
          ResponsivePagination: {
            props: ['currentPage', 'pageSize', 'pageSizes', 'total', 'isMobile'],
            emits: ['currentChange', 'sizeChange', 'update:currentPage', 'update:pageSize'],
            watch: {
              total(value: number) {
                if (value === 0 && this.currentPage > 1) {
                  this.$emit('update:currentPage', 1)
                  this.$emit('currentChange', 1)
                }
              },
            },
            template: `
              <div
                data-testid="responsive-pagination"
                :data-total="total"
                :data-current-page="currentPage"
                :data-page-size="pageSize"
              >
                <button
                  data-testid="page-2"
                  @click="$emit('update:currentPage', 2); $emit('currentChange', 2)"
                >
                  2
                </button>
              </div>
            `,
          },
        },
      },
    })

    await flushPromises()
    await flushPromises()

    expect(requestedPages).toEqual([1])

    await wrapper.find('[data-testid="page-2"]').trigger('click')
    await nextTick()

    const paginationWhileLoading = wrapper.find('[data-testid="responsive-pagination"]')
    const totalWhileLoading = paginationWhileLoading.attributes('data-total')

    pageTwoResponse.resolve(createFilesResponse(2))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    const pagination = wrapper.find('[data-testid="responsive-pagination"]')
    expect(totalWhileLoading).toBe('305')
    expect(requestedPages).toEqual([1, 2])
    expect(pagination.attributes('data-current-page')).toBe('2')
    expect(pagination.attributes('data-total')).toBe('305')
  })
})
