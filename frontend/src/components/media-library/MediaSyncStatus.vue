<script setup lang="ts">
import { computed } from 'vue'
import {
  Refresh,
  Timer,
  Clock,
  FolderOpened,
  Calendar,
  WarningFilled,
  Loading,
} from '@element-plus/icons-vue'
import type { MediaLibrarySyncInfo, MediaLibrarySyncMode } from '@/api/mediaLibrary'
import { formatMaybeUnixDateTime, formatRelativeTime } from '@/utils/timeUtils'

const props = defineProps<{
  productName: string
  syncInfo: MediaLibrarySyncInfo | null
  errorMessage?: string
  isSyncRunning: boolean
  starting: boolean
  disabled: boolean
}>()
const emit = defineEmits<{ start: [] }>()
const syncModeLabels = computed<Record<Exclude<MediaLibrarySyncMode, ''>, string>>(() => ({
  idle: '空闲',
  full: `全量同步 ${props.productName} 条目`,
  incremental: `增量同步 ${props.productName} 条目`,
  webhook: `Webhook 单条同步 ${props.productName} 条目`,
  refresh_library: `刷新 ${props.productName} 媒体库`,
}))
const currentSyncModeLabel = computed(
  () =>
    syncModeLabels.value[props.syncInfo?.sync_mode || 'idle'] ||
    (props.isSyncRunning ? '同步中' : '空闲'),
)
const lastSuccessSyncHelper = computed(() => {
  if (!props.syncInfo?.last_sync_time) return ''
  const mode = props.syncInfo.last_success_sync_mode
  return [
    formatSyncAbsoluteTime(props.syncInfo.last_sync_time),
    mode && mode !== 'idle' ? syncModeLabels.value[mode] : '',
  ]
    .filter(Boolean)
    .join(' · ')
})
const formatSyncRelativeTime = (timestamp: number | null | undefined) => {
  const formatted = formatRelativeTime(timestamp)
  return formatted === '-' ? '未同步' : formatted
}
const formatSyncAbsoluteTime = (timestamp: number | null | undefined) => {
  const formatted = formatMaybeUnixDateTime(timestamp)
  return formatted === '-' ? '' : formatted
}
</script>

<template>
  <el-card class="sync-management-card" shadow="hover">
    <template #header>
      <div class="card-header-wrapper">
        <div class="card-header-icon sync-icon">
          <el-icon :size="24"><Refresh /></el-icon>
        </div>
        <div class="card-header-content">
          <h3 class="card-title">同步管理</h3>
          <p class="card-subtitle">管理 {{ productName }} 条目同步到本地状态</p>
        </div>
        <div class="card-header-action">
          <el-button
            type="primary"
            @click="emit('start')"
            :loading="starting"
            :icon="Refresh"
            :disabled="disabled || isSyncRunning"
            size="default"
          >
            {{ isSyncRunning ? '同步进行中…' : '启动全量同步' }}
          </el-button>
        </div>
      </div>
    </template>

    <el-alert
      v-if="errorMessage"
      :title="`查询 ${productName} 同步状态失败`"
      :description="errorMessage"
      type="error"
      :closable="false"
      show-icon
      class="sync-status-error"
      role="alert"
    />

    <div v-if="syncInfo" class="sync-info-grid">
      <div class="sync-stat-card">
        <div class="stat-icon status-icon">
          <el-icon :size="28"><Refresh /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">当前状态</div>
          <div class="stat-value" :class="{ 'is-enabled': isSyncRunning }">
            {{ currentSyncModeLabel }}
          </div>
          <div v-if="syncInfo.started_at" class="stat-helper">
            开始：{{ formatSyncAbsoluteTime(syncInfo.started_at) }}
          </div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon auto-sync-icon">
          <el-icon :size="28"><Timer /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">自动同步</div>
          <div class="stat-value" :class="{ 'is-enabled': syncInfo.sync_enabled }">
            {{ syncInfo.sync_enabled ? '已启用' : '已禁用' }}
          </div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon cycle-icon">
          <el-icon :size="28"><Clock /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">同步周期</div>
          <div class="stat-value">{{ syncInfo.sync_cron }}</div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon items-icon">
          <el-icon :size="28"><FolderOpened /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">已同步 Item 数</div>
          <div class="stat-value highlight">{{ syncInfo.total_items || 0 }}</div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon time-icon">
          <el-icon :size="28"><Calendar /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">最近成功同步</div>
          <div class="stat-value">{{ formatSyncRelativeTime(syncInfo.last_sync_time) }}</div>
          <div class="stat-helper">{{ lastSuccessSyncHelper }}</div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon time-icon">
          <el-icon :size="28"><Calendar /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">最近增量同步</div>
          <div class="stat-value">
            {{ formatSyncRelativeTime(syncInfo.last_incremental_sync_at) }}
          </div>
          <div class="stat-helper">
            {{ formatSyncAbsoluteTime(syncInfo.last_incremental_sync_at) }}
          </div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon time-icon">
          <el-icon :size="28"><Calendar /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">最近全量同步</div>
          <div class="stat-value">{{ formatSyncRelativeTime(syncInfo.last_full_sync_at) }}</div>
          <div class="stat-helper">
            {{ formatSyncAbsoluteTime(syncInfo.last_full_sync_at) }}
          </div>
        </div>
      </div>

      <div class="sync-stat-card">
        <div class="stat-icon items-icon">
          <el-icon :size="28"><FolderOpened /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">最近处理数量</div>
          <div class="stat-value highlight">{{ syncInfo.last_processed_count || 0 }}</div>
        </div>
      </div>

      <div v-if="syncInfo.last_error" class="sync-stat-card error-card">
        <div class="stat-icon error-icon">
          <el-icon :size="28"><WarningFilled /></el-icon>
        </div>
        <div class="stat-content">
          <div class="stat-label">最近错误</div>
          <div class="stat-value error-text">上次同步失败，请查看服务日志</div>
        </div>
      </div>
    </div>

    <div v-if="isSyncRunning" class="sync-progress">
      <div class="progress-indicator">
        <el-icon class="sync-progress-loading-icon" :size="20"><Loading /></el-icon>
        <span>{{ currentSyncModeLabel }}进行中，请稍候…</span>
      </div>
    </div>

    <div v-if="!syncInfo" class="sync-empty">
      <el-empty description="暂无同步数据，请点击上方按钮启动同步" :image-size="80" />
    </div>
  </el-card>
