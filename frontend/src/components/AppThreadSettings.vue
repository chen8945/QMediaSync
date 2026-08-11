<template>
  <div class="main-content-container thread-settings-container">
    <PageHeader />

    <el-form
      :model="formData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="200"
      class="thread-form"
    >
      <el-form-item label="下载队列每秒处理数量" prop="downloadThreads">
        <el-input-number
          v-model="formData.downloadThreads"
          :min="THREAD_LIMITS.downloadThreads.min"
          :max="THREAD_LIMITS.downloadThreads.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">
          控制每秒加入“正在下载”的任务数，并不是实际每秒下载数。因此“正在下载”数量可能高于该值，这是正常的。建议保留余量给播放和刮削，范围
          1 到 10。
        </div>
      </el-form-item>

      <el-form-item label="网盘接口每秒请求数量" prop="fileDetailThreads">
        <el-input-number
          v-model="formData.fileDetailThreads"
          :min="THREAD_LIMITS.fileDetailThreads.min"
          :max="THREAD_LIMITS.fileDetailThreads.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">
          控制 115 或百度网盘开放平台每秒请求数，影响同步速度，范围 2 到 10。
        </div>
      </el-form-item>

      <el-form-item label="OpenList 接口请求 QPS" prop="openlistQPS">
        <el-input-number
          v-model="formData.openlistQPS"
          :min="THREAD_LIMITS.openlistQPS.min"
          :max="THREAD_LIMITS.openlistQPS.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">控制 OpenList 接口每秒请求数，影响同步速度，范围 2 到 10。</div>
      </el-form-item>

      <el-form-item label="OpenList 接口重试次数" prop="openlistRetryCount">
        <el-input-number
          v-model="formData.openlistRetryCount"
          :min="THREAD_LIMITS.openlistRetry.min"
          :max="THREAD_LIMITS.openlistRetry.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">
          OpenList
          接口请求失败后的重试次数。超过次数仍失败时会停止同步；次数越多，失败场景下耗时越长。范围 1
          到 10。
        </div>
      </el-form-item>

      <el-form-item label="OpenList 接口重试间隔秒数" prop="openlistRetryDelay">
        <el-input-number
          v-model="formData.openlistRetryDelay"
          :min="THREAD_LIMITS.openlistRetryDelay.min"
          :max="THREAD_LIMITS.openlistRetryDelay.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">
          OpenList 接口每次重试的间隔时间，单位为秒。间隔越大，失败后恢复越慢，范围 30 到 3600。
        </div>
      </el-form-item>

      <el-form-item label="115 文件列表每页查询数量" prop="fileListPageSize">
        <el-input-number
          v-model="formData.fileListPageSize"
          :min="THREAD_LIMITS.fileListPageSize.min"
          :max="THREAD_LIMITS.fileListPageSize.max"
          :disabled="loading"
          size="large"
        />
        <div class="form-help">
          115 网盘文件列表接口每页返回的文件数量。网络较慢时可减小此值避免超时，范围 100 到
          1150，默认 1150。
        </div>
      </el-form-item>

      <el-divider content-position="left">115 直链缓存有效性检查</el-divider>
      <el-form-item label="启用有效性检查" prop="urlValidityCheckEnabled">
        <el-switch
          v-model="formData.urlValidityCheckEnabled"
          :active-value="true"
          :inactive-value="false"
          :disabled="loading"
        />
        <div class="form-help">
          仅影响 115 直链缓存命中后的 HEAD 检查。关闭后会直接使用缓存链接，不会发起 HEAD。
        </div>
      </el-form-item>
      <el-form-item label="有效性检查总超时秒数" prop="urlValidityCheckTimeoutSeconds">
        <el-input-number
          v-model="formData.urlValidityCheckTimeoutSeconds"
          :min="THREAD_LIMITS.urlValidityCheckTimeout.min"
          :max="THREAD_LIMITS.urlValidityCheckTimeout.max"
          :step="1"
          :precision="0"
          :disabled="loading || !formData.urlValidityCheckEnabled"
          size="large"
        />
        <div class="form-help">
          HEAD 检查的总超时时间，范围 1 到 9 秒，默认 3 秒。该设置不影响百度网盘或 OpenList。
        </div>
      </el-form-item>

      <el-divider content-position="left">115 秒传等待策略</el-divider>
      <el-form-item label="启用秒传等待" prop="uploadRapidWaitEnabled">
        <el-switch
          v-model="formData.uploadRapidWaitEnabled"
          :active-value="true"
          :inactive-value="false"
          :disabled="loading"
        />
        <div class="form-help">上传初始化未命中秒传时，按配置重复尝试秒传；默认关闭。</div>
      </el-form-item>
      <el-form-item label="最小等待文件大小 (MB)" prop="uploadRapidWaitMinSizeMB">
        <el-input-number
          v-model="formData.uploadRapidWaitMinSizeMB"
          :min="0"
          :step="100"
          :precision="0"
          :disabled="loading || !formData.uploadRapidWaitEnabled"
          size="large"
        />
        <div class="form-help">小于该大小的文件不进入秒传等待。设置为 0 表示不限制。</div>
      </el-form-item>
      <el-form-item label="强制等待文件大小 (MB)" prop="uploadRapidWaitForceSizeMB">
        <el-input-number
          v-model="formData.uploadRapidWaitForceSizeMB"
          :min="0"
          :step="100"
          :precision="0"
          :disabled="loading || !formData.uploadRapidWaitEnabled"
          size="large"
        />
        <div class="form-help">达到该大小的文件会等待到超时。设置为 0 表示不启用强制等待。</div>
      </el-form-item>
      <el-form-item label="等待间隔秒数" prop="uploadRapidWaitIntervalSeconds">
        <el-input-number
          v-model="formData.uploadRapidWaitIntervalSeconds"
          :min="1"
          :max="3600"
          :step="10"
          :precision="0"
          :disabled="loading || !formData.uploadRapidWaitEnabled"
          size="large"
        />
        <div class="form-help">
          没秒传成功时，每隔多少秒再问一次 115。间隔越短越快发现秒传，接口请求也会更多。
        </div>
      </el-form-item>
      <el-form-item label="等待超时秒数" prop="uploadRapidWaitTimeoutSeconds">
        <el-input-number
          v-model="formData.uploadRapidWaitTimeoutSeconds"
          :min="0"
          :max="86400"
          :step="60"
          :precision="0"
          :disabled="loading || !formData.uploadRapidWaitEnabled"
          size="large"
        />
        <div class="form-help">
          最多等多久。到点后就停止等待，不会因为上面的间隔更长而多等；设置为 0
          表示不等待，未命中秒传时直接进入真实上传。
        </div>
      </el-form-item>
      <el-form-item label="超时后跳过真实上传" prop="uploadRapidWaitSkipUpload">
        <el-switch
          v-model="formData.uploadRapidWaitSkipUpload"
          :active-value="true"
          :inactive-value="false"
          :disabled="loading || !formData.uploadRapidWaitEnabled"
        />
        <div class="form-help">
          开启后，秒传等待超时不会进入 OSS 分片上传；该任务不会生成后续 STRM。
        </div>
      </el-form-item>

      <div class="form-actions">
        <el-button
          type="success"
          @click="saveSettings"
          :loading="loading"
          size="large"
          :icon="Check"
        >
          保存设置
        </el-button>
      </div>
    </el-form>

    <!-- 保存状态显示 -->
    <el-alert
      v-if="saveStatus"
      :title="saveStatus.title"
      :type="saveStatus.type"
      :description="saveStatus.description"
      :closable="false"
      show-icon
      class="save-status"
    />
    <div class="security-content">
      <div class="warning-section">
        <el-alert title="使用提示" type="warning" :closable="false" show-icon>
          <template #default>
            线程数过高可能导致 115 提示访问量过高，一般建议总线程数不超过 10。<br />
            如果还授权了其他第三方平台，也要把它们的线程数一起算进去。<br />
            如果日志提示访问量过高，请降低接口请求 QPS。<br />
            如果下载文件时出现 403，请降低下载 QPS。<br />
          </template>
        </el-alert>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { THREAD_LIMITS } from '@/constants/validation'
