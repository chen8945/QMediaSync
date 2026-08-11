// @vitest-environment happy-dom
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppScrapeRecords from '@/components/AppScrapeRecords.vue'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const storageKey = 'qmediasync-page-state'

function primeScrapeRecordsState() {
  sessionStorage.setItem(
    storageKey,
    JSON.stringify({
      'scrape-records': {
        currentPage: 1,
        pageSize: 20,
        filters: {
          status: '',
          type: '',
          name: '',
        },
        expandedRowKeys: [],
        scrollTop: 0,
      },
    }),
  )
}

function createScrapeRecordsResponse(page: number, total = 45) {
  return {
    data: {
      code: 200,
      data: {
        list: Array.from({ length: 20 }, (_, index) => ({
          id: (page - 1) * 20 + index + 1,
        })),
        total,
        page,
        page_size: 20,
      },
    },
  }
}

describe('AppScrapeRecords 分页', () => {
  let unmountWrapper: (() => void) | null = null

  beforeEach(() => {
    sessionStorage.clear()
    vi.restoreAllMocks()
  })

  afterEach(() => {
    unmountWrapper?.()
    unmountWrapper = null
  })

  it('切换页码时保留上一份总数，避免分页组件把当前页归一到第一页', async () => {
    primeScrapeRecordsState()

    const requestedPages: number[] = []
    const pageTwoResponse = createDeferred<ReturnType<typeof createScrapeRecordsResponse>>()
    const http = {
      get: vi.fn((url: string, config?: { params?: { page?: number } }) => {
        if (!url.endsWith('/scrape/records')) {
          return Promise.reject(new Error(`unexpected url: ${url}`))
        }

        const page = config?.params?.page ?? 1
        requestedPages.push(page)

        if (page === 2) {
          return pageTwoResponse.promise
        }

        return Promise.resolve(createScrapeRecordsResponse(page))
      }),
    }

    const wrapper = shallowMount(AppScrapeRecords, {
      global: {
        plugins: [createPinia()],
        provide: {
          [httpKey]: http,
        },
        stubs: {
          ResponsiveRecordTable: {
            props: ['rows'],
            template: '<div data-testid="record-table"><slot /></div>',
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
    unmountWrapper = () => wrapper.unmount()

    await flushPromises()
    await flushPromises()

    expect(requestedPages).toEqual([1])

    await wrapper.find('[data-testid="page-2"]').trigger('click')
    await nextTick()

    const paginationWhileLoading = wrapper.find('[data-testid="responsive-pagination"]')
    const totalWhileLoading = paginationWhileLoading.attributes('data-total')

    pageTwoResponse.resolve(createScrapeRecordsResponse(2))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    const pagination = wrapper.find('[data-testid="responsive-pagination"]')
    expect(totalWhileLoading).toBe('45')
    expect(requestedPages).toEqual([1, 2])
    expect(pagination.attributes('data-current-page')).toBe('2')
    expect(pagination.attributes('data-total')).toBe('45')
  })
})
