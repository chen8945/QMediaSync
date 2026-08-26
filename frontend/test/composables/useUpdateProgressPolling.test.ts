import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { isUpdateRunningStatus, isUpdateTerminalStatus, useUpdate } from '@/composables/useUpdate'

const { getMock, postMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postMock: vi.fn(),
}))

vi.mock('@/http/client', () => ({
  useHttpClient: () => ({ get: getMock, post: postMock }),
}))

vi.mock('element-plus', () => ({
  ElMessage: { error: vi.fn(), info: vi.fn(), success: vi.fn() },
}))

const terminalStatuses = ['completed', 'failed', 'cancelled'] as const

const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

afterEach(() => {
  vi.clearAllMocks()
  vi.useRealTimers()
})

describe('useUpdate 进度状态分类', () => {
  it.each(terminalStatuses)('把 %s 识别为终态', (status) => {
    expect(isUpdateTerminalStatus(status)).toBe(true)
  })

  it.each(['downloading', 'install'])('把 %s 识别为进行态', (status) => {
    expect(isUpdateRunningStatus(status)).toBe(true)
  })

  it('终态不属于进行态', () => {
    expect(isUpdateRunningStatus('completed')).toBe(false)
    expect(isUpdateRunningStatus('failed')).toBe(false)
    expect(isUpdateRunningStatus('cancelled')).toBe(false)
  })

  it('取消后忽略仍在途的旧进度响应', async () => {
    vi.useFakeTimers()
    const progressRequest = deferred<{
      data: { code: number; data: { status: string; progress: number } }
    }>()
    getMock.mockImplementation((url: string) => {
      if (url.includes('/update/last')) return Promise.resolve({ data: { data: [] } })
      return progressRequest.promise
    })
    postMock.mockResolvedValue({ data: { code: 200 } })

    let update!: ReturnType<typeof useUpdate>
    const wrapper = mount(
      defineComponent({
        setup() {
          update = useUpdate()
          return () => null
        },
      }),
    )
    await flushPromises()

    const start = update.updateToVersion('v-next')
    await flushPromises()
    const cancel = update.cancelUpdate()
    await cancel
    progressRequest.resolve({ data: { code: 200, data: { status: 'completed', progress: 100 } } })
    await start
    await flushPromises()

    expect(update.showUpdateCompleteDialog.value).toBe(false)
    expect(update.isUpdating.value).toBe(false)
    wrapper.unmount()
  })
})
