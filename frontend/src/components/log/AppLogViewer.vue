<template>
  <div
    class="log-viewer-container"
    :class="{ 'is-fullscreen': props.fullscreen }"
    :style="logViewerStyle"
  >
    <el-card class="log-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <div class="header-title-group">
            <h2 class="card-title">日志查看器</h2>
            <LogLevelFilter v-model="selectedLogLevels" />
          </div>
          <div class="header-actions">
            <LogActionToolbar
              :connected="isConnected"
              :show-realtime-controls="isRealTime"
              :download-disabled="!props.logPath.trim()"
              @connect="connect"
              @disconnect="stop"
              @clear="clearLogs"
              @view="viewLogs"
              @download="downloadLogs"
            />
          </div>
        </div>
      </template>

      <div class="log-content">
        <!-- 日志显示区域 -->
        <div
          class="logs"
          id="logs"
          ref="logsContainer"
          @scroll="handleScroll"
          @wheel.passive="handleWheel"
          @touchstart.passive="handleTouchStart"
          @touchmove.passive="handleTouchMove"
          @keydown="handleKeydown"
          tabindex="0"
          aria-label="日志，向下滚动加载更早内容"
          v-loading="loading && !logLines.length && !history"
        >
          <div :style="{ height: `${before}px` }" aria-hidden="true" />
          <div
            v-for="log in limitedLogLines"
            :key="log.id"
            :data-log-id="log.id"
            class="log-line"
            :class="`log-level-${log.level}`"
          >
            <span class="log-timestamp">{{ log.timestamp }}</span>
            <span class="log-level">{{ log.level.toUpperCase() }}</span>
            <span class="log-message">{{ log.message }}</span>
          </div>
          <div :style="{ height: `${after}px` }" aria-hidden="true" />
          <div v-if="limitedLogLines.length === 0" class="empty-logs">暂无日志内容</div>
        </div>

        <!-- 日志信息 -->
        <div class="log-info">
          <el-text size="small">
            <span v-if="streamConnectionState === 'connected'" class="status-indicator connected">
              ● 已连接
            </span>
            <span v-else-if="streamConnectionState === 'connecting'" class="status-indicator">
              ● 正在连接
            </span>
            <span
              v-else-if="streamConnectionState !== 'unsupported'"
              class="status-indicator disconnected"
            >
              ● 已断开
            </span>
            当前显示 {{ limitedLogLines.length }} / 已加载 {{ loadedCount }} 行日志
            <span
              v-if="props.isRealTime && streamConnectionState === 'unsupported'"
              class="stream-status-message"
            >
              当前浏览器不支持实时日志，请手动刷新查看最新内容
            </span>
            <span
              v-else-if="props.isRealTime && streamConnectionState === 'reconnecting'"
              class="stream-status-message"
            >
              实时日志暂时断开，正在重新连接…
            </span>
            <span v-if="history" class="history-status" aria-live="polite">
              <span v-if="historyLoading">正在加载历史日志…</span>
              <template v-else-if="historyError">
                {{ historyError }}
                <el-button link type="primary" size="small" @click="retry">重试</el-button>
              </template>
              <span v-else-if="!hasOlder">已到最早日志</span>
            </span>
          </el-text>
          <el-button v-if="history" link type="primary" size="small" @click="returnLatest">
            返回最新
          </el-button>
        </div>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, shallowRef, watch, useTemplateRef } from 'vue'
import LogActionToolbar from '@/components/log/LogActionToolbar.vue'
import LogLevelFilter from '@/components/log/LogLevelFilter.vue'
import { useLogFileActions } from '@/composables/useLogFileActions'
import { useLogBuffer } from '@/composables/useLogBuffer'
import { useLogScroll } from '@/composables/useLogScroll'
import { registerRealtimeSource } from '@/composables/realtimeSources'
import type { LogEntry, LogLevel } from '@/types/log'
import { DEFAULT_VISIBLE_LOG_LEVELS } from '@/utils/logLevel'
import { formatDateTime } from '@/utils/timeUtils'
import { fetchLogSnapshot, fetchLogCatchUp } from '@/api/logs'
import { parseHttpError } from '@/http/errors'

