<template>
  <div class="upload-queue-container">
    <PageHeader class="upload-queue-page-header">
      <template #actions>
        <div class="header-actions">
          <div class="queue-control-actions">
            <el-button
              type="info"
              :size="queueControlSize"
              @click="refreshQueue"
              :loading="backgroundRefreshing"
              >刷新</el-button
            >
            <el-button
              type="warning"
              :size="queueControlSize"
              @click="pauseAllTasks"
              :disabled="!canPauseAllTasks"
              >全部暂停</el-button
            >
            <el-button
              type="success"
              :size="queueControlSize"
              @click="resumeAllTasks"
              :disabled="!canResumeAllTasks"
              >全部恢复</el-button
            >
          </div>
          <div class="queue-cleanup-actions">
            <el-button type="warning" :size="queueControlSize" @click="retryAllFailedTasks"
              >重试失败</el-button
            >
            <el-button type="warning" :size="queueControlSize" @click="clearQueue"
              >清空等待</el-button
            >
            <el-button type="danger" :size="queueControlSize" @click="clearSuccessAndFailedTasks"
              >清空完成/失败</el-button
            >
          </div>
        </div>
      </template>
    </PageHeader>

    <div class="queue-toolbar-row">
      <div class="filter-container">
        <el-select
          v-model="statusFilter"
          :size="queueControlSize"
          placeholder="请选择状态"
          @change="handleStatusChange"
        >
          <el-option label="全部状态" :value="-1"></el-option>
          <el-option label="等待上传" :value="0"></el-option>
          <el-option label="正在上传" :value="1"></el-option>
          <el-option label="上传完成" :value="2"></el-option>
          <el-option label="上传失败" :value="3"></el-option>
          <el-option label="已取消" :value="4"></el-option>
          <el-option label="等待完成处理" :value="5"></el-option>
          <el-option label="正在完成处理" :value="6"></el-option>
        </el-select>
      </div>

      <div class="queue-stats">
        <el-statistic :value="uploading">
          <template #title>
            <div style="display: inline-flex; align-items: center">
              <el-text class="mx-1" type="primary">正在上传的任务总数</el-text>
            </div>
          </template>
        </el-statistic>
      </div>
    </div>

    <el-table
      v-if="isMobileView"
      ref="mobileTableRef"
      :data="queueData"
      style="width: 100%"
      v-loading="initialLoading || queryLoading"
      :show-header="queueData.length > 0"
      empty-text="暂无上传任务"
      :row-key="(row: UploadTask) => String(row.id)"
      :expand-row-keys="pageState.expandedRowKeys"
      @expand-change="handleExpandChange"
      :row-class-name="tableRowClassName"
      :height="tableHeight"
      flexible
      class="queue-table-mobile"
    >
      <el-table-column
        type="expand"
        width="1"
        class-name="queue-expand-carrier"
        label-class-name="queue-expand-carrier"
      >
        <template #default="scope">
          <section
            :id="getUploadDetailRegionId(scope.row)"
            class="queue-task-details-region"
            role="region"
            :aria-label="`上传任务 ${scope.row.id} 详情`"
          >
            <QueueTaskDetails :groups="buildUploadTaskDetailGroups(scope.row)" :max-columns="2" />
          </section>
        </template>
      </el-table-column>
      <el-table-column
        width="44"
        align="center"
        class-name="queue-expand-column"
        label-class-name="queue-expand-column"
      >
        <template #default="scope">
          <QueueTaskExpandButton
            :expanded="isUploadRowExpanded(scope.row)"
            :label="getUploadExpandButtonLabel(scope.row)"
            :controls="
              isUploadRowExpanded(scope.row) ? getUploadDetailRegionId(scope.row) : undefined
            "
            @toggle="toggleUploadRowExpansion(scope.row)"
          />
        </template>
      </el-table-column>

      <el-table-column prop="speed" label="上传文件">
        <template #default="scope">
          <div class="mobile-task-summary">
            <div class="mobile-task-meta">
              <span class="mobile-task-id"># {{ scope.row.id }}</span>
              <el-tag size="small" effect="plain">{{
                getUploadSourceName(scope.row.source)
              }}</el-tag>
              <el-tag size="small" :type="getTaskSourceTypeTagType(scope.row.source_type)">
                {{ getTaskSourceTypeName(scope.row.source_type) }}
              </el-tag>
              <el-tag size="small" :type="getUploadStatusTagType(scope.row.status)">
                {{ getUploadStatusText(scope.row.status) }}
              </el-tag>
            </div>
            <div class="mobile-task-title-row">
              <span class="mobile-task-file-name">{{ getUploadTaskName(scope.row) }}</span>
            </div>
            <div class="mobile-task-metrics">
              <span>{{ getUploadedSizeLabel(scope.row) }}</span>
              <span> · {{ getUploadProgressPercent(scope.row) }}%</span>
              <span v-if="scope.row.status === 1 && scope.row.upload_speed_bytes">
                · {{ formatByteRate(scope.row.upload_speed_bytes) }}
              </span>
            </div>
            <el-progress
              class="queue-progress mobile-progress"
              :percentage="getUploadProgressPercent(scope.row)"
              :stroke-width="6"
              :show-text="false"
            />
          </div>
        </template>
      </el-table-column>
    </el-table>
    <el-table
      v-else
      ref="desktopTableRef"
      :data="queueData"
      style="width: 100%"
      v-loading="initialLoading || queryLoading"
      empty-text="暂无上传任务"
      :row-key="(row: UploadTask) => String(row.id)"
      :expand-row-keys="pageState.expandedRowKeys"
      @expand-change="handleExpandChange"
      :row-class-name="tableRowClassName"
      :height="tableHeight"
      flexible
      class="queue-table-desktop"
    >
      <el-table-column
        type="expand"
        width="1"
        class-name="queue-expand-carrier"
        label-class-name="queue-expand-carrier"
      >
        <template #default="scope">
          <section
            :id="getUploadDetailRegionId(scope.row)"
            class="queue-task-details-region"
            role="region"
            :aria-label="`上传任务 ${scope.row.id} 详情`"
          >
            <QueueTaskDetails :groups="buildUploadTaskDetailGroups(scope.row)" :max-columns="5" />
          </section>
        </template>
      </el-table-column>
      <el-table-column
        width="44"
        align="center"
        class-name="queue-expand-column"
        label-class-name="queue-expand-column"
      >
        <template #default="scope">
          <QueueTaskExpandButton
            :expanded="isUploadRowExpanded(scope.row)"
            :label="getUploadExpandButtonLabel(scope.row)"
            :controls="
              isUploadRowExpanded(scope.row) ? getUploadDetailRegionId(scope.row) : undefined
            "
            @toggle="toggleUploadRowExpansion(scope.row)"
          />
        </template>
      </el-table-column>
      <el-table-column label="任务" width="320">
        <template #default="scope">
          <div class="desktop-task-summary">
            <el-tooltip
              :content="getUploadTaskTooltipContent(scope.row)"
              placement="top"
              popper-class="qms-contained-tooltip"
              append-to="body"
            >
              <div class="desktop-task-summary-text">
                <span class="desktop-task-file-name">{{ getUploadTaskName(scope.row) }}</span>
                <span class="desktop-task-meta">
                  <span class="desktop-task-id"># {{ scope.row.id }}</span> ·
                  <el-tag class="desktop-task-source" size="small" effect="plain">
                    {{ getUploadSourceName(scope.row.source) }}
                  </el-tag>
                  ·
                  <el-tag
                    class="desktop-task-source-type"
                    size="small"
                    :type="getTaskSourceTypeTagType(scope.row.source_type)"
                  >
                    {{ getTaskSourceTypeName(scope.row.source_type) }}
                  </el-tag>
                </span>
              </div>
            </el-tooltip>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="status" label="状态" width="104">
        <template #default="scope">
          <div v-if="scope.row.error">
            <el-tooltip
              :content="scope.row.error"
              placement="top"
              popper-class="qms-contained-tooltip"
              append-to="body"
            >
              <el-tag :type="getUploadStatusTagType(scope.row.status)">
                <el-icon>
                  <WarningFilled />
                </el-icon>
                {{ getUploadStatusText(scope.row.status) }}
              </el-tag>
            </el-tooltip>
          </div>
          <div v-else>
            <el-tag :type="getUploadStatusTagType(scope.row.status)">
              {{ getUploadStatusText(scope.row.status) }}
            </el-tag>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="progress_percent" label="进度" width="160">
        <template #default="scope">
          <div class="progress-cell">
            <el-progress
              class="queue-progress"
              :percentage="getUploadProgressPercent(scope.row)"
              :stroke-width="8"
            />
            <div class="progress-meta">
              <span>{{ getUploadedSizeLabel(scope.row) }}</span>
              <span v-if="scope.row.status === 1 && scope.row.upload_speed_bytes">
                {{ formatByteRate(scope.row.upload_speed_bytes) }}
              </span>
            </div>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="upload_phase" label="结果" width="180">
        <template #default="scope">
          <div class="stage-result-cell">
            <el-tag
              v-if="shouldShowUploadStageSummary(scope.row)"
              :type="getUploadStageResultTagType(scope.row)"
              effect="light"
            >
              {{ getUploadStageSummaryLabel(scope.row) }}
            </el-tag>
            <el-tooltip
              v-if="getUploadTransportDetailSummary(scope.row)"
              :content="getUploadTransportDetailSummary(scope.row)"
              placement="top"
              popper-class="qms-contained-tooltip"
              append-to="body"
            >
              <el-text class="stage-detail-link" type="info">详情</el-text>
            </el-tooltip>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="上传位置" min-width="200">
        <template #default="scope">
          <el-tooltip
            v-if="getUploadQueueLocationSummary(scope.row)"
            :content="getUploadQueueLocationSummary(scope.row)"
            placement="top"
            popper-class="qms-contained-tooltip"
            append-to="body"
          >
            <span class="desktop-location-summary">
              <template
                v-if="
                  scope.row.source_type !== 'local' &&
                  scope.row.local_full_path &&
                  scope.row.remote_full_path
                "
              >
                {{ scope.row.local_full_path }}<br />
                <span class="desktop-location-direction">上传至</span>
                {{ scope.row.remote_full_path }}
              </template>
              <template v-else>{{ getUploadQueueLocationSummary(scope.row) }}</template>
            </span>
          </el-tooltip>
        </template>
      </el-table-column>
      <el-table-column prop="start_time" label="时间" width="260">
        <template #default="scope">
          <div class="queue-time-summary">
            <span v-if="scope.row.start_time"
              >开始时间：{{ formatDateTime(scope.row.start_time) }}</span
            >
            <span v-if="scope.row.end_time"
              >结束时间：{{ formatDateTime(scope.row.end_time) }}</span
            >
            <span v-if="scope.row.retry_count > 0">重试 {{ scope.row.retry_count }} 次</span>
          </div>
        </template>
      </el-table-column>
    </el-table>
    <ResponsivePagination
      v-model:current-page="currentPage"
      v-model:page-size="pageSize"
      :page-sizes="[10, 20, 50, 100]"
      :total="total"
      :is-mobile="isMobileView"
      @size-change="handleSizeChange"
      @current-change="handleCurrentChange"
    />
  </div>
