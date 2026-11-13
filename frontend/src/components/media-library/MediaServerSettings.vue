<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  toRef,
  useTemplateRef,
  watch,
} from 'vue'
import {
  Check,
  Refresh,
  Monitor,
  Link,
  Key,
  Setting,
  InfoFilled,
  WarningFilled,
  FolderOpened,
  Clock,
} from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'
import {
  extractMediaLibraryMediaInfo,
  fetchMediaLibraryConfig,
  fetchMediaLibraryLibraries,
  mediaLibraryMessages,
  saveMediaLibraryConfig,
  startMediaLibrarySync,
  type MediaLibraryConfig,
  type MediaLibraryLibraryOption,
  type MediaServerType,
} from '@/api/mediaLibrary'
import { fetchCronTimes, systemSettingsPublicMessages } from '@/api/systemSettings'
import { CRON_DEFAULTS, HTTP_URL_PATTERN } from '@/constants/validation'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { useMediaLibrarySyncStatus } from '@/composables/useMediaLibrarySyncStatus'
import { useDeviceType } from '@/composables/useDeviceType'
import { useFeedbackScroll } from '@/composables/useFeedbackScroll'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'
import { formatMaybeUnixDateTime } from '@/utils/timeUtils'
import ResponsiveActionBar from '@/components/common/ResponsiveActionBar.vue'
import MediaWebhookSettings from './MediaWebhookSettings.vue'
import MediaSyncStatus from './MediaSyncStatus.vue'

