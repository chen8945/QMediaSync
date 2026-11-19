import {
  computed,
  nextTick,
  onMounted,
  onScopeDispose,
  ref,
  shallowRef,
  watch,
  type Ref,
} from 'vue'
import { fetchLogSnapshot } from '@/api/logs'
import { parseHttpError } from '@/http/errors'
import type { LogEntry, LogLevel } from '@/types/log'
import { filterLogEntriesByLevels } from '@/utils/logLevel'

interface HistoryPage {
  end: number
  pos: number
  limit: number
  skipCursor?: number
  olderSkipCursor?: number
}

// 正文只缓存相邻两页；已退出窗口的历史按固定文件位置重读。
export function useLogScroll(
  logs: Ref<LogEntry[]>,
  levels: Ref<LogLevel[]>,
  container: Ref<HTMLElement | null>,
  loading: Ref<boolean>,
  path: () => string,
  fileID?: () => string | undefined,
  onCursorInvalid?: () => void,
) {
  const history = shallowRef<{ index: number; entries: LogEntry[] }[] | null>(null)
  const historyLoading = ref(false)
  const historyError = ref('')
  const top = ref(0)
  const heights = shallowRef(new Map<string, number>())
  const entries = computed(() => history.value?.flatMap((page) => page.entries) ?? logs.value)
  const filtered = computed(() => filterLogEntriesByLevels(entries.value, levels.value))
  const offsets = computed(() => {
    const result = [0]
    for (const entry of filtered.value)
      result.push(result[result.length - 1]! + (heights.value.get(entry.id!) ?? 24))
    return result
  })
  const visibleIndex = () => {
    let low = 0,
      high = filtered.value.length
    while (low < high) {
      const middle = (low + high) >>> 1
      if (offsets.value[middle + 1]! <= top.value) low = middle + 1
      else high = middle
    }
    return Math.min(low, Math.max(0, filtered.value.length - 1))
  }
  const start = computed(() => Math.max(0, visibleIndex() - 20))
  const rows = computed(() => filtered.value.slice(start.value, start.value + 200))
  const before = computed(() => offsets.value[start.value] ?? 0)
  const after = computed(
    () => offsets.value[filtered.value.length]! - offsets.value[start.value + rows.value.length]!,
  )
  let pages: HistoryPage[] = []
  let snapshotEnd = 0,
    snapshotPos = 0
  let controller: AbortController | null = null
  let failedDirection = 1
  let navigationDirection = 0
  let observer: ResizeObserver | undefined
  let measureFrame: number | undefined
  let touchY = 0
  let adjusting = false
  const hasOlder = computed(() => {
    if (!history.value) return true
    const last = history.value[history.value.length - 1]!
    return last.index < pages.length - 1 || pages[last.index]!.pos > 0
  })
  const anchor = () => {
    const index = visibleIndex()
    return { id: filtered.value[index]?.id, offset: top.value - offsets.value[index]! }
  }
  const restore = async (saved: ReturnType<typeof anchor>) => {
    const current = history.value
    await nextTick()
    if (history.value !== current) return
    const index = filtered.value.findIndex((entry) => entry.id === saved.id)
    if (index < 0 || !container.value) return
    container.value.scrollTop = Math.max(0, offsets.value[index]! + saved.offset)
    top.value = container.value.scrollTop
  }
  const measure = async () => {
    if (!container.value || adjusting) return
    const measured = new Map(heights.value)
    let changed = false
    for (const row of container.value.querySelectorAll<HTMLElement>('.log-line')) {
      const height = row.getBoundingClientRect().height
      if (height > 0 && measured.get(row.dataset.logId!) !== height) {
        measured.set(row.dataset.logId!, height)
        changed = true
      }
    }
    if (!changed) return
    const saved = anchor()
    adjusting = true
    heights.value = measured
    await restore(saved)
    adjusting = false
  }
  const scheduleMeasure = () => {
    if (measureFrame !== undefined) return
    // 不在 ResizeObserver 通知期间写入行高，避免同一帧反复触发布局。
    measureFrame = requestAnimationFrame(() => {
      measureFrame = undefined
      void measure()
    })
  }
  watch(
    rows,
    async () => {
      await nextTick()
      observer?.disconnect()
      if (container.value) {
        observer?.observe(container.value)
        container.value.querySelectorAll('.log-line').forEach((row) => observer?.observe(row))
      }
      scheduleMeasure()
    },
    { flush: 'post' },
  )
  watch(entries, () => {
    const keys = new Set(entries.value.map((entry) => entry.id))
    heights.value = new Map([...heights.value].filter(([key]) => keys.has(key)))
  })
  onMounted(() => {
    observer = new ResizeObserver(scheduleMeasure)
    if (container.value) observer.observe(container.value)
  })
  const cancel = () => {
    controller?.abort()
    controller = null
    historyLoading.value = false
  }
  const returnLatest = () => {
    cancel()
    history.value = null
    pages = []
    historyError.value = ''
    top.value = 0
    if (container.value) container.value.scrollTop = 0
  }
  const freeze = () => {
    if (history.value) return
    const saved = anchor()
    const chunks: LogEntry[][] = []
    for (let index = 0; index < logs.value.length; index += 200)
      chunks.push(logs.value.slice(index, index + 200))
    if (!chunks.length) chunks.push([])
    let filePosition = snapshotEnd
    let oldestCursor: number | undefined
    pages = chunks.map((chunk, index) => {
      // cursor=0 是未换行的文件尾行；本地提示没有 cursor，不占磁盘行数。
      const fileEntries = chunk.filter((entry) => entry.cursor !== undefined)
      const end = fileEntries[0]?.cursor || filePosition
      if (fileEntries.length) oldestCursor = fileEntries[fileEntries.length - 1]!.cursor
      const next = chunks[index + 1]?.find((entry) => entry.cursor !== undefined)
      const pos =
        next?.cursor ||
        (fileEntries.length
          ? oldestCursor || snapshotPos
          : oldestCursor || filePosition || snapshotPos)
      filePosition = pos
      return {
        end,
        limit: fileEntries.length,
        pos,
        olderSkipCursor: index === chunks.length - 1 ? oldestCursor : undefined,
      }
    })
    const index = Math.max(
      0,
      Math.floor(logs.value.findIndex((entry) => entry.id === saved.id) / 200),
    )
    const first = Math.min(index, Math.max(0, chunks.length - 2))
    history.value = chunks
      .slice(first, first + 2)
      .map((entries, offset) => ({ index: first + offset, entries }))
    void restore(saved)
  }
  const load = async (direction: number) => {
    if (
      (loading.value && !logs.value.length && !history.value) ||
      historyLoading.value ||
      !levels.value.length
    )
      return
    if (!history.value && !logs.value.length && !snapshotEnd && !snapshotPos) return
    freeze()
    const cached = history.value!
    const edge = cached[direction > 0 ? cached.length - 1 : 0]!
    let index = edge.index + direction
    while (index >= 0 && pages[index]?.limit === 0) index += direction
    if (index < 0) {
      returnLatest()
      return
    }
    if (direction > 0 && !hasOlder.value) return
    const boundary = pages[direction > 0 ? index - 1 : edge.index]!
    const page = pages[index] ?? {
      end: boundary.pos,
      limit: 200,
      pos: 0,
      skipCursor: boundary.olderSkipCursor,
    }
    const current = new AbortController()
    controller = current
    historyLoading.value = true
    historyError.value = ''
    failedDirection = direction
    navigationDirection = direction
    try {
      const data = await fetchLogSnapshot(path(), page.end, page.limit, current.signal, fileID?.())
      if (current.signal.aborted) return
      const incoming = data.entries
        .filter((entry) => !page.skipCursor || entry.cursor !== page.skipCursor)
        .map((entry, offset) => ({
          ...entry,
          id: entry.cursor ? `log-${entry.cursor}` : `history-${page.end}-${offset}`,
        }))
      if (!incoming.length) {
        if (direction > 0) pages[edge.index]!.pos = data.pos
        history.value = [...cached]
        return
      }
      pages[index] = { ...page, pos: data.pos }
      const saved = anchor()
      let kept = direction > 0 ? cached[cached.length - 1]! : cached[0]!
      // 慢请求返回前用户可能已经反向滚动，不移走其正在阅读的那一页。
      if (saved.id && !kept.entries.some((entry) => entry.id === saved.id)) {
        if (navigationDirection !== direction || !atEdge(direction)) return
        if (!filterLogEntriesByLevels(kept.entries, levels.value).length) {
          kept = cached.find((page) => page.entries.some((entry) => entry.id === saved.id))!
        } else if (
          !container.value ||
          container.value.scrollHeight > container.value.clientHeight
        ) {
          return
        }
      }
      const added = { index, entries: incoming }
      history.value = direction > 0 ? [kept, added] : [added, kept]
      await restore(saved)
    } catch (error) {
      if (!current.signal.aborted) {
        const failure = parseHttpError(error, { fallbackMessage: '加载历史日志失败' })
        if (failure.diagnostics.errorCode === 'LOG_CURSOR_INVALID' && onCursorInvalid) {
          onCursorInvalid()
          return
        }
        if (failure.shouldNotify) {
          console.error('加载历史日志失败：', failure.diagnostics)
          historyError.value = failure.message
        }
      }
    } finally {
      if (controller === current) {
        controller = null
        historyLoading.value = false
      }
    }
  }
  const atEdge = (direction: number) => {
    const el = container.value
    return (
      !!el &&
      (direction > 0
        ? el.scrollTop + el.clientHeight >= el.scrollHeight - Math.max(100, el.clientHeight)
        : el.scrollTop <= (history.value?.[0]?.index === 0 ? 0 : 100))
    )
  }
  const move = (direction: number) => {
    navigationDirection = direction
    if (!historyError.value && atEdge(direction)) void load(direction)
  }
  const handleScroll = () => {
    if (!container.value || adjusting) return
    const previous = top.value
    top.value = container.value.scrollTop
    if (!history.value && top.value > 20) freeze()
    if (top.value !== previous) move(top.value > previous ? 1 : -1)
  }
  const handleWheel = (event: WheelEvent) => {
    if (event.deltaY) move(Math.sign(event.deltaY))
  }
  const handleTouchStart = (event: TouchEvent) => {
    touchY = event.touches[0]!.clientY
  }
  const handleTouchMove = (event: TouchEvent) => {
    const y = event.touches[0]!.clientY
    if (y !== touchY) move(y < touchY ? 1 : -1)
    touchY = y
  }
  const handleKeydown = (event: KeyboardEvent) => {
    if ((event.key === 'End' || event.key === 'Home') && container.value) {
      event.preventDefault()
      container.value.scrollTop = event.key === 'End' ? container.value.scrollHeight : 0
      handleScroll()
    }
    if (['ArrowDown', 'PageDown', 'End', ' '].includes(event.key)) move(1)
    if (['ArrowUp', 'PageUp', 'Home'].includes(event.key)) move(-1)
  }
  watch(levels, () => {
    cancel()
    const cached = history.value
    if (cached?.[1] && cached[1].index > cached[0]!.index + 1) history.value = [cached[0]!]
    top.value = 0
    if (container.value) container.value.scrollTop = 0
  })
  watch(logs, async () => {
    snapshotEnd = Math.max(snapshotEnd, logs.value.find((entry) => entry.cursor)?.cursor ?? 0)
    if (history.value) return
    top.value = 0
    await nextTick()
    if (container.value) container.value.scrollTop = 0
  })
  onScopeDispose(() => {
    cancel()
    observer?.disconnect()
    if (measureFrame !== undefined) cancelAnimationFrame(measureFrame)
  })
  return {
    loadedCount: computed(() => entries.value.length),
    rows,
    before,
    after,
    history,
    historyLoading,
    historyError,
    hasOlder,
    handleScroll,
    handleWheel,
    handleTouchStart,
    handleTouchMove,
    handleKeydown,
    returnLatest,
    cancel,
    retry: () => load(failedDirection),
    setSnapshot: (pos: number, end: number) => {
      snapshotPos = pos
      snapshotEnd = end
    },
  }
}