</template>

<style scoped>
.sync-management-card {
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

.sync-management-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.sync-management-card :deep(.el-card__header) {
  padding: 18px 20px;
  border-bottom: 1px solid var(--el-border-color);
}

.sync-management-card :deep(.el-card__body) {
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

.sync-icon {
  background: linear-gradient(135deg, #43e97b 0%, #38f9d7 100%);
}

.card-header-content {
  flex: 1;
}

.card-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.card-header-action {
  margin-left: auto;
}

.sync-management-card {
  margin-bottom: 20px;
  width: 100%;
}

.sync-info-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 20px;
}

.sync-status-error {
  margin-bottom: 20px;
}

.sync-stat-card {
  display: flex;
  align-items: center;
  width: 100%;
  gap: 16px;
  padding: 20px;
  background: linear-gradient(135deg, #f8f9fa 0%, #ffffff 100%);
  border-radius: 12px;
  border: 1px solid var(--el-border-color-lighter);
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
}

.sync-stat-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}

.stat-icon {
  width: 56px;
  height: 56px;
  border-radius: 12px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
}

.auto-sync-icon {
  background: var(--qms-gradient-brand);
}

.cycle-icon {
  background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
}

.items-icon {
  background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
}

.time-icon {
  background: linear-gradient(135deg, #fa709a 0%, #fee140 100%);
}

.status-icon {
  background: linear-gradient(135deg, #22c55e 0%, #14b8a6 100%);
}

.error-icon {
  background: linear-gradient(135deg, #ef4444 0%, #f97316 100%);
}

.stat-content {
  flex: 1;
  min-width: 0;
}

.stat-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.stat-value {
  font-size: 18px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  overflow-wrap: anywhere;
}

.stat-value.is-enabled {
  color: var(--el-color-success);
}

.stat-value.highlight {
  color: var(--el-color-primary);
}

.stat-helper {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.4;
  overflow-wrap: anywhere;
}

.error-card {
  border-color: #fde2e2;
  background: linear-gradient(135deg, #fff5f5 0%, #ffffff 100%);
}

.error-text {
  color: var(--el-color-danger);
  font-size: 14px;
}

.sync-progress {
  margin-top: 20px;
  padding: 16px;
  background: linear-gradient(135deg, var(--el-color-primary-light-9) 0%, #f0f9ff 100%);
  border-radius: 8px;
  border: 1px solid #d9ecff;
}

.progress-indicator {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--el-color-primary);
  font-weight: 500;
}

.sync-progress-loading-icon {
  animation: rotating 2s linear infinite;
}

@keyframes rotating {
  0% {
    transform: rotate(0deg);
  }

  100% {
    transform: rotate(360deg);
  }
}

.sync-empty {
  padding: 20px;
}

@media (max-width: 1200px) {
  .sync-info-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (max-width: 768px) {
  .sync-management-card {
    border-radius: 8px;
  }

  .sync-management-card:hover {
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

  .card-header-action {
    width: 100%;
    margin-top: 12px;
  }

  .card-header-action .el-button {
    width: 100%;
  }

  .sync-management-card {
    border-radius: 8px;
  }

  .sync-info-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .sync-stat-card {
    padding: 14px;
    gap: 12px;
  }

  .sync-stat-card:hover {
    transform: none;
  }

  .stat-icon {
    width: 44px;
    height: 44px;
  }

  .stat-icon .el-icon {
    font-size: 22px;
  }

  .stat-label {
    font-size: 12px;
  }

  .stat-value {
    font-size: 15px;
  }

  .sync-progress {
    padding: 12px;
  }
}

@media (max-width: 480px) {
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

  .sync-stat-card {
    padding: 12px;
  }

  .stat-icon {
    width: 40px;
    height: 40px;
  }

  .stat-icon .el-icon {
    font-size: 20px;
  }

  .stat-value {
    font-size: 14px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .sync-progress-loading-icon {
    animation: none;
  }
}
</style>