</template>

<script setup lang="ts">
import {
  computed,
  nextTick,
  onActivated,
  onDeactivated,
  onMounted,
  onUnmounted,
  ref,
  useTemplateRef,
} from 'vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import QueueTaskExpandButton from '@/components/queue/QueueTaskExpandButton.vue'
import QueueTaskDetails from '@/components/queue/QueueTaskDetails.vue'
import { ElMessage, ElMessageBox, type TableInstance } from 'element-plus'
import { WarningFilled } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { useBackgroundRefresh } from '@/composables/useBackgroundRefresh'
import { useHttpClient } from '@/http/client'
import { mergeStableList, retainExistingKeys } from '@/composables/useStableList'
import { useRealtimeEvent } from '@/composables/useRealtimeEvents'
import { usePageStateStore } from '@/stores/pageState'
import {
  getTaskSourceTypeName,
  getTaskSourceTypeTagType,
  getUploadSourceName,
} from '@/utils/taskSourceUtils'
import { formatDateTime } from '@/utils/timeUtils'
import {
  buildUploadTaskDetailGroups,
  getUploadStageResultTagType,
  getUploadStageSummaryLabel,
  getUploadStatusTagType,
  getUploadStatusText,
  getUploadQueueLocationSummary,
  getUploadTransportDetailSummary,
} from '@/utils/queueTaskDetailUtils'
import {
  applyUploadQueuePatch,
  formatByteRate,
  getUploadedSizeLabel,
  getUploadProgressPercent,
  type UploadQueuePatch,
} from '@/utils/uploadQueueDisplayUtils'
import { useDeviceType } from '@/composables/useDeviceType'
import {
  canPauseQueue,
  canResumeQueue,
  emptyQueueStatusSnapshot,
  hasActiveQueueTasks,
  normalizeQueueStatusSnapshot,
  removePendingQueueRows,
  type QueueStatusSnapshot,
} from '@/utils/queueStatusUtils'

