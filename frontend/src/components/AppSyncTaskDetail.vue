<template>
  <div class="sync-task-detail-container">
    <el-card class="task-detail-card" shadow="hover">
      <template #header>
        <PageHeader
          :title="`同步任务详情 #${taskId}`"
          actions-position="start"
          actions-layout="stacked"
          icon=""
          show-identity-on-mobile
        >
          <template #actions>
            <el-button type="primary" :icon="ArrowLeft" @click="goBack" size="small" link>
              返回同步记录
            </el-button>
          </template>
        </PageHeader>
      </template>

      <div class="task-content">
        <!-- 任务基本信息 -->
        <div class="task-info" v-loading="loading && !taskInfo">
          <h3>任务信息</h3>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="任务 ID">{{ taskId }}</el-descriptions-item>
            <el-descriptions-item label="任务状态">
              <el-tag v-if="taskInfo" :type="getStatusType(taskInfo.status)">
                {{ getStatusText(taskInfo.status) }}
              </el-tag>
              <span v-else>-</span>
            </el-descriptions-item>
            <el-descriptions-item label="子状态">
              <el-tag
                v-if="taskInfo && taskInfo.status === 1"
                type="primary"
                size="small"
                effect="light"
              >
                {{ getSubStatusText(taskInfo.sub_status) }}
              </el-tag>
              <span v-else>-</span>
            </el-descriptions-item>
            <el-descriptions-item label="开始时间">
              {{ taskInfo?.start_time ? formatDateTime(taskInfo.start_time) : '-' }}
            </el-descriptions-item>
            <el-descriptions-item label="结束时间">
              {{ taskInfo?.end_time ? formatDateTime(taskInfo.end_time) : '-' }}
            </el-descriptions-item>
            <el-descriptions-item label="总文件数">
              {{ taskInfo?.processed_files || 0 }}
            </el-descriptions-item>
            <el-descriptions-item label="生成 STRM">
              {{ taskInfo?.created_strm || 0 }}
            </el-descriptions-item>
            <el-descriptions-item label="下载元数据数">
              {{ taskInfo?.downloaded_meta || 0 }}
            </el-descriptions-item>
            <el-descriptions-item label="上传元数据数">
              {{ taskInfo?.uploaded_meta || 0 }}
            </el-descriptions-item>
            <el-descriptions-item label="媒体库刷新">
              <el-tag v-if="taskInfo" :type="embyRefreshDecision.type" effect="light">
                {{ embyRefreshDecision.label }}
              </el-tag>
              <span v-else>-</span>
              <span v-if="taskInfo" class="emby-refresh-reason">
                {{ embyRefreshDecision.reason }}
              </span>
            </el-descriptions-item>
            <el-descriptions-item label="执行时长">
              {{ getExecutionDuration() }}
            </el-descriptions-item>
          </el-descriptions>
        </div>

        <el-alert
          v-if="unsupported"
          class="task-stream-alert"
          type="warning"
          title="当前浏览器不支持实时更新，任务状态会定期刷新"
          :closable="false"
          show-icon
        />
        <el-alert
          v-else-if="!terminal && connectionState === 'reconnecting'"
          class="task-stream-alert"
          type="warning"
          title="实时更新暂时断开，正在重新连接…"
          :closable="false"
          show-icon
        />
        <el-alert
          v-else-if="errorMessage"
          class="task-stream-alert"
          type="warning"
          :title="errorMessage"
          :closable="false"
          show-icon
        />

        <!-- 执行时间线 -->
        <div class="execution-timeline" v-if="taskInfo">
          <h3>执行时间线</h3>
          <div class="horizontal-timeline">
            <div
              v-for="(item, index) in getTimelineItems()"
              :key="index"
              class="timeline-step"
              :class="{ completed: item.completed, current: item.current }"
            >
              <div class="step-icon">
                <el-icon :class="{ loading: item.current && !item.completed }">
                  <component :is="item.icon" />
                </el-icon>
              </div>
              <div class="step-content">
                <div class="step-title">{{ item.title }}</div>
                <div class="step-time" v-if="item.time && item.time !== '进行中…'">
                  {{ item.time }}
                </div>
                <div class="step-time" v-else-if="item.time === '进行中…'">进行中…</div>
                <div class="step-duration" v-if="item.duration">耗时：{{ item.duration }}</div>
              </div>
              <div
                v-if="index < getTimelineItems().length - 1"
                class="step-connector"
                :class="{ active: item.completed }"
              ></div>
            </div>
          </div>
        </div>

        <!-- 同步日志 -->
        <div class="task-logs">
          <SyncTaskLogPanel
            :logs="logs"
            :connected="connected"
            :log-path="resolvedLogPath"
            @connect="reconnect"
            @disconnect="disconnect"
            @clear="clearLogs"
            @download="downloadTaskLogs"
          />
        </div>
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import {
  ArrowLeft,
  Clock,
  Loading,
  Document,
  Download,
  SuccessFilled,
} from '@element-plus/icons-vue'
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '@/components/common/PageHeader.vue'
import SyncTaskLogPanel from '@/components/sync-task/SyncTaskLogPanel.vue'
import { useLogFileActions } from '@/composables/useLogFileActions'
import { useSyncTaskStream } from '@/composables/useSyncTaskStream'
import { navigateBackOrReplace } from '@/utils/navigation'
import { getEmbyRefreshDecision } from '@/utils/syncRefreshDecision'
import { formatDateTime } from '@/utils/timeUtils'

