<template>
  <div class="backup-records-container">
    <el-alert
      title="重要提示：当前备份与恢复功能仍在完善，建议同时使用外部方式备份重要数据；后续将持续完善。"
      type="warning"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <div class="action-section">
      <el-button
        type="primary"
        size="large"
        :icon="Upload"
        :loading="backupStarting"
        :disabled="backupStore.isRunning"
        @click="startManualBackup"
      >
        <span>手动备份</span>
      </el-button>
      <span v-if="backupStore.isRunning" style="margin-left: 12px; color: #909399">
        备份正在进行中…
      </span>
    </div>

    <div class="records-section">
      <el-tabs v-model="activeTab" @tab-change="handleTabChange">
        <el-tab-pane label="备份记录" name="records">
          <ResponsiveRecordTable
            :rows="backupRecords"
            :columns="backupRecordColumns"
            :actions="backupRecordActions"
            :row-key="getBackupRecordRowKey"
            :loading="recordsLoading"
            :is-mobile="isMobile"
            show-all-details
            :detail-columns="3"
            @action="handleBackupRecordAction"
          >
            <template #cell-status="{ row }">
              <el-tag :type="getStatusTagType(row.status)" size="small">
                {{ getStatusText(row.status) }}
              </el-tag>
            </template>
            <template #cell-backup_type="{ row }">
              <el-tag :type="row.backup_type === 'manual' ? 'primary' : 'info'" size="small">
                {{ row.backup_type === 'manual' ? '手动' : '自动' }}
              </el-tag>
            </template>
            <template #cell-created_at="{ row }">
              {{ formatTimestamp(row.created_at) }}
            </template>
          </ResponsiveRecordTable>

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
import { ref, onMounted } from 'vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import ResponsiveRecordTable from '@/components/records/ResponsiveRecordTable.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { Upload } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { useBackupStore } from '@/stores/backup'
import type { BackupRecordListItem, BackupRecordsResponse, BackupStatus } from '@/typing'
import type { RecordAction, RecordActionPayload, RecordColumn } from '@/types/recordTable'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatTimestamp, formatDuration } from '@/utils/timeUtils'

const http = useHttpClient()
const backupStore = useBackupStore()
const { isMobile } = useDeviceType()
const API_SUCCESS_CODE = 200

const activeTab = ref('records')
const backupStarting = ref(false)
const recordsLoading = ref(false)
const restoringBackup = ref(false)
const backupRecords = ref<BackupRecordListItem[]>([])
const currentPage = ref(1)
const pageSize = ref(20)
const totalRecords = ref(0)

const backupRecordColumns: RecordColumn<BackupRecordListItem>[] = [
  {
    key: 'id',
    label: 'ID',
    priority: 'primary',
    width: 80,
    align: 'center',
    detailField: { key: 'id', label: 'ID', value: (row) => row.id },
  },
  {
    key: 'status',
    label: '状态',
    priority: 'primary',
    width: 80,
    align: 'center',
    detailField: { key: 'status', label: '状态', value: (row) => getStatusText(row.status) },
  },
  {
    key: 'backup_type',
    label: '类型',
    priority: 'primary',
    width: 100,
    align: 'center',
    detailField: {
      key: 'backup_type',
      label: '类型',
      value: (row) => (row.backup_type === 'manual' ? '手动' : '自动'),
    },
  },
  {
    key: 'created_at',
    label: '创建时间',
    priority: 'primary',
    width: 180,
    detailField: {
      key: 'created_at',
      label: '创建时间',
      value: (row) => formatTimestamp(row.created_at),
    },
  },
  {
    key: 'backup_duration',
    label: '耗时',
    priority: 'detail',
    detailField: {
      key: 'backup_duration',
      label: '耗时',
      value: (row) => formatDuration(row.backup_duration),
    },
  },
  {
    key: 'file_size',
    label: '文件大小',
    priority: 'detail',
    detailField: {
      key: 'file_size',
      label: '文件大小',
      value: (row) => (row.file_size ? formatFileSize(row.file_size) : '-'),
    },
  },
  {
    key: 'file_path',
    label: '文件路径',
    priority: 'secondary',
    minWidth: 320,
    detailField: {
      key: 'file_path',
      label: '文件路径',
      value: (row) => row.file_path,
      span: 3,
      isLongText: true,
    },
  },
  {
    key: 'created_reason',
    label: '原因',
    priority: 'detail',
    detailField: {
      key: 'created_reason',
      label: '原因',
      value: (row) => row.created_reason,
      span: 3,
      isLongText: true,
    },
  },
]