interface UploadTask {
  id: string
  source: string
  source_type: string
  file_name: string
  local_full_path: string
  remote_path_id?: string
  status: 0 | 1 | 2 | 3 | 4 | 5 | 6
  file_size: number
  start_time: number
  end_time: number
  remote_file_id: string
  remote_full_path: string
  remote_pick_code?: string
  remote_sha1?: string
  remote_md5?: string
  replaced_remote_file_id?: string
  error: string
  retry_count: number
  last_retry_time: number
  uploaded_bytes?: number
  upload_result?: string
  resume_state?: string
  rapid_wait_until?: number
  upload_phase?: string
  upload_speed_bytes?: number
  progress_percent?: number
  total_parts?: number
  uploaded_parts?: number
  source_cleanup_status?: string
  source_cleanup_error?: string
  rapid_wait_attempts?: number
  relative_path?: string
  source_deleted_at?: number
}

interface QueueMutationContextSnapshot {
  contextVersion: number
}

const http = useHttpClient()

// 数据状态
const pageStateStore = usePageStateStore()
const pageState = pageStateStore.getPageState('upload-queue', {
  currentPage: 1,
  pageSize: 20,
  filters: { status: -1 },
})
const { initialLoading, backgroundRefreshing, isRefreshing, runRefresh } = useBackgroundRefresh()
const queueData = ref<UploadTask[]>([])
const queryLoading = ref(false)
const total = ref(0)
const uploading = ref(0)
const queueStatusSnapshot = ref<QueueStatusSnapshot>(emptyQueueStatusSnapshot())
const canPauseAllTasks = computed(() => canPauseQueue(queueStatusSnapshot.value))
const canResumeAllTasks = computed(() => canResumeQueue(queueStatusSnapshot.value))
const { isMobile: isMobileView } = useDeviceType()
const mobileTableRef = useTemplateRef<TableInstance>('mobileTableRef')
const desktopTableRef = useTemplateRef<TableInstance>('desktopTableRef')
const tableHeight = computed(() => (isMobileView.value ? undefined : 'calc(100vh - 300px)'))
const queueControlSize = computed<'small' | 'default'>(() =>
  isMobileView.value ? 'small' : 'default',
)
const currentPage = computed({
  get: () => pageState.currentPage,
  set: (value) => pageStateStore.setPagination('upload-queue', value, pageState.pageSize),
})
const pageSize = computed({
  get: () => pageState.pageSize,
  set: (value) => pageStateStore.setPagination('upload-queue', pageState.currentPage, value),
})
const statusFilter = computed({
  get: () => Number(pageState.filters.status ?? -1),
  set: (value) => pageStateStore.setFilter('upload-queue', 'status', value),
})
const hasActiveQueueWork = computed(
  () =>
    hasActiveQueueTasks(queueStatusSnapshot.value) ||
    uploading.value > 0 ||
    queueData.value.some(
      (task) => task.status === 0 || task.status === 1 || task.status === 5 || task.status === 6,
    ),
)

