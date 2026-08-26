<template>
  <div class="main-content-container ai-settings-container">
    <PageHeader />

    <!-- <el-alert title="" type="error" :closable="false" style="margin-bottom: 20px">
      <template #default>
        推荐
        <a href="https://cloud.siliconflow.cn/i/fNSX73Tt" target="_blank">硅基流动</a>
        的模型，新账号填写我的邀请码可获赠 2000 万 Token：
        <b>fNSX73Tt</b>
      </template>
    </el-alert> -->
    <el-form
      :model="formData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="120"
      :rules="formRules"
      ref="formRef"
      class="ai-form"
    >
      <el-form-item label="API 接口地址" prop="aiBaseUrl">
        <el-input
          v-model="formData.aiBaseUrl"
          placeholder="留空使用默认值：https://api.siliconflow.cn"
          :disabled="loading"
          maxlength="255"
        />
        <div class="form-help">例如：https://api.deepseek.com</div>
      </el-form-item>

      <el-form-item label="API Key" prop="aiApiKey">
        <el-input
          v-model="formData.aiApiKey"
          placeholder="留空使用系统默认 Key；填写自己的更稳定"
          type="password"
          :disabled="loading"
          show-password
          maxlength="255"
        />
        <div class="form-help">API 服务的访问密钥</div>
      </el-form-item>

      <el-form-item label="模型名称" prop="aiModelName">
        <el-input
          v-model="formData.aiModelName"
          placeholder="留空使用默认值：deepseek-ai/DeepSeek-R1"
          :disabled="loading"
          maxlength="100"
        />
        <div class="form-help">可在硅基流动模型广场或模型提供商的 API 文档中查看</div>
      </el-form-item>

      <el-form-item label="请求超时时间" prop="ai_timeout">
        <el-input-number
          v-model="formData.ai_timeout"
          :disabled="loading"
          :min="5"
          :max="120"
          step="1"
          style="width: 100%"
        />
        <div class="form-help">
          请求超时时间，单位为秒，默认 120 秒。调小后刮削会更快，但 AI 识别更容易超时失败
        </div>
      </el-form-item>

      <div class="form-actions">
        <div>
          <el-button
            type="primary"
            @click="testConnection"
            :loading="testing"
            :disabled="loading"
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
            支持兼容 OpenAI API 的模型。<br />
            系统已内置硅基流动访问配置，使用量不高时可以全部留空，直接使用默认值<br />
            使用 AI 识别会消耗 Token 额度，请合理配置。
          </template>
        </el-alert>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted, computed, useTemplateRef } from 'vue'
import { ElMessage, type FormInstance } from 'element-plus'
import { Check, Refresh } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { useDeviceType } from '@/composables/useDeviceType'
import PageHeader from '@/components/common/PageHeader.vue'

interface AiSettings {
  aiBaseUrl: string
  aiApiKey: string
  aiModelName: string
  ai_timeout: number
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
const formRef = useTemplateRef<FormInstance>('formRef')

// 表单数据
const formData = reactive<AiSettings>({
  aiBaseUrl: '',
  aiApiKey: '',
  aiModelName: '',
  ai_timeout: 120,
})

// 动态表单验证规则
const formRules = computed(() => {
  return {
    aiBaseUrl: [
      {
        message: '请输入 API 接口地址',
        trigger: 'blur',
      },
    ],
    aiApiKey: [
      {
        message: '请输入 API Key',
        trigger: 'blur',
      },
    ],
    aiModelName: [
      {
        message: '请输入模型名称',
        trigger: 'blur',
      },
    ],
  }
})

// 页面挂载时获取当前设置
onMounted(async () => {
  await fetchAiSettings()
})

// 获取 AI 设置
async function fetchAiSettings() {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/scrape/ai-settings`)
    formData.aiBaseUrl = response?.data.data.ai_base_url || ''
    formData.aiApiKey = response?.data.data.ai_api_key || ''
    formData.aiModelName = response?.data.data.ai_model_name || ''
    formData.ai_timeout = response?.data.data.ai_timeout || 120
  } catch (error) {
    console.error('获取 AI 设置失败：', error)
    ElMessage.error('获取 AI 设置失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

// 保存 AI 设置
async function saveSettings() {
  try {
    // 执行表单验证
    await formRef.value?.validate()
    if (formData.aiModelName && !formData.aiApiKey) {
      ElMessage.error('如果填写了模型名称，必须填写 API Key')
      return
    }
    if (formData.aiBaseUrl && (!formData.aiApiKey || !formData.aiModelName)) {
      ElMessage.error('如果填写了 API 接口地址，必须填写模型名称和 API Key')
      return
    }
    loading.value = true

    const payload = {
      ai_base_url: formData.aiBaseUrl,
      ai_api_key: formData.aiApiKey,
      ai_model_name: formData.aiModelName,
      ai_timeout: formData.ai_timeout,
    }

    await http.post(`${SERVER_URL}/scrape/ai-settings`, payload)

    saveStatus.value = {
      title: '保存成功',
      type: 'success',
      description: 'AI 识别设置已成功保存',
    }

    // 3 秒后清除状态提示
    setTimeout(() => {
      saveStatus.value = null
    }, 3000)
  } catch (error) {
    // 如果是验证错误，则不显示保存失败的消息
    if (error !== false) {
      console.error('保存 AI 设置失败：', error)
      saveStatus.value = {
        title: '保存失败',
        type: 'error',
        description: '保存 AI 设置失败，请稍后重试',
      }
    }
  } finally {
    loading.value = false
  }
}

// 测试 AI 连通性
async function testConnection() {
  try {
    // 执行表单验证
    await formRef.value?.validate()

    testing.value = true
    testStatus.value = null

    const payload = {
      ai_base_url: formData.aiBaseUrl,
      ai_api_key: formData.aiApiKey,
      ai_model_name: formData.aiModelName,
    }

    const response = await http.post(`${SERVER_URL}/scrape/ai-test`, payload, {
      timeout: 120000,
    })

    // 根据接口返回结果显示不同的状态
    if (response?.data?.code === 200) {
      testStatus.value = {
        title: '测试成功',
        type: 'success',
        description: response.data.message || 'AI 服务连通性测试成功',
      }
    } else {
      testStatus.value = {
        title: '测试失败',
        type: 'error',
        description: response?.data?.message || 'AI 服务连通性测试失败，请检查设置',
      }
    }

    // 5 秒后清除状态提示
    setTimeout(() => {
      testStatus.value = null
    }, 5000)
  } catch (error) {
    // 如果是验证错误，则不显示测试失败的消息
    if (error !== false) {
      console.error('测试 AI 连通性失败：', error)
      testStatus.value = {
        title: '连接失败',
        type: 'error',
        description: '测试过程中发生错误，请检查网络连接和设置',
      }
    }
  } finally {
    testing.value = false
  }
}
</script>

<style scoped>
.ai-settings-container {
  padding: 20px;
  max-width: 800px;
}

.ai-form {
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