interface Props {
  logPath: string
  isRealTime: boolean
  height?: string
  mobileHeight?: string
  fullscreen?: boolean
}
const props = withDefaults(defineProps<Props>(), {
  isRealTime: true,
  height: '',
  mobileHeight: '',
  fullscreen: false,
})
const logViewerStyle = computed<Record<string, string>>(() => ({
  '--log-viewer-height': props.height || 'calc(100vh - 320px)',
  '--log-viewer-mobile-height': props.mobileHeight || props.height || 'calc(100dvh - 220px)',
}))
type StreamConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'unsupported'
const stream = shallowRef<EventSource | null>(null)
const buffer = useLogBuffer()
const logLines = buffer.logs
const selectedLogLevels = ref<LogLevel[]>([...DEFAULT_VISIBLE_LOG_LEVELS])
const streamConnectionState = ref<StreamConnectionState>('idle')
const isConnected = computed(() => streamConnectionState.value === 'connected')
const loading = ref(false)
const logsContainer = useTemplateRef<HTMLElement>('logsContainer')
const { viewLogFile, downloadLogFile } = useLogFileActions()
let fileID: string | undefined
const scrolling = useLogScroll(
  logLines,
  selectedLogLevels,
  logsContainer,
  loading,
  () => props.logPath,
  () => fileID,
  () => void recover(true),
)
const {
  loadedCount,
  rows: limitedLogLines,
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
  retry,
} = scrolling
const returnLatest = () => {
  buffer.flush()
  scrolling.returnLatest()
}
let cursor: number | undefined
let hasInitialSnapshot = false
let unregisterStream: (() => void) | null = null
let requestID = 0
let controller: AbortController | null = null
let recovering = false
let liveRequested = false
let closedRecoveryAttempted = false

