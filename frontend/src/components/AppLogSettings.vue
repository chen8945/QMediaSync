<script setup lang="ts">
import { SERVER_URL } from '@/const'
import type { LogLevel } from '@/types/log'
import { isLogLevel, LOG_LEVEL_OPTIONS } from '@/utils/logLevel'
import { isMobile } from '@/utils/deviceUtils'
import { useHttpClient } from '@/http/client'
import { ElMessage } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import { onMounted, reactive, shallowRef } from 'vue'
import PageHeader from '@/components/common/PageHeader.vue'

interface APIResponse<T> {
  code: number
  message: string
  data: T
}

interface LogSettingResponse {
  level: string
  levels: string[]
  maxSizeMB: number
  maxBackups: number
  maxAgeDays: number
}

interface SaveStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}

const http = useHttpClient()
const checkIsMobile = shallowRef(isMobile())
const loading = shallowRef(false)
const saveStatus = shallowRef<SaveStatus | null>(null)
const LOG_ROTATION_LIMITS = {
  maxSizeMB: { min: 1, max: 1024 },
  maxBackups: { min: 1, max: 100 },
  maxAgeDays: { min: 1, max: 365 },
} as const

const formData = reactive<{
  level: LogLevel
  maxSizeMB: number
  maxBackups: number
  maxAgeDays: number
}>({
  level: 'info',
  maxSizeMB: 10,
  maxBackups: 3,
  maxAgeDays: 7,
})

function normalizeLevel(value: unknown): LogLevel {
  return isLogLevel(value) ? value : 'info'
}

function normalizeNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function validateRange(label: string, value: number, min: number, max: number): string | null {
  if (!Number.isInteger(value)) {
    return `${label}必须是整数`
  }
  if (value < min || value > max) {
    return `${label}必须在 ${min}-${max} 之间`
  }
  return null
}

function validateLogSettingForm(): string | null {
  const sizeError = validateRange(
    '单文件最大大小',
    formData.maxSizeMB,
    LOG_ROTATION_LIMITS.maxSizeMB.min,
    LOG_ROTATION_LIMITS.maxSizeMB.max,
  )
  if (sizeError) {
    return sizeError
  }

  const backupsError = validateRange(
    '保留备份数',
    formData.maxBackups,
    LOG_ROTATION_LIMITS.maxBackups.min,
    LOG_ROTATION_LIMITS.maxBackups.max,
  )
  if (backupsError) {
    return backupsError
  }

  return validateRange(
    '保留天数',
    formData.maxAgeDays,
    LOG_ROTATION_LIMITS.maxAgeDays.min,
    LOG_ROTATION_LIMITS.maxAgeDays.max,
  )
}

async function fetchLogSetting() {
  try {
    loading.value = true
    const response = await http.get<APIResponse<LogSettingResponse>>(`${SERVER_URL}/setting/log`)
    if (!response || response.data.code !== 200) {
      throw new Error(response?.data.message || '获取日志设置失败')
    }
    formData.level = normalizeLevel(response.data.data.level)
    formData.maxSizeMB = normalizeNumber(response.data.data.maxSizeMB, 10)
    formData.maxBackups = normalizeNumber(response.data.data.maxBackups, 3)
    formData.maxAgeDays = normalizeNumber(response.data.data.maxAgeDays, 7)
  } catch (error) {
    console.error('获取日志设置失败：', error)
    ElMessage.error(error instanceof Error ? error.message : '获取日志设置失败')
  } finally {
    loading.value = false
  }
}