import { useHttpClient } from '@/http/client'
import { isMobile } from '@/utils/deviceUtils'
import PageHeader from '@/components/common/PageHeader.vue'

interface ThreadSettings {
  downloadThreads: number
  fileDetailThreads: number
  openlistQPS: number
  openlistRetryCount: number
  openlistRetryDelay: number
  fileListPageSize: number
  urlValidityCheckEnabled: boolean
  urlValidityCheckTimeoutSeconds: number
  uploadRapidWaitEnabled: boolean
  uploadRapidWaitTimeoutSeconds: number
  uploadRapidWaitIntervalSeconds: number
  uploadRapidWaitMinSizeMB: number
  uploadRapidWaitForceSizeMB: number
  uploadRapidWaitSkipUpload: boolean
}

interface SaveStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}
const http = useHttpClient()
const checkIsMobile = ref(isMobile())
const loading = ref(false)
const saveStatus = ref<SaveStatus | null>(null)

// 表单数据
const formData = reactive<ThreadSettings>({
  downloadThreads: 1,
  fileDetailThreads: 3,
  openlistQPS: 2,
  openlistRetryCount: 1,
  openlistRetryDelay: 30,
  fileListPageSize: 1150,
  urlValidityCheckEnabled: true,
  urlValidityCheckTimeoutSeconds: 3,
  uploadRapidWaitEnabled: false,
  uploadRapidWaitTimeoutSeconds: 0,
  uploadRapidWaitIntervalSeconds: 60,
  uploadRapidWaitMinSizeMB: 0,
  uploadRapidWaitForceSizeMB: 0,
  uploadRapidWaitSkipUpload: false,
})

