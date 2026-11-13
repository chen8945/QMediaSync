<script setup lang="ts">
import { computed, onBeforeUnmount } from 'vue'
import { DocumentCopy, Link } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { SERVER_URL } from '@/const'
import type { MediaServerType } from '@/api/mediaLibrary'
import { copyText } from '@/utils/clipboard'

const props = defineProps<{
  serverType: MediaServerType
  productName: string
  disabled: boolean
  active: boolean
}>()
const auth = defineModel<number>('auth', { required: true })
const notification = defineModel<number>('notification', { required: true })
const overview = defineModel<number>('overview', { required: true })
const progress = defineModel<number>('progress', { required: true })
let mounted = true
onBeforeUnmount(() => {
  mounted = false
})
const webhookUrl = computed(
  () =>
    `${SERVER_URL === '/api' ? window.location.origin : SERVER_URL.replace(/\/api$/, '')}/${props.serverType}/webhook`,
)
const copyWebhookUrl = async () => {
  const copied = await copyText(webhookUrl.value)
  if (!mounted || !props.active) return
  if (copied) ElMessage.success('Webhook 链接已复制到剪贴板')
  else ElMessage.error('复制失败，请手动复制')
}
</script>

<template>
  <el-card class="settings-card webhook-card" shadow="hover">
    <template #header>
      <h3 class="card-title">通知链接配置</h3>
      <p class="card-subtitle">连接 {{ productName }} 与 QMediaSync</p>
    </template>
    <el-form-item :label="`${productName} 通知链接`">
      <el-input :model-value="webhookUrl" readonly :prefix-icon="Link" class="limited-width-input">
        <template #append
          ><el-button :icon="DocumentCopy" @click="copyWebhookUrl">复制</el-button></template
        >
      </el-input>
      <p v-if="serverType === 'jellyfin'" class="form-help">
        安装 Jellyfin 官方 Webhook 插件，添加 Generic Destination，将此链接设为 Webhook URL。
        使用标准 JSON 输出，并设置请求头 Content-Type:
        application/json；按需勾选媒体新增、移除和播放事件。
        提取媒体信息需要服务器中有可播放对应媒体的用户。
      </p>
      <div v-else class="form-help">
        <p>
          在 Emby 通知设置中选择
          JSON（application/json），关闭“分组”（GroupItems），按需勾选媒体库、“已添加新媒体”（library.new）和“媒体已移除”（library.deleted）。
        </p>
        <p>
          不要勾选“移动”“重命名”“扫描失效”“移除媒体库”等独立事件。通知所属用户与用户事件过滤分别设置，实际操作用户须能发送所选事件。
        </p>
        <p>
          官方通知即可使用。神医助手或 MediaInfoKeeper
          只需开启“通知增强”，并加勾“媒体深度删除”（deep.delete）；请关闭插件自带的物理删除功能。
        </p>
      </div>
      <p class="form-help">
        删除源文件由“删除时联动删除网盘文件”控制，通知开关不影响同步、提取或联动删除。
      </p>
      <p v-if="auth" class="form-help warning-text">
        已开启鉴权，请通过 X-API-Key 请求头或 api_key 查询参数携带 QMediaSync API Key，例如：<code
          >{{ webhookUrl }}?api_key=你的 API Key</code
        >。
      </p>
    </el-form-item>
    <el-form-item label="通知链接鉴权" prop="enable_auth">
      <el-switch
        v-model="auth"
        :active-value="1"
        :inactive-value="0"
        :disabled="disabled"
        aria-label="通知链接鉴权"
      />
      <p class="form-help">
        外网使用时建议开启。前往
        <router-link to="/settings/api-keys">API Key 模块</router-link>生成密钥。
      </p>
    </el-form-item>
    <el-form-item label="发送媒体通知" prop="enable_media_notification">
      <el-switch
        v-model="notification"
        :active-value="1"
        :inactive-value="0"
        :disabled="disabled"
        aria-label="发送媒体通知"
      />
      <p class="form-help">只控制本服务器的通知发送，渠道和事件规则在通知管理中统一设置。</p>
    </el-form-item>
    <el-form-item label="播放通知显示剧情简介" prop="enable_playback_overview">
      <el-switch
        v-model="overview"
        :active-value="1"
        :inactive-value="0"
        :disabled="disabled"
        aria-label="播放通知显示剧情简介"
      />
      <p class="form-help">显示剧情简介，超过 100 字自动截断。</p>
    </el-form-item>
    <el-form-item label="播放通知显示播放进度" prop="enable_playback_progress">
      <el-switch
        v-model="progress"
        :active-value="1"
        :inactive-value="0"
        :disabled="disabled"
        aria-label="播放通知显示播放进度"
      />
      <p class="form-help">显示播放进度与总时长。</p>
    </el-form-item>
  </el-card>
</template>

<style scoped>
.form-help {
  width: 100%;
  color: var(--el-text-color-secondary);
  line-height: 1.7;
  overflow-wrap: anywhere;
}
.limited-width-input {
  max-width: 680px;
}
.warning-text {
  color: var(--el-color-warning);
}
</style>
