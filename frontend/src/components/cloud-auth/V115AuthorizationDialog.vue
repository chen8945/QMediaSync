<script setup lang="ts">
import { useV115DeviceAuthorization } from '@/composables/useV115DeviceAuthorization'
import V115QrCode from './V115QrCode.vue'
import { Refresh } from '@element-plus/icons-vue'
import { computed, watch } from 'vue'
import { useHttpClient } from '@/http/client'

const visible = defineModel<boolean>('visible', { required: true })

const props = defineProps<{
  accountId: number | null
  accountName: string
  authorizationId?: string | null
}>()

const emit = defineEmits<{
  confirmed: []
}>()

const http = useHttpClient()
const {
  qrCode,
  status,
  tip,
  loading,
  isPolling,
  startAuthorization,
  stopPolling,
  resetAuthorization,
} = useV115DeviceAuthorization(http)

const statusType = computed(() => {
  if (status.value === 'confirmed') return 'success' as const
  if (status.value === 'expired' || status.value === 'failed') return 'danger' as const
  if (status.value === 'scanned') return 'warning' as const
  if (status.value === 'waiting') return 'primary' as const
  return 'info' as const
})

watch(
  () => visible.value,
  (isVisible) => {
    if (isVisible && props.accountId) {
      void startAuthorization(props.accountId, props.authorizationId ?? undefined)
      return
    }
    stopPolling()
  },
  { immediate: true },
)

watch(status, (value) => {
  if (value === 'confirmed') {
    visible.value = false
    emit('confirmed')
  }
})

const refreshQrCode = () => {
  if (props.accountId) {
    void startAuthorization(props.accountId, props.authorizationId ?? undefined)
  }
}

const handleClosed = () => {
  resetAuthorization()
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="115 二维码授权"
    width="360px"
    destroy-on-close
    @closed="handleClosed"
  >
    <div class="v115-auth-dialog">
      <div class="v115-auth-dialog__name">{{ accountName }}</div>
      <V115QrCode v-if="qrCode" :content="qrCode.qrcode" />
      <el-skeleton v-else class="v115-auth-dialog__skeleton" animated>
        <template #template>
          <el-skeleton-item class="v115-auth-dialog__skeleton-item" variant="text" />
          <el-skeleton-item class="v115-auth-dialog__skeleton-item" variant="text" />
          <el-skeleton-item class="v115-auth-dialog__skeleton-item" variant="text" />
        </template>
      </el-skeleton>
      <el-tag
        :type="statusType"
        class="v115-auth-dialog__status"
        :class="{ 'v115-auth-dialog__status--failed': status === 'failed' }"
      >
        {{ tip || '等待授权' }}
      </el-tag>
      <el-button
        :icon="Refresh"
        :loading="loading"
        :disabled="isPolling && status !== 'expired'"
        @click="refreshQrCode"
      >
        刷新二维码
      </el-button>
    </div>
  </el-dialog>
</template>

<style scoped>
.v115-auth-dialog {
  display: grid;
  justify-items: center;
  gap: 12px;
  min-width: 0;
}

.v115-auth-dialog__name {
  max-width: 100%;
  overflow-wrap: anywhere;
  color: #606266;
}

.v115-auth-dialog__skeleton {
  display: grid;
  width: 100%;
  gap: 12px;
}

.v115-auth-dialog__skeleton-item {
  width: 100%;
  height: 15px;
}

.v115-auth-dialog__status {
  width: 100%;
  max-width: 100%;
  min-width: 0;
  height: auto;
  max-height: 96px;
  box-sizing: border-box;
  align-items: center;
  justify-content: center;
  padding: 6px 9px;
  overflow-y: auto;
  overscroll-behavior: contain;
  overflow-wrap: anywhere;
  word-break: break-word;
  white-space: normal;
  text-align: center;
  line-height: 1.5;
}

.v115-auth-dialog__status--failed {
  align-items: flex-start;
  justify-content: flex-start;
  text-align: left;
}
</style>
