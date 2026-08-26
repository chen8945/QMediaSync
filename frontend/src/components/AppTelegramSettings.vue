<template>
  <div>
    <el-alert type="warning" :show-icon="true" style="margin-top: 12px">
      新创建的机器人，请先给机器人发送一条消息激活机器人
    </el-alert>
    <div class="main-content-container telegram-settings-container">
      <el-form
        :model="formData"
        :label-position="checkIsMobile ? 'top' : 'left'"
        :label-width="100"
        class="telegram-form"
      >
        <el-form-item label="启用" prop="enabled">
          <div class="enable-switch">
            <el-switch
              v-model="formData.enabled"
              :loading="loading"
              size="large"
              active-text="已启用"
              inactive-text="已禁用"
            />
            <div class="form-help">开启后，系统将通过 Telegram 机器人发送重要通知</div>
          </div>
        </el-form-item>

        <el-form-item label="机器人 Token" prop="telegram_bot_token">
          <el-input
            v-model="formData.telegram_bot_token"
            placeholder="搜索 @BotFather 创建机器人并获取 Token"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">在 Telegram 中搜索 @BotFather，创建机器人后获取 Token</div>
        </el-form-item>

        <el-form-item label="用户 ID" prop="telegram_user_id">
          <el-input
            v-model="formData.telegram_user_id"
            placeholder="搜索 @get_id_bot，点击开始，获取 Your Chat ID 后的数字"
            :disabled="loading || !formData.enabled"
          />
          <div class="form-help">在 Telegram 中搜索 @get_id_bot，点击开始并获取你的 Chat ID</div>
        </el-form-item>

        <el-form-item>
          <div class="form-actions">
            <div>
              <el-button
                type="primary"
                @click="testBot"
                :loading="testing"
                :disabled="loading || !formData.enabled"
                size="large"
                :icon="Message"
              >
                测试机器人
              </el-button>
            </div>
            <div>
              <el-button
                type="success"
                @click="saveSettings"
                :loading="loading"
                :disabled="testing"
                size="large"
                :icon="Check"
              >
                保存设置
              </el-button>
            </div>
          </div>
        </el-form-item>
      </el-form>
    </div>
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
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Message, Check } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { useDeviceType } from '@/composables/useDeviceType'

interface TelegramSettings {
  enabled: boolean
  telegram_bot_token: string
  telegram_user_id: string
}

interface TestStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}
const { isMobile: checkIsMobile } = useDeviceType()
const http = useHttpClient()
const loading = ref(false)
const testing = ref(false)
const testStatus = ref<TestStatus | null>(null)

const formData = reactive<TelegramSettings>({
  enabled: false,
  telegram_bot_token: '',
  telegram_user_id: '',
})

// 测试机器人
const testBot = async () => {
  if (!formData.enabled) {
    ElMessage.warning('请先启用 Telegram 通知功能')
    return
  }

  if (!formData.telegram_bot_token || !formData.telegram_user_id) {
    ElMessage.warning('请先填写机器人 Token 和用户 ID')
    return
  }

  try {
    testing.value = true
    testStatus.value = null

    const requestData = {
      enabled: formData.enabled ? 1 : 0,
      token: formData.telegram_bot_token,
      chat_id: formData.telegram_user_id,
    }

    const response = await http.post(`${SERVER_URL}/telegram/test`, requestData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      testStatus.value = {
        title: '机器人测试成功',
        type: 'success',
        description: '测试消息已发送到 Telegram，请检查是否收到',
      }
      ElMessage.success('机器人测试成功')
    } else {
      testStatus.value = {
        title: '机器人测试失败',
        type: 'error',
        description: response?.data.message || '无法发送测试消息，请检查 Token 和用户 ID 是否正确',
      }
      ElMessage.error(response?.data.message || '机器人测试失败')
    }
  } catch (error) {
    console.error('机器人测试错误：', error)
    testStatus.value = {
      title: '机器人测试出错',
      type: 'error',
      description: '测试过程中发生错误，请检查网络连接和配置信息',
    }
    ElMessage.error('机器人测试出错')
  } finally {
    testing.value = false
  }
}

// 保存设置
const saveSettings = async () => {
  try {
    loading.value = true

    const requestData = {
      enabled: formData.enabled ? 1 : 0,
      token: formData.telegram_bot_token,
      chat_id: formData.telegram_user_id,
    }

    const response = await http.post(`${SERVER_URL}/setting/telegram`, requestData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      const statusMessage = formData.enabled ? 'Telegram 通知已启用并保存' : 'Telegram 通知已禁用'

      testStatus.value = {
        title: statusMessage,
        type: 'info',
        description: formData.enabled
          ? '配置已成功保存，您可以开始接收 Telegram 通知了'
          : 'Telegram 通知功能已禁用，不会发送任何通知',
      }
    } else {
      ElMessage.error(response?.data.message || '保存设置失败，请重试')
    }
  } catch (error) {
    console.error('保存设置错误：', error)
    ElMessage.error('保存设置失败，请重试')
  } finally {
    loading.value = false
  }
}

// 加载设置
const loadSettings = async () => {
  try {
    loading.value = true

    const response = await http.get(`${SERVER_URL}/setting/telegram`)

    if (response?.data.code === 200 && response.data.data) {
      formData.enabled = response.data.data.enabled === '1'
      formData.telegram_bot_token = response.data.data.token || ''
      formData.telegram_user_id = response.data.data.chat_id || ''
    }
  } catch (error) {
    console.error('加载设置错误：', error)
    ElMessage.warning('加载已保存的设置失败')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadSettings()
})
</script>
