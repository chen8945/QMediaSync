<template>
  <div class="backup-settings-container">
    <div class="page-header">
      <el-icon>
        <Setting />
      </el-icon>
      <span>备份配置</span>
    </div>

    <el-alert
      title="重要提示：当前备份与恢复功能仍在完善，建议同时使用外部方式备份重要数据；后续将持续完善。"
      type="warning"
      :closable="false"
      style="margin-bottom: 20px"
    />

    <el-form
      :model="configForm"
      label-width="120px"
      :label-position="isMobile ? 'top' : 'left'"
      hide-required-asterisk
    >
      <el-form-item label="启用自动备份">
        <el-switch v-model="configForm.backup_enabled" :active-value="1" :inactive-value="0" />
      </el-form-item>

      <el-form-item label="定时策略" required class="backup-cron-field">
        <cron-selector v-model="configForm.backup_cron" v-model:custom-value="backupCustomCron" />
        <div v-if="cronTimes.length > 0" class="cron-next-times">
          <p><strong>下 5 次执行时间：</strong></p>
          <div v-loading="cronTimesLoading" class="cron-times-list">
            <div v-for="(time, index) in cronTimes" :key="index" class="cron-time-item">
              <el-tag type="info" size="small">{{ time }}</el-tag>
            </div>
          </div>
        </div>
      </el-form-item>

      <el-form-item label="保留天数" required>
        <el-input-number
          v-model="configForm.backup_retention"
          :min="1"
          :max="365"
          controls-position="right"
        />
        <span style="margin-left: 8px; color: #909399">天</span>
      </el-form-item>

      <el-form-item label="最大备份数" required>
        <el-input-number
          v-model="configForm.backup_max_count"
          :min="1"
          :max="100"
          controls-position="right"
        />
        <span style="margin-left: 8px; color: #909399">个</span>
      </el-form-item>

      <el-form-item class="backup-settings-actions">
        <el-button
          type="success"
          size="large"
          :icon="Check"
          :loading="configSaving"
          @click="saveConfig"
        >
          保存配置
        </el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, watch, shallowRef } from 'vue'
import { Check, Setting } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import type { BackupConfig } from '@/typing'
import { isMobile as checkIsMobile } from '@/utils/deviceUtils'
import CronSelector from './CronSelector.vue'

const http = useHttpClient()
const isMobile = checkIsMobile()
const API_SUCCESS_CODE = 200

const configForm = reactive({
  backup_enabled: 1 as 0 | 1,
  backup_cron: '0 3 * * *',
  backup_retention: 7,
  backup_max_count: 10,
  backup_compress: 1 as 0 | 1,
})

const configSaving = ref(false)
const cronTimes = ref<string[]>([])
const cronTimesLoading = ref(false)
const backupCustomCron = shallowRef('')

const loadBackupConfig = async () => {
  if (!http) return

  try {
    const res = await http.get<{ code: number; data: BackupConfig }>(`${SERVER_URL}/backup/config`)

    if (res.data.code === API_SUCCESS_CODE && res.data.data) {
      const config = res.data.data
      const cronExpression = config.backup_cron || '0 3 * * *'
      const cronChanged = configForm.backup_cron !== cronExpression

      Object.assign(configForm, {
        backup_enabled: config.backup_enabled,
        backup_cron: cronExpression,
        backup_retention: config.backup_retention,
        backup_max_count: config.backup_max_count,
        backup_compress: config.backup_compress,
      })
      backupCustomCron.value = cronExpression
      if (!cronChanged) {
        await loadCronTimes()
      }
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '加载备份配置失败'
    ElMessage.error(errorMsg)
  }
}

const saveConfig = async () => {
  if (!http) return

  configSaving.value = true
  try {
    const res = await http.put(`${SERVER_URL}/backup/config`, configForm)

    if (res.data.code === API_SUCCESS_CODE) {
      ElMessage.success('备份配置保存成功')
      await loadCronTimes()
    } else {
      ElMessage.error(res.data.message || '保存配置失败')
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : '保存配置失败'
    ElMessage.error(errorMsg)
  } finally {
    configSaving.value = false
  }
}

const loadCronTimes = async () => {
  if (!configForm.backup_cron || !http) {
    cronTimes.value = []
    return
  }

  try {
    cronTimesLoading.value = true
    const response = await http.get(`${SERVER_URL}/setting/cron`, {
      params: { cron: configForm.backup_cron },
    })

    if (response?.data.code === 200 && response.data.data) {
      cronTimes.value = response.data.data || []
    } else {
      cronTimes.value = []
    }
  } catch (error) {
    console.error('查询 Cron 执行时间错误：', error)
    cronTimes.value = []
  } finally {
    cronTimesLoading.value = false
  }
}

watch(
  () => configForm.backup_cron,
  (newCron) => {
    if (newCron && newCron.trim()) {
      loadCronTimes()
    } else {
      cronTimes.value = []
    }
  },
)

onMounted(() => {
  loadBackupConfig()
})
</script>

<style scoped>
.backup-settings-container {
  padding: 20px;
  max-width: 1200px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 18px;
  margin-bottom: 20px;
  padding-bottom: 12px;
  border-bottom: 1px solid #e4e7ed;
}

.cron-next-times {
  margin-top: 12px;
  padding: 12px;
  background-color: #f5f7fa;
  border-radius: 4px;
}

.backup-cron-field :deep(.el-form-item__content) {
  max-width: 720px;
}

.cron-next-times p {
  margin: 0 0 8px 0;
  color: #606266;
}

.cron-times-list {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.cron-time-item {
  display: inline-flex;
}

.backup-settings-actions :deep(.el-form-item__content) {
  margin-left: 0 !important;
}

@media (max-width: 768px) {
  .backup-settings-container {
    padding: 10px;
  }

  .backup-cron-field :deep(.el-form-item__content) {
    max-width: none;
  }
}
</style>