const bytesToMB = (value: number | undefined): number => {
  if (!value || value <= 0) {
    return 0
  }
  return Math.round(value / 1024 / 1024)
}

const mbToBytes = (value: number): number => {
  if (!value || value <= 0) {
    return 0
  }
  return Math.round(value * 1024 * 1024)
}

// 页面挂载时获取当前设置
onMounted(async () => {
  await fetchThreadSettings()
})

// 获取线程设置
async function fetchThreadSettings() {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/setting/threads`)

    formData.downloadThreads = response?.data.data.download_threads
    formData.fileDetailThreads = response?.data.data.file_detail_threads
    formData.openlistQPS = response?.data.data.openlist_qps
    formData.openlistRetryCount = response?.data.data.openlist_retry
    formData.openlistRetryDelay = response?.data.data.openlist_retry_delay
    formData.fileListPageSize = response?.data.data.file_list_page_size || 1150
    formData.urlValidityCheckEnabled = response?.data.data.url_validity_check_enabled !== 0
    const urlValidityCheckTimeout = response?.data.data.url_validity_check_timeout_seconds || 3
    formData.urlValidityCheckTimeoutSeconds = Math.min(
      Math.max(urlValidityCheckTimeout, THREAD_LIMITS.urlValidityCheckTimeout.min),
      THREAD_LIMITS.urlValidityCheckTimeout.max,
    )
    formData.uploadRapidWaitEnabled = response?.data.data.upload_rapid_wait_enabled === 1
    formData.uploadRapidWaitTimeoutSeconds =
      response?.data.data.upload_rapid_wait_timeout_seconds || 0
    formData.uploadRapidWaitIntervalSeconds =
      response?.data.data.upload_rapid_wait_interval_seconds || 60
    formData.uploadRapidWaitMinSizeMB = bytesToMB(response?.data.data.upload_rapid_wait_min_size)
    formData.uploadRapidWaitForceSizeMB = bytesToMB(
      response?.data.data.upload_rapid_wait_force_size,
    )
    formData.uploadRapidWaitSkipUpload = response?.data.data.upload_rapid_wait_skip_upload === 1
  } catch (error) {
    console.error('获取线程设置失败：', error)
    ElMessage.error('获取线程设置失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

// 保存线程设置
async function saveSettings() {
  try {
    loading.value = true

    const payload = {
      download_threads: formData.downloadThreads,
      file_detail_threads: formData.fileDetailThreads,
      openlist_qps: formData.openlistQPS,
      openlist_retry: formData.openlistRetryCount,
      openlist_retry_delay: formData.openlistRetryDelay,
      file_list_page_size: formData.fileListPageSize,
      url_validity_check_enabled: formData.urlValidityCheckEnabled ? 1 : 0,
      url_validity_check_timeout_seconds: formData.urlValidityCheckTimeoutSeconds,
      upload_rapid_wait_enabled: formData.uploadRapidWaitEnabled ? 1 : 0,
      upload_rapid_wait_timeout_seconds: formData.uploadRapidWaitTimeoutSeconds,
      upload_rapid_wait_interval_seconds: formData.uploadRapidWaitIntervalSeconds,
      upload_rapid_wait_min_size: mbToBytes(formData.uploadRapidWaitMinSizeMB),
      upload_rapid_wait_force_size: mbToBytes(formData.uploadRapidWaitForceSizeMB),
      upload_rapid_wait_skip_upload: formData.uploadRapidWaitSkipUpload ? 1 : 0,
    }

    const response = await http.post(`${SERVER_URL}/setting/threads`, payload)
    if (response?.data.code !== 200) {
      throw new Error(response?.data.message || '保存线程设置失败')
    }

    saveStatus.value = {
      title: '保存成功',
      type: 'success',
      description: '线程设置已成功保存',
    }

    // 3 秒后清除状态提示
    setTimeout(() => {
      saveStatus.value = null
    }, 3000)
  } catch (error) {
    console.error('保存线程设置失败：', error)
    saveStatus.value = {
      title: '保存失败',
      type: 'error',
      description: error instanceof Error ? error.message : '保存线程设置失败，请稍后重试',
    }
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.thread-settings-container {
  padding: 20px;
  max-width: 800px;
  /* margin: 0 auto; */
}

/* .thread-form { */
/* background: #fff;
  padding: 20px;
  border-radius: 8px;
  box-shadow: 0 2px 12px 0 rgba(0, 0, 0, 0.1); */
/* } */

.form-help {
  color: #909399;
  font-size: 12px;
  margin-top: 5px;
}

.form-actions {
  margin-top: 30px;
  text-align: center;
}

.save-status {
  margin-top: 20px;
}

.security-content {
  margin-top: 30px;
}

.warning-section {
  margin-bottom: 20px;
}

@media (max-width: 768px) {
  .thread-form :deep(.el-input-number) {
    width: 100%;
  }
}
</style>