// 任务详情数据结构
interface TaskInfo {
  id: number
  start_time: number
  end_time: number | null
  status: 0 | 1 | 2 | 3 // 0-待开始，1-运行中，2-完成，3-失败
  sub_status: 0 | 1 | 2 // 0-待开始，1-正在处理网盘文件列表，2-正在处理本地文件列表
  processed_files: number
  created_strm: number
  downloaded_meta: number
  uploaded_meta: number
  // 时间线相关字段
  net_file_start_at: number | null
  net_file_finish_at: number | null
  local_file_start_at: number | null
  local_file_finish_at: number | null
}

const route = useRoute()
const router = useRouter()

// 获取任务 ID
const taskId = computed(() => Number(route.params.id))

const {
  task,
  logs,
  loading,
  connected,
  connectionState,
  terminal,
  unsupported,
  errorMessage,
  logPath,
  reconnect,
  disconnect,
  clearLogs,
} = useSyncTaskStream(taskId)
const { downloadLogFile } = useLogFileActions()

const resolvedLogPath = computed(() => logPath.value || `sync/sync_${taskId.value}.log`)

const downloadTaskLogs = () => {
  downloadLogFile(resolvedLogPath.value, {
    emptyMessage: '同步任务日志路径为空',
    errorPrefix: '下载同步任务日志失败',
  })
}

const taskInfo = computed<TaskInfo | null>(() => {
  if (!task.value) return null
  return {
    id: task.value.id,
    start_time: task.value.created_at,
    end_time: task.value.finish_at || null,
    status: task.value.status,
    sub_status: task.value.sub_status,
    processed_files: task.value.total,
    created_strm: task.value.new_strm,
    downloaded_meta: task.value.new_meta || 0,
    uploaded_meta: task.value.new_upload || 0,
    net_file_start_at: task.value.net_file_start_at || null,
    net_file_finish_at: task.value.net_file_finish_at || null,
    local_file_start_at: task.value.local_file_start_at || null,
    local_file_finish_at: task.value.local_file_finish_at || null,
  }
})

const embyRefreshDecision = computed(() =>
  getEmbyRefreshDecision({
    createdStrm: taskInfo.value?.created_strm || 0,
    downloadedMeta: taskInfo.value?.downloaded_meta || 0,
    status: taskInfo.value?.status,
  }),
)

// 返回上一页
const goBack = () => {
  void navigateBackOrReplace(router, { name: 'sync-records' })
}

// 获取状态标签类型
const getStatusType = (status: number) => {
  switch (status) {
    case 0:
      return 'info' // 待开始
    case 1:
      return 'primary' // 运行中
    case 2:
      return 'success' // 完成
    case 3:
      return 'danger' // 失败
    default:
      return 'info'
  }
}

// 获取状态文本
const getStatusText = (status: number) => {
  switch (status) {
    case 0:
      return '待开始'
    case 1:
      return '运行中'
    case 2:
      return '已完成'
    case 3:
      return '失败'
    default:
      return '未知'
  }
}

