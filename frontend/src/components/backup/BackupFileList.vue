<script setup lang="ts">
import { onMounted, onUnmounted, shallowRef } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { fetchBackupFiles, type BackupFile } from '@/api/backup'
import ResponsiveRecordTable from '@/components/records/ResponsiveRecordTable.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'
import type { RecordAction, RecordActionPayload, RecordColumn } from '@/types/recordTable'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatTimestamp } from '@/utils/timeUtils'

const props = defineProps<{ disabled: boolean }>()
const emit = defineEmits<{ restore: [file: BackupFile] }>()
const http = useHttpClient()
const { isMobile } = useDeviceType()
const files = shallowRef<BackupFile[]>([])
const directory = shallowRef('')
const loading = shallowRef(false)
const loadError = shallowRef('')
let active = true

const columns: RecordColumn<BackupFile>[] = [
  { key: 'file_name', label: '文件名', priority: 'primary', minWidth: 240 },
  { key: 'file_size', label: '文件大小', priority: 'primary', width: 120 },
  { key: 'modified_at', label: '修改时间', priority: 'primary', width: 180 },
]
const actions: RecordAction<BackupFile>[] = [
  {
    key: 'restore',
    label: '恢复',
    type: 'warning',
    disabled: () => props.disabled || loading.value,
  },
]
const getRowKey = (file: BackupFile) => file.file_name

const loadFiles = async () => {
  if (loading.value || props.disabled) return
  loading.value = true
  loadError.value = ''
  try {
    const result = await fetchBackupFiles(http)
    if (!active || props.disabled) return
    directory.value = result.directory
    files.value = result.files
  } catch (error) {
    if (!active || props.disabled) return
    const parsed = parseHttpError(error, { fallbackMessage: '加载服务器备份文件失败' })
    if (parsed.shouldNotify) loadError.value = parsed.message
  } finally {
    if (active) loading.value = false
  }
}

const handleAction = ({ actionKey, row }: RecordActionPayload<BackupFile>) => {
  if (actionKey === 'restore' && !props.disabled && !loading.value) emit('restore', row)
}

onMounted(loadFiles)
onUnmounted(() => {
  active = false
})
</script>

<template>
  <div class="backup-file-list">
    <div class="file-list-toolbar">
      <span v-if="directory" class="backup-directory">备份目录：{{ directory }}</span>
      <el-button :icon="Refresh" :loading="loading" :disabled="disabled" @click="loadFiles">
        刷新文件列表
      </el-button>
    </div>
    <p class="file-list-notice">
      列表仅表示文件存在，每次恢复时会校验所选备份。上传或手动放入目录的文件不参与自动清理，需自行删除。
    </p>
    <el-alert v-if="loadError" :title="loadError" type="error" :closable="false" />
    <ResponsiveRecordTable
      :rows="files"
      :columns="columns"
      :actions="actions"
      :row-key="getRowKey"
      :loading="loading"
      :is-mobile="isMobile"
      :empty-text="loadError ? '文件列表暂不可用，请刷新重试' : '备份目录中暂无 ZIP 文件'"
      @action="handleAction"
    >
      <template #cell-file_size="{ row }">{{ formatFileSize(row.file_size) }}</template>
      <template #cell-modified_at="{ row }">{{ formatTimestamp(row.modified_at) }}</template>
    </ResponsiveRecordTable>
  </div>
</template>

<style scoped>
.file-list-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}

.backup-directory {
  overflow-wrap: anywhere;
}

.file-list-notice {
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}
</style>
