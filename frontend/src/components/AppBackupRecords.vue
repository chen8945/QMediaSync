<template>
  <div class="backup-records-container">
    <div class="action-section">
      <el-button
        type="primary"
        size="large"
        :icon="Upload"
        :loading="backupStarting"
        :disabled="backupStore.isRunning"
        @click="openBackupPasswordDialog"
      >
        <span>手动备份</span>
      </el-button>
      <span v-if="backupStore.isRunning" style="margin-left: 12px; color: #909399">
        {{ backupStore.taskType === 'restore' ? '正在恢复…' : '正在备份…' }}
      </span>
    </div>

    <el-dialog
      v-model="passwordDialogVisible"
      title="创建手动备份"
      :width="isMobile ? '90%' : '520px'"
      :close-on-click-modal="false"
    >
      <el-alert type="warning" :closable="false" show-icon>
        备份可能包含 TLS 私钥，请妥善保管备份文件。
      </el-alert>
      <el-form label-position="top" style="margin-top: 16px">
        <el-form-item label="备份密码（留空表示不加密）">
          <el-input
            v-model="backupPassword"
            type="password"
            show-password
            autocomplete="new-password"
            placeholder="至少 10 个字符，含大写字母、小写字母和数字，且不能包含空格"
            @update:model-value="onBackupPasswordChange"
          />
          <div v-if="backupPasswordError" class="backup-password-error">
            {{ backupPasswordError }}
          </div>
        </el-form-item>
        <el-form-item v-if="backupPassword.length === 0">
          <el-checkbox v-model="confirmUnencrypted"> 我已了解风险，确认创建未加密备份 </el-checkbox>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="passwordDialogVisible = false">取消</el-button>
        <el-button
          type="primary"
          :loading="backupStarting"
          :disabled="backupPassword.length === 0 && !confirmUnencrypted"
          @click="startManualBackup"
        >
          开始备份
        </el-button>
      </template>
    </el-dialog>

    <div class="records-section">
      <el-alert
        v-if="latestOperation"
        :title="latestOperationText"
        :type="latestOperation.state === 'completed' ? 'success' : 'error'"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
      />
      <el-alert
        v-else-if="!backupStore.isRunning && inventoryStatus === 'scanning'"
        title="正在校验备份目录中的导入文件，可能需要较长时间"
        type="info"
        :closable="false"
        show-icon
        style="margin-bottom: 12px"
      />
      <el-tabs v-model="activeTab" @tab-change="handleTabChange">
        <el-tab-pane label="备份记录" name="records">
          <el-table
            :data="backupRecords"
            v-loading="recordsLoading"
            :height="isMobile ? 'auto' : 400"
            style="width: 100%"
          >
            <el-table-column type="expand" width="42">
              <template #default="{ row }">
                <el-descriptions :column="isMobile ? 1 : 2" border size="small">
                  <el-descriptions-item label="文件路径" :span="2">
                    <span class="backup-detail-long">{{ row.file_path || '-' }}</span>
                  </el-descriptions-item>
                  <el-descriptions-item label="文件大小">
                    {{ row.file_size ? formatFileSize(row.file_size) : '-' }}
                  </el-descriptions-item>
                  <el-descriptions-item label="耗时">
                    {{ row.backup_duration ? formatDuration(row.backup_duration) : '-' }}
                  </el-descriptions-item>
                  <el-descriptions-item label="原因" :span="2">
                    <span class="backup-detail-long">{{ row.created_reason || '-' }}</span>
                  </el-descriptions-item>
                </el-descriptions>
              </template>
            </el-table-column>
            <el-table-column prop="id" label="ID" width="80" />
            <el-table-column prop="status" label="状态" width="100">
              <template #default="{ row }">
                <el-tag :type="getStatusTagType(row.status)" size="small">
                  {{ getStatusText(row.status) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="backup_type" label="类型" width="100">
              <template #default="{ row }">
                <el-tag :type="row.backup_type === 'manual' ? 'primary' : 'info'" size="small">
                  {{ backupTypeText(row.backup_type) }}
                </el-tag>
                <div
                  v-if="row.verification_state === 'pending_password'"
                  class="backup-record-note"
                >
                  目录导入（待密码验证）
                </div>
                <div v-else-if="row.verification_state === 'invalid'" class="backup-record-note">
                  目录导入（无效）
                </div>
                <div v-else-if="row.backup_type === 'temporary_upload'" class="backup-record-note">
                  上传暂存：将在下次启动或下一次定时备份前自动清理
                </div>
              </template>
            </el-table-column>
            <el-table-column prop="created_at" label="创建时间" :width="isMobile ? 100 : 180">
              <template #default="{ row }">
                {{ formatTimestamp(row.created_at) }}
              </template>
            </el-table-column>
            <el-table-column label="操作" :width="isMobile ? 168 : 190" align="center">
              <template #default="{ row }">
                <el-button
                  v-if="row.status === 'completed'"
                  type="primary"
                  size="small"
                  link
                  :disabled="backupStore.isMaintenance"
                  @click="downloadBackup(row.id, getFilenameFromPath(row.file_path))"
                >
                  下载
                </el-button>
                <el-button
                  v-if="row.status === 'completed'"
                  type="warning"
                  size="small"
                  link
                  :disabled="restoringBackup || backupStore.isRunning || !canRestore(row)"
                  @click="handleRestoreBackup(row)"
                >
                  恢复
                </el-button>
                <el-button
                  type="danger"
                  size="small"
                  link
                  :disabled="backupStore.isMaintenance"
                  @click="deleteBackupRecord(row.id)"
                >
                  删除
                </el-button>
              </template>
            </el-table-column>
          </el-table>

          <ResponsivePagination
            v-model:current-page="currentPage"
            v-model:page-size="pageSize"
            :total="totalRecords"
            :page-sizes="[10, 20, 50, 100]"
            :is-mobile="isMobile"
            @current-change="loadBackupRecords"
            @size-change="handlePageSizeChange"
          />
        </el-tab-pane>
      </el-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { Upload } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { useBackupStore } from '@/stores/backup'
import type {
  BackupOperationAccepted,
  BackupRecordListItem,
  BackupRecordsResponse,
  BackupRunningTask,
  BackupStatus,
  BackupTerminalOperation,
  BackupType,
} from '@/typing'
import { validateBackupPassword } from '@/utils/backupPassword'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatTimestamp, formatDuration } from '@/utils/timeUtils'

const http = useHttpClient()
const backupStore = useBackupStore()
const { isMobile } = useDeviceType()
const API_SUCCESS_CODE = 200

const activeTab = ref('records')
const backupStarting = ref(false)
const passwordDialogVisible = ref(false)
const backupPassword = ref('')
const confirmUnencrypted = ref(false)
const backupPasswordError = ref('')
const recordsLoading = ref(false)
const restoringBackup = ref(false)
const backupRecords = ref<BackupRecordListItem[]>([])
const currentPage = ref(1)
const pageSize = ref(20)
const totalRecords = ref(0)
const inventoryStatus = ref<BackupRecordsResponse['inventory_status']>('ready')
const latestOperation = ref<BackupTerminalOperation | null>(null)
let inventoryPollingTimer: number | null = null
let inventoryPollingGeneration = 0
let recordsRefreshPending = false
let isPageActive = false

const latestOperationText = computed(() => {
  if (!latestOperation.value) return ''
  const operation = latestOperation.value
  if (operation.kind === 'restore' && operation.state === 'failed') {
    if (operation.rollback_state === 'succeeded') return '恢复失败，已自动回滚'
    if (operation.rollback_state === 'failed') return '恢复失败，自动回滚失败，请查看控制台日志'
  }
  if (operation.state === 'completed') return operation.kind === 'restore' ? '恢复完成' : '备份完成'
  if (operation.state === 'cancelled')
    return operation.kind === 'restore' ? '恢复已取消' : '备份已取消'
  return operation.kind === 'restore' ? '恢复失败' : '备份失败'
})

const openBackupPasswordDialog = () => {
  backupPassword.value = ''
  confirmUnencrypted.value = false
  backupPasswordError.value = ''
  passwordDialogVisible.value = true
}

const onBackupPasswordChange = (value: string) => {
  backupPasswordError.value = validateBackupPassword(value)
}

const startManualBackup = async () => {
  if (!http) return

  backupPasswordError.value = validateBackupPassword(backupPassword.value)
  if (backupPasswordError.value) return

  backupStarting.value = true
  try {
    const res = await http.post<{ code: number; message: string; data: BackupOperationAccepted }>(
      `${SERVER_URL}/backup/create`,
      {
        reason: '手动备份',
        password: backupPassword.value,
        confirm_unencrypted: confirmUnencrypted.value,
      },
      {
        validateStatus: (status) =>
          status === 200 || status === 202 || status === 409 || status === 503,
      },
    )

    if (res.status === 409) {
      const running = (res.data.data as unknown as BackupRunningTask[] | null) ?? []
      const summary = running.map((task) => `${task.name} ${task.running}`).join('、')
      ElMessage.warning(summary ? `${res.data.message}：${summary}` : res.data.message)
      return
    }
    if (res.status !== 202 || res.data.code !== API_SUCCESS_CODE) {
      ElMessage.error(res.data.message || '启动备份任务失败')
      return
    }

    // 明文令牌只出现一次，交给 store 保存在内存中用于状态轮询
    passwordDialogVisible.value = false
    backupPassword.value = ''
    ElMessage.success('备份任务已受理')
    backupStore.startOperationPolling('backup', res.data.data, http)
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '启动备份任务失败'
    ElMessage.error(errorMsg)
  } finally {
    backupStarting.value = false
  }
}

const loadBackupRecords = async () => {
  if (
    !http ||
    !isPageActive ||
    document.hidden ||
    recordsLoading.value ||
    backupStore.isMaintenance
  )
    return

  const generation = inventoryPollingGeneration
  recordsLoading.value = true
  try {
    const res = await http.get<{ code: number; message?: string; data: BackupRecordsResponse }>(
      `${SERVER_URL}/backup/list`,
      {
        params: {
          page: currentPage.value,
          page_size: pageSize.value,
          type: 'all',
        },
        validateStatus: (status) => status === 200 || status === 503,
      },
    )

    if (generation !== inventoryPollingGeneration || !isPageActive) return

    if (res.data.code === API_SUCCESS_CODE) {
      backupRecords.value = res.data.data.list
      totalRecords.value = res.data.data.total
      inventoryStatus.value = res.data.data.inventory_status
      latestOperation.value = res.data.data.latest_operation ?? null
    } else {
      ElMessage.error(res.data.message || '加载备份记录失败')
    }
  } catch (error: unknown) {
    if (generation !== inventoryPollingGeneration || !isPageActive) return
    const errorMsg = error instanceof Error ? error.message : '加载备份记录失败'
    ElMessage.error(errorMsg)
  } finally {
    recordsLoading.value = false
    if (!isPageActive || document.hidden) return
    if (generation !== inventoryPollingGeneration || recordsRefreshPending) {
      recordsRefreshPending = false
      void loadBackupRecords()
      return
    }
    syncInventoryPolling()
  }
}

const stopInventoryPolling = () => {
  inventoryPollingGeneration++
  if (inventoryPollingTimer !== null) {
    window.clearTimeout(inventoryPollingTimer)
    inventoryPollingTimer = null
  }
}

const syncInventoryPolling = () => {
  if (
    !isPageActive ||
    document.hidden ||
    inventoryStatus.value !== 'scanning' ||
    backupStore.isMaintenance
  ) {
    stopInventoryPolling()
    return
  }
  if (inventoryPollingTimer !== null || recordsLoading.value) return

  const generation = inventoryPollingGeneration
  inventoryPollingTimer = window.setTimeout(() => {
    inventoryPollingTimer = null
    if (generation !== inventoryPollingGeneration) return
    void loadBackupRecords()
  }, 1000)
}

const refreshBackupRecords = () => {
  if (!isPageActive || document.hidden) return
  if (recordsLoading.value) {
    recordsRefreshPending = true
    return
  }
  void loadBackupRecords()
}

const handleVisibilityChange = () => {
  if (document.hidden) {
    stopInventoryPolling()
    return
  }
  refreshBackupRecords()
}

const handlePageSizeChange = () => {
  currentPage.value = 1
  loadBackupRecords()
}

const handleTabChange = () => {
  loadBackupRecords()
}

const getFilenameFromPath = (filePath: string): string => {
  if (!filePath) return 'backup.sql.zip'
  return filePath.split('/').pop() || 'backup.sql.zip'
}

const downloadBackup = async (recordId: number, filename: string) => {
  if (!http) return

  try {
    const res = await http.get(`${SERVER_URL}/backup/download/${recordId}`, {
      responseType: 'blob',
    })

    const url = window.URL.createObjectURL(new Blob([res.data]))
    const link = document.createElement('a')
    link.href = url
    link.setAttribute('download', filename)
    document.body.appendChild(link)
    link.click()
    link.remove()
    window.URL.revokeObjectURL(url)
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '下载备份文件失败'
    ElMessage.error(errorMsg)
  }
}

const deleteBackupRecord = async (recordId: number) => {
  try {
    await ElMessageBox.confirm('确定要删除此备份记录吗？相关的备份文件也将被删除。', '确认删除', {
      confirmButtonText: '删除',
      cancelButtonText: '取消',
      type: 'warning',
    })

    if (!http) return

    const res = await http.delete(`${SERVER_URL}/backup/records/${recordId}`)

    if (res.data.code === API_SUCCESS_CODE) {
      ElMessage.success('备份记录已删除')
      loadBackupRecords()
    } else {
      ElMessage.error(res.data.message || '删除备份记录失败')
    }
  } catch (error: unknown) {
    if (error !== 'cancel') {
      const errorMsg = error instanceof Error ? error.message : '删除备份记录失败'
      ElMessage.error(errorMsg)
    }
  }
}

const handleRestoreBackup = async (record: BackupRecordListItem) => {
  try {
    const { value: password } = await ElMessageBox.prompt(
      '如工件受密码保护，请输入密码。',
      '恢复预检',
      {
        inputType: 'password',
        inputPlaceholder: '留空表示未加密工件',
        inputValidator: (value) => validateBackupPassword(value) || true,
        confirmButtonText: '开始验证',
        cancelButtonText: '取消',
      },
    )
    await restoreBackup(record.id, password)
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error('恢复预检失败')
  }
}

const restoreBackup = async (recordId: number, password: string) => {
  if (!http) return

  try {
    restoringBackup.value = true
    const preflight = await http.post(
      `${SERVER_URL}/backup/restore`,
      {
        record_id: recordId,
        phase: 'preflight',
        password,
      },
      { validateStatus: (status) => status === 200 || status === 409 || status === 503 },
    )
    if (preflight.status === 409) {
      const running = (preflight.data.data as BackupRunningTask[] | null) ?? []
      const summary = running.map((task) => `${task.name} ${task.running}`).join('、')
      ElMessage.warning(summary ? `${preflight.data.message}：${summary}` : preflight.data.message)
      return
    }
    if (preflight.status !== 200 || preflight.data.code !== API_SUCCESS_CODE) {
      ElMessage.error(preflight.data.message || '密码错误或工件损坏')
      return
    }

    const result = preflight.data.data as { preflight_id: string; target_label: string }
    await ElMessageBox.confirm(
      `配置和全部数据都会被覆盖。恢复目标：${result.target_label}\n\n将覆盖备份配置指定的数据库、白名单配置、TLS 证书/私钥和日志。恢复结束后请由部署平台或操作者重新启动服务。`,
      '确认完整恢复',
      { confirmButtonText: '确认恢复', cancelButtonText: '取消', type: 'warning' },
    )
    const confirmed = await http.post<{
      code: number
      message: string
      data: BackupOperationAccepted
    }>(
      `${SERVER_URL}/backup/restore`,
      {
        record_id: recordId,
        phase: 'confirm',
        preflight_id: result.preflight_id,
        password,
        confirm_overwrite: true,
      },
      {
        validateStatus: (status) =>
          status === 200 || status === 202 || status === 409 || status === 503,
      },
    )
    if (confirmed.status === 202 && confirmed.data.code === API_SUCCESS_CODE) {
      backupStore.startOperationPolling('restore', confirmed.data.data, http)
      ElMessage.success('恢复任务已受理')
    } else if (confirmed.status === 409) {
      const running = (confirmed.data.data as unknown as BackupRunningTask[] | null) ?? []
      const summary = running.map((task) => `${task.name} ${task.running}`).join('、')
      ElMessage.warning(summary ? `${confirmed.data.message}：${summary}` : confirmed.data.message)
    } else {
      ElMessage.error(confirmed.data.message || '恢复任务受理失败')
    }
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') ElMessage.error('恢复备份失败')
  } finally {
    restoringBackup.value = false
  }
}

const canRestore = (record: BackupRecordListItem) =>
  record.status === 'completed' &&
  record.format !== 'legacy' &&
  record.verification_state !== 'invalid'

const backupTypeText = (type: BackupType) => {
  switch (type) {
    case 'manual':
      return '手动'
    case 'auto':
      return '自动'
    case 'legacy':
      return '旧格式'
    case 'imported':
      return '目录导入'
    case 'temporary_upload':
      return '上传暂存'
  }
}

const getStatusTagType = (status: BackupStatus): string => {
  switch (status) {
    case 'completed':
      return 'success'
    case 'failed':
      return 'danger'
    case 'cancelled':
      return 'info'
    case 'timeout':
      return 'warning'
    default:
      return ''
  }
}

const getStatusText = (status: BackupStatus): string => {
  switch (status) {
    case 'completed':
      return '成功'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    case 'timeout':
      return '超时'
    case 'running':
      return '运行中'
    case 'pending':
      return '等待中'
    default:
      return status
  }
}

onMounted(() => {
  isPageActive = true
  document.addEventListener('visibilitychange', handleVisibilityChange)
  refreshBackupRecords()
})

watch(
  () => backupStore.isMaintenance,
  (maintenance) => {
    if (maintenance) stopInventoryPolling()
    else syncInventoryPolling()
  },
)

onUnmounted(() => {
  isPageActive = false
  recordsRefreshPending = false
  stopInventoryPolling()
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<style scoped>
.backup-password-error {
  margin-top: 8px;
  color: var(--el-color-danger);
  line-height: 1.5;
}

.backup-records-container {
  padding: 20px;
}

.action-section {
  margin-bottom: 20px;
  padding: 16px;
  background: #f5f7fa;
  border-radius: 4px;
  max-width: 1200px;
}

.records-section {
  margin-bottom: 20px;
  max-width: 1400px;
}

.backup-detail-long {
  overflow-wrap: anywhere;
}

.backup-record-note {
  margin-top: 4px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
  line-height: 1.4;
}

@media (max-width: 768px) {
  .backup-records-container {
    padding: 10px;
  }

  .action-section {
    padding: 12px;
  }
}
</style>