const taskMatchesCurrentStatusFilter = (task: UploadTask): boolean => {
  return statusFilter.value === -1 || task.status === statusFilter.value
}

const removeQueueRowByTaskId = (taskId: string | number | undefined): boolean => {
  if (taskId === undefined) {
    return false
  }
  const beforeCount = queueData.value.length
  queueData.value = queueData.value.filter((task) => String(task.id) !== String(taskId))
  const removed = beforeCount - queueData.value.length
  if (removed > 0) {
    total.value = Math.max(0, total.value - removed)
    pageStateStore.setExpandedRowKeys(
      'upload-queue',
      retainExistingKeys(pageState.expandedRowKeys, queueData.value, (row) => row.id),
    )
    return true
  }
  return false
}

// 定时器
const refreshTimer = ref<number | null>(null)
const pendingQueueDataRefresh = ref(false)
let isPageActive = false
const queueDataRequestGate = createActiveRequestGate(() => isPageActive)
const queueStatusRequestGate = createActiveRequestGate(() => isPageActive)
const queueMutationContextVersion = ref(0)
const activeQueueMutationContext = ref<QueueMutationContextSnapshot | null>(null)

const invalidateQueueMutationContext = () => {
  queueMutationContextVersion.value += 1
  activeQueueMutationContext.value = null
}

const startQueueMutationContext = (): QueueMutationContextSnapshot => {
  invalidateQueueMutationContext()
  const snapshot = {
    contextVersion: queueMutationContextVersion.value,
  }
  activeQueueMutationContext.value = snapshot
  return snapshot
}

