<script setup lang="ts">
import { Close, Connection, Delete, Download, View } from '@element-plus/icons-vue'

withDefaults(
  defineProps<{
    connected: boolean
    showRealtimeControls?: boolean
    connectDisabled?: boolean
    disconnectDisabled?: boolean
    clearDisabled?: boolean
    downloadDisabled?: boolean
    compact?: boolean
  }>(),
  {
    showRealtimeControls: true,
    connectDisabled: false,
    disconnectDisabled: false,
    clearDisabled: false,
    downloadDisabled: false,
    compact: false,
  },
)

const emit = defineEmits<{
  connect: []
  disconnect: []
  clear: []
  view: []
  download: []
}>()
</script>

<template>
  <div class="log-action-toolbar" :class="{ 'is-compact': compact }">
    <template v-if="showRealtimeControls">
      <el-tooltip content="连接实时日志" placement="top">
        <el-button
          type="primary"
          :icon="Connection"
          aria-label="连接实时日志"
          size="small"
          :disabled="connected || connectDisabled"
          @click="emit('connect')"
        >
          <span class="button-text">连接</span>
        </el-button>
      </el-tooltip>
      <el-tooltip content="断开实时日志" placement="top">
        <el-button
          type="danger"
          :icon="Close"
          aria-label="断开实时日志"
          size="small"
          :disabled="!connected || disconnectDisabled"
          @click="emit('disconnect')"
        >
          <span class="button-text">断开</span>
        </el-button>
      </el-tooltip>
    </template>
    <el-tooltip content="清空当前显示日志" placement="top">
      <el-button
        type="info"
        :icon="Delete"
        aria-label="清空当前显示日志"
        size="small"
        :disabled="clearDisabled"
        @click="emit('clear')"
      >
        <span class="button-text">清空</span>
      </el-button>
    </el-tooltip>
    <el-dropdown
      split-button
      type="success"
      size="small"
      trigger="click"
      placement="bottom-end"
      :disabled="downloadDisabled"
      :button-props="{ 'aria-label': '查看日志' }"
      @click="emit('view')"
      @command="emit('download')"
    >
      <el-icon><View /></el-icon>
      <span class="button-text">查看日志</span>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item command="download" :icon="Download" aria-label="下载日志文件">
            下载日志
          </el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
  </div>
</template>

<style scoped>
.log-action-toolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: wrap;
}

.log-action-toolbar :deep(.el-button + .el-button) {
  margin-left: 0;
}

@media (max-width: 768px) {
  .log-action-toolbar {
    justify-content: stretch;
  }

  .log-action-toolbar :deep(.el-button) {
    flex: 1 1 calc(50% - 4px);
  }

  .log-action-toolbar :deep(.el-dropdown) {
    flex: 1 1 calc(50% - 4px);
  }

  .log-action-toolbar :deep(.el-button-group) {
    display: flex;
    width: 100%;
  }

  .log-action-toolbar :deep(.el-dropdown__caret-button) {
    flex: 0 0 auto;
  }

  .log-action-toolbar.is-compact :deep(.el-button) {
    flex: 1 1 auto;
    min-width: 42px;
  }

  .log-action-toolbar.is-compact .button-text {
    display: none;
  }
}
</style>