const backupRecordActions: RecordAction<BackupRecordListItem>[] = [
  { key: 'download', label: '下载', type: 'primary', visible: (row) => row.status === 'completed' },
  {
    key: 'restore',
    label: '恢复',
    type: 'warning',
    visible: (row) => row.status === 'completed',
    disabled: () => restoringBackup.value,
  },
  { key: 'delete', label: '删除', type: 'danger' },
]

const getBackupRecordRowKey = (row: BackupRecordListItem) => row.id

const startManualBackup = async () => {
  if (!http) return

  backupStarting.value = true
  try {
    const res = await http.post(`${SERVER_URL}/backup/create`, {
      reason: '手动备份',
    })

    if (res.data.code === API_SUCCESS_CODE) {
      ElMessage.success('备份任务已启动')
      backupStore.startProgressPolling('backup', undefined, http)
      setTimeout(() => {
        loadBackupRecords()
      }, 2000)
    } else {
      ElMessage.error(res.data.message || '启动备份任务失败')
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '启动备份任务失败'
    ElMessage.error(errorMsg)
  } finally {
    backupStarting.value = false
  }
}

const loadBackupRecords = async () => {
  if (!http) return

  recordsLoading.value = true
  try {
    const res = await http.get<{ code: number; data: BackupRecordsResponse }>(
      `${SERVER_URL}/backup/list`,
      {
        params: {
          page: currentPage.value,
          page_size: pageSize.value,
          type: 'all',
        },
      },
    )

    if (res.data.code === API_SUCCESS_CODE) {
      backupRecords.value = res.data.data.list
      totalRecords.value = res.data.data.total
    } else {
      ElMessage.error('加载备份记录失败')
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '加载备份记录失败'
    ElMessage.error(errorMsg)
  } finally {
    recordsLoading.value = false
  }
}

const handlePageSizeChange = () => {
  currentPage.value = 1
  loadBackupRecords()
}

const handleTabChange = () => {
  loadBackupRecords()
}

const handleBackupRecordAction = ({
  actionKey,
  row,
}: RecordActionPayload<BackupRecordListItem>) => {
  if (actionKey === 'download') {
    void downloadBackup(row.id, getFilenameFromPath(row.file_path))
    return
  }
  if (actionKey === 'restore') {
    void handleRestoreBackup(row)
    return
  }
  if (actionKey === 'delete') {
    void deleteBackupRecord(row.id)
  }
}

const getFilenameFromPath = (filePath: string): string => {
  if (!filePath) return 'backup.zip'
  return filePath.split('/').pop() || 'backup.zip'
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
    await ElMessageBox.confirm(
      `<div style="line-height: 1.8;">
        <p><strong>备份时间：</strong>${formatTimestamp(record.created_at)}</p>
        <p><strong>备份类型：</strong>${record.backup_type === 'manual' ? '手动备份' : '自动备份'}</p>
        ${record.created_reason ? `<p><strong>备份原因：</strong>${record.created_reason}</p>` : ''}
        <p style="color: #E6A23C; font-weight: bold; margin-top: 12px;">⚠️ 警告：此操作不可逆！</p>
        <p style="color: #F56C6C; font-weight: bold; font-size: 16px; margin-top: 8px;">⚠️ 注意：恢复成功后请重启服务让所有数据和配置生效！</p>
      </div>`,
      '确认恢复备份',
      {
        confirmButtonText: '确认恢复',
        cancelButtonText: '取消',
        type: 'warning',
        dangerouslyUseHTMLString: true,
      },
    )

    // 用户确认后，调用恢复 API
    await restoreBackup(record.id)
  } catch (error) {
    // 用户取消操作
    if (error !== 'cancel') {
      console.error('恢复备份失败：', error)
    }
  }
}

const restoreBackup = async (recordId: number) => {
  if (!http) return

  try {
    restoringBackup.value = true
    ElMessage.info('正在启动恢复任务…')

    const response = await http.post(`${SERVER_URL}/backup/restore`, {
      record_id: recordId,
    })

    if (response?.data.code === API_SUCCESS_CODE) {
      ElMessage.success('恢复任务已启动')
      // 启动进度轮询，与现有的恢复流程相同
      backupStore.startProgressPolling('restore', undefined, http)
    } else {
      ElMessage.error(response?.data.message || '恢复备份失败')
    }
  } catch (error) {
    console.error('恢复备份失败：', error)
    ElMessage.error('恢复备份失败')
  } finally {
    restoringBackup.value = false
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
  loadBackupRecords()
})
</script>

<style scoped>
.backup-records-container {
  padding: 20px;
}

.action-section {
  margin-bottom: 20px;
}

.records-section {
  margin-bottom: 20px;
  max-width: 1400px;
}

@media (max-width: 768px) {
  .backup-records-container {
    padding: 10px;
  }
}
</style>