const isQueueMutationContextCurrent = (snapshot: QueueMutationContextSnapshot | null) => {
  return (
    isPageActive &&
    !!snapshot &&
    !!activeQueueMutationContext.value &&
    activeQueueMutationContext.value.contextVersion === snapshot.contextVersion &&
    snapshot.contextVersion === queueMutationContextVersion.value
  )
}

const finishQueueMutationContext = (snapshot: QueueMutationContextSnapshot) => {
  if (isQueueMutationContextCurrent(snapshot)) {
    activeQueueMutationContext.value = null
  }
}

const isMessageBoxCancelError = (error: unknown): boolean => {
  if (error === 'cancel' || error === 'close') {
    return true
  }

  const errorMessage = error instanceof Error ? error.message : String(error)
  return errorMessage.includes('用户取消操作')
}

const getUploadTaskName = (task: UploadTask): string => task.file_name || task.local_full_path

const shouldShowUploadStageSummary = (task: UploadTask): boolean =>
  Boolean(getUploadStageSummaryLabel(task))

const getUploadTaskTooltipContent = (task: UploadTask): string =>
  [
    getUploadTaskName(task),
    `${getUploadSourceName(task.source)} · ${getTaskSourceTypeName(task.source_type)}`,
  ]
    .filter(Boolean)
    .join('\n')

const getUploadDetailRegionId = (task: UploadTask): string =>
  `queue-upload-task-${encodeURIComponent(String(task.id))}-details`

const isUploadRowExpanded = (task: UploadTask): boolean =>
  pageState.expandedRowKeys.includes(String(task.id))

const getUploadExpandButtonLabel = (task: UploadTask): string =>
  `${isUploadRowExpanded(task) ? '收起' : '展开'}上传任务 ${task.id} 详情`

const toggleUploadRowExpansion = (task: UploadTask) => {
  const table = isMobileView.value ? mobileTableRef.value : desktopTableRef.value
  table?.toggleRowExpansion(task, !isUploadRowExpanded(task))
}

// 表格行类名
const tableRowClassName = ({ row }: { row: UploadTask }) => {
  switch (row.status) {
    case 2:
      return 'success-row'
    case 3:
      return 'error-row'
    case 4:
      return 'cancelled-row'
    default:
      return ''
  }
}

const handleExpandChange = (row: UploadTask, expandedRows: UploadTask[]) => {
  pageStateStore.setExpandedRowKeys(
    'upload-queue',
    expandedRows.map((item) => String(item.id)),
  )
}

const pruneExpandedRowsAfterLoad = () => {
  pageStateStore.setExpandedRowKeys(
    'upload-queue',
    retainExistingKeys(pageState.expandedRowKeys, queueData.value, (row) => row.id),
  )
}

// 加载队列数据
const loadQueueData = async () => {
  if (!isPageActive) {
    return
  }

  const requestId = queueDataRequestGate.next()

  if (isRefreshing.value) {
    pendingQueueDataRefresh.value = true
    return
  }

  try {
    await runRefresh(async () => {
      try {
        const response = await http.get(`${SERVER_URL}/upload/queue`, {
          params: {
            page: currentPage.value,
            page_size: pageSize.value,
            status: statusFilter.value,
          },
        })

        if (!queueDataRequestGate.isCurrent(requestId)) {
          return
        }

        if (response?.data.code === 200) {
          const rows = response.data.data.list || []
          queueData.value = mergeStableList(queueData.value, rows, (row) => row.id)
          total.value = response.data.data.total
          uploading.value = response.data.data.uploading || 0
          queueStatusSnapshot.value = normalizeQueueStatusSnapshot(
            response.data.data.queue_status,
            queueStatusSnapshot.value.running,
          )
          pruneExpandedRowsAfterLoad()
          if (hasActiveQueueWork.value) {
            startAutoRefresh()
          } else {
            stopAutoRefresh()
          }
        } else {
          ElMessage.error('获取上传队列数据失败')
        }
      } catch (error) {
        if (!queueDataRequestGate.isCurrent(requestId)) {
          return
        }
        console.error('加载上传队列数据错误：', error)
        ElMessage.error('获取上传队列数据失败')
      }
    })
  } finally {
    if (pendingQueueDataRefresh.value && isPageActive) {
      pendingQueueDataRefresh.value = false
      await loadQueueData()
    }
    queryLoading.value = false
  }
}

// 刷新队列
const refreshQueue = () => {
  loadQueueData()
}