const cancelRequest = () => {
  requestID++
  controller?.abort()
  controller = null
  loading.value = false
}
const cancelHistory = scrolling.cancel
const addSystemLog = (message: string, level: LogEntry['level'] = 'info') => {
  buffer.append([{ level, message, timestamp: formatDateTime(Math.floor(Date.now() / 1000)) }])
  buffer.flush()
}
const showFailure = (error: unknown, message: string) => {
  const failure = parseHttpError(error, { fallbackMessage: message })
  if (failure.shouldNotify) {
    console.error(`${message}：`, failure.diagnostics)
    addSystemLog(failure.message, 'error')
  }
}
const loadInitialLogs = async (resetHistory = false): Promise<boolean> => {
  const path = props.logPath.trim()
  if (!path) return false
  if (resetHistory) {
    cursor = undefined
    fileID = undefined
    hasInitialSnapshot = false
    scrolling.returnLatest()
    scrolling.setSnapshot(0, 0)
    buffer.clear()
  }
  cancelRequest()
  const currentID = ++requestID
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const page = await fetchLogSnapshot(path, -1, 1000, current.signal)
    if (currentID !== requestID) return false
    if (fileID !== undefined && page.file_id !== fileID) {
      scrolling.returnLatest()
    }
    buffer.replace(
      props.isRealTime && page.cursor !== undefined
        ? page.entries.filter((entry) => !!entry.cursor)
        : page.entries,
    )
    cursor = page.cursor
    fileID = page.file_id
    scrolling.setSnapshot(page.pos, page.start_pos ?? page.cursor ?? 0)
    hasInitialSnapshot = true
    return true
  } catch (error) {
    if (!current.signal.aborted && currentID === requestID) {
      showFailure(error, '加载初始日志失败')
      hasInitialSnapshot = false
      streamConnectionState.value = 'idle'
    }
    return false
  } finally {
    if (currentID === requestID) {
      loading.value = false
      controller = null
    }
  }
}
const disconnect = (current = stream.value) => {
  if (current && stream.value !== current) return
  stream.value = null
  unregisterStream?.()
  unregisterStream = null
  current?.close()
  streamConnectionState.value = 'idle'
  buffer.flush()
}
const stop = () => {
  liveRequested = false
  cancelRequest()
  cancelHistory()
  disconnect()
}
const recover = async (reset: boolean) => {
  if (recovering) return
  recovering = true
  disconnect()
  cancelRequest()
  const currentID = ++requestID
  const current = new AbortController()
  controller = current
  streamConnectionState.value = 'reconnecting'
  let reloaded = false
  try {
    if (reset || cursor === undefined || fileID === undefined) {
      if (!(await loadInitialLogs(reset))) return
      reloaded = true
      if (reset) addSystemLog('日志位置已变化，已重新加载最新窗口')
    } else {
      let more = true
      // shortcut: 一次恢复最多补读 20 页，持续落后时回到最新窗口，历史仍可分页。
      for (let page = 0; page < 20 && more; page++) {
        const data = await fetchLogCatchUp(props.logPath, cursor!, current.signal, fileID)
        if (currentID !== requestID) return
        buffer.append(data.entries)
        cursor = data.cursor
        more = data.has_more
      }
      if (more) {
        if (!(await loadInitialLogs())) return
        reloaded = true
        addSystemLog('实时日志积压较多，已恢复最新窗口；较早内容可通过历史分页查看')
      }
    }
    buffer.flush()
    if (reloaded || !current.signal.aborted) openStream()
  } catch (error) {
    if (!current.signal.aborted && currentID === requestID) {
      const changed = parseHttpError(error).diagnostics.errorCode === 'LOG_CURSOR_INVALID'
      if (await loadInitialLogs(changed)) {
        if (changed) addSystemLog('日志位置已变化，已重新加载最新窗口')
        if (props.isRealTime) openStream()
      }
    }
  } finally {
    recovering = false
  }
}
const openStream = () => {
  if (!liveRequested || stream.value || !props.logPath.trim()) return
  if (typeof EventSource === 'undefined') {
    streamConnectionState.value = 'unsupported'
    return
  }
  const position = cursor === undefined ? '' : `&cursor=${cursor}`
  const identity = fileID === undefined ? '' : `&file_id=${encodeURIComponent(fileID)}`
  const source = new EventSource(
    `/api/logs/stream?path=${encodeURIComponent(props.logPath.trim())}${position}${identity}`,
  )
  stream.value = source
  streamConnectionState.value = 'connecting'
  unregisterStream = registerRealtimeSource(stop)
  let opened = false
  source.onopen = () => {
    if (stream.value !== source) return
    closedRecoveryAttempted = false
    if (opened) {
      void recover(false)
      return
    }
    opened = true
    streamConnectionState.value = 'connected'
  }
  source.onerror = (event) => {
    if ('data' in event || stream.value !== source) return
    if (source.readyState === EventSource.CLOSED) {
      disconnect(source)
      if (!closedRecoveryAttempted) {
        closedRecoveryAttempted = true
        void recover(false)
      }
    } else streamConnectionState.value = 'reconnecting'
  }
  source.addEventListener('log_batch', (event) => {
    if (stream.value !== source) return
    try {
      const data = JSON.parse((event as MessageEvent<string>).data) as {
        entries: LogEntry[]
        cursor: number
        file_id: string
      }
      if (!data.file_id || data.file_id !== fileID) {
        void recover(true)
        return
      }
      buffer.append(
        data.entries.filter(
          (entry) => !entry.cursor || cursor === undefined || entry.cursor > cursor,
        ),
      )
      cursor = Math.max(cursor || 0, data.cursor)
    } catch {
      addSystemLog('解析实时日志失败', 'error')
    }
  })
  source.addEventListener('resync_required', (event) => {
    if (stream.value !== source) return
    const { reason } = JSON.parse((event as MessageEvent<string>).data) as { reason: string }
    void recover(!['subscriber_queue_full', 'catch_up_limit_reached'].includes(reason))
  })
  source.addEventListener('error', (event) => {
    if (stream.value !== source) return
    try {
      const { reason } = JSON.parse((event as MessageEvent<string>).data) as { reason?: string }
      addSystemLog(`实时日志错误：${reason || '未知错误'}`, 'error')
    } catch {
      addSystemLog('解析实时日志错误失败', 'error')
    }
  })
}
const connect = async () => {
  if (stream.value || recovering) return
  closedRecoveryAttempted = false
  liveRequested = true
  if (!props.logPath.trim()) {
    addSystemLog('请输入日志文件路径', 'error')
    return
  }
  streamConnectionState.value = 'connecting'
  if (!hasInitialSnapshot && !(await loadInitialLogs())) return
  openStream()
}
watch(
  () => [props.logPath, props.isRealTime] as const,
  async ([path, live], previous) => {
    closedRecoveryAttempted = false
    liveRequested = live
    disconnect()
    cancelRequest()
    if (path !== previous?.[0]) {
      cancelHistory()
      buffer.clear()
      scrolling.returnLatest()
      cursor = undefined
      fileID = undefined
      hasInitialSnapshot = false
    }
    if (path.trim() && (await loadInitialLogs()) && live && props.isRealTime) openStream()
  },
)
onMounted(async () => {
  liveRequested = props.isRealTime
  if (!props.logPath.trim()) return
  if (props.isRealTime) streamConnectionState.value = 'connecting'
  if ((await loadInitialLogs()) && props.isRealTime) openStream()
})
onUnmounted(stop)
const clearLogs = () => {
  cancelHistory()
  buffer.clear()
  scrolling.returnLatest()
}
const viewLogs = () => viewLogFile(props.logPath)
const downloadLogs = () =>
  downloadLogFile(props.logPath, {
    emptyMessage: '请输入日志文件路径',
    errorPrefix: '下载日志失败',
  })
