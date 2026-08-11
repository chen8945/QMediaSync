<template>
  <div class="notification-page">
    <PageHeader class="notification-page-header">
      <template #actions>
        <div class="header-actions">
          <ResponsiveIconButton
            type="primary"
            :icon="Plus"
            label="添加渠道"
            :is-mobile="checkIsMobile"
            @click="showCreateDialog"
          />
          <ResponsiveIconButton
            :icon="Refresh"
            label="刷新"
            :is-mobile="checkIsMobile"
            :loading="loading"
            @click="loadChannels"
          />
        </div>
      </template>
    </PageHeader>

    <div class="main-content-container notification-channels-container">
      <div v-if="channels.length === 0 && !loading" class="empty-state">
        <el-empty description="暂无通知渠道">
          <template #image>
            <el-icon :size="64" color="var(--el-text-color-placeholder)"><Bell /></el-icon>
          </template>
          <el-button type="primary" :icon="Plus" @click="showCreateDialog"
            >添加第一个渠道</el-button
          >
        </el-empty>
      </div>

      <div v-else class="channels-grid" v-loading="loading">
        <el-card
          v-for="channel in channels"
          :key="channel.id"
          class="channel-card"
          :class="{ 'channel-disabled': !channel.is_enabled }"
          shadow="hover"
        >
          <div class="channel-card-header">
            <div class="channel-type-badge" :class="`type-${channel.channel_type}`">
              <el-icon :size="20">
                <component :is="getChannelIcon(channel.channel_type)" />
              </el-icon>
            </div>
            <div class="channel-info">
              <h3 class="channel-name">{{ channel.channel_name }}</h3>
              <span class="channel-type-label">{{ getChannelTypeName(channel.channel_type) }}</span>
            </div>
            <el-switch
              v-model="channel.is_enabled"
              @change="toggleChannelStatus(channel as ChannelWithStatus)"
              :loading="(channel as ChannelWithStatus)._switching"
              size="small"
            />
          </div>

          <div class="channel-card-body">
            <div class="channel-meta">
              <span class="meta-item">
                <el-icon><Clock /></el-icon>
                {{ formatDateTime(channel.created_at) }}
              </span>
            </div>
          </div>

          <div class="channel-card-actions">
            <el-button
              size="small"
              text
              :icon="Edit"
              @click="showEditDialog(channel)"
              :loading="editLoading"
            >
              编辑
            </el-button>
            <el-button size="small" text :icon="Setting" @click="showRulesDialog(channel)">
              规则
            </el-button>
            <el-button
              size="small"
              text
              type="primary"
              :icon="Promotion"
              @click="testChannel(channel as ChannelWithStatus)"
              :loading="(channel as ChannelWithStatus)._testing"
            >
              测试
            </el-button>
            <el-button
              size="small"
              text
              type="danger"
              :icon="Delete"
              @click="deleteChannel(channel)"
            >
              删除
            </el-button>
          </div>
        </el-card>
      </div>
    </div>

    <!-- 创建渠道对话框 -->
    <el-dialog
      v-model="createDialogVisible"
      title="添加通知渠道"
      :width="checkIsMobile ? '95%' : '600px'"
      :close-on-click-modal="false"
      top="5vh"
    >
      <!-- 渠道类型选择 -->
      <div v-if="!selectedChannelType" class="channel-type-selector-wrapper">
        <p class="selector-title">选择通知渠道类型</p>
        <div class="channel-type-selector">
          <div
            v-for="type in channelTypes"
            :key="type.value"
            class="channel-type-card"
            :class="`type-${type.value}`"
            @click="selectedChannelType = type.value"
          >
            <div class="channel-type-icon">
              <el-icon :size="28"><component :is="type.icon" /></el-icon>
            </div>
            <div class="channel-type-info">
              <div class="channel-type-name">{{ type.label }}</div>
              <div class="channel-type-desc">{{ type.description }}</div>
            </div>
          </div>
        </div>
      </div>

      <!-- 渠道配置表单 -->
      <el-form
        v-else
        :model="channelForm"
        :label-position="checkIsMobile ? 'top' : 'left'"
        label-width="120px"
        ref="channelFormRef"
      >
        <el-form-item>
          <el-button size="small" @click="selectedChannelType = ''" :icon="Back">
            重新选择类型
          </el-button>
        </el-form-item>

        <el-form-item label="渠道名称" required>
          <el-input v-model="channelForm.channel_name" placeholder="请输入渠道显示名称" />
        </el-form-item>

        <!-- Telegram 配置 -->
        <template v-if="selectedChannelType === 'telegram'">
          <el-form-item label="Bot Token" required>
            <el-input v-model="channelForm.bot_token" placeholder="123456:ABC-DEF..." />
          </el-form-item>
          <el-form-item label="Chat ID" required>
            <el-input v-model="channelForm.chat_id" placeholder="123456789" />
          </el-form-item>
        </template>

        <!-- MeoW 配置 -->
        <template v-if="selectedChannelType === 'meow'">
          <el-form-item label="昵称" required>
            <el-input v-model="channelForm.nickname" placeholder="my_nickname" />
          </el-form-item>
          <el-form-item label="API 地址">
            <el-input v-model="channelForm.endpoint" placeholder="http://api.chuckfang.com" />
          </el-form-item>
        </template>

        <!-- Bark 配置 -->
        <template v-if="selectedChannelType === 'bark'">
          <el-form-item label="设备密钥" required>
            <el-input v-model="channelForm.device_key" placeholder="your_device_key_here" />
          </el-form-item>
          <el-form-item label="服务器地址">
            <el-input v-model="channelForm.server_url" placeholder="https://api.day.app" />
          </el-form-item>
          <el-form-item label="通知声音">
            <el-input v-model="channelForm.sound" placeholder="alert" />
          </el-form-item>
          <el-form-item label="通知图标">
            <el-input v-model="channelForm.icon" placeholder="https://example.com/icon.png" />
          </el-form-item>
        </template>

        <!-- Server酱 配置 -->
        <template v-if="selectedChannelType === 'serverchan'">
          <el-form-item label="SCKEY" required>
            <el-input v-model="channelForm.sc_key" placeholder="SCU1234567890abcdef" />
          </el-form-item>
          <el-form-item label="API 地址">
            <el-input v-model="channelForm.endpoint" placeholder="https://sc.ftqq.com" />
          </el-form-item>
        </template>

        <!-- Webhook 配置 -->
        <template v-if="selectedChannelType === 'webhook'">
          <el-form-item label="请求地址" required>
            <el-input v-model="channelForm.endpoint" placeholder="https://example.com/webhook" />
          </el-form-item>
          <el-form-item label="请求方法" required>
            <el-select v-model="channelForm.method" placeholder="选择请求方法" style="width: 100%">
              <el-option label="GET" value="GET" />
              <el-option label="POST" value="POST" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="channelForm.method === 'POST'" label="数据格式" required>
            <el-select v-model="channelForm.format" placeholder="选择数据格式" style="width: 100%">
              <el-option label="JSON" value="json" />
              <el-option label="Form" value="form" />
              <el-option label="Text" value="text" />
            </el-select>
          </el-form-item>
          <el-form-item label="消息模板" required>
            <el-input
              v-model="channelForm.template"
              type="textarea"
              :rows="6"
              placeholder="支持变量：&#123;&#123;title&#125;&#125;, &#123;&#123;content&#125;&#125;, &#123;&#123;timestamp&#125;&#125;, &#123;&#123;image&#125;&#125;"
            />
            <div style="font-size: 12px; color: var(--el-text-color-secondary); margin-top: 4px">
              支持的变量：<br />
              &#123;&#123;title&#125;&#125; - 通知标题<br />
              &#123;&#123;content&#125;&#125; - 通知内容<br />
              &#123;&#123;timestamp&#125;&#125; - 时间戳<br />
              &#123;&#123;image&#125;&#125; - 图片 URL（如果有） <br />
              POST JSON 示例：
              {"title":"&#123;&#123;title&#125;&#125;","content":"&#123;&#123;content&#125;&#125;"}
              <br />
              POST Form 示例：
              title=&#123;&#123;title&#125;&#125;&content=&#123;&#123;content&#125;&#125;
              <br />
              GET/Text 示例：【&#123;&#123;title&#125;&#125;】&#123;&#123;content&#125;&#125;
            </div>
          </el-form-item>
          <el-form-item v-if="channelForm.method === 'GET'" label="查询参数名">
            <el-input v-model="channelForm.query_param" placeholder="默认：q" />
          </el-form-item>
          <el-form-item label="鉴权类型">
            <el-select
              v-model="channelForm.auth_type"
              placeholder="选择鉴权方式"
              style="width: 100%"
            >
              <el-option label="无鉴权" value="none" />
              <el-option label="Bearer Token" value="bearer" />
              <el-option label="Basic Auth" value="basic" />
              <el-option label="自定义 Header" value="header" />
              <el-option label="Query 参数" value="query" />
            </el-select>
          </el-form-item>
          <template v-if="channelForm.auth_type === 'bearer' || channelForm.auth_type === 'query'">
            <el-form-item :label="channelForm.auth_type === 'bearer' ? 'Token' : '参数值'">
              <el-input v-model="channelForm.auth_token" placeholder="输入 Token 或参数值" />
            </el-form-item>
            <el-form-item v-if="channelForm.auth_type === 'query'" label="参数名">
              <el-input v-model="channelForm.auth_query_key" placeholder="例如：token" />
            </el-form-item>
          </template>
          <template v-if="channelForm.auth_type === 'basic'">
            <el-form-item label="用户名">
              <el-input v-model="channelForm.auth_user" placeholder="Basic Auth 用户名" />
            </el-form-item>
            <el-form-item label="密码">
              <el-input
                v-model="channelForm.auth_pass"
                type="password"
                placeholder="Basic Auth 密码"
                show-password
              />
            </el-form-item>
          </template>
          <template v-if="channelForm.auth_type === 'header'">
            <el-form-item label="Header 名称">
              <el-input v-model="channelForm.auth_header_key" placeholder="例如：X-Api-Key" />
            </el-form-item>
            <el-form-item label="Header 值">
              <el-input v-model="channelForm.auth_token" placeholder="输入 Header 值" />
            </el-form-item>
          </template>
          <WebhookHeadersEditor v-model="channelForm.headers" />
          <el-form-item label="备注说明">
            <el-input
              v-model="channelForm.description"
              type="textarea"
              :rows="2"
              placeholder="可选的备注信息"
            />
          </el-form-item>
        </template>
      </el-form>

      <template #footer>
        <el-button @click="createDialogVisible = false">取消</el-button>
        <el-button
          v-if="selectedChannelType"
          type="primary"
          @click="createChannel"
          :loading="creating"
        >
          创建
        </el-button>
      </template>
    </el-dialog>

    <!-- 通知规则对话框 -->
    <el-dialog
      v-model="rulesDialogVisible"
      :title="`${currentChannel?.channel_name} - 通知规则`"
      :width="checkIsMobile ? '95%' : '600px'"
      top="5vh"
    >
      <el-table :data="currentRules" v-loading="rulesLoading">
        <el-table-column prop="event_type" label="事件类型" width="180">
          <template #default="{ row }">
            {{ getEventTypeName(row.event_type) }}
          </template>
        </el-table-column>
        <el-table-column prop="is_enabled" label="启用状态">
          <template #default="{ row }">
            <el-switch
              v-model="row.is_enabled"
              @change="updateRule(row)"
              :loading="row._updating"
            />
          </template>
        </el-table-column>
      </el-table>

      <template #footer>
        <el-button @click="rulesDialogVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 编辑渠道对话框 -->
    <el-dialog
      v-model="editDialogVisible"
      :title="`编辑渠道 - ${editingChannel?.channel_name}`"
      :width="checkIsMobile ? '95%' : '600px'"
      :close-on-click-modal="false"
      top="5vh"
    >
      <el-form
        :model="channelForm"
        :label-position="checkIsMobile ? 'top' : 'left'"
        label-width="120px"
        ref="channelFormRef"
      >
        <el-form-item label="渠道名称" required>
          <el-input v-model="channelForm.channel_name" placeholder="请输入渠道显示名称" />
        </el-form-item>

        <!-- Telegram 编辑 -->
        <template v-if="editingChannel?.channel_type === 'telegram'">
          <el-form-item label="Bot Token">
            <el-input v-model="channelForm.bot_token" placeholder="123456:ABC-DEF..." />
          </el-form-item>
          <el-form-item label="Chat ID">
            <el-input v-model="channelForm.chat_id" placeholder="123456789" />
          </el-form-item>
        </template>

        <!-- MeoW 编辑 -->
        <template v-if="editingChannel?.channel_type === 'meow'">
          <el-form-item label="昵称">
            <el-input v-model="channelForm.nickname" placeholder="my_nickname" />
          </el-form-item>
          <el-form-item label="API 地址">
            <el-input v-model="channelForm.endpoint" placeholder="http://api.chuckfang.com" />
          </el-form-item>
        </template>

        <!-- Bark 编辑 -->
        <template v-if="editingChannel?.channel_type === 'bark'">
          <el-form-item label="设备密钥">
            <el-input v-model="channelForm.device_key" placeholder="your_device_key_here" />
          </el-form-item>
          <el-form-item label="服务器地址">
            <el-input v-model="channelForm.server_url" placeholder="https://api.day.app" />
          </el-form-item>
          <el-form-item label="通知声音">
            <el-input v-model="channelForm.sound" placeholder="alert" />
          </el-form-item>
          <el-form-item label="通知图标">
            <el-input v-model="channelForm.icon" placeholder="https://example.com/icon.png" />
          </el-form-item>
        </template>

        <!-- Server酱 编辑 -->
        <template v-if="editingChannel?.channel_type === 'serverchan'">
          <el-form-item label="SCKEY">
            <el-input v-model="channelForm.sc_key" placeholder="SCU1234567890abcdef" />
          </el-form-item>
          <el-form-item label="API 地址">
            <el-input v-model="channelForm.endpoint" placeholder="https://sc.ftqq.com" />
          </el-form-item>
        </template>

        <!-- Webhook 编辑 -->
        <template v-if="editingChannel?.channel_type === 'webhook'">
          <el-form-item label="请求地址">
            <el-input v-model="channelForm.endpoint" placeholder="https://example.com/webhook" />
          </el-form-item>
          <el-form-item label="请求方法">
            <el-select v-model="channelForm.method" placeholder="选择请求方法" style="width: 100%">
              <el-option label="GET" value="GET" />
              <el-option label="POST" value="POST" />
            </el-select>
          </el-form-item>
          <el-form-item v-if="channelForm.method === 'POST'" label="数据格式">
            <el-select v-model="channelForm.format" placeholder="选择数据格式" style="width: 100%">
              <el-option label="JSON" value="json" />
              <el-option label="Form" value="form" />
              <el-option label="Text" value="text" />
            </el-select>
          </el-form-item>
          <el-form-item label="消息模板">
            <el-input
              v-model="channelForm.template"
              type="textarea"
              :rows="6"
              placeholder="支持变量：&#123;&#123;title&#125;&#125;, &#123;&#123;content&#125;&#125;, &#123;&#123;timestamp&#125;&#125;, &#123;&#123;image&#125;&#125;"
            />
          </el-form-item>
          <el-form-item v-if="channelForm.method === 'GET'" label="查询参数名">
            <el-input v-model="channelForm.query_param" placeholder="默认：q" />
          </el-form-item>
          <el-form-item label="鉴权类型">
            <el-select
              v-model="channelForm.auth_type"
              placeholder="选择鉴权方式"
              style="width: 100%"
            >
              <el-option label="无鉴权" value="none" />
              <el-option label="Bearer Token" value="bearer" />
              <el-option label="Basic Auth" value="basic" />
              <el-option label="自定义 Header" value="header" />
              <el-option label="Query 参数" value="query" />
            </el-select>
          </el-form-item>
          <template v-if="channelForm.auth_type === 'bearer' || channelForm.auth_type === 'query'">
            <el-form-item :label="channelForm.auth_type === 'bearer' ? 'Token' : '参数值'">
              <el-input v-model="channelForm.auth_token" placeholder="输入 Token 或参数值" />
            </el-form-item>
            <el-form-item v-if="channelForm.auth_type === 'query'" label="参数名">
              <el-input v-model="channelForm.auth_query_key" placeholder="例如：token" />
            </el-form-item>
          </template>
          <template v-if="channelForm.auth_type === 'basic'">
            <el-form-item label="用户名">
              <el-input v-model="channelForm.auth_user" placeholder="Basic Auth 用户名" />
            </el-form-item>
            <el-form-item label="密码">
              <el-input
                v-model="channelForm.auth_pass"
                type="password"
                placeholder="Basic Auth 密码"
                show-password
              />
            </el-form-item>
          </template>
          <template v-if="channelForm.auth_type === 'header'">
            <el-form-item label="Header 名称">
              <el-input v-model="channelForm.auth_header_key" placeholder="例如：X-Api-Key" />
            </el-form-item>
            <el-form-item label="Header 值">
              <el-input v-model="channelForm.auth_token" placeholder="输入 Header 值" />
            </el-form-item>
          </template>
          <WebhookHeadersEditor v-model="channelForm.headers" />
          <el-form-item label="备注说明">
            <el-input
              v-model="channelForm.description"
              type="textarea"
              :rows="2"
              placeholder="可选的备注信息"
            />
          </el-form-item>
        </template>
      </el-form>

      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="updateChannel" :loading="updating"> 保存 </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, onMounted, computed, useTemplateRef, type Component } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance } from 'element-plus'