// 清空队列
const clearQueue = async () => {
  const operationContext = startQueueMutationContext()

  try {
    await ElMessageBox.confirm('只能清空等待上传的记录，是否继续？此操作不可恢复。', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    const response = await http.post(`${SERVER_URL}/upload/queue/clear-pending`)

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      const beforeCount = queueData.value.length
      queueData.value = removePendingQueueRows(queueData.value)
      const removedCount = beforeCount - queueData.value.length
      total.value = Math.max(0, total.value - removedCount)
      queueStatusSnapshot.value = {
        ...queueStatusSnapshot.value,
        pending: 0,
        total: Math.max(0, queueStatusSnapshot.value.total - removedCount),
      }
      ElMessage.success('队列已清空')
      await loadQueueData()
      await loadQueueStatus()
    } else {
      ElMessage.error('清空队列失败')
    }
  } catch (error) {
    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }
    if (!isMessageBoxCancelError(error)) {
      console.error('清空队列错误：', error)
      ElMessage.error('清空队列失败')
    }
  } finally {
    if (isQueueMutationContextCurrent(operationContext)) {
      finishQueueMutationContext(operationContext)
    }
  }
}

const clearSuccessAndFailedTasks = async () => {
  const operationContext = startQueueMutationContext()

  try {
    await ElMessageBox.confirm(
      '只能清空所有已完成和失败的数据，此操作不可恢复，是否继续？',
      '提示',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    const response = await http.post(`${SERVER_URL}/upload/queue/clear-success-failed`)

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('队列已清空')
      loadQueueData()
    } else {
      ElMessage.error(`清空队列失败：${response?.data.message || ''}`)
    }
  } catch (error) {
    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }
    if (!isMessageBoxCancelError(error)) {
      console.error('清空队列失败：', error)
      ElMessage.error('清空队列失败')
    }
  } finally {
    if (isQueueMutationContextCurrent(operationContext)) {
      finishQueueMutationContext(operationContext)
    }
  }
}

// 重试所有失败的任务
const retryAllFailedTasks = async () => {
  const operationContext = startQueueMutationContext()

  try {
    await ElMessageBox.confirm('是否重试所有失败的任务？', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    const response = await http.post(`${SERVER_URL}/upload/queue/retry-failed`)

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('已开始重试所有失败任务')
      loadQueueData()
    } else {
      ElMessage.error(`重试失败任务时出错：${response?.data.message || ''}`)
    }
  } catch (error) {
    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }
    if (!isMessageBoxCancelError(error)) {
      console.error('重试失败任务时出错：', error)
      ElMessage.error('重试失败任务时出错')
    }
  } finally {
    if (isQueueMutationContextCurrent(operationContext)) {
      finishQueueMutationContext(operationContext)
    }
  }
}

// 全局暂停所有任务
const pauseAllTasks = async () => {
  const operationContext = startQueueMutationContext()

  try {
    const response = await http.post(`${SERVER_URL}/upload/queue/stop`)

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('已暂停所有任务')
      loadQueueData()
    } else {
      ElMessage.error(`暂停所有任务失败：${response?.data.message || ''}`)
    }
  } catch (error) {
    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }
    console.error('暂停所有任务失败：', error)
    ElMessage.error('暂停所有任务失败')
  } finally {
    if (isQueueMutationContextCurrent(operationContext)) {
      finishQueueMutationContext(operationContext)
    }
  }
}

// 全局继续所有任务
const resumeAllTasks = async () => {
  const operationContext = startQueueMutationContext()

  try {
    const response = await http.post(`${SERVER_URL}/upload/queue/start`)

    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('已恢复所有任务')
      loadQueueData()
    } else {
      ElMessage.error(`恢复所有任务失败：${response?.data.message || ''}`)
    }
  } catch (error) {
    if (!isQueueMutationContextCurrent(operationContext)) {
      return
    }
    console.error('恢复所有任务失败：', error)
    ElMessage.error('恢复所有任务失败')
  } finally {
    if (isQueueMutationContextCurrent(operationContext)) {
      finishQueueMutationContext(operationContext)
    }
  }
}

// 获取队列状态
const loadQueueStatus = async () => {
  const requestId = queueStatusRequestGate.next()

  try {
    const response = await http.get(`${SERVER_URL}/upload/queue/status`)

    if (!queueStatusRequestGate.isCurrent(requestId)) {
      return
    }

    if (response?.data.code === 200) {
      queueStatusSnapshot.value = normalizeQueueStatusSnapshot(
        response.data.data,
        queueStatusSnapshot.value.running,
      )
    } else {
      console.error('获取队列状态失败：', response?.data.message)
    }
  } catch (error) {
    if (!queueStatusRequestGate.isCurrent(requestId)) {
      return
    }
    console.error('获取队列状态错误：', error)
  }
}

