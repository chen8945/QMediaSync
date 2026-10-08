<template>
  <div class="backup-restore-container">
    <PageHeader />

    <el-alert
      title="警告：数据库恢复操作将覆盖当前业务数据，本实例的备份历史保留，请谨慎操作！"
      type="error"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-alert
      title="提示：恢复结束后服务保持暂停，需重启应用服务后重新登录；支持的运行方式可在结果弹窗中点击“重启服务”。"
      type="warning"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-alert
      title="重要提示：当前备份与恢复功能仍在完善，建议同时使用外部方式备份重要数据；后续将持续完善。"
      type="warning"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-alert
      title="恢复说明：仅支持不超过 1 GB 的 .zip 备份。迁移时请一并保留原 config/（尤其是 encryption.key），否则两步验证密钥将无法使用。"
      type="info"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-tabs v-model="activeSource">
      <el-tab-pane label="本地备份" name="server" :disabled="restoreDisabled">
        <BackupFileList
          v-if="activeSource === 'server'"
          :disabled="restoreDisabled"
          @restore="restoreServerFile"
        />
      </el-tab-pane>
      <el-tab-pane label="上传备份" name="upload" :disabled="restoreDisabled">
        <el-alert
          title="上传校验通过后，文件将保存到服务器备份目录。后续数据库恢复成功或失败都会保留，可在“服务器备份”中再次选择，无需重复上传。"
          type="info"
          :closable="false"
          class="upload-retention-notice"
        />
        <el-upload
          ref="uploadRef"
          action="#"
          :auto-upload="false"
          :limit="1"
          accept=".zip"
          :on-change="handleFileChange"
          :on-exceed="handleExceed"
          :disabled="restoreStarting || backupStore.isRunning || backupStore.restartRequired"
          drag
        >
          <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
          <div class="el-upload__text">将备份文件拖到此处，或<em>点击选择文件</em></div>
          <template #tip>
            <div class="el-upload__tip">只支持 .zip 文件，且不超过 1 GB</div>
          </template>
        </el-upload>

        <div class="action-buttons">
          <el-button
            type="warning"
            size="large"
            :icon="CircleCheck"
            :loading="restoreStarting"
            :disabled="!selectedFile || backupStore.isRunning || backupStore.restartRequired"
            @click="startRestore()"
          >
            开始恢复
          </el-button>
          <el-button
            size="large"
            :disabled="
              !selectedFile ||
              restoreStarting ||
              backupStore.isRunning ||
              backupStore.restartRequired
            "
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
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, useTemplateRef } from 'vue'
import { UploadFilled, CircleCheck } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox, type UploadFile, type UploadInstance } from 'element-plus'
import { useHttpClient } from '@/http/client'
import * as backupAPI from '@/api/backup'
import { notifyHttpError } from '@/utils/httpErrorNotification'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import { useBackupStore } from '@/stores/backup'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatTimestamp } from '@/utils/timeUtils'
import { useDeviceType } from '@/composables/useDeviceType'
import PageHeader from '@/components/common/PageHeader.vue'
import BackupFileList from '@/components/backup/BackupFileList.vue'

const http = useHttpClient()
const backupStore = useBackupStore()
const { isMobile } = useDeviceType()

const uploadRef = useTemplateRef<UploadInstance>('uploadRef')
const selectedFile = ref<File | null>(null)
const restoreStarting = ref(false)
const activeSource = shallowRef('server')
const restoreDisabled = computed(
  () => restoreStarting.value || backupStore.isRunning || backupStore.restartRequired,
)

const handleFileChange = (uploadFile: UploadFile) => {
  const file = uploadFile.raw
  if (!file) {
    return
  }

  const isValidFormat = file.name.toLowerCase().endsWith('.zip')

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
  uploadRef.value?.clearFiles()
  ElMessage.info('已清除选择的文件')
}

const restoreServerFile = (file: backupAPI.BackupFile) => startRestore(file)

const startRestore = async (serverFile?: backupAPI.BackupFile) => {
  const uploadFile = selectedFile.value
  if (restoreDisabled.value || (!serverFile && !uploadFile)) return
  restoreStarting.value = true

  try {
    await ElMessageBox.confirm(
      `<div style="line-height: 1.8;">
        <p>此操作将替换当前业务数据，本实例的备份历史保留，导入失败会回滚；如果提交结果异常，请查看日志核验数据。</p>
        <p>迁移时请一并保留原 config/，尤其是 encryption.key。</p>
        <p style="color: var(--el-color-danger); font-weight: bold; font-size: 16px; margin-top: 8px;">恢复期间服务暂停；进入维护后，无论恢复成功或失败，都需重启应用服务后重新登录。支持的运行方式可在结果弹窗中点击“重启服务”。</p>
      </div>`,
      '危险操作确认',
      {
        confirmButtonText: '确认恢复',
        cancelButtonText: '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger',
        dangerouslyUseHTMLString: true,
      },
    )

    const result = serverFile
      ? await backupAPI.restoreBackup(http, { file_name: serverFile.file_name })
      : await backupAPI.uploadAndRestoreBackup(http, uploadFile!)
    ElMessage.success('恢复任务已启动')
    backupStore.startProgressPolling('restore', undefined, http, result?.restore_receipt)
    if (!serverFile) clearFile()
  } catch (error: unknown) {
    if (isMessageBoxCancelError(error)) return
    notifyHttpError(error, '启动恢复任务失败', {
      fallbackMessage: '启动恢复任务失败',
      publicMessages: backupAPI.backupPublicMessages,
    })
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

.action-buttons {
  margin-top: 20px;
  display: flex;
  gap: 12px;
}

.file-info {
  margin-top: 20px;
}

.upload-retention-notice {
  margin-bottom: 20px;
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