import {
  Plus,
  Refresh,
  Setting,
  Promotion,
  Delete,
  Back,
  Edit,
  Bell,
  Clock,
  ChatDotRound,
  Position,
  Apple,
  Cellphone,
  Link,
} from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import PageHeader from '@/components/common/PageHeader.vue'
import ResponsiveIconButton from '@/components/common/ResponsiveIconButton.vue'
import WebhookHeadersEditor from '@/components/notification/WebhookHeadersEditor.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { formatDateTime } from '@/utils/timeUtils'
import {
  getChannelTypeName,
  getEventTypeName,
  webhookHeaderRecordToRows,
  webhookHeaderRowsToRecord,
  type NotificationChannel,
  type NotificationRule,
  type ChannelType,
  type WebhookHeaderRow,
} from '@/utils/notificationUtils'

// 渠道表单接口
interface ChannelFormData {
  channel_name: string
  // Telegram
  bot_token: string
  chat_id: string
  // MeoW
  nickname: string
  // 通用
  endpoint: string
  // Bark
  device_key: string
  server_url: string
  sound: string
  icon: string
  // Server酱
  sc_key: string
  // Webhook
  method: string
  format: string
  template: string
  query_param: string
  auth_type: string
  auth_token: string
  auth_user: string
  auth_pass: string
  auth_header_key: string
  auth_query_key: string
  headers: WebhookHeaderRow[]
  description: string
}

