<template>
  <div class="main-content-container tmdb-settings-container">
    <PageHeader />

    <el-form
      :model="formData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="120"
      class="tmdb-form"
    >
      <el-form-item label="启用代理" prop="tmdbEnableProxy">
        <el-switch
          v-model="formData.tmdbEnableProxy"
          active-text="启用"
          inactive-text="禁用"
          :disabled="loading"
        />
        <div class="form-help">如果当前环境可以直连 https://api.tmdb.org，请保持关闭</div>
      </el-form-item>

      <el-form-item label="TMDB 接口地址" prop="tmdbUrl">
        <el-input
          v-model="formData.tmdbUrl"
          placeholder="留空使用默认值：https://api.tmdb.org"
          :disabled="loading"
          maxlength="255"
        />
        <div class="form-help">可填写镜像地址，不确定时可以留空</div>
      </el-form-item>

      <el-form-item label="TMDB 图片地址" prop="tmdbImageUrl">
        <el-input
          v-model="formData.tmdbImageUrl"
          placeholder="留空使用默认值：https://image.tmdb.org"
          :disabled="loading"
          maxlength="255"
        />
        <div class="form-help">可填写镜像地址，不确定时可以留空</div>
      </el-form-item>

      <el-form-item label="TMDB 密钥" prop="tmdbApiKey">
        <el-input
          v-model="formData.tmdbApiKey"
          placeholder="系统已内置默认 Key，留空也可使用；填写自己的更稳定"
          type="password"
          :disabled="loading"
          show-password
          maxlength="255"
        />
        <div class="form-help">TMDB API Key，用于刮削元数据；不确定时可以留空</div>
      </el-form-item>

      <el-form-item label="fanart.tv API Key" prop="fanartApiKey">
        <el-input
          v-model="formData.fanartApiKey"
          placeholder="系统已内置默认 Key，留空也可使用；填写自己的更稳定"
          type="password"
          :disabled="loading"
          show-password
          maxlength="255"
        />
        <div class="form-help">fanart.tv API Key，用于刮削艺术图；不确定时可以留空</div>
      </el-form-item>

      <el-form-item label="首选元数据语言" prop="tmdbLanguage">
        <el-radio-group v-model="formData.tmdbLanguage" :disabled="loading" size="large">
          <el-radio-button value="zh-CN">中文</el-radio-button>
          <el-radio-button value="en-US">英文</el-radio-button>
        </el-radio-group>
        <div class="form-help">如果首选语言没有数据，则获取英文数据</div>
      </el-form-item>
      <el-form-item label="首选图片语言" prop="tmdbImageLanguage">
        <el-radio-group v-model="formData.tmdbImageLanguage" :disabled="loading" size="large">
          <el-radio-button value="zh-CN">中文</el-radio-button>
          <el-radio-button value="en-US">英文</el-radio-button>
        </el-radio-group>
        <div class="form-help">如果首选语言没有数据，则获取英文图片</div>
      </el-form-item>

      <div class="form-actions">
        <div>
          <el-button
            type="primary"
            @click="testConnection"
            :loading="testing"
            size="large"
            :icon="Refresh"
            style="margin-right: 15px"
          >
            测试连通性
          </el-button>
        </div>
        <div>
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

    <!-- 测试状态显示 -->
    <el-alert
      v-if="testStatus"
      :title="testStatus.title"
      :type="testStatus.type"
      :description="testStatus.description"
      :closable="false"
      show-icon
      class="test-status"
    />

    <div class="security-content">
      <div class="warning-section">
        <el-alert title="使用提示" type="warning" :closable="false" show-icon>
          <template #default>
            如果不了解如何设置，请全部留空，系统会使用默认配置。默认配置可能需要代理才能访问。<br />
            如果 TMDB 无法直接访问，请开启代理，并确认代理配置正确。<br />
          </template>
        </el-alert>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Check, Refresh } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { useDeviceType } from '@/composables/useDeviceType'
import PageHeader from '@/components/common/PageHeader.vue'

interface TmdbSettings {
  tmdbUrl: string
  tmdbImageUrl: string
  tmdbEnableProxy: boolean
  tmdbApiKey: string
  tmdbAccessToken: string
  fanartApiKey: string
  tmdbLanguage: string
  tmdbImageLanguage: string
  local_max_threads: number
}

interface SaveStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}
const http = useHttpClient()
const { isMobile: checkIsMobile } = useDeviceType()
const loading = ref(false)
const testing = ref(false)
const saveStatus = ref<SaveStatus | null>(null)
const testStatus = ref<SaveStatus | null>(null)

