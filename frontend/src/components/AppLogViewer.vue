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
              @disconnect="disconnect"
              @clear="clearLogs"
              @download="downloadLogs"
            />
          </div>
        </div>
      </template>

      <div class="log-content">
        <!-- 日志显示区域 -->
        <div class="logs" id="logs" ref="logsContainer" @scroll="handleScroll" v-loading="loading">
          <div
            v-for="(log, index) in limitedLogLines"
            :key="log.id || index"
            class="log-line"
            :class="`log-level-${log.level}`"
          >
            <span class="log-timestamp">{{ log.timestamp }}</span>
            <span class="log-level">{{ log.level.toUpperCase() }}</span>
            <span class="log-message">{{ log.message }}</span>
          </div>
          <div v-if="limitedLogLines.length === 0" class="empty-logs">暂无日志内容</div>
        </div>

        <!-- 日志信息 -->
        <div class="log-info">
          <el-text size="small">
            当前显示 {{ limitedLogLines.length }} / 已加载 {{ logLines.length }} 行日志
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
          </el-text>
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
import { registerRealtimeSource } from '@/composables/realtimeSources'
import type { LogEntry, LogLevel } from '@/types/log'
import { DEFAULT_VISIBLE_LOG_LEVELS, filterLogEntriesByLevels } from '@/utils/logLevel'
import { formatDateTime } from '@/utils/timeUtils'

// 定义组件属性
interface Props {
  logPath: string
  isRealTime: boolean
  height?: string
  mobileHeight?: string
  fullscreen?: boolean
}

type StreamConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'unsupported'

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

// 组件状态
const stream = shallowRef<EventSource | null>(null)
const logLines = ref<LogEntry[]>([])
const selectedLogLevels = ref<LogLevel[]>([...DEFAULT_VISIBLE_LOG_LEVELS])
const streamConnectionState = ref<StreamConnectionState>('idle')
const isConnected = computed(() => streamConnectionState.value === 'connected')
const loading = ref(false)
const logsContainer = useTemplateRef<HTMLElement>('logsContainer')
const { downloadLogFile } = useLogFileActions()

// 日志数量限制配置
const MAX_LOG_ENTRIES = 2000
const CLEANUP_THRESHOLD = 2500
let cleanupTimer: ReturnType<typeof setTimeout> | null = null

// 日志配置
let isLoadingOldLogs = false
let currentOffset = 0
let hasReachedEnd = false
let followLatest = true
let unregisterStream: (() => void) | null = null
let streamOpened = false
let hasInitialSnapshot = false
let snapshotRequestID = 0
let snapshotController: AbortController | null = null

const HTTP_URL = '/api/logs/old'
const STREAM_URL = '/api/logs/stream'

const readLogResponseError = async (response: Response) => {
  try {
    const body = await response.json()
    if (body?.error) return String(body.error)
  } catch {
    // 忽略非 JSON 错误响应
  }
  return 'HTTP 请求失败'
}

// 限制显示的日志条目
const limitedLogLines = computed(() => {
  return filterLogEntriesByLevels(logLines.value, selectedLogLevels.value).slice(0, MAX_LOG_ENTRIES)
})

const resetLogState = () => {
  cancelSnapshotRequest()
  currentOffset = 0
  hasReachedEnd = false
  followLatest = true
  hasInitialSnapshot = false
  logLines.value = []
}

const cancelSnapshotRequest = () => {
  snapshotRequestID++
  snapshotController?.abort()
  snapshotController = null
  isLoadingOldLogs = false
  loading.value = false
}

// 监听日志路径和实时模式变化，自动维护连接
watch(
  () => [props.logPath, props.isRealTime] as const,
  async ([logPath, isRealTime], oldValue) => {
    const oldLogPath = oldValue?.[0] ?? ''
    const normalizedLogPath = logPath.trim()
    if (logPath !== oldLogPath) {
      disconnect()
      resetLogState()
    }
    if (!isRealTime) {
      disconnect()
      return
    }
    if (normalizedLogPath) {
      streamConnectionState.value = 'connecting'
      const loaded = await loadInitialLogs()
      if (loaded) connect()
    }
  },
)