// 渠道状态扩展接口
interface ChannelWithStatus extends NotificationChannel {
  _switching: boolean
  _testing: boolean
}

// 规则状态扩展接口
interface RuleWithStatus extends NotificationRule {
  _updating: boolean
}

const { isMobile: checkIsMobile } = useDeviceType()
const http = useHttpClient()

const loading = ref(false)
const creating = ref(false)
const updating = ref(false)
const editLoading = ref(false)
const channels = ref<NotificationChannel[]>([])
const createDialogVisible = ref(false)
const editDialogVisible = ref(false)
const rulesDialogVisible = ref(false)
const selectedChannelType = ref<ChannelType | ''>('')
const editingChannel = ref<NotificationChannel | null>(null)
const currentChannel = ref<NotificationChannel | null>(null)
const currentRules = ref<NotificationRule[]>([])
const rulesLoading = ref(false)
const channelFormRef = useTemplateRef<FormInstance>('channelFormRef')

// 所有渠道类型选项
const allChannelTypes = [
  {
    value: 'telegram' as ChannelType,
    label: 'Telegram',
    icon: Position,
    description: 'Telegram Bot 推送',
  },
  {
    value: 'meow' as ChannelType,
    label: 'MeoW',
    icon: ChatDotRound,
    description: 'MeoW 推送服务',
  },
  {
    value: 'bark' as ChannelType,
    label: 'Bark',
    icon: Apple,
    description: 'iOS Bark 推送',
  },
  {
    value: 'serverchan' as ChannelType,
    label: 'Server酱',
    icon: Cellphone,
    description: '微信推送服务',
  },
  {
    value: 'webhook' as ChannelType,
    label: 'Webhook',
    icon: Link,
    description: '自定义 Webhook 推送',
  },
]

