<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import V115AppSelector from './V115AppSelector.vue'
import {
  buildV115CreatePayload,
  getDefaultV115ChangeSelection,
  type V115AccountAuthInfo,
  type V115CreatePayload,
  type V115CreateSelection,
} from './v115AuthSources'

interface V115AuthorizationChangeAccount extends V115AccountAuthInfo {
  id: number
  name: string
  user_id?: string
}

const visible = defineModel<boolean>('visible', { required: true })

const props = defineProps<{
  account: V115AuthorizationChangeAccount | null
}>()

const emit = defineEmits<{
  confirmed: [payload: V115CreatePayload]
}>()

const selection = reactive<V115CreateSelection>(
  getDefaultV115ChangeSelection({
    source_type: '115',
  }),
)
const riskConfirmed = ref(false)

const resetForm = () => {
  Object.assign(selection, getDefaultV115ChangeSelection(props.account ?? { source_type: '115' }))
  riskConfirmed.value = false
}

watch(
  () => [visible.value, props.account?.id] as const,
  ([isVisible]) => {
    if (isVisible) resetForm()
  },
)

const submit = () => {
  if (!props.account || !riskConfirmed.value) return

  const payload = buildV115CreatePayload(selection)
  if (payload.auth_source_type === 'custom_appid') {
    if (!payload.app_id.trim()) {
      ElMessage.warning('请填写自定义 APP ID')
      return
    }
    if (!payload.custom_app_name?.trim()) {
      ElMessage.warning('请填写自定义应用名')
      return
    }
  }

  emit('confirmed', payload)
  visible.value = false
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="更换 115 授权"
    width="min(560px, calc(100vw - 32px))"
    destroy-on-close
  >
    <template v-if="account">
      <el-alert type="warning" :closable="false">
        <template #title>账号关联会保留，但云盘用户可能发生变化</template>
        <div class="authorization-change-warning">
          <p>
            当前账号 ID：{{ account.id }}。更换成功后，STRM
            同步目录、刮削目录和任务历史仍关联此账号。
          </p>
          <p>如果新授权属于其他 115 用户，旧路径或文件 ID 可能不再对应，后续同步和刮削可能失败。</p>
        </div>
      </el-alert>

      <el-form class="authorization-change-form" label-position="top">
        <V115AppSelector
          v-model:auth-mode="selection.authMode"
          v-model:selected-qr-app="selection.selectedQrApp"
          v-model:selected-web-provider="selection.selectedWebProvider"
          v-model:custom-app-id="selection.customAppId"
          v-model:custom-app-name="selection.customAppName"
        />
        <el-checkbox v-model="riskConfirmed">
          我已确认保留本地账号关联，并了解不同 115 用户可能导致旧路径或文件 ID 失效
        </el-checkbox>
      </el-form>
    </template>

    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :disabled="!account || !riskConfirmed" @click="submit">
        确认更换授权
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.authorization-change-warning {
  display: grid;
  gap: 6px;
  line-height: 1.6;
}

.authorization-change-warning p {
  margin: 0;
}

.authorization-change-form {
  display: grid;
  gap: 14px;
  margin-top: 18px;
}

:deep(.el-checkbox) {
  align-items: flex-start;
  white-space: normal;
}

:deep(.el-checkbox__label) {
  line-height: 1.5;
  white-space: normal;
}
</style>