defineExpose({ disconnect: stop, refresh: loadInitialLogs })
</script>

<style scoped>
.log-viewer-container {
  width: 100%;
  max-width: none;
  display: flex;
  flex-direction: column;
  gap: 20px;
  padding: 0;
}

.log-card {
  width: 100%;
  max-width: none;
  margin: 0;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px 20px;
}

.header-title-group {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  min-width: 0;
}

.header-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 12px;
}

.card-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.log-content {
  margin-top: 20px;
}

.logs {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  padding: 10px;
  height: var(--log-viewer-height);
  overflow-y: auto;
  overflow-anchor: none;
  background-color: #fafafa;
  font-family: 'Courier New', Courier, monospace;
  font-size: 13px;
  line-height: 1.5;
}

.log-line {
  padding: 2px 0;
  display: flex;
  flex-direction: row;
  align-items: flex-start;
  word-break: break-all;
  white-space: pre-wrap;
}

.log-timestamp {
  color: var(--el-text-color-secondary);
  margin-right: 12px;
  min-width: 180px;
  flex-shrink: 0;
}

.log-level {
  font-weight: bold;
  margin-right: 12px;
  min-width: 60px;
  text-align: center;
  border-radius: 3px;
  padding: 0 4px;
  font-size: 11px;
  line-height: 16px;
  flex-shrink: 0;
}

.log-level-info {
  color: var(--el-text-color-regular);
}

.log-level-info .log-level {
  background-color: var(--el-color-primary-light-9);
  color: var(--el-color-primary);
}

.log-level-warn {
  color: var(--el-color-warning);
}

.log-level-warn .log-level {
  background-color: var(--el-color-warning-light-9);
  color: var(--el-color-warning);
}

.log-level-error {
  color: var(--el-color-danger);
}

.log-level-error .log-level {
  background-color: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.log-level-debug {
  color: var(--el-text-color-secondary);
}

.log-level-debug .log-level {
  background-color: var(--el-color-info-light-9);
  color: var(--el-text-color-secondary);
}

.log-message {
  flex: 1;
  color: var(--el-text-color-primary);
}

.empty-logs {
  text-align: center;
  color: var(--el-text-color-secondary);
  padding: 20px 0;
}

.log-info {
  margin-top: 10px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}

.log-info > .el-button {
  flex-shrink: 0;
  min-height: 24px;
}

.history-status {
  margin-left: 10px;
}

.status-indicator {
  margin-right: 10px;
  font-size: 12px;
  font-weight: 500;
}

.status-indicator.connected {
  color: var(--el-color-success);
}

.status-indicator.disconnected {
  color: var(--el-color-danger);
}

.stream-status-message {
  margin-left: 10px;
  color: var(--el-color-warning);
}

/* 滚动条样式：日志是高频长内容容器，保留更大的拖动目标。 */
.logs {
  scrollbar-width: auto;
  scrollbar-color: #c1c1c1 #f1f1f1;
}

.logs::-webkit-scrollbar {
  width: 8px;
  height: 8px;
}

.logs::-webkit-scrollbar-track {
  background: #f1f1f1;
  border-radius: 4px;
}

.logs::-webkit-scrollbar-thumb {
  background: #c1c1c1;
  border-radius: 4px;
}

.logs::-webkit-scrollbar-thumb:hover {
  background: #a8a8a8;
}

/* 移动端适配 */
@media (max-width: 768px) {
  .card-header {
    align-items: stretch;
    flex-direction: column;
    gap: 8px;
  }

  .header-title-group {
    align-items: stretch;
    flex-direction: column;
    gap: 2px;
  }

  .card-title {
    font-size: 18px;
  }

  .header-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .logs {
    height: var(--log-viewer-mobile-height);
    min-height: 360px;
    max-height: none;
    font-size: 12px;
  }

  .log-viewer-container.is-fullscreen .logs {
    min-height: 0;
  }

  .log-line {
    flex-wrap: wrap;
  }

  .log-timestamp {
    min-width: auto;
    width: 100%;
    margin-right: 0;
    margin-bottom: 2px;
    font-size: 11px;
  }

  .log-level {
    min-width: 40px;
    margin-right: 8px;
    font-size: 10px;
  }

  .log-message {
    width: calc(100% - 60px);
  }
}
</style>
