<script setup lang="ts">
import V115AppIdSelect from './V115AppIdSelect.vue'
import V115WebAuthSelect from './V115WebAuthSelect.vue'
import type { V115AuthMode, V115AuthProvider, V115SelectedQrApp } from './v115AuthSources'

const authMode = defineModel<V115AuthMode>('authMode', { required: true })
const selectedQrApp = defineModel<V115SelectedQrApp>('selectedQrApp', { required: true })
const selectedWebProvider = defineModel<V115AuthProvider>('selectedWebProvider', {
  required: true,
})
const customAppId = defineModel<string>('customAppId', { required: true })
const customAppName = defineModel<string>('customAppName', { required: true })

const authModeOptions = [
  { label: '扫码授权', value: 'qr' },
  { label: '网页授权', value: 'oauth' },
]
</script>

<template>
  <div class="v115-app-selector">
    <el-form-item label="授权方式">
      <el-segmented v-model="authMode" :options="authModeOptions" class="v115-auth-mode" />
    </el-form-item>
    <V115AppIdSelect
      v-if="authMode === 'qr'"
      v-model:selected-qr-app="selectedQrApp"
      v-model:custom-app-id="customAppId"
      v-model:custom-app-name="customAppName"
    />
    <V115WebAuthSelect v-else v-model:selected-web-provider="selectedWebProvider" />
  </div>
</template>

<style scoped>
.v115-app-selector {
  width: 100%;
}

.v115-auth-mode {
  max-width: 100%;
}
</style>