// 获取子状态文本
const getSubStatusText = (subStatus: number) => {
  switch (subStatus) {
    case 0:
      return '待开始'
    case 1:
      return '正在处理网盘文件'
    case 2:
      return '正在处理本地文件'
    default:
      return '未知'
  }
}

// 计算执行时长
const getExecutionDuration = () => {
  if (!taskInfo.value?.start_time) return '-'

  // 如果任务未完成，使用当前时间计算
  if (!taskInfo.value.end_time) {
    const currentTime = Math.floor(Date.now() / 1000)
    const duration = currentTime - taskInfo.value.start_time
    return formatDuration(duration)
  }

  // 已完成任务使用 finish_at - created_at 计算
  const duration = taskInfo.value.end_time - taskInfo.value.start_time
  return formatDuration(duration)
}

// 格式化时长
const formatDuration = (duration: number) => {
  if (duration < 60) {
    return `${duration} 秒`
  } else if (duration < 3600) {
    const minutes = Math.floor(duration / 60)
    const seconds = duration % 60
    return `${minutes} 分 ${seconds} 秒`
  } else {
    const hours = Math.floor(duration / 3600)
    const minutes = Math.floor((duration % 3600) / 60)
    return `${hours} 小时 ${minutes} 分`
  }
}

// 获取时间线项目
const getTimelineItems = () => {
  if (!taskInfo.value) return []

  const items = []

  // 阶段 1：开始
  const startTime = taskInfo.value.start_time
  items.push({
    title: '开始任务',
    time: startTime ? formatDateTime(startTime) : '未开始',
    icon: Clock,
    duration: null, // 不计算时长
    completed: !!startTime,
    current:
      taskInfo.value.status === 1 &&
      taskInfo.value.sub_status === 0 &&
      !taskInfo.value.net_file_start_at,
  })

  // 阶段 2：处理网盘文件
  const netFileStartAt = taskInfo.value.net_file_start_at
  const netFileFinishAt = taskInfo.value.net_file_finish_at
  const netFileCompleted = netFileStartAt && netFileFinishAt
  const netFileCurrent = taskInfo.value.status === 1 && taskInfo.value.sub_status === 1

  let netFileDuration = null
  if (netFileCompleted) {
    netFileDuration = formatDuration(netFileFinishAt! - netFileStartAt!)
  }

  items.push({
    title: netFileCompleted
      ? '已完成处理网盘文件'
      : netFileCurrent
        ? '正在处理网盘文件'
        : '处理网盘文件',
    time: netFileStartAt
      ? netFileFinishAt
        ? formatDateTime(netFileStartAt)
        : '进行中…'
      : '未开始',
    icon: netFileCurrent ? Loading : Document,
    duration: netFileDuration,
    completed: !!netFileCompleted,
    current: netFileCurrent,
  })

  // 阶段 3：处理本地文件
  const localFileStartAt = taskInfo.value.local_file_start_at
  const localFileFinishAt = taskInfo.value.local_file_finish_at
  const localFileCompleted = localFileStartAt && localFileFinishAt
  const localFileCurrent = taskInfo.value.status === 1 && taskInfo.value.sub_status === 2

  let localFileDuration = null
  if (localFileCompleted) {
    localFileDuration = formatDuration(localFileFinishAt! - localFileStartAt!)
  }

  items.push({
    title: localFileCompleted
      ? '已完成处理本地文件'
      : localFileCurrent
        ? '正在处理本地文件'
        : '处理本地文件',
    time: localFileStartAt
      ? localFileFinishAt
        ? formatDateTime(localFileStartAt)
        : '进行中…'
      : '未开始',
    icon: localFileCurrent ? Loading : Download,
    duration: localFileDuration,
    completed: !!localFileCompleted,
    current: localFileCurrent,
  })

  // 阶段 4：结束
  const endTime = taskInfo.value.end_time
  items.push({
    title: '完成任务',
    time: endTime ? formatDateTime(endTime) : '未完成',
    icon: SuccessFilled,
    duration: null, // 不计算时长
    completed: !!endTime,
    current: false,
  })

  return items
}
</script>

<style scoped>
.sync-task-detail-container {
  width: 100%;
  max-width: none;
  display: flex;
  flex-direction: column;
  gap: 0;
  padding: 0;
}

.task-detail-card {
  width: 100%;
  max-width: none;
  margin: 0;
}

