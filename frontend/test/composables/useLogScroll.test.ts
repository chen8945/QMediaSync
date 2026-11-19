// @vitest-environment happy-dom
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, shallowRef } from 'vue'
import { useLogScroll } from '@/composables/useLogScroll'
import { fetchLogSnapshot } from '@/api/logs'
import { HttpResponseError } from '@/http/errors'
import type { LogEntry, LogLevel } from '@/types/log'

vi.mock('@/api/logs', () => ({ fetchLogSnapshot: vi.fn() }))

enableAutoUnmount(afterEach)
afterEach(() => vi.clearAllMocks())

const fileEntry = (cursor: number): LogEntry => ({
  id: `log-${cursor}`,
  cursor,
  level: 'info',
  timestamp: 't',
  message: `file-${cursor}`,
})
const localEntry: LogEntry = {
  id: 'local-1',
  level: 'info',
  timestamp: 't',
  message: '浏览器本地恢复提示',
}

function mountLogs(
  entries: LogEntry[],
  end = 1000,
  fileID?: () => string | undefined,
  onCursorInvalid?: () => void,
  levels = ref<LogLevel[]>(['info']),
) {
  const wrapper = mount(
    defineComponent({
      setup() {
        const viewport = ref<HTMLElement | null>(null)
        const scroll = useLogScroll(
          shallowRef(entries),
          levels,
          viewport,
          ref(false),
          () => 'app.log',
          fileID,
          onCursorInvalid,
        )
        scroll.setSnapshot(0, end)
        return () =>
          h(
            'div',
            {
              ref: viewport,
              onScroll: scroll.handleScroll,
              onWheel: scroll.handleWheel,
              'data-history-error': scroll.historyError.value,
            },
            [
              h('div', { style: { height: `${scroll.before.value}px` } }),
              ...scroll.rows.value.map((entry) =>
                h(
                  'div',
                  { key: entry.id, class: 'log-line', 'data-log-id': entry.id },
                  entry.message,
                ),
              ),
              h('div', { style: { height: `${scroll.after.value}px` } }),
            ],
          )
      },
    }),
  )
  const el = wrapper.element as HTMLElement
  Object.defineProperties(el, {
    clientHeight: { configurable: true, value: 240 },
    scrollHeight: {
      configurable: true,
      get: () =>
        [...el.children].reduce(
          (total, child) =>
            total +
            (child.classList.contains('log-line')
              ? 24
              : Number.parseFloat((child as HTMLElement).style.height) || 0),
          0,
        ),
    },
  })
  return wrapper
}

