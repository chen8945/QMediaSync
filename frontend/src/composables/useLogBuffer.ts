import { onScopeDispose, shallowRef } from 'vue'
import type { LogEntry } from '@/types/log'

// 实时窗口有界；完整历史由日志文件分页提供，超长单行不截断。
export function useLogBuffer() {
  const maxRows = 2000
  const logs = shallowRef<LogEntry[]>([])
  const byteBudget = 2 * 1024 * 1024
  let pending: LogEntry[] = []
  let pendingBytes = 0
  let timer: ReturnType<typeof setTimeout> | null = null
  let nextID = 0
  const identify = (entry: LogEntry) => ({
    ...entry,
    id: entry.id || (entry.cursor ? `log-${entry.cursor}` : `local-${++nextID}`),
  })
  const size = (entry: LogEntry) => (entry.message.length + entry.timestamp.length) * 2 + 64
  const bounded = (entries: LogEntry[]) => {
    let bytes = 0
    let end = 0
    for (const entry of entries) {
      if (end && (end >= maxRows || bytes + size(entry) > byteBudget)) break
      bytes += size(entry)
      end++
    }
    return entries.slice(0, end)
  }
  const cancel = () => {
    if (timer) clearTimeout(timer)
    timer = null
  }
  const flush = () => {
    cancel()
    if (!pending.length) return
    logs.value = bounded([...pending.reverse(), ...logs.value])
    pending = []
    pendingBytes = 0
  }
  const append = (entries: LogEntry[]) => {
    for (const entry of entries) {
      pending.push(identify(entry))
      pendingBytes += size(entry)
    }
    if (pending.length > maxRows || pendingBytes > byteBudget) {
      pending = bounded(pending.reverse()).reverse()
      pendingBytes = pending.reduce((total, entry) => total + size(entry), 0)
    }
    if (!timer) timer = setTimeout(flush, 100)
  }
  const clear = () => {
    cancel()
    pending = []
    pendingBytes = 0
    logs.value = []
  }
  const replace = (entries: LogEntry[]) => {
    clear()
    logs.value = bounded(entries).map(identify)
  }
  onScopeDispose(clear)
  return { logs, append, flush, replace, clear }
}