const getChannelIcon = (type: ChannelType): Component => {
  const iconMap: Record<ChannelType, Component> = {
    telegram: Position,
    meow: ChatDotRound,
    bark: Apple,
    serverchan: Cellphone,
    webhook: Link,
  }
  return iconMap[type] || Link
}

const channelTypes = computed(() => allChannelTypes)

// 渠道表单
const channelForm = reactive<ChannelFormData>({
  channel_name: '',
  bot_token: '',
  chat_id: '',
  nickname: '',
  endpoint: '',
  device_key: '',
  server_url: '',
  sound: '',
  icon: '',
  sc_key: '',
  // Webhook 字段
  method: 'POST',
  format: 'json',
  template: '',
  query_param: 'q',
  auth_type: 'none',
  auth_token: '',
  auth_user: '',
  auth_pass: '',
  auth_header_key: '',
  auth_query_key: '',
  headers: [],
  description: '',
})

// 加载渠道列表
const loadChannels = async () => {
  loading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/setting/notification/channels`)
    if (response?.data.code === 0) {
      channels.value = response.data.data.map(
        (channel: NotificationChannel): ChannelWithStatus => ({
          ...channel,
          _switching: false,
          _testing: false,
        }),
      )
    } else {
      ElMessage.error(response?.data.message || '加载失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '加载渠道列表失败'
    ElMessage.error(errorMessage)
  } finally {
    loading.value = false
  }
}

// 显示创建对话框
const showCreateDialog = () => {
  selectedChannelType.value = ''
  resetChannelForm()
  createDialogVisible.value = true
}

// 重置表单
const resetChannelForm = () => {
  channelForm.channel_name = ''
  channelForm.bot_token = ''
  channelForm.chat_id = ''
  channelForm.nickname = ''
  channelForm.endpoint = ''
  channelForm.device_key = ''
  channelForm.server_url = ''
  channelForm.sound = ''
  channelForm.icon = ''
  channelForm.sc_key = ''
  channelForm.method = 'POST'
  channelForm.format = 'json'
  channelForm.template = ''
  channelForm.query_param = 'q'
  channelForm.auth_type = 'none'
  channelForm.auth_token = ''
  channelForm.auth_user = ''
  channelForm.auth_pass = ''
  channelForm.auth_header_key = ''
  channelForm.auth_query_key = ''
  channelForm.headers = []
  channelForm.description = ''
}

// 显示编辑对话框
const showEditDialog = async (channel: NotificationChannel) => {
  editingChannel.value = channel
  editLoading.value = true

  try {
    // 根据渠道类型调用对应的查询接口获取详细配置
    const response = await http.get(
      `${SERVER_URL}/setting/notification/channels/${channel.channel_type}/${channel.id}`,
    )

    if (response?.data.code === 0) {
      const { channel: channelData, config } = response.data.data

      // 填充基本信息
      channelForm.channel_name = channelData.channel_name || ''
      channelForm.description = channelData.description || ''

      if (config) {
        // Telegram
        if (channel.channel_type === 'telegram') {
          channelForm.bot_token = config.bot_token || ''
          channelForm.chat_id = config.chat_id || ''
        }
        // MeoW
        else if (channel.channel_type === 'meow') {
          channelForm.nickname = config.nickname || ''
          channelForm.endpoint = config.endpoint || ''
        }
        // Bark
        else if (channel.channel_type === 'bark') {
          channelForm.device_key = config.device_key || ''
          channelForm.server_url = config.server_url || ''
          channelForm.sound = config.sound || ''
          channelForm.icon = config.icon || ''
        }
        // Server酱
        else if (channel.channel_type === 'serverchan') {
          channelForm.sc_key = config.sc_key || ''
          channelForm.endpoint = config.endpoint || ''
        }
        // Webhook
        else if (channel.channel_type === 'webhook') {
          channelForm.endpoint = config.endpoint || ''
          channelForm.method = config.method || 'POST'
          channelForm.format = config.format || 'json'
          channelForm.template = config.template || ''
          channelForm.query_param = config.query_param || 'q'
          channelForm.auth_type = config.auth_type || 'none'
          channelForm.auth_token = config.auth_token || ''
          channelForm.auth_user = config.auth_user || ''
          channelForm.auth_pass = config.auth_pass || ''
          channelForm.auth_header_key = config.auth_header_key || ''
          channelForm.auth_query_key = config.auth_query_key || ''
          channelForm.headers = webhookHeaderRecordToRows(config.headers)
        }
      }

      editDialogVisible.value = true
    } else {
      ElMessage.error(response?.data.message || '获取渠道配置失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '获取渠道配置失败'
    ElMessage.error(errorMessage)
  } finally {
    editLoading.value = false
  }
}

// 创建渠道
const createChannel = async () => {
  if (!channelForm.channel_name) {
    ElMessage.warning('请输入渠道名称')
    return
  }

  // 根据类型验证必填字段
  if (selectedChannelType.value === 'telegram') {
    if (!channelForm.bot_token || !channelForm.chat_id) {
      ElMessage.warning('请填写 Bot Token 和 Chat ID')
      return
    }
  } else if (selectedChannelType.value === 'meow') {
    if (!channelForm.nickname) {
      ElMessage.warning('请填写昵称')
      return
    }
  } else if (selectedChannelType.value === 'bark') {
    if (!channelForm.device_key) {
      ElMessage.warning('请填写设备密钥')
      return
    }
  } else if (selectedChannelType.value === 'serverchan') {
    if (!channelForm.sc_key) {
      ElMessage.warning('请填写 SCKEY')
      return
    }
  } else if (selectedChannelType.value === 'webhook') {
    if (!channelForm.endpoint || !channelForm.method || !channelForm.template) {
      ElMessage.warning('请填写请求地址、请求方法和消息模板')
      return
    }
    if (channelForm.method === 'POST' && !channelForm.format) {
      ElMessage.warning('请选择 POST 数据格式')
      return
    }
  }

  creating.value = true
  try {
    const requestData: Record<string, unknown> = {
      channel_name: channelForm.channel_name,
    }

    // 根据类型添加配置字段
    if (selectedChannelType.value === 'telegram') {
      requestData.bot_token = channelForm.bot_token
      requestData.chat_id = channelForm.chat_id
    } else if (selectedChannelType.value === 'meow') {
      requestData.nickname = channelForm.nickname
      if (channelForm.endpoint) {
        requestData.endpoint = channelForm.endpoint
      }
    } else if (selectedChannelType.value === 'bark') {
      requestData.device_key = channelForm.device_key
      if (channelForm.server_url) {
        requestData.server_url = channelForm.server_url
      }
      if (channelForm.sound) {
        requestData.sound = channelForm.sound
      }
      if (channelForm.icon) {
        requestData.icon = channelForm.icon
      }
    } else if (selectedChannelType.value === 'serverchan') {
      requestData.sc_key = channelForm.sc_key
      if (channelForm.endpoint) {
        requestData.endpoint = channelForm.endpoint
      }
    } else if (selectedChannelType.value === 'webhook') {
      requestData.endpoint = channelForm.endpoint
      requestData.method = channelForm.method
      requestData.template = channelForm.template
      if (channelForm.method === 'POST') {
        requestData.format = channelForm.format
      }
      if (channelForm.method === 'GET' && channelForm.query_param) {
        requestData.query_param = channelForm.query_param
      }
      if (channelForm.auth_type && channelForm.auth_type !== 'none') {
        requestData.auth_type = channelForm.auth_type
        if (channelForm.auth_type === 'bearer' || channelForm.auth_type === 'query') {
          requestData.auth_token = channelForm.auth_token
          if (channelForm.auth_type === 'query' && channelForm.auth_query_key) {
            requestData.auth_query_key = channelForm.auth_query_key
          }
        } else if (channelForm.auth_type === 'basic') {
          requestData.auth_user = channelForm.auth_user
          requestData.auth_pass = channelForm.auth_pass
        } else if (channelForm.auth_type === 'header') {
          requestData.auth_header_key = channelForm.auth_header_key
          requestData.auth_token = channelForm.auth_token
        }
      }
      const headers = webhookHeaderRowsToRecord(channelForm.headers)
      if (Object.keys(headers).length > 0) {
        requestData.headers = headers
      }
      if (channelForm.description) {
        requestData.description = channelForm.description
      }
    }

    const response = await http.post(
      `${SERVER_URL}/setting/notification/channels/${selectedChannelType.value}`,
      requestData,
    )

    if (response?.data.code === 0) {
      ElMessage.success('创建成功')
      createDialogVisible.value = false
      loadChannels()
    } else {
      ElMessage.error(response?.data.message || '创建失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '创建渠道失败'
    ElMessage.error(errorMessage)
  } finally {
    creating.value = false
  }
}

// 切换渠道状态
const toggleChannelStatus = async (channel: ChannelWithStatus) => {
  channel._switching = true
  try {
    const response = await http.post(`${SERVER_URL}/setting/notification/channels/status`, {
      channel_id: channel.id,
      is_enabled: channel.is_enabled,
    })

    if (response?.data.code === 0) {
      ElMessage.success(channel.is_enabled ? '已启用' : '已禁用')
    } else {
      // 恢复原状态
      channel.is_enabled = !channel.is_enabled
      ElMessage.error(response?.data.message || '操作失败')
    }
  } catch (error: unknown) {
    // 恢复原状态
    channel.is_enabled = !channel.is_enabled
    const errorMessage = error instanceof Error ? error.message : '切换状态失败'
    ElMessage.error(errorMessage)
  } finally {
    channel._switching = false
  }
}

// 更新渠道
const updateChannel = async () => {
  if (!editingChannel.value || !channelForm.channel_name) {
    ElMessage.warning('请输入渠道名称')
    return
  }

  updating.value = true
  try {
    const requestData: Record<string, unknown> = {
      channel_id: editingChannel.value.id,
      channel_name: channelForm.channel_name,
    }

    const channelType = editingChannel.value.channel_type

    // 根据类型添加配置字段
    if (channelType === 'telegram') {
      if (channelForm.bot_token) requestData.bot_token = channelForm.bot_token
      if (channelForm.chat_id) requestData.chat_id = channelForm.chat_id
    } else if (channelType === 'meow') {
      if (channelForm.nickname) requestData.nickname = channelForm.nickname
      if (channelForm.endpoint) requestData.endpoint = channelForm.endpoint
    } else if (channelType === 'bark') {
      if (channelForm.device_key) requestData.device_key = channelForm.device_key
      if (channelForm.server_url) requestData.server_url = channelForm.server_url
      if (channelForm.sound) requestData.sound = channelForm.sound
      if (channelForm.icon) requestData.icon = channelForm.icon
    } else if (channelType === 'serverchan') {
      if (channelForm.sc_key) requestData.sc_key = channelForm.sc_key
      if (channelForm.endpoint) requestData.endpoint = channelForm.endpoint
    } else if (channelType === 'webhook') {
      if (channelForm.endpoint) requestData.endpoint = channelForm.endpoint
      if (channelForm.method) requestData.method = channelForm.method
      if (channelForm.template) requestData.template = channelForm.template
      if (channelForm.method === 'POST' && channelForm.format) {
        requestData.format = channelForm.format
      }
      if (channelForm.method === 'GET' && channelForm.query_param) {
        requestData.query_param = channelForm.query_param
      }
      if (channelForm.auth_type) {
        requestData.auth_type = channelForm.auth_type
        if (channelForm.auth_type === 'bearer' || channelForm.auth_type === 'query') {
          if (channelForm.auth_token) requestData.auth_token = channelForm.auth_token
          if (channelForm.auth_type === 'query' && channelForm.auth_query_key) {
            requestData.auth_query_key = channelForm.auth_query_key
          }
        } else if (channelForm.auth_type === 'basic') {
          if (channelForm.auth_user) requestData.auth_user = channelForm.auth_user
          if (channelForm.auth_pass) requestData.auth_pass = channelForm.auth_pass
        } else if (channelForm.auth_type === 'header') {
          if (channelForm.auth_header_key) requestData.auth_header_key = channelForm.auth_header_key
          if (channelForm.auth_token) requestData.auth_token = channelForm.auth_token
        }
      }
      requestData.headers = webhookHeaderRowsToRecord(channelForm.headers)
      if (channelForm.description) requestData.description = channelForm.description
    }

    const response = await http.put(
      `${SERVER_URL}/setting/notification/channels/${channelType}`,
      requestData,
    )

    if (response?.data.code === 0) {
      ElMessage.success('更新成功')
      editDialogVisible.value = false
      loadChannels()
    } else {
      ElMessage.error(response?.data.message || '更新失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '更新渠道失败'
    ElMessage.error(errorMessage)
  } finally {
    updating.value = false
  }
}

// 测试渠道
const testChannel = async (channel: ChannelWithStatus) => {
  channel._testing = true
  try {
    const response = await http.post(`${SERVER_URL}/setting/notification/channels/test`, {
      channel_id: channel.id,
    })

    if (response?.data.code === 0) {
      ElMessage.success('测试消息已发送，请检查设备')
    } else {
      ElMessage.error(response?.data.message || '测试失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '测试连接失败'
    ElMessage.error(errorMessage)
  } finally {
    channel._testing = false
  }
}

// 删除渠道
const deleteChannel = async (channel: NotificationChannel) => {
  try {
    await ElMessageBox.confirm(
      `确定要删除渠道“${channel.channel_name}”吗？此操作将同时删除所有相关配置和规则。`,
      '确认删除',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    const response = await http.delete(`${SERVER_URL}/setting/notification/channels/${channel.id}`)

    if (response?.data.code === 0) {
      ElMessage.success('删除成功')
      loadChannels()
    } else {
      ElMessage.error(response?.data.message || '删除失败')
    }
  } catch (error: unknown) {
    if (error !== 'cancel') {
      const errorMessage = error instanceof Error ? error.message : '删除渠道失败'
      ElMessage.error(errorMessage)
    }
  }
}

// 显示规则对话框
const showRulesDialog = async (channel: NotificationChannel) => {
  currentChannel.value = channel
  rulesDialogVisible.value = true
  await loadRules(channel.id)
}

// 加载规则
const loadRules = async (channelId: number) => {
  rulesLoading.value = true
  try {
    const response = await http.get(
      `${SERVER_URL}/setting/notification/rules?channel_id=${channelId}`,
    )

    if (response?.data.code === 0) {
      currentRules.value = response.data.data.map((rule: NotificationRule): RuleWithStatus => ({
        ...rule,
        _updating: false,
      }))
    } else {
      ElMessage.error(response?.data.message || '加载规则失败')
    }
  } catch (error: unknown) {
    const errorMessage = error instanceof Error ? error.message : '加载通知规则失败'
    ElMessage.error(errorMessage)
  } finally {
    rulesLoading.value = false
  }
}

// 更新规则
const updateRule = async (rule: RuleWithStatus) => {
  rule._updating = true
  try {
    const response = await http.put(`${SERVER_URL}/setting/notification/rules`, {
      channel_id: rule.channel_id,
      event_type: rule.event_type,
      is_enabled: rule.is_enabled,
    })

    if (response?.data.code === 0) {
      ElMessage.success('更新成功')
    } else {
      // 恢复原状态
      rule.is_enabled = !rule.is_enabled
      ElMessage.error(response?.data.message || '更新失败')
    }
  } catch (error: unknown) {
    // 恢复原状态
    rule.is_enabled = !rule.is_enabled
    const errorMessage = error instanceof Error ? error.message : '更新规则失败'
    ElMessage.error(errorMessage)
  } finally {
    rule._updating = false
  }
}

onMounted(() => {
  loadChannels()
})
</script>

<style scoped>
.notification-page {
  padding: 0;
}

.notification-page :deep(.notification-page-header) {
  margin-bottom: 0;
  padding: 20px 24px;
}

.header-actions {
  display: flex;
  gap: 8px;
}

.notification-channels-container {
  padding: 20px 24px;
}

.empty-state {
  padding: 60px 0;
  text-align: center;
}

.channels-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
  gap: 16px;
}

.channel-card {
  border-radius: 12px;
  border: 1px solid var(--el-border-color-lighter);
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
}

.channel-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.08);
}

.channel-card.channel-disabled {
  opacity: 0.7;
}

.channel-card.channel-disabled .channel-type-badge {
  filter: grayscale(50%);
}

.channel-card-header {
  display: flex;
  align-items: center;
  gap: 12px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.channel-type-badge {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  flex-shrink: 0;
}

.channel-type-badge.type-telegram {
  background: linear-gradient(135deg, #37aee2, #1e96c8);
}

.channel-type-badge.type-meow {
  background: linear-gradient(135deg, #667eea, #764ba2);
}

.channel-type-badge.type-bark {
  background: linear-gradient(135deg, #ff6b6b, #ee5a5a);
}

.channel-type-badge.type-serverchan {
  background: linear-gradient(135deg, #07c160, #06ad56);
}

.channel-type-badge.type-webhook {
  background: linear-gradient(135deg, #6b7280, #4b5563);
}

.channel-info {
  flex: 1;
  min-width: 0;
}

.channel-name {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.channel-type-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.channel-card-body {
  padding: 12px 0;
}

.channel-meta {
  display: flex;
  gap: 16px;
}

.meta-item {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.channel-card-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.channel-card-actions .el-button {
  padding: 6px 12px;
}

.channel-type-selector-wrapper {
  padding: 8px 0;
}

.selector-title {
  margin: 0 0 16px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.channel-type-selector {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.channel-type-card {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px;
  border-radius: 12px;
  border: 2px solid var(--el-border-color-lighter);
  cursor: pointer;
  transition:
    border-color 0.2s ease,
    background-color 0.2s ease;
  background: var(--el-bg-color);
}

.channel-type-card:hover {
  border-color: var(--el-color-primary-light-5);
  background: var(--el-fill-color-light);
}

.channel-type-card .channel-type-icon {
  width: 48px;
  height: 48px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  flex-shrink: 0;
}

.channel-type-card.type-telegram .channel-type-icon {
  background: linear-gradient(135deg, #37aee2, #1e96c8);
}

.channel-type-card.type-meow .channel-type-icon {
  background: linear-gradient(135deg, #667eea, #764ba2);
}

.channel-type-card.type-bark .channel-type-icon {
  background: linear-gradient(135deg, #ff6b6b, #ee5a5a);
}

.channel-type-card.type-serverchan .channel-type-icon {
  background: linear-gradient(135deg, #07c160, #06ad56);
}

.channel-type-card.type-webhook .channel-type-icon {
  background: linear-gradient(135deg, #6b7280, #4b5563);
}

.channel-type-info {
  flex: 1;
}

.channel-type-card .channel-type-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin-bottom: 4px;
}

.channel-type-card .channel-type-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

@media (max-width: 768px) {
  .notification-page :deep(.notification-page-header) {
    padding: 10px 12px;
  }

  .header-actions {
    display: flex;
    width: 100%;
    justify-content: center;
    gap: 8px;
  }

  .header-actions :deep(.el-button + .el-button) {
    margin-left: 0;
  }

  .notification-channels-container {
    padding: 12px;
  }

  .channels-grid {
    grid-template-columns: 1fr;
  }

  .channel-card-header {
    flex-wrap: wrap;
  }

  .channel-card-actions {
    justify-content: center;
  }

  .channel-card-actions .el-button {
    flex: 1;
    min-width: 70px;
  }
}

@media (max-width: 480px) {
  .header-actions {
    gap: 8px;
  }

  .channel-type-badge {
    width: 38px;
    height: 38px;
  }

  .channel-name {
    font-size: 14px;
  }
}
</style>