// 处理每页数量变更
const handleSizeChange = (val: number) => {
  pageSize.value = val
  currentPage.value = 1
  queryLoading.value = true
  loadQueueData()
}

// 处理当前页变更
const handleCurrentChange = (val: number) => {
  currentPage.value = val
  queryLoading.value = true
  loadQueueData()
}

// 启动定时刷新
const startAutoRefresh = () => {
  if (refreshTimer.value || !hasActiveQueueWork.value) {
    return
  }

  refreshTimer.value = window.setInterval(() => {
    if (!document.hidden && hasActiveQueueWork.value) {
      loadQueueData()
    }
    if (!hasActiveQueueWork.value) {
      stopAutoRefresh()
    }
  }, 5000)
}

// 停止定时刷新
const stopAutoRefresh = () => {
  if (refreshTimer.value) {
    clearInterval(refreshTimer.value)
    refreshTimer.value = null
  }
}

const activateQueuePage = () => {
  if (isPageActive) {
    return
  }
  isPageActive = true
  loadQueueStatus()
  loadQueueData()
  startAutoRefresh()
}

const deactivateQueuePage = () => {
  if (!isPageActive) {
    return
  }
  isPageActive = false
  queryLoading.value = false
  pendingQueueDataRefresh.value = false
  queueDataRequestGate.invalidate()
  queueStatusRequestGate.invalidate()
  invalidateQueueMutationContext()
  stopAutoRefresh()
}

// 处理状态筛选变更
const handleStatusChange = (val: number) => {
  statusFilter.value = val
  currentPage.value = 1
  queryLoading.value = true
  loadQueueData()
}

const progressPatchFields = [
  'uploaded_bytes',
  'file_size',
  'progress_percent',
  'upload_speed_bytes',
  'upload_phase',
  'upload_result',
  'resume_state',
  'rapid_wait_until',
  'total_parts',
  'uploaded_parts',
  'source_cleanup_status',
  'source_cleanup_error',
  'source_deleted_at',
] as const

const uploadQueuePatchReasons = ['progress', 'source_cleanup_changed'] as const

const isUploadQueuePatch = (data: Record<string, unknown>): boolean => {
  if (!uploadQueuePatchReasons.includes(data.reason as (typeof uploadQueuePatchReasons)[number])) {
    return false
  }
  if (data.task_id === undefined && data.id === undefined) {
    return false
  }
  return progressPatchFields.some((field) => data[field] !== undefined)
}

const applyUploadQueuePatchForCurrentFilter = (patch: UploadQueuePatch): boolean => {
  const taskId = patch.task_id ?? patch.id
  if (!applyUploadQueuePatch(queueData.value, patch)) {
    return false
  }
  const row = queueData.value.find((item) => String(item.id) === String(taskId))
  if (row && !taskMatchesCurrentStatusFilter(row)) {
    removeQueueRowByTaskId(taskId)
  }
  return true
}

useRealtimeEvent(
  'upload_queue_status_changed',
  (data) => {
    if (typeof data.running === 'boolean') {
      queueStatusSnapshot.value = {
        ...queueStatusSnapshot.value,
        running: data.running,
      }
    }
    if (isPageActive) {
      loadQueueData()
    }
  },
  () => {
    if (isPageActive) void loadQueueData()
  },
)

useRealtimeEvent('upload_queue_changed', (data) => {
  if (!isPageActive || document.hidden) {
    return
  }
  if (isUploadQueuePatch(data) && applyUploadQueuePatchForCurrentFilter(data as UploadQueuePatch)) {
    if (hasActiveQueueWork.value) {
      startAutoRefresh()
    } else {
      stopAutoRefresh()
    }
    return
  }
  if (isPageActive && !document.hidden) {
    loadQueueData()
  }
})

// 页面生命周期
onMounted(() => {
  activateQueuePage()
})

onActivated(activateQueuePage)

onActivated(() => {
  if (queueData.value.length > 0) {
    pageStateStore.pruneExpandedRowKeys(
      'upload-queue',
      queueData.value.map((row) => String(row.id)),
    )
  }
  nextTick(() => {
    const table = isMobileView.value ? mobileTableRef.value : desktopTableRef.value
    table?.doLayout()
  })
})