// 初始化
onMounted(async () => {
  // 如果提供了日志路径，先加载历史日志
  if (props.logPath) {
    resetLogState()
    if (props.isRealTime) streamConnectionState.value = 'connecting'
    // 加载历史日志，设置 limit 为 1000
    const loaded = await loadInitialLogs()
    if (props.isRealTime && loaded) {
      connect()
    }
  }
})

// 加载初始历史日志
const loadInitialLogs = async (): Promise<boolean> => {
  const logPath = props.logPath.trim()
  if (!logPath) {
    return false
  }

  cancelSnapshotRequest()
  const requestID = ++snapshotRequestID
  const controller = new AbortController()
  snapshotController = controller
  const isCurrentSnapshotRequest = () =>
    snapshotRequestID === requestID &&
    snapshotController === controller &&
    props.logPath.trim() === logPath

  isLoadingOldLogs = true
  loading.value = true

  // 构建 HTTP 请求 URL，加载前 1000 条日志
  const apiUrl = `${HTTP_URL}?path=${encodeURIComponent(logPath)}&pos=-1&direction=forward&limit=1000`

  try {
    const response = await fetch(apiUrl, { credentials: 'include', signal: controller.signal })
    if (!isCurrentSnapshotRequest()) return false
    if (!response.ok) {
      throw new Error(await readLogResponseError(response))
    }
    const rs = await response.json()
    if (!isCurrentSnapshotRequest()) return false
    const entries = rs.entries || []
    currentOffset = rs.pos
    // 处理返回的旧日志条目
    logLines.value = Array.isArray(entries) ? [...entries] : []
    hasInitialSnapshot = true
    return true
  } catch (error) {
    if (controller.signal.aborted || !isCurrentSnapshotRequest()) {
      return false
    }
    console.error('加载初始日志失败：', error)
    addSystemLog(
      `加载初始日志失败：${error instanceof Error ? error.message : '未知错误'}`,
      'error',
    )
    hasInitialSnapshot = false
    if (!stream.value && streamConnectionState.value === 'connecting') {
      streamConnectionState.value = 'idle'
    }
    return false
  } finally {
    if (isCurrentSnapshotRequest()) {
      isLoadingOldLogs = false
      loading.value = false
      snapshotController = null
    }
  }
}

// 清理资源
onUnmounted(() => {
  disconnect()
  cancelSnapshotRequest()
  if (cleanupTimer) {
    clearTimeout(cleanupTimer)
  }
})

// 处理滚动事件
const handleScroll = () => {
  if (!logsContainer.value) return

  const scrollTop = logsContainer.value.scrollTop
  const scrollHeight = logsContainer.value.scrollHeight
  const clientHeight = logsContainer.value.clientHeight

  followLatest = scrollTop <= 20

  // 检查是否滚动到底部，需要加载更多旧日志
  if (scrollHeight - scrollTop - clientHeight < 50 && !isLoadingOldLogs && !hasReachedEnd) {
    loadOldLogs()
  }
}

// 防抖清理函数
const debouncedCleanupLogs = () => {
  if (cleanupTimer) {
    clearTimeout(cleanupTimer)
  }

  cleanupTimer = setTimeout(() => {
    if (logLines.value.length > CLEANUP_THRESHOLD) {
      logLines.value = logLines.value.slice(0, MAX_LOG_ENTRIES)
    }
  }, 300)
}

// 添加日志条目
const addLogEntry = (entry: LogEntry) => {
  // 为日志条目添加唯一 ID
  const entryWithId = {
    ...entry,
    id: `${Date.now()}-${Math.random().toString(36).substr(2, 9)}`,
  }

  // 添加到日志数组前面（最新的日志在最前面）
  logLines.value = [entryWithId, ...logLines.value]

  // 防抖清理检查
  debouncedCleanupLogs()

  if (followLatest) {
    requestAnimationFrame(() => {
      if (logsContainer.value) {
        logsContainer.value.scrollTop = 0
      }
    })
  }
}

// 添加系统日志
const addSystemLog = (message: string, level: LogEntry['level'] = 'info') => {
  addLogEntry({
    level,
    message,
    timestamp: formatDateTime(Math.floor(Date.now() / 1000)),
  })
}

