<script setup lang="ts">
import { webAuthProviders, type V115AuthProvider } from './v115AuthSources'

const selectedWebProvider = defineModel<V115AuthProvider>('selectedWebProvider', {
  required: true,
})
</script>

<template>
  <el-form-item label="授权服务">
    <el-select v-model="selectedWebProvider" class="v115-web-auth-select">
      <el-option
        v-for="provider in webAuthProviders"
        :key="provider.provider"
        :label="provider.label"
        :value="provider.provider"
        :disabled="provider.disabled"
      />
    </el-select>
  </el-form-item>
</template>

<style scoped>
.v115-web-auth-select {
  width: 100%;
}
</style>
