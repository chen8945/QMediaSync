<script setup lang="ts">
import { onMounted, reactive, shallowRef } from 'vue'
import { ElMessage } from 'element-plus'
import { Key, CircleCheck, Lock } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { useHttpClient } from '@/http/client'
import {
  fetchTwoFactorStatus,
  setupTwoFactor as requestTwoFactorSetup,
  enableTwoFactor as requestTwoFactorEnable,
  disableTwoFactor as requestTwoFactorDisable,
  userSettingsPublicMessages,
} from '@/api/userSettings'
import { notifyHttpError } from '@/utils/httpErrorNotification'

interface TwoFactorStatus {
  enabled: boolean
}

interface TwoFactorSetupData {
  secret: string
  otpauth_url: string
}

const http = useHttpClient()

const twoFactorStatus = reactive<TwoFactorStatus>({
  enabled: false,
})
const twoFactorSetup = reactive<TwoFactorSetupData>({
  secret: '',
  otpauth_url: '',
})
const twoFactorEnableCode = shallowRef('')
const twoFactorDisableForm = reactive({
  password: '',
  totpCode: '',
})
const twoFactorLoading = shallowRef(false)
const twoFactorQrCode = shallowRef('')

const reportError = (error: unknown, fallbackMessage: string) => {
  notifyHttpError(error, fallbackMessage, {
    publicMessages: userSettingsPublicMessages,
    fallbackMessage,
  })
}

const loadTwoFactorStatus = async () => {
  try {
    const data = await fetchTwoFactorStatus(http)
    twoFactorStatus.enabled = data.enabled
  } catch (error) {
    reportError(error, '加载两步验证状态失败')
  }
}

const setupTwoFactor = async () => {
  twoFactorLoading.value = true
  try {
    const data = await requestTwoFactorSetup(http)
    const qrCode = await QRCode.toDataURL(data.otpauth_url)
    twoFactorSetup.secret = data.secret
    twoFactorSetup.otpauth_url = data.otpauth_url
    twoFactorQrCode.value = qrCode
  } catch (error) {
    reportError(error, '生成两步验证密钥失败')
  } finally {
    twoFactorLoading.value = false
  }
}

const enableTwoFactor = async () => {
  if (!twoFactorEnableCode.value) {
    ElMessage.error('请输入动态验证码')
    return
  }
  try {
    await requestTwoFactorEnable(http, twoFactorEnableCode.value)
    ElMessage.success('两步验证已启用')
    twoFactorEnableCode.value = ''
    twoFactorSetup.secret = ''
    twoFactorSetup.otpauth_url = ''
    twoFactorQrCode.value = ''
    await loadTwoFactorStatus()
  } catch (error) {
    reportError(error, '启用两步验证失败')
  }
}

const disableTwoFactor = async () => {
  if (!twoFactorDisableForm.password || !twoFactorDisableForm.totpCode) {
    ElMessage.error('请输入当前密码和当前动态验证码')
    return
  }
  try {
    await requestTwoFactorDisable(http, {
      password: twoFactorDisableForm.password,
      totp_code: twoFactorDisableForm.totpCode,
    })
    ElMessage.success('两步验证已关闭')
    twoFactorDisableForm.password = ''
    twoFactorDisableForm.totpCode = ''
    await loadTwoFactorStatus()
  } catch (error) {
    reportError(error, '关闭两步验证失败')
  }
}

onMounted(() => {
  loadTwoFactorStatus()
})
</script>

<template>
  <section class="two-factor-section">
    <h3 class="two-factor-title">两步验证</h3>
    <el-alert
      :title="twoFactorStatus.enabled ? '已启用' : '未启用'"
      :type="twoFactorStatus.enabled ? 'success' : 'info'"
      :closable="false"
      class="two-factor-status"
    />

    <template v-if="!twoFactorStatus.enabled">
      <el-button
        type="primary"
        size="large"
        :icon="Key"
        :loading="twoFactorLoading"
        class="two-factor-setup-button"
        @click="setupTwoFactor"
      >
        生成配置
      </el-button>
      <div v-if="twoFactorQrCode" class="two-factor-setup">
        <img :src="twoFactorQrCode" alt="TOTP QR Code" class="two-factor-qr" />
        <el-input v-model="twoFactorSetup.secret" readonly />
        <el-input
          v-model="twoFactorEnableCode"
          placeholder="输入动态验证码确认启用"
          maxlength="6"
          inputmode="numeric"
        />
        <el-button type="success" size="large" :icon="CircleCheck" @click="enableTwoFactor">
          启用两步验证
        </el-button>
      </div>
    </template>

    <template v-else>
      <div class="two-factor-disable">
        <el-input
          v-model="twoFactorDisableForm.password"
          type="password"
          show-password
          placeholder="当前密码"
        />
        <el-input
          v-model="twoFactorDisableForm.totpCode"
          placeholder="当前动态验证码"
          maxlength="6"
          inputmode="numeric"
        />
        <el-button type="danger" size="large" :icon="Lock" @click="disableTwoFactor">
          关闭两步验证
        </el-button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.two-factor-section {
  width: 100%;
  display: grid;
  gap: 12px;
}

.two-factor-title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.two-factor-status {
  width: fit-content;
  max-width: 100%;
  margin-bottom: 4px;
}

.two-factor-setup-button {
  justify-self: start;
}

.two-factor-setup,
.two-factor-disable {
  display: grid;
  gap: 12px;
  width: 100%;
  max-width: 360px;
}

.two-factor-qr {
  width: 180px;
  height: 180px;
}
</style>
