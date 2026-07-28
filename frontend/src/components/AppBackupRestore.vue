<template>
  <div class="backup-restore-container">
    <div class="page-header">
      <span>数据库恢复</span>
    </div>

    <el-alert
      title="警告：数据库恢复操作将覆盖当前数据库，请谨慎操作！"
      type="error"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-alert
      title="提示：恢复成功后请重启服务，让所有数据和配置生效！"
      type="warning"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-alert
      title="恢复说明：仅支持 .zip 格式的备份文件，文件大小不超过 1 GB"
      type="info"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-upload
      ref="uploadRef"
      action="#"
      :auto-upload="false"
      :limit="1"
      accept=".zip"
      :on-change="handleFileChange"
      :on-exceed="handleExceed"
      :disabled="backupStore.isRunning"
      drag
    >
      <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
      <div class="el-upload__text">将备份文件拖到此处，或<em>点击选择文件</em></div>
      <template #tip>
        <div class="el-upload__tip">只支持 .zip 文件，且不超过 1 GB</div>
      </template>
    </el-upload>

    <el-form label-position="top" class="restore-password-form">
      <el-form-item label="工件密码（留空表示未加密）">
        <el-input
          v-model="restorePassword"
          type="password"
          show-password
          autocomplete="new-password"
          placeholder="至少 10 个字符，含大写字母、小写字母和数字，且不能包含空白字符"
          @update:model-value="onRestorePasswordChange"
        />
        <div v-if="restorePasswordError" class="restore-password-error">
          {{ restorePasswordError }}
        </div>
      </el-form-item>
    </el-form>

    <div class="action-buttons">
      <el-button
        type="warning"
        size="large"
        :icon="CircleCheck"
        :loading="restoreStarting"
        :disabled="!selectedFile || backupStore.isRunning"
        @click="startRestore"
      >
        开始恢复
      </el-button>
      <el-button
        size="large"
        :disabled="!selectedFile || restoreStarting || backupStore.isRunning"
        @click="clearFile"
      >
        清除
      </el-button>
    </div>

    <div v-if="selectedFile" class="file-info">
      <el-descriptions :column="isMobile ? 1 : 2" border>
        <el-descriptions-item label="文件名">
          {{ selectedFile.name }}
        </el-descriptions-item>
        <el-descriptions-item label="文件大小">
          {{ formatFileSize(selectedFile.size) }}
        </el-descriptions-item>
        <el-descriptions-item label="文件类型"> ZIP 压缩 </el-descriptions-item>
        <el-descriptions-item label="最后修改">
          {{ formatTimestamp(selectedFile.lastModified / 1000) }}
        </el-descriptions-item>
      </el-descriptions>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, useTemplateRef } from 'vue'
import { UploadFilled, CircleCheck } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type UploadFile, type UploadInstance } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { useBackupStore } from '@/stores/backup'
import type { BackupOperationAccepted, BackupRunningTask } from '@/typing'
import { validateBackupPassword } from '@/utils/backupPassword'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatTimestamp } from '@/utils/timeUtils'
import { isMobile as checkIsMobile } from '@/utils/deviceUtils'

const http = useHttpClient()
const backupStore = useBackupStore()
const isMobile = checkIsMobile()
const API_SUCCESS_CODE = 200

const uploadRef = useTemplateRef<UploadInstance>('uploadRef')
const selectedFile = ref<File | null>(null)
const restoreStarting = ref(false)
const restorePassword = ref('')
const restorePasswordError = ref('')

const onRestorePasswordChange = (value: string) => {
  restorePasswordError.value = validateBackupPassword(value)
}

const handleFileChange = (uploadFile: UploadFile) => {
  const file = uploadFile.raw
  if (!file) {
    return
  }

  const validExtensions = ['.zip']
  const isValidFormat = validExtensions.some((ext) => file.name.toLowerCase().endsWith(ext))

  if (!isValidFormat) {
    ElMessage.error('只支持 .zip 格式的文件')
    uploadRef.value?.clearFiles()
    return
  }

  const maxSize = 1073741824
  if (file.size > maxSize) {
    ElMessage.error('文件大小不能超过 1 GB')
    uploadRef.value?.clearFiles()
    return
  }

  selectedFile.value = file
  ElMessage.success('文件已选择')
}

const handleExceed = (files: File[]) => {
  if (files.length > 0) {
    ElMessage.warning('每次只能上传一个备份文件')
  }
}

const clearFile = () => {
  selectedFile.value = null
  restorePassword.value = ''
  restorePasswordError.value = ''
  uploadRef.value?.clearFiles()
  ElMessage.info('已清除选择的文件')
}

const startRestore = async () => {
  if (!selectedFile.value || !http) {
    return
  }

  restorePasswordError.value = validateBackupPassword(restorePassword.value)
  if (restorePasswordError.value) return

  try {
    restoreStarting.value = true
    const formData = new FormData()
    formData.append('file', selectedFile.value)
    formData.append('phase', 'preflight')
    formData.append('password', restorePassword.value)

    const preflight = await http.post(`${SERVER_URL}/backup/upload-restore`, formData, {
      timeout: 600000,
      validateStatus: (status) => status === 200 || status === 409 || status === 503,
    })
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
    const confirmData = new FormData()
    confirmData.append('phase', 'confirm')
    confirmData.append('preflight_id', result.preflight_id)
    confirmData.append('password', restorePassword.value)
    confirmData.append('confirm_overwrite', 'true')
    const confirmed = await http.post<{
      code: number
      message: string
      data: BackupOperationAccepted
    }>(`${SERVER_URL}/backup/upload-restore`, confirmData, {
      validateStatus: (status) =>
        status === 200 || status === 202 || status === 409 || status === 503,
    })
    if (confirmed.status === 202 && confirmed.data.code === API_SUCCESS_CODE) {
      backupStore.startOperationPolling('restore', confirmed.data.data, http)
      ElMessage.success('恢复任务已受理')
      clearFile()
    } else if (confirmed.status === 409) {
      const running = (confirmed.data.data as unknown as BackupRunningTask[] | null) ?? []
      const summary = running.map((task) => `${task.name} ${task.running}`).join('、')
      ElMessage.warning(summary ? `${confirmed.data.message}：${summary}` : confirmed.data.message)
    } else {
      ElMessage.error(confirmed.data.message || '恢复任务受理失败')
    }
  } catch (error: unknown) {
    if (error !== 'cancel') {
      const errorMsg = error instanceof Error ? error.message : '启动恢复任务失败'
      ElMessage.error(errorMsg)
    }
  } finally {
    restoreStarting.value = false
  }
}
</script>

<style scoped>
.backup-restore-container {
  padding: 20px;
  max-width: 1000px;
}

.page-header {
  font-weight: 600;
  font-size: 18px;
  margin-bottom: 20px;
  padding-bottom: 12px;
  border-bottom: 1px solid #e4e7ed;
}

.action-buttons {
  margin-top: 20px;
  display: flex;
  gap: 12px;
}

.file-info {
  margin-top: 20px;
}

.restore-password-form {
  margin-top: 20px;
}

.restore-password-error {
  margin-top: 8px;
  color: var(--el-color-danger);
  line-height: 1.5;
}

:deep(.el-upload-dragger) {
  padding: 40px;
}

@media (max-width: 768px) {
  .backup-restore-container {
    padding: 10px;
  }

  .action-buttons {
    flex-direction: column;
  }

  .action-buttons .el-button {
    width: 100%;
  }
}
</style>