.task-detail-card :deep(.qms-page-header) {
  margin-bottom: 0;
}

.task-content {
  margin-top: 20px;
}

.task-info {
  margin-bottom: 30px;
}

.task-info h3 {
  margin: 0 0 16px 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.execution-timeline {
  margin-bottom: 30px;
}

.task-stream-alert {
  margin-bottom: 24px;
}

.emby-refresh-reason {
  margin-left: 8px;
  color: #909399;
  font-size: 13px;
}

.execution-timeline h3 {
  margin: 0 0 20px 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.horizontal-timeline {
  display: flex;
  align-items: flex-start;
  gap: 0;
  padding: 20px 0;
  overflow-x: auto;
}

.timeline-step {
  display: flex;
  flex-direction: column;
  align-items: center;
  position: relative;
  min-width: 140px;
  flex-shrink: 0;
}

.timeline-step.completed .step-icon {
  background-color: #67c23a;
  border-color: #67c23a;
  color: #fff;
}

.timeline-step.current .step-icon {
  background-color: #409eff;
  border-color: #409eff;
  color: #fff;
  animation: pulse 2s infinite;
}

.timeline-step:not(.completed):not(.current) .step-icon {
  background-color: #f5f7fa;
  border-color: #dcdfe6;
  color: #c0c4cc;
}

.step-icon {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  border: 2px solid;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 12px;
  transition:
    border-color 0.2s ease,
    background-color 0.2s ease,
    color 0.2s ease;
  z-index: 2;
  position: relative;
}

.step-icon .el-icon {
  font-size: 20px;
}

.step-icon .el-icon.loading {
  animation: rotate 2s linear infinite;
}

.step-content {
  text-align: center;
  max-width: 120px;
}

.step-title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 4px;
  line-height: 1.4;
  word-break: break-word;
}

.timeline-step:not(.completed):not(.current) .step-title {
  color: #c0c4cc;
}

.step-time {
  font-size: 12px;
  color: #909399;
  margin-bottom: 2px;
}

.timeline-step:not(.completed):not(.current) .step-time {
  color: #c0c4cc;
}

.step-duration {
  font-size: 12px;
  color: #67c23a;
  font-weight: 500;
}

.step-connector {
  position: absolute;
  top: 19px;
  left: 50%;
  right: -50%;
  height: 2px;
  background-color: #dcdfe6;
  z-index: 1;
}

.step-connector.active {
  background-color: #67c23a;
}

.timeline-step:last-child .step-connector {
  display: none;
}

@keyframes pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(64, 158, 255, 0.4);
  }

  70% {
    box-shadow: 0 0 0 10px rgba(64, 158, 255, 0);
  }

  100% {
    box-shadow: 0 0 0 0 rgba(64, 158, 255, 0);
  }
}

@keyframes rotate {
  from {
    transform: rotate(0deg);
  }

  to {
    transform: rotate(360deg);
  }
}

.timeline-content {
  padding: 8px 0;
}

.timeline-title {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 4px;
}

.timeline-duration {
  font-size: 14px;
  color: #909399;
}

.file-compare-table h3 {
  margin: 0 0 16px 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.table-container {
  width: 100%;
  overflow-x: auto;
}

.compare-table {
  width: 100%;
  margin-bottom: 20px;
}

/* 为表格列添加竖线分隔 */
.compare-table :deep(.el-table__cell) {
  border-right: 1px solid #ebeef5;
}

.file-paths {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.file-path {
  font-size: 13px;
  color: #606266;
  word-break: break-all;
  line-height: 1.4;
}

/* 移动端适配 */
@media (max-width: 768px) {
  .task-info h3,
  .task-logs h3 {
    font-size: 16px;
  }

  .logs-table {
    font-size: 12px;
  }

  .horizontal-timeline {
    padding: 15px 0;
  }

  .timeline-step {
    min-width: 100px;
  }

  .step-content {
    max-width: 90px;
  }

  .step-title {
    font-size: 12px;
  }

  .step-time {
    font-size: 11px;
  }

  .step-duration {
    font-size: 11px;
  }

  .step-icon {
    width: 35px;
    height: 35px;
    margin-bottom: 8px;
  }

  .step-icon .el-icon {
    font-size: 18px;
  }

  .step-connector {
    top: 16px;
  }
}
</style>