const connect = () => {
  if (stream.value) {
    return
  }

  const logPath = props.logPath.trim()
  if (!logPath) {
    addSystemLog('请输入日志文件路径', 'error')
    return
  }
  if (!hasInitialSnapshot) {
    streamConnectionState.value = 'connecting'
    void loadInitialLogs().then((loaded) => {
      if (loaded) connect()
    })
    return
  }

  if (typeof EventSource === 'undefined') {
    streamConnectionState.value = 'unsupported'
    return
  }

  streamConnectionState.value = 'connecting'
  const currentStream = new EventSource(`${STREAM_URL}?path=${encodeURIComponent(logPath)}`)
  stream.value = currentStream
  unregisterStream = registerRealtimeSource(() => disconnect(currentStream))
  currentStream.onopen = () => {
    if (stream.value !== currentStream) return
    streamConnectionState.value = 'connected'
    if (streamOpened) void loadInitialLogs()
    streamOpened = true
  }
  currentStream.onerror = (event) => {
    if ('data' in event) return
    if (stream.value === currentStream) {
      streamConnectionState.value = 'reconnecting'
    }
  }
  currentStream.addEventListener('log_append', (event) => {
    if (stream.value !== currentStream) return
    try {
      addLogEntry(JSON.parse((event as MessageEvent<string>).data) as LogEntry)
    } catch {
      addSystemLog('解析实时日志失败', 'error')
    }
  })
  currentStream.addEventListener('resync_required', () => {
    if (stream.value === currentStream) void loadInitialLogs()
  })
  currentStream.addEventListener('error', (event) => {
    if (stream.value !== currentStream) return
    try {
      const { reason } = JSON.parse((event as MessageEvent<string>).data) as { reason?: string }
      addSystemLog(`实时日志错误：${reason || '未知错误'}`, 'error')
    } catch {
      addSystemLog('解析实时日志错误失败', 'error')
    }
  })
}

const disconnect = (currentStream = stream.value) => {
  if (!currentStream) {
    streamConnectionState.value = 'idle'
    streamOpened = false
    return
  }
  if (stream.value !== currentStream) return
  stream.value = null
  unregisterStream?.()
  unregisterStream = null
  currentStream.close()
  streamConnectionState.value = 'idle'
  streamOpened = false
}

// 通过 HTTP 接口加载旧日志
const loadOldLogs = () => {
  const logPath = props.logPath.trim()
  if (!logPath) {
    return
  }

  // 如果已经到达日志文件末尾，不再加载
  if (hasReachedEnd) {
    return
  }

  isLoadingOldLogs = true
  loading.value = true

  // 构建 HTTP 请求 URL
  const apiUrl = `${HTTP_URL}?path=${encodeURIComponent(logPath)}&pos=${currentOffset}&direction=forward&limit=100`

  // 发送 HTTP 请求
  fetch(apiUrl, { credentials: 'include' })
    .then(async (response) => {
      if (!response.ok) {
        throw new Error(await readLogResponseError(response))
      }
      return response.json() // 解析为 JSON 格式
    })
    .then((rs) => {
      const entries = rs.entries || []
      currentOffset = rs.pos
      // 处理返回的旧日志条目
      if (Array.isArray(entries) && entries.length > 0) {
        // 直接添加新日志
        logLines.value = [...logLines.value, ...entries]
      } else {
        // 返回数据为空，说明已经到达日志文件末尾
        hasReachedEnd = true
      }
    })
    .catch((error) => {
      console.error('加载旧日志失败：', error)
      addSystemLog(
        `加载旧日志失败：${error instanceof Error ? error.message : '未知错误'}`,
        'error',
      )
    })
    .finally(() => {
      isLoadingOldLogs = false
      loading.value = false
    })
}

// 清空日志
const clearLogs = () => {
  logLines.value = []
}

// 下载日志文件
const downloadLogs = () => {
  downloadLogFile(props.logPath, {
    emptyMessage: '请输入日志文件路径',
    errorPrefix: '下载日志失败',
  })
}

// 暴露方法给父组件
defineExpose({
  disconnect,
})
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
}

.status-indicator {
  margin-left: 10px;
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
