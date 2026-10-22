<script setup lang="ts">
import { computed } from 'vue'
import type { FnosProxyRuntime } from '@/api/fnosProxy'

const props = defineProps<{
  runtime: FnosProxyRuntime | null
  savedEnabled: boolean
}>()
const status = computed(() => {
  if (!props.runtime) return { label: '尚未读取', type: 'info' as const }
  if (props.runtime.last_error)
    return props.runtime.running
      ? { label: '运行异常', type: 'warning' as const }
      : { label: '未能启用', type: 'danger' as const }
  if (props.runtime.running) return { label: '运行中', type: 'success' as const }
  if (props.savedEnabled) return { label: '未运行', type: 'warning' as const }
  return { label: '未启用', type: 'info' as const }
})
</script>

<template>
  <section class="fnos-status" aria-label="飞牛影视运行状态">
    <div class="runtime-summary" role="status" aria-live="polite" aria-atomic="true">
      <span class="runtime-label">运行状态</span>
      <el-tag class="runtime-status" :type="status.type" effect="light" round>
        <span class="runtime-dot" aria-hidden="true" />
        {{ status.label }}
      </el-tag>
    </div>
    <el-alert
      v-if="runtime?.last_error"
      class="runtime-error"
      :title="runtime.running ? '部分服务未能启用' : '未能启用飞牛影视'"
      :description="runtime.last_error"
      :type="runtime.running ? 'warning' : 'error'"
      :closable="false"
      show-icon
    />
    <el-alert
      v-if="runtime?.restart_required && !runtime.last_error"
      class="restart-notice"
      title="设置尚未完全应用"
      description="请检查运行提示，处理后重新保存设置。"
      type="warning"
      :closable="false"
      show-icon
    />
  </section>
</template>

<style scoped>
.fnos-status {
  display: grid;
  width: 100%;
  min-width: 0;
  gap: 12px;
}
.runtime-summary {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 28px;
}
.runtime-label {
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.runtime-status {
  border-color: transparent;
  font-weight: 500;
}
.runtime-status :deep(.el-tag__content) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.runtime-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.runtime-error,
.restart-notice {
  overflow-wrap: anywhere;
}
</style>