const props = defineProps<{ serverType: MediaServerType; active: boolean }>()
// eslint-disable-next-line vue/no-setup-props-reactivity-loss -- 实例在页面生命周期内固定绑定产品，切标签只改变 active。
const serverType = props.serverType
const productName = serverType === 'emby' ? 'Emby' : 'Jellyfin'
const http = useHttpClient()
const { isMobile } = useDeviceType()
const formRef = useTemplateRef<FormInstance>('formRef')
const defaults: MediaLibraryConfig = {
  enabled: false,
  server_url: '',
  api_key: '',
  enable_media_notification: 0,
  sync_enabled: 0,
  sync_cron: CRON_DEFAULTS.embySync,
  enable_refresh_library: 0,
  enable_extract_media_info: 0,
  enable_delete_netdisk: 0,
  enable_auth: 1,
  sync_all_libraries: 1,
  selected_libraries: '[]',
  enable_daily_first_full_sync: 1,
  enable_playback_overview: 0,
  enable_playback_progress: 0,
  path_mappings: [],
}
const form = reactive<MediaLibraryConfig>({ ...defaults })
const loaded = ref(false)
const busy = ref(false)
const syncStartLoading = ref(false)
const savedEnabled = ref(false)
const savedSyncEnabled = ref(false)
const savedConnection = ref(false)
const availableLibraries = ref<MediaLibraryLibraryOption[]>([])
const selectedLibraryIds = ref<string[]>([])
const cronNextTimes = ref<string[]>([])
const cronError = ref('')
const readbackFailure = ref('')
const feedback = ref<{
  title: string
  type: 'success' | 'warning' | 'error'
  description: string
} | null>(null)
const { feedbackRef, revealFeedback } = useFeedbackScroll(feedback)
let mounted = true
const cronGate = createActiveRequestGate(() => mounted)
const libraryGate = createActiveRequestGate(() => mounted)
const {
  status: syncInfo,
  running: isSyncRunning,
  error: syncError,
  refresh: refreshStatus,
} = useMediaLibrarySyncStatus(http, serverType, toRef(props, 'active'))
const isStartSyncDisabled = computed(
  () =>
    busy.value ||
    syncStartLoading.value ||
    !loaded.value ||
    !savedEnabled.value ||
    !savedSyncEnabled.value ||
    !savedConnection.value ||
    isSyncRunning.value,
)
const publicMessages = mediaLibraryMessages(serverType)
const failureMessage = (error: unknown, title: string) => {
  const failure = parseHttpError(error, { publicMessages, fallbackMessage: title })
  if (!failure.shouldNotify) return null
  console.error(title, failure.diagnostics)
  return failure
}
const showFailure = (error: unknown, title: string) => {
  const failure = failureMessage(error, title)
  if (failure)
    feedback.value = {
      title:
        failure.kind === 'origin' || failure.kind === 'csrf' ? '请求被 QMediaSync 拒绝' : title,
      type: 'error',
      description: failure.message,
    }
}
const syncFailure = computed(() =>
  syncError.value
    ? parseHttpError(syncError.value, {
        publicMessages,
        fallbackMessage: `查询 ${productName} 同步状态失败`,
      })
    : null,
)
watch(syncFailure, (failure) => {
  if (failure?.shouldNotify) console.error(`查询 ${productName} 同步状态失败`, failure.diagnostics)
})
const clearFeedback = () => {
  feedback.value = null
  readbackFailure.value = ''
}
const reveal = () => {
  if (props.active) void revealFeedback()
}
const formRules: FormRules = {
  server_url: [
    {
      pattern: HTTP_URL_PATTERN,
      message: '请输入有效的 URL 格式，如：http://ip:port',
      trigger: 'blur',
    },
  ],
}
const loadConfig = async (afterSave = false) => {
  try {
    const result = await fetchMediaLibraryConfig(http, serverType)
    if (!mounted) return false
    Object.assign(form, defaults, result.exists ? result.config : {})
    form.server_url ??= ''
    form.api_key ??= ''
    savedEnabled.value = form.enabled
    savedSyncEnabled.value = Boolean(form.sync_enabled)
    savedConnection.value = Boolean(form.server_url && form.api_key)
    try {
      const selected: unknown = JSON.parse(form.selected_libraries)
      selectedLibraryIds.value = Array.isArray(selected)
        ? selected.filter((id): id is string => typeof id === 'string')
        : []
    } catch {
      selectedLibraryIds.value = []
    }
    loaded.value = true
    return true
  } catch (error) {
    if (!mounted) return false
    const title = afterSave
      ? `${productName} 配置已保存，但刷新失败`
      : `加载 ${productName} 配置失败`
    if (afterSave) {
      const failure = failureMessage(error, title)
      if (failure)
        readbackFailure.value = `${title}：${failure.kind === 'origin' || failure.kind === 'csrf' ? '请求被 QMediaSync 拒绝：' : ''}${failure.message}`
    } else showFailure(error, title)
    return false
  }
}
const loadLibraries = async (afterSave = false) => {
  if (form.sync_all_libraries !== 0) return
  const requestId = libraryGate.next()
  try {
    const libraries = await fetchMediaLibraryLibraries(http, serverType)
    if (libraryGate.isCurrent(requestId)) availableLibraries.value = libraries
  } catch (error) {
    if (!libraryGate.isCurrent(requestId)) return
    const title = afterSave
      ? `${productName} 配置已保存，但无法获取媒体库`
      : `无法获取 ${productName} 媒体库`
    if (afterSave) {
      const failure = failureMessage(error, title)
      if (failure) readbackFailure.value = `${title}：${failure.message}`
    } else showFailure(error, title)
  }
}
const loadSettings = async () => {
  if (busy.value) return
  busy.value = true
  clearFeedback()
  try {
    if (await loadConfig()) await loadLibraries()
  } finally {
    if (mounted) busy.value = false
  }
}
const handleSyncModeChange = (value: number) => {
  libraryGate.invalidate()
  if (value === 0) void loadLibraries()
}
const save = async () => {
  if (!loaded.value || busy.value || syncStartLoading.value || !formRef.value) return
  libraryGate.invalidate()
  busy.value = true
  try {
    await formRef.value.validate()
  } catch {
    if (mounted) busy.value = false
    return
  }
  if (!mounted) return
  clearFeedback()
  try {
    // 隐藏映射不提交，保留服务器上更新后的映射。
    const fields = { ...form }
    delete (fields as Partial<MediaLibraryConfig>).path_mappings
    const result = await saveMediaLibraryConfig(http, serverType, {
      ...fields,
      server_url: form.server_url.trim(),
      api_key: form.api_key.trim(),
      selected_libraries: JSON.stringify(selectedLibraryIds.value),
    })
    if (!mounted) return
    savedEnabled.value = form.enabled
    savedSyncEnabled.value = Boolean(form.sync_enabled)
    savedConnection.value = Boolean(form.server_url.trim() && form.api_key.trim())
    feedback.value = {
      title:
        result.warnings.length || result.restart_required ? '配置已保存，尚未完全生效' : '保存成功',
      type: result.warnings.length || result.restart_required ? 'warning' : 'success',
      description: [
        `${productName} 配置已保存`,
        ...result.warnings,
        result.restart_required ? 'Emby 代理设置需重启 QMediaSync 生效' : '',
      ]
        .filter(Boolean)
        .join('；'),
    }
    if (await loadConfig(true)) await loadLibraries(true)
    void refreshStatus()
  } catch (error) {
    if (mounted) showFailure(error, `保存 ${productName} 配置失败`)
  } finally {
    if (mounted) {
      busy.value = false
      reveal()
    }
  }
}
const extract = async () => {
  if (busy.value || syncStartLoading.value || !savedEnabled.value || !savedConnection.value) return
  busy.value = true
  clearFeedback()
  try {
    await extractMediaLibraryMediaInfo(http, serverType)
    if (mounted)
      feedback.value = {
        title: '触发提取媒体信息成功',
        type: 'success',
        description: '已提交提取任务，请在下载队列查看进度',
      }
  } catch (error) {
    if (mounted) showFailure(error, '触发提取媒体信息失败')
  } finally {
    if (mounted) busy.value = false
  }
}
const startSync = async () => {
  if (isStartSyncDisabled.value) return
  syncStartLoading.value = true
  clearFeedback()
  try {
    await startMediaLibrarySync(http, serverType)
    if (!mounted) return
    feedback.value = {
      title: '同步已启动',
      type: 'success',
      description: `${productName} 条目同步任务已启动`,
    }
    await refreshStatus(true)
  } catch (error) {
    if (mounted) showFailure(error, `启动 ${productName} 同步失败`)
  } finally {
    if (mounted) syncStartLoading.value = false
  }
}
const fetchCronNextTimes = async () => {
  const requestId = cronGate.next()
  cronError.value = ''
  if (!form.sync_cron.trim()) {
    cronNextTimes.value = []
    return
  }
  try {
    const times = await fetchCronTimes(http, form.sync_cron.trim())
    if (cronGate.isCurrent(requestId)) cronNextTimes.value = times.map(formatMaybeUnixDateTime)
  } catch (error) {
    if (!cronGate.isCurrent(requestId)) return
    cronNextTimes.value = []
    const failure = parseHttpError(error, {
      fallbackMessage: '获取 Cron 执行时间失败',
      publicMessages: systemSettingsPublicMessages,
    })
    if (failure.shouldNotify) {
      console.error('获取 Cron 执行时间失败', failure.diagnostics)
      cronError.value = failure.message
    }
  }
}
watch(
  () => form.sync_cron,
  () => {
    cronGate.invalidate()
    cronNextTimes.value = []
    cronError.value = ''
  },
  { flush: 'sync' },
)
onMounted(loadSettings)
onBeforeUnmount(() => {
  mounted = false
  cronGate.invalidate()
  libraryGate.invalidate()
})
</script>