describe('日志历史重读', () => {
  it.each([1, 3])(
    '等级筛选跨过 %s 页隐藏日志，重新启用等级后仍能回读中间页',
    async (hiddenPages) => {
      const end = (hiddenPages + 2) * 200
      const entries = Array.from({ length: end }, (_, index) => ({
        ...fileEntry(end - index),
        level: (index >= 200 && index < end - 200 ? 'debug' : 'info') as LogLevel,
      }))
      vi.mocked(fetchLogSnapshot).mockImplementation(async (_path, position, limit) => ({
        entries: entries.slice(end - position, end - position + limit),
        pos: Math.max(0, position - limit),
        start_pos: position,
        cursor: position,
        file_id: 'file-id-test',
      }))
      const levels = ref<LogLevel[]>(['info'])
      const wrapper = mountLogs(entries, end, undefined, undefined, levels)
      await flushPromises()
      const el = wrapper.element as HTMLElement
      el.scrollTop = 189 * 24 + 7
      await wrapper.trigger('scroll')
      await flushPromises()
      for (let page = 0; page < hiddenPages; page++) {
        await wrapper.trigger('wheel', { deltaY: 100 })
        await flushPromises()
      }
      expect(wrapper.text()).toContain('file-200')
      expect(el.scrollTop).toBe(189 * 24 + 7)
      expect(vi.mocked(fetchLogSnapshot).mock.calls.map((call) => call[1])).toEqual(
        Array.from({ length: hiddenPages }, (_, index) => end - 400 - index * 200),
      )

      levels.value = ['info', 'debug']
      await flushPromises()
      el.scrollTop = 189 * 24 + 7
      await wrapper.trigger('scroll')
      await flushPromises()
      expect(fetchLogSnapshot).toHaveBeenLastCalledWith(
        'app.log',
        end - 200,
        200,
        expect.any(AbortSignal),
        undefined,
      )
      expect(wrapper.text()).toContain(`file-${end - 200}`)
    },
  )

  it('筛选后不足一屏仍可双向翻页，反向操作不会被迟到页覆盖', async () => {
    const entries = Array.from({ length: 1000 }, (_, index) => ({
      ...fileEntry(1000 - index),
      level: (index % 200 === 0 ? 'info' : 'debug') as LogLevel,
    }))
    const reply = (position: number, limit: number) => ({
      entries: entries.slice(1000 - position, 1000 - position + limit),
      pos: Math.max(0, position - limit),
      start_pos: position,
      cursor: position,
      file_id: 'file-id-test',
    })
    vi.mocked(fetchLogSnapshot).mockImplementation(async (_path, position, limit) =>
      reply(position, limit),
    )
    const wrapper = mountLogs(entries)
    await flushPromises()
    Object.defineProperty(wrapper.element, 'scrollTop', {
      configurable: true,
      get: () => 0,
      set: () => {},
    })
    const visible = () => wrapper.findAll('.log-line').map((row) => row.text())
    await wrapper.trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(visible()).toEqual(['file-800', 'file-600'])
    await wrapper.trigger('wheel', { deltaY: -100 })
    await flushPromises()
    expect(visible()).toEqual(['file-1000', 'file-800'])

    let complete!: (page: ReturnType<typeof reply>) => void
    vi.mocked(fetchLogSnapshot).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          complete = resolve
        }),
    )
    await wrapper.trigger('wheel', { deltaY: 100 })
    await wrapper.trigger('wheel', { deltaY: -100 })
    complete(reply(600, 200))
    await flushPromises()
    expect(visible()).toEqual(['file-1000', 'file-800'])
    await wrapper.trigger('wheel', { deltaY: 100 })
    await flushPromises()
    expect(visible()).toEqual(['file-800', 'file-600'])
  })

  it.each([0, 200])('本地提示位于第 %s 行时，淘汰后回看仍按文件边界连续显示', async (offset) => {
    const entries = Array.from({ length: 1000 }, (_, index) => fileEntry(1000 - index))
    entries.splice(offset, 0, localEntry)
    vi.mocked(fetchLogSnapshot).mockImplementation(async (_path, end, limit) => ({
      entries: Array.from({ length: limit }, (_, index) => fileEntry(end - index)),
      pos: end - limit,
      start_pos: end,
      cursor: end,
      file_id: 'file-id-test',
    }))
    const wrapper = mountLogs(entries)
    await flushPromises()
    const el = wrapper.element as HTMLElement
    el.scrollTop = (offset + 200) * 24 + 5
    await wrapper.trigger('scroll')
    await flushPromises()
    await wrapper.trigger('wheel', { deltaY: -100 })
    await flushPromises()

    expect(fetchLogSnapshot).toHaveBeenCalledWith(
      'app.log',
      1000 - offset,
      199,
      expect.any(AbortSignal),
      undefined,
    )
    const visible = wrapper.findAll('.log-line').map((row) => Number(row.text().slice(5)))
    expect(visible).toEqual(visible.map((_, index) => visible[0]! - index))
    expect(new Set(visible).size).toBe(visible.length)
    expect(visible).toContain(801 - offset)
    expect(wrapper.findAll('.log-line').length).toBeLessThanOrEqual(200)
  })

  it('无换行尾行仍计入文件页，并从固定 EOF 重读', async () => {
    const partial = { ...fileEntry(0), message: '未换行的真实文件尾行' }
    const entries = [
      partial,
      ...Array.from({ length: 1000 }, (_, index) => fileEntry(1000 - index)),
    ]
    vi.mocked(fetchLogSnapshot).mockImplementation(async (_path, end, limit) => ({
      entries: [
        partial,
        ...Array.from({ length: limit - 1 }, (_, index) => fileEntry(1000 - index)),
      ],
      pos: 1001 - limit,
      start_pos: end,
      cursor: 1000,
      file_id: 'file-id-test',
    }))
    const wrapper = mountLogs(entries, 2000)
    await flushPromises()
    const el = wrapper.element as HTMLElement
    el.scrollTop = 200 * 24 + 5
    await wrapper.trigger('scroll')
    await flushPromises()
    await wrapper.trigger('wheel', { deltaY: -100 })
    await flushPromises()
    expect(fetchLogSnapshot).toHaveBeenCalledWith(
      'app.log',
      2000,
      200,
      expect.any(AbortSignal),
      undefined,
    )
    const visible = wrapper.findAll('.log-line').map((row) => row.text())
    expect(new Set(visible).size).toBe(visible.length)
    expect(visible).toContain('file-801')
  })

  it('整页只有本地提示时，回看跳过该磁盘空页', async () => {
    const entries = Array.from({ length: 1000 }, (_, index) => fileEntry(1000 - index))
    entries.splice(
      200,
      0,
      ...Array.from({ length: 200 }, (_, index) => ({ ...localEntry, id: `local-${index}` })),
    )
    vi.mocked(fetchLogSnapshot).mockImplementation(async (_path, end, limit) => ({
      entries: Array.from({ length: limit }, (_, index) => fileEntry(end - index)),
      pos: end - limit,
      start_pos: end,
      cursor: end,
      file_id: 'file-id-test',
    }))
    const wrapper = mountLogs(entries)
    await flushPromises()
    const el = wrapper.element as HTMLElement
    el.scrollTop = 400 * 24 + 5
    await wrapper.trigger('scroll')
    await flushPromises()
    await wrapper.trigger('wheel', { deltaY: -100 })
    await flushPromises()
    expect(fetchLogSnapshot).toHaveBeenCalledTimes(1)
    expect(fetchLogSnapshot).toHaveBeenCalledWith(
      'app.log',
      1000,
      200,
      expect.any(AbortSignal),
      undefined,
    )
    const visible = wrapper.findAll('.log-line').map((row) => Number(row.text().slice(5)))
    expect(visible).toEqual(visible.map((_, index) => visible[0]! - index))
    el.scrollTop = 0
    await wrapper.trigger('scroll')
    await flushPromises()
    expect(fetchLogSnapshot).toHaveBeenCalledTimes(1)
  })

  it('历史文件标识失效时交由恢复回调处理，不覆盖新视图错误', async () => {
    vi.mocked(fetchLogSnapshot).mockRejectedValue(
      new HttpResponseError({
        status: 409,
        data: {
          status: 409,
          data: null,
          error_code: 'LOG_CURSOR_INVALID',
          message: '游标失效测试',
          instance: '/request-errors/HistoryCursorFailure0001',
        },
      }),
    )
    const invalid = vi.fn()
    const wrapper = mountLogs(
      Array.from({ length: 1000 }, (_, index) => fileEntry(1000 - index)),
      1000,
      () => 'file-id-test',
      invalid,
    )
    await flushPromises()
    const el = wrapper.element as HTMLElement
    el.scrollTop = 200 * 24 + 5
    await wrapper.trigger('scroll')
    await flushPromises()
    await wrapper.trigger('wheel', { deltaY: -100 })
    await flushPromises()
    expect(fetchLogSnapshot).toHaveBeenCalledWith(
      'app.log',
      1000,
      200,
      expect.any(AbortSignal),
      'file-id-test',
    )
    expect(invalid).toHaveBeenCalledOnce()
    expect(wrapper.attributes('data-history-error')).toBe('')
  })
})