onDeactivated(deactivateQueuePage)

onUnmounted(() => {
  isPageActive = false
  queryLoading.value = false
  pendingQueueDataRefresh.value = false
  queueDataRequestGate.invalidate()
  queueStatusRequestGate.invalidate()
  invalidateQueueMutationContext()
  stopAutoRefresh()
})
</script>

<style scoped>
.upload-queue-container {
  position: relative;
  width: 100%;
  padding: 20px;
  box-sizing: border-box;
}

.upload-queue-container :deep(.upload-queue-page-header) {
  margin-bottom: 0;
}

.upload-queue-card {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
}

.header-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-start;
  gap: 12px;
}

.queue-control-actions,
.queue-cleanup-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}

.header-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.queue-toolbar-row {
  display: flex;
  gap: 20px;
  align-items: center;
}

.filter-container {
  width: 120px;
}

.queue-stats {
  display: flex;
  gap: 16px;
  margin: 16px 0;
  flex-wrap: wrap;
}

.desktop-task-summary {
  min-width: 0;
}

.desktop-task-summary :deep(.el-tooltip__trigger) {
  display: block;
  flex: 1 1 0;
  min-width: 0;
}

.desktop-task-summary-text {
  flex: 1 1 0;
  min-width: 0;
}

.desktop-task-file-name {
  display: -webkit-box;
  overflow: hidden;
  font-weight: 500;
  line-height: 1.4;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.desktop-task-meta,
.queue-time-summary span {
  display: block;
  overflow: hidden;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.desktop-task-source,
.desktop-task-source-type {
  vertical-align: baseline;
}

.desktop-location-summary {
  display: -webkit-box;
  overflow: hidden;
  white-space: pre-line;
  line-height: 1.4;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
}

.desktop-location-direction {
  color: var(--el-text-color-secondary);
}

.queue-time-summary {
  min-width: 0;
}

.progress-cell {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.queue-progress {
  width: 100%;
}

.progress-meta {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
  line-height: 1.4;
  color: #606266;
}

.stage-result-cell {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.stage-detail-link {
  cursor: help;
  font-size: 12px;
}

.mobile-task-summary {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.mobile-task-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.mobile-task-id,
.desktop-task-id {
  color: var(--el-color-primary);
  font-variant-numeric: tabular-nums;
}

.mobile-task-title-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.mobile-task-file-name {
  min-width: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.mobile-task-file-name {
  flex: 1 1 0;
}

.mobile-task-metrics {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  color: var(--el-text-color-regular);
}

.mobile-progress {
  max-width: 100%;
}

:deep(.queue-expand-carrier) {
  width: 1px !important;
  min-width: 1px !important;
  max-width: 1px !important;
  padding: 0 !important;
}

:deep(.queue-expand-carrier .cell) {
  display: none;
}

:deep(.queue-expand-column .cell) {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: 0;
}

:deep(.queue-table-desktop .el-table__body tr > td.el-table__cell) {
  height: 110px;
}

/* 表格行样式 */
:deep(.success-row) {
  background-color: #f0f9ff;
}

:deep(.error-row) {
  background-color: #fef0f0;
}

:deep(.cancelled-row) {
  background-color: #f5f7fa;
}

/* 移动端适配 */
@media (max-width: 768px) {
  .upload-queue-container {
    padding: 12px;
  }

  .header-actions {
    width: 100%;
    align-self: stretch;
    display: grid;
    justify-content: stretch;
    gap: 6px;
  }

  .queue-control-actions {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    width: 100%;
    gap: 6px;
  }

  .queue-cleanup-actions {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    width: 100%;
    gap: 6px;
  }

  .queue-control-actions :deep(.el-button),
  .queue-cleanup-actions :deep(.el-button) {
    width: 100%;
    min-width: 0;
  }

  .queue-toolbar-row {
    gap: 8px;
    align-items: stretch;
  }

  .filter-container {
    width: 112px !important;
    margin: 8px 0;
  }

  .queue-stats {
    margin: 8px 0;
    gap: 8px;
  }

  .queue-stats :deep(.el-statistic__head) {
    font-size: 12px;
  }

  .queue-stats :deep(.el-statistic__content) {
    font-size: 20px;
  }

  .queue-table-mobile {
    margin-top: 4px;
  }

  :deep(.el-table) {
    font-size: 12px;
  }

  :deep(.el-table th) {
    padding: 8px 0;
  }

  :deep(.el-table td) {
    padding: 6px 0;
  }
}
</style>