// 表单数据
const formData = reactive<TmdbSettings>({
  tmdbUrl: '',
  tmdbImageUrl: '',
  tmdbEnableProxy: false,
  tmdbApiKey: '',
  tmdbAccessToken: '',
  fanartApiKey: '',
  tmdbLanguage: 'zh-CN',
  tmdbImageLanguage: 'en-US',
  local_max_threads: 5,
})

// 页面挂载时获取当前设置
onMounted(async () => {
  await fetchTmdbSettings()
})

// 获取 TMDB 设置
async function fetchTmdbSettings() {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/scrape/tmdb`)

    formData.tmdbUrl = response?.data.data.tmdb_url || ''
    formData.tmdbImageUrl = response?.data.data.tmdb_image_url || ''
    formData.tmdbEnableProxy = response?.data.data.tmdb_enable_proxy || false
    formData.tmdbApiKey = response?.data.data.tmdb_api_key || ''
    formData.tmdbAccessToken = response?.data.data.tmdb_access_token || ''
    formData.fanartApiKey = response?.data.data.fanart_api_key || ''
    formData.tmdbLanguage = response?.data.data.tmdb_language || 'zh-CN'
    formData.tmdbImageLanguage = response?.data.data.tmdb_image_language || 'en-US'
    formData.local_max_threads = response?.data.data.local_max_threads || 5
  } catch (error) {
    console.error('获取 TMDB 设置失败：', error)
    ElMessage.error('获取刮削设置失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

// 保存 TMDB 设置
async function saveSettings() {
  try {
    loading.value = true

    const payload = {
      tmdb_url: formData.tmdbUrl,
      tmdb_image_url: formData.tmdbImageUrl,
      tmdb_enable_proxy: formData.tmdbEnableProxy,
      tmdb_api_key: formData.tmdbApiKey,
      tmdb_access_token: formData.tmdbAccessToken,
      fanart_api_key: formData.fanartApiKey,
      tmdb_language: formData.tmdbLanguage,
      tmdb_image_language: formData.tmdbImageLanguage,
      local_max_threads: formData.local_max_threads,
    }
    if (!formData.tmdbApiKey) {
      payload.local_max_threads = 5
    }

    await http.post(`${SERVER_URL}/scrape/tmdb`, payload)

    saveStatus.value = {
      title: '保存成功',
      type: 'success',
      description: '刮削设置已成功保存',
    }

    // 3 秒后清除状态提示
    setTimeout(() => {
      saveStatus.value = null
    }, 3000)
  } catch (error) {
    console.error('保存 TMDB 设置失败：', error)
    saveStatus.value = {
      title: '保存失败',
      type: 'error',
      description: '保存刮削设置失败，请稍后重试',
    }
  } finally {
    loading.value = false
  }
}

// 测试 TMDB 连通性
async function testConnection() {
  try {
    testing.value = true
    testStatus.value = null

    const payload = {
      tmdb_url: formData.tmdbUrl,
      tmdb_image_url: formData.tmdbImageUrl,
      tmdb_enable_proxy: formData.tmdbEnableProxy,
      tmdb_api_key: formData.tmdbApiKey,
      tmdb_access_token: formData.tmdbAccessToken,
      tmdb_language: formData.tmdbLanguage,
      tmdb_image_language: formData.tmdbImageLanguage,
    }

    const response = await http.post(`${SERVER_URL}/scrape/tmdb-test`, payload, {
      timeout: 20000,
    })

    // 根据接口返回结果显示不同的状态
    if (response?.data?.data) {
      testStatus.value = {
        title: '连接成功',
        type: 'success',
        description: response.data.message || 'TMDB 连通性测试成功',
      }
    } else {
      testStatus.value = {
        title: '连接失败',
        type: 'error',
        description: response?.data?.message || 'TMDB 连通性测试失败，请检查设置',
      }
    }

    // 5 秒后清除状态提示
    setTimeout(() => {
      testStatus.value = null
    }, 5000)
  } catch (error) {
    console.error('测试 TMDB 连通性失败：', error)
    testStatus.value = {
      title: '连接失败',
      type: 'error',
      description: '测试过程中发生错误，请检查网络连接和设置',
    }
  } finally {
    testing.value = false
  }
}
</script>

<style scoped>
.tmdb-settings-container {
  padding: 20px;
  max-width: 800px;
}

.tmdb-form {
  background: #fff;
  padding: 20px;
}

.form-help {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-top: 5px;
}

.form-actions {
  margin-top: 30px;
  text-align: center;
}

.save-status,
.test-status {
  margin-top: 20px;
}

.security-content {
  margin-top: 30px;
}

.warning-section {
  margin-bottom: 20px;
}
</style>