<template>
  <div class="media-server-settings">
    <div class="emby-settings-wrapper">
      <el-form
        :model="form"
        :rules="formRules"
        :disabled="!loaded || busy || syncStartLoading"
        :label-position="isMobile ? 'top' : 'left'"
        :label-width="200"
        class="emby-form"
        ref="formRef"
        @submit.prevent="save"
      >
        <el-card class="settings-card emby-server-card" shadow="hover">
          <template #header>
            <div class="card-header-wrapper">
              <div class="card-header-icon server-icon">
                <el-icon :size="24"><Monitor /></el-icon>
              </div>
              <div class="card-header-content">
                <h2 class="card-title">{{ productName }} 服务器配置</h2>
                <p class="card-subtitle">配置服务器连接信息</p>
              </div>
            </div>
          </template>
          <el-form-item :label="`启用 ${productName}`" prop="enabled">
            <el-switch v-model="form.enabled" :aria-label="`启用 ${productName}`" />
            <p class="form-help">关闭后暂停此服务器的同步与联动，已保存的设置会保留。</p>
          </el-form-item>
          <el-form-item :label="`${productName} 服务器地址`" prop="server_url">
            <el-input
              v-model="form.server_url"
              :placeholder="`请输入 ${productName} 服务器地址，格式：http://ip:port`"
              :aria-label="`${productName} 服务器地址`"
              class="limited-width-input"
              :prefix-icon="Link"
              clearable
            />
            <p class="form-help">填写完整服务地址，支持反向代理子路径。更改后请保存设置。</p>
            <p v-if="serverType === 'emby'" class="form-help">
              使用 Emby 302 代理时，连接设置更改需重启 QMediaSync 生效。
            </p>
          </el-form-item>
          <el-form-item :label="`${productName} API Key`" prop="api_key">
            <el-input
              v-model="form.api_key"
              :placeholder="`请输入 ${productName} API Key`"
              :aria-label="`${productName} API Key`"
              class="limited-width-input"
              :prefix-icon="Key"
            />
            <p class="form-help">
              使用对应服务器生成的 API Key。同步、刷新和提取使用已保存的配置。
            </p>
          </el-form-item>
        </el-card>
        <MediaWebhookSettings
          :server-type="serverType"
          :product-name="productName"
          :disabled="!loaded || busy || syncStartLoading"
          :active="active"
          v-model:auth="form.enable_auth"
          v-model:notification="form.enable_media_notification"
          v-model:overview="form.enable_playback_overview"
          v-model:progress="form.enable_playback_progress"
        />
        <el-card class="settings-card sync-features-card" shadow="hover">
          <template #header>
            <div class="card-header-wrapper">
              <div class="card-header-icon features-icon">
                <el-icon :size="24"><Setting /></el-icon>
              </div>
              <div class="card-header-content">
                <h3 class="card-title">同步和功能配置</h3>
                <p class="card-subtitle">配置 STRM 同步与媒体库联动功能</p>
              </div>
            </div>
          </template>

          <div class="feature-item">
            <el-form-item label="入库后提取媒体信息" prop="enable_extract_media_info">
              <div class="switch-wrapper">
                <el-switch
                  v-model="form.enable_extract_media_info"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="busy"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span class="switch-label" :class="{ 'is-active': form.enable_extract_media_info }">
                  {{ form.enable_extract_media_info ? '启用' : '禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <p class="feature-note">
                QMediaSync 收到
                {{ productName }} 入库通知后，会自动提取该资源的视频、音频、内封字幕等媒体信息，帮助
                {{ productName }} 更快起播。
              </p>
            </div>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item">
            <el-form-item label="启用同步" prop="sync_enabled">
              <div class="switch-wrapper">
                <el-switch
                  v-model="form.sync_enabled"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="busy"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span class="switch-label" :class="{ 'is-active': form.sync_enabled }">
                  {{ form.sync_enabled ? '已启用' : '已禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <p class="feature-note">
                启用后会把 {{ productName }} 条目同步到 QMediaSync
                本地，并与网盘文件建立关联，用于刷新媒体库和联动删除网盘文件。
              </p>
            </div>
          </div>

          <!-- {{ productName }} 条目同步范围选择卡片 - 只有启用同步时才显示 -->
          <el-card
            v-if="form.sync_enabled === 1"
            class="settings-card library-selection-card"
            shadow="hover"
          >
            <template #header>
              <div class="card-header-wrapper">
                <div class="card-header-icon library-icon">
                  <el-icon :size="24"><FolderOpened /></el-icon>
                </div>
                <div class="card-header-content">
                  <h3 class="card-title">媒体库条目同步选择</h3>
                  <p class="card-subtitle">选择需要同步条目的 {{ productName }} 媒体库</p>
                </div>
              </div>
            </template>

            <el-form-item label="同步模式">
              <el-radio-group v-model="form.sync_all_libraries" @change="handleSyncModeChange">
                <el-radio :value="1">全部媒体库</el-radio>
                <el-radio :value="0">指定媒体库</el-radio>
              </el-radio-group>
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span
                  >选择“全部媒体库”会同步所有媒体库中的条目（包括未来新增的媒体库）；选择“指定媒体库”可手动选择需要同步条目的媒体库</span
                >
              </div>
            </el-form-item>

            <el-form-item label="选择媒体库" v-if="form.sync_all_libraries === 0">
              <el-checkbox-group v-model="selectedLibraryIds" class="library-checkbox-group">
                <el-checkbox
                  v-for="lib in availableLibraries"
                  :key="lib.library_id"
                  :value="lib.library_id"
                  class="library-checkbox"
                >
                  {{ lib.name }}
                </el-checkbox>
              </el-checkbox-group>
              <div class="form-help" v-if="availableLibraries.length === 0">
                <el-icon><WarningFilled /></el-icon>
                <span class="warning-text">请先保存服务器连接配置，再重新选择媒体库</span>
              </div>
            </el-form-item>
          </el-card>

          <el-divider class="feature-divider" />

          <div class="feature-item" :class="{ 'is-disabled': !form.sync_enabled }">
            <el-form-item label="同步时间" prop="sync_cron" :error="cronError">
              <el-input
                v-model="form.sync_cron"
                :placeholder="`请输入 Cron 表达式，如：${CRON_DEFAULTS.embySync}`"
                :disabled="busy || !form.sync_enabled"
                class="limited-width-input"
                @blur="fetchCronNextTimes"
                clearable
              />
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span
                  >Cron 表达式，格式：分 时 日 月 周（如：{{
                    CRON_DEFAULTS.embySync
                  }}
                  表示每小时整点执行）</span
                >
              </div>
            </el-form-item>
            <div v-if="cronNextTimes.length > 0" class="cron-next-times">
              <div class="cron-times-header">
                <el-icon><Clock /></el-icon>
                <span>接下来 5 次执行时间：</span>
              </div>
              <ul class="cron-times-list">
                <li v-for="(time, index) in cronNextTimes" :key="index">{{ time }}</li>
              </ul>
            </div>
            <el-form-item label="每日首次全量同步" prop="enable_daily_first_full_sync">
              <div class="switch-wrapper">
                <el-switch
                  v-model="form.enable_daily_first_full_sync"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="busy || !form.sync_enabled"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span
                  class="switch-label"
                  :class="{ 'is-active': form.enable_daily_first_full_sync }"
                >
                  {{ form.enable_daily_first_full_sync ? '启用' : '禁用' }}
                </span>
              </div>
              <div class="form-help">
                <el-icon><InfoFilled /></el-icon>
                <span>启用后，每天第一次定时同步执行全量同步，之后当天定时同步执行增量同步</span>
              </div>
            </el-form-item>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item" :class="{ 'is-disabled': !form.sync_enabled }">
            <el-form-item label="同步后刷新媒体库" prop="enable_refresh_library">
              <div class="switch-wrapper">
                <el-switch
                  v-model="form.enable_refresh_library"
                  :active-value="1"
                  :inactive-value="0"
                  :disabled="busy || !form.sync_enabled"
                  active-color="#67c23a"
                  inactive-color="#dcdfe6"
                />
                <span class="switch-label" :class="{ 'is-active': form.enable_refresh_library }">
                  {{ form.enable_refresh_library ? '启用' : '禁用' }}
                </span>
              </div>
            </el-form-item>
            <div class="feature-description">
              <p>
                开启后，已保存的 STRM 同步目录新增 STRM 或元数据下载任务，以及上传后 STRM
                实际新增或更新时，QMediaSync 会通知 {{ productName }} 刷新相关媒体。
              </p>
              <p>
                请先完成一次条目全量同步，建立媒体库与同步目录的关联。刷新会等待相关同步和元数据下载结束；是否扫描完成，以
                {{ productName }} 中的实际结果为准。条目同步本身不会触发服务器扫描。
              </p>
              <p>
                临时手动同步不触发刷新；STRM Webhook
                需由调用方显式开启刷新。服务器实时监控可作为补充。
              </p>
            </div>
          </div>

          <el-divider class="feature-divider" />

          <div class="feature-item danger-item">
            <div class="delete-settings" :class="{ 'is-disabled': !form.sync_enabled }">
              <el-form-item label="删除时联动删除网盘文件" prop="enable_delete_netdisk">
                <div class="switch-wrapper">
                  <el-switch
                    v-model="form.enable_delete_netdisk"
                    :active-value="1"
                    :inactive-value="0"
                    :disabled="busy || !form.sync_enabled"
                    active-color="#f56c6c"
                    inactive-color="#dcdfe6"
                  />
                  <span class="switch-label" :class="{ 'is-danger': form.enable_delete_netdisk }">
                    {{ form.enable_delete_netdisk ? '启用' : '禁用' }}
                  </span>
                </div>
              </el-form-item>
              <div class="feature-description">
                <p>
                  开启前，请先完成当前服务器的条目全量同步，并
                  <template v-if="serverType === 'jellyfin'"
                    >配置 Jellyfin 官方 Webhook 插件，</template
                  >
                  <template v-else>在 Emby 中配置媒体删除通知，</template>
                  确认 STRM、同步目录与网盘源文件对应。收到删除通知后，QMediaSync 会核对原条目、STRM
                  和文件归属，再删除已确认的网盘源文件及附件。
                </p>
                <el-alert type="warning" :closable="false" class="danger-alert">
                  <p>
                    电影、整季或整剧满足目录清理条件时，可能删除整个网盘目录，包括其中的其他文件和子目录；条件不足时，仅处理已确认的文件及附件。仍被本服务器其他媒体使用的文件会保留。
                  </p>
                  <p>
                    只想清理本地 STRM
                    时，请先关闭并保存。关闭期间收到的删除通知不会在重新开启后补删。
                  </p>
                  <p>
                    删除仅核验本服务器的媒体；多个服务器共用源文件时，各端不会互相检查引用，请自行管理。
                  </p>
                </el-alert>
              </div>
            </div>
          </div>
        </el-card>

        <ResponsiveActionBar :is-mobile="isMobile">
          <template #actions>
            <el-button
              type="success"
              size="large"
              :icon="Check"
              :loading="busy"
              :disabled="!loaded || busy || syncStartLoading"
              @click="save"
              >保存设置</el-button
            >

            <el-button v-if="!loaded" :disabled="busy" @click="loadSettings">重试加载</el-button>
            <el-button
              type="primary"
              size="large"
              :icon="Refresh"
              :disabled="!savedEnabled || !savedConnection || busy || syncStartLoading"
              @click="extract"
              >提取媒体信息</el-button
            >
          </template>
          <template #help
            ><p>
              提取结果可在<router-link to="/download-queue">下载队列页</router-link>查看。
            </p></template
          >
        </ResponsiveActionBar>
      </el-form>
      <el-alert
        v-if="feedback"
        ref="feedbackRef"
        :title="feedback.title"
        :description="feedback.description"
        :type="feedback.type"
        closable
        show-icon
        class="emby-status-alert"
        role="status"
        @close="clearFeedback"
      />
      <el-alert
        v-if="readbackFailure"
        :title="readbackFailure"
        type="error"
        :closable="false"
        show-icon
        class="readback-failure"
        role="alert"
      />
      <MediaSyncStatus
        :product-name="productName"
        :sync-info="syncInfo"
        :error-message="syncFailure?.shouldNotify ? syncFailure.message : ''"
        :is-sync-running="isSyncRunning"
        :starting="syncStartLoading"
        :disabled="isStartSyncDisabled"
        @start="startSync"
      />
    </div>
  </div>
</template>
<style scoped>
.media-server-settings {
  display: block;
  background: transparent;
}

.emby-settings-wrapper {
  margin: 0 auto;
}

.emby-form {
  display: flex;
  flex-direction: column;
  gap: 20px;
  margin-bottom: 24px;
}

.settings-card {
  border-radius: 12px;
  border: 1px solid var(--el-border-color);
  background: #fff;
  overflow: hidden;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.06);
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
  width: 100%;
  margin-right: 0;
}

.settings-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.settings-card :deep(.el-card__header) {
  padding: 18px 20px;
  border-bottom: 1px solid var(--el-border-color);
}

.settings-card :deep(.el-card__body) {
  background: #fff;
}

.card-header-wrapper {
  display: flex;
  align-items: center;
  gap: 16px;
}

.card-header-icon {
  width: 48px;
  height: 48px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
}

.server-icon {
  background: var(--qms-gradient-brand);
}

.features-icon {
  background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
}

.library-icon {
  background: linear-gradient(135deg, #8b5cf6 0%, #06b6d4 100%);
}

.card-header-content {
  flex: 1;
}

.card-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.limited-width-input {
  max-width: 600px;
}

.form-help {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-size: 14px;
  color: #3c3d40;
  margin-top: 8px;
  line-height: 1.5;
}

.form-help .el-icon {
  margin-top: 2px;
  flex-shrink: 0;
}

.switch-wrapper {
  display: flex;
  align-items: center;
  gap: 12px;
}

.switch-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
  font-weight: 500;
  transition: color 0.3s;
}

.switch-label.is-active {
  color: var(--el-color-success);
}

.switch-label.is-danger {
  color: var(--el-color-danger);
}

.warning-text {
  color: var(--el-color-warning);
}

.feature-item {
  padding: 16px 0;
}

.feature-item:first-child {
  padding-top: 0;
}

.feature-item.is-disabled,
.delete-settings.is-disabled {
  opacity: 0.5;
  pointer-events: none;
}

.danger-item {
  background: linear-gradient(135deg, #fef6f6 0%, #fff 100%);
  margin: 0 -20px;
  padding: 20px;
  border-radius: 8px;
  border: 1px solid #fde2e2;
}

.danger-alert {
  margin-bottom: 12px;
  border-radius: 8px;
}

.feature-description {
  font-size: 13px;
  color: var(--el-text-color-regular);
  line-height: 1.8;
  margin-top: 8px;
}

.feature-description p {
  margin: 6px 0;
}

.feature-note {
  color: var(--el-text-color-secondary);
  font-style: italic;
}

.cron-next-times {
  margin-top: 12px;
  padding: 12px 16px;
  background: linear-gradient(135deg, #f0f9ff 0%, #e0f2fe 100%);
  border-radius: 8px;
  border: 1px solid #bae6fd;
}

.cron-times-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 500;
  color: #0284c7;
  margin-bottom: 8px;
}

.cron-times-list {
  margin: 0;
  padding-left: 20px;
  list-style: decimal;
}

.cron-times-list li {
  font-size: 13px;
  color: #0369a1;
  line-height: 1.8;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
}

.feature-divider {
  margin: 16px 0;
}

.emby-status-alert {
  margin-bottom: 20px;
  border-radius: 8px;
}

@media (max-width: 768px) {
  .media-server-settings {
    padding: 12px;
  }

  .emby-settings-wrapper {
    max-width: 100%;
  }

  .emby-form {
    gap: 16px;
  }

  .settings-card {
    border-radius: 8px;
  }

  .settings-card:hover {
    transform: none;
  }

  .card-header-wrapper {
    flex-wrap: wrap;
  }

  .card-header-icon {
    width: 40px;
    height: 40px;
  }

  .card-header-icon .el-icon {
    font-size: 20px;
  }

  .card-title {
    font-size: 16px;
  }

  .card-subtitle {
    font-size: 12px;
  }

  .limited-width-input {
    max-width: 100%;
  }

  .form-help {
    flex-wrap: wrap;
  }

  .feature-item {
    padding: 12px 0;
  }

  .danger-item {
    margin: 0 -12px;
    padding: 12px;
    border-radius: 0;
  }

  .danger-alert {
    font-size: 12px;
  }

  .emby-status-alert {
    border-radius: 8px;
  }
}

@media (max-width: 480px) {
  .media-server-settings {
    padding: 8px;
  }

  .card-header-icon {
    width: 36px;
    height: 36px;
  }

  .card-header-icon .el-icon {
    font-size: 18px;
  }

  .card-title {
    font-size: 15px;
  }

  .switch-wrapper {
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
  }

  .switch-label {
    font-size: 13px;
  }

  .feature-description {
    font-size: 12px;
  }
}
</style>
