<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, shallowRef, useTemplateRef } from 'vue'
import type { FormInstance, FormRules } from 'element-plus'
import { Check } from '@element-plus/icons-vue'
import {
  fetchFnosProxySettings,
  fnosProxyPublicMessage,
  saveFnosProxySettings,
  type FnosProxyConfig,
  type FnosProxyRuntime,
  type FnosProxySettings,
} from '@/api/fnosProxy'
import PageHeader from '@/components/common/PageHeader.vue'
import FnosPathMappings from '@/components/fnos-proxy/FnosPathMappings.vue'
import FnosProxyStatus from '@/components/fnos-proxy/FnosProxyStatus.vue'
import { useDeviceType } from '@/composables/useDeviceType'
import { useFeedbackScroll } from '@/composables/useFeedbackScroll'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'

const http = useHttpClient()
const { isMobile } = useDeviceType()
const formRef = useTemplateRef<FormInstance>('formRef')
const form = reactive<FnosProxyConfig>({
  enabled: false,
  fnos_url: '',
  path_mappings: [],
})
const runtime = shallowRef<FnosProxyRuntime | null>(null)
const savedEnabled = shallowRef(false)
const loaded = shallowRef(false)
const loading = shallowRef(false)
const saving = shallowRef(false)
const feedback = shallowRef<{
  title: string
  type: 'success' | 'warning' | 'error'
  description: string
} | null>(null)
const { feedbackRef, revealFeedback } = useFeedbackScroll(feedback)
const disabled = computed(() => !loaded.value || loading.value || saving.value)
let active = true
onBeforeUnmount(() => {
  active = false
})

const applySettings = (settings: FnosProxySettings) => {
  Object.assign(form, settings.config)
  savedEnabled.value = settings.config.enabled
  runtime.value = settings.runtime
}
const showFailure = (error: unknown, title: string) => {
  const failure = parseHttpError(error, { fallbackMessage: title })
  if (failure.shouldNotify) {
    feedback.value = {
      type: 'error',
      title,
      description: fnosProxyPublicMessage(failure.message),
    }
  }
}
const load = async () => {
  if (loading.value || saving.value) return
  loading.value = true
  feedback.value = null
  try {
    const result = await fetchFnosProxySettings(http)
    if (!active) return
    applySettings(result)
    loaded.value = true
  } catch (error) {
    if (active) showFailure(error, '读取设置失败')
  } finally {
    if (active) loading.value = false
  }
}

const rules: FormRules = {
  fnos_url: [
    {
      validator: (_rule, value: string, callback) => {
        const address = value.trim()
        if (!address && !form.enabled) return callback()
        // 保留后端允许的本机地址简写，不能让 URL 构造器悄悄修正错误输入。
        if (!/^https?:\/\/[^/?#\s@]+\/?$/i.test(address)) {
          return callback(new Error('请输入飞牛影视完整服务地址，如 http://192.168.1.10:8005'))
        }
        try {
          const parsed = new URL(
            address.replace(/^(https?:\/\/):/i, (_match, prefix: string) => `${prefix}127.0.0.1:`),
          )
          if (parsed.port === '0') throw new Error('port')
          callback()
        } catch {
          callback(new Error('请检查飞牛影视地址，端口须在 1–65535 之间'))
        }
      },
      trigger: 'blur',
    },
  ],
}

const save = async () => {
  if (disabled.value || !formRef.value) return
  saving.value = true
  try {
    await formRef.value.validate()
  } catch {
    saving.value = false
    return
  }
  feedback.value = null
  try {
    const result = await saveFnosProxySettings(http, {
      enabled: form.enabled,
      fnos_url: form.fnos_url.trim(),
      path_mappings: form.path_mappings.map((row) => ({ ...row })),
    })
    if (!active) return
    applySettings(result)
    feedback.value =
      result.runtime.last_error || result.runtime.restart_required
        ? {
            title: '设置未完全应用',
            type: 'warning',
            description: '设置已保存，请检查运行状态后重新保存',
          }
        : { title: '保存成功', type: 'success', description: '设置已保存并应用' }
  } catch (error) {
    if (active) showFailure(error, '保存失败')
  } finally {
    if (active) {
      saving.value = false
      void revealFeedback()
    }
  }
}

onMounted(load)
</script>

<template>
  <div class="main-content-container fnos-proxy-content">
    <PageHeader>
      <template #stats>
        <FnosProxyStatus :runtime="runtime" :saved-enabled="savedEnabled" />
      </template>
    </PageHeader>
    <div class="fnos-settings-wrapper">
      <div v-if="!loaded" class="load-state">
        <p>
          {{ loading ? '正在读取飞牛影视设置…' : '设置尚未读取，重试成功后才能编辑和保存。' }}
        </p>
        <el-button v-if="!loading" @click="load">重试加载</el-button>
      </div>

      <el-form
        class="fnos-settings-form"
        ref="formRef"
        :model="form"
        :rules="rules"
        :disabled="disabled"
        :label-position="isMobile ? 'top' : 'left'"
        :label-width="200"
        scroll-to-error
        @submit.prevent="save"
      >
        <el-card shadow="never">
          <template #header><h2 class="card-title">连接设置</h2></template>
          <el-form-item label="启用反代" prop="enabled">
            <el-switch v-model="form.enabled" aria-label="启用反代" />
            <p class="form-help">在飞牛影视网页和支持 Emby 连接方式的播放器中播放 STRM 媒体。</p>
          </el-form-item>
          <el-form-item label="飞牛影视地址" prop="fnos_url">
            <el-input
              v-model="form.fnos_url"
              aria-label="飞牛影视地址"
              placeholder="如 http://192.168.1.10:8005"
              clearable
            />
            <p class="form-help">填写完整服务地址，包含 http:// 或 https:// 和所需端口。</p>
          </el-form-item>
          <p class="form-help">
            想减少播放等待？可前往
            <router-link to="/settings/strm">STRM 设置</router-link>开启预加载。
          </p>
        </el-card>
        <el-card shadow="never">
          <FnosPathMappings v-model="form.path_mappings" :disabled="disabled" />
        </el-card>
        <div class="fnos-save-actions" :class="{ 'fnos-save-actions--mobile': isMobile }">
          <el-button
            type="success"
            size="large"
            :icon="Check"
            :loading="saving"
            :disabled="disabled"
            native-type="submit"
          >
            保存设置
          </el-button>
        </div>
        <el-alert
          v-if="feedback"
          ref="feedbackRef"
          class="settings-feedback"
          :title="feedback.title"
          :description="feedback.description"
          :type="feedback.type"
          closable
          show-icon
          role="status"
          @close="feedback = null"
        />
      </el-form>
    </div>
  </div>
</template>

<style scoped>
.fnos-settings-wrapper {
  width: 100%;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.fnos-settings-form {
  display: grid;
  min-width: 0;
  gap: 20px;
}
.fnos-settings-wrapper :deep(.el-card) {
  width: 100%;
  min-width: 0;
  margin: 0;
}
.fnos-save-actions {
  display: flex;
}
.fnos-save-actions--mobile :deep(.el-button) {
  width: 100%;
}
</style>