async function saveLogSetting() {
  const validationError = validateLogSettingForm()
  if (validationError) {
    saveStatus.value = {
      title: '保存失败',
      type: 'error',
      description: validationError,
    }
    return
  }

  try {
    loading.value = true
    saveStatus.value = null
    const response = await http.post<APIResponse<LogSettingResponse>>(`${SERVER_URL}/setting/log`, {
      level: formData.level,
      maxSizeMB: formData.maxSizeMB,
      maxBackups: formData.maxBackups,
      maxAgeDays: formData.maxAgeDays,
    })
    if (!response || response.data.code !== 200) {
      throw new Error(response?.data.message || '保存日志设置失败')
    }
    formData.level = normalizeLevel(response.data.data.level)
    formData.maxSizeMB = normalizeNumber(response.data.data.maxSizeMB, formData.maxSizeMB)
    formData.maxBackups = normalizeNumber(response.data.data.maxBackups, formData.maxBackups)
    formData.maxAgeDays = normalizeNumber(response.data.data.maxAgeDays, formData.maxAgeDays)
    saveStatus.value = {
      title: '保存成功',
      type: 'success',
      description: '日志设置已保存',
    }
    setTimeout(() => {
      saveStatus.value = null
    }, 3000)
  } catch (error) {
    console.error('保存日志设置失败：', error)
    saveStatus.value = {
      title: '保存失败',
      type: 'error',
      description: error instanceof Error ? error.message : '保存日志设置失败',
    }
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void fetchLogSetting()
})
</script>

<template>
  <div class="main-content-container log-settings-container">
    <PageHeader />

    <el-form
      :model="formData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="120"
      class="log-form"
    >
      <el-form-item label="日志等级" prop="level">
        <el-radio-group v-model="formData.level" :disabled="loading" size="large">
          <el-radio-button
            v-for="option in LOG_LEVEL_OPTIONS"
            :key="option.value"
            :value="option.value"
          >
            {{ option.label }}
          </el-radio-button>
        </el-radio-group>
      </el-form-item>

      <el-form-item label="单文件最大大小" prop="maxSizeMB">
        <el-input-number
          v-model="formData.maxSizeMB"
          :disabled="loading"
          :min="LOG_ROTATION_LIMITS.maxSizeMB.min"
          :max="LOG_ROTATION_LIMITS.maxSizeMB.max"
          :step="1"
          controls-position="right"
        />
        <span class="field-unit">MB</span>
      </el-form-item>

      <el-form-item label="保留备份数" prop="maxBackups">
        <el-input-number
          v-model="formData.maxBackups"
          :disabled="loading"
          :min="LOG_ROTATION_LIMITS.maxBackups.min"
          :max="LOG_ROTATION_LIMITS.maxBackups.max"
          :step="1"
          controls-position="right"
        />
      </el-form-item>

      <el-form-item label="保留天数" prop="maxAgeDays">
        <el-input-number
          v-model="formData.maxAgeDays"
          :disabled="loading"
          :min="LOG_ROTATION_LIMITS.maxAgeDays.min"
          :max="LOG_ROTATION_LIMITS.maxAgeDays.max"
          :step="1"
          controls-position="right"
        />
        <span class="field-unit">天</span>
      </el-form-item>

      <div class="form-actions">
        <el-button
          type="success"
          size="large"
          :icon="Check"
          :loading="loading"
          @click="saveLogSetting"
        >
          保存设置
        </el-button>
      </div>
    </el-form>

    <el-alert
      v-if="saveStatus"
      :title="saveStatus.title"
      :type="saveStatus.type"
      :description="saveStatus.description"
      :closable="false"
      show-icon
      class="save-status"
    />
  </div>
</template>

<style scoped>
.log-settings-container {
  max-width: 800px;
  padding: 20px;
}

.log-form {
  margin-top: 16px;
}

.form-actions {
  margin-top: 30px;
  text-align: center;
}

.field-unit {
  margin-left: 8px;
  color: var(--el-text-color-secondary);
}

.log-form :deep(.el-input-number) {
  width: 180px;
}

.save-status {
  margin-top: 20px;
}

@media (max-width: 768px) {
  .log-settings-container {
    padding: 16px;
  }

  .log-form :deep(.el-radio-group) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    width: 100%;
  }

  .log-form :deep(.el-radio-button__inner) {
    width: 100%;
  }
}
</style>
