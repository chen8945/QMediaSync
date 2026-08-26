<script setup lang="ts">
import { reactive, shallowRef, useTemplateRef } from 'vue'
import { Key, Lock, User } from '@element-plus/icons-vue'
import type { FormInstance, FormRules } from 'element-plus'
import {
  createElementCredentialRule,
  createPasswordRule,
  createUsernameRule,
} from '@/utils/userCredentials'

export interface InitialAdminSubmitPayload {
  setup_token: string
  username: string
  password: string
}

defineProps<{
  loading: boolean
}>()

const emit = defineEmits<{
  submit: [payload: InitialAdminSubmitPayload]
}>()

const formRef = useTemplateRef<FormInstance>('formRef')
const validating = shallowRef(false)
const form = reactive({
  setupToken: '',
  username: 'admin',
  password: '',
  passwordConfirm: '',
})

const rules: FormRules = {
  setupToken: [{ required: true, message: '请输入初始化码', trigger: 'blur' }],
  username: [createElementCredentialRule(createUsernameRule('管理员用户名'))],
  password: [createElementCredentialRule(createPasswordRule('管理员密码'))],
  passwordConfirm: [
    { required: true, message: '请确认管理员密码', trigger: 'blur' },
    {
      validator: (_rule, value, callback) => {
        if (value !== form.password) {
          callback(new Error('两次输入的密码不一致'))
          return
        }
        callback()
      },
      trigger: 'blur',
    },
  ],
}

const handleSubmit = async () => {
  if (!formRef.value || validating.value) return
  validating.value = true
  try {
    const valid = await formRef.value.validate()
    if (!valid) return
    emit('submit', {
      setup_token: form.setupToken,
      username: form.username,
      password: form.password,
    })
  } finally {
    validating.value = false
  }
}
</script>

<template>
  <el-form
    ref="formRef"
    :model="form"
    :rules="rules"
    class="initial-admin-form"
    autocomplete="on"
    @submit.prevent="handleSubmit"
  >
    <p class="setup-hint">初始化码请查看启动日志</p>

    <el-form-item prop="setupToken">
      <el-input
        v-model="form.setupToken"
        size="large"
        name="setup-token"
        autocomplete="one-time-code"
        placeholder="初始化码"
        :prefix-icon="Key"
        :disabled="loading"
      />
    </el-form-item>

    <el-form-item prop="username">
      <el-input
        v-model="form.username"
        size="large"
        name="username"
        autocomplete="username"
        placeholder="管理员用户名"
        :prefix-icon="User"
        :disabled="loading"
      />
    </el-form-item>

    <el-form-item prop="password">
      <el-input
        v-model="form.password"
        type="password"
        size="large"
        name="new-password"
        autocomplete="new-password"
        placeholder="管理员密码"
        :prefix-icon="Lock"
        show-password
        :disabled="loading"
      />
    </el-form-item>

    <el-form-item prop="passwordConfirm">
      <el-input
        v-model="form.passwordConfirm"
        type="password"
        size="large"
        name="new-password-confirm"
        autocomplete="new-password"
        placeholder="确认管理员密码"
        :prefix-icon="Lock"
        show-password
        :disabled="loading"
      />
    </el-form-item>

    <el-form-item>
      <el-button
        type="primary"
        size="large"
        class="setup-button"
        native-type="submit"
        :loading="loading || validating"
      >
        {{ loading || validating ? '创建中…' : '创建管理员' }}
      </el-button>
    </el-form-item>
  </el-form>
</template>

<style scoped>
.initial-admin-form {
  width: 100%;
}

.setup-hint {
  margin: 0 0 18px;
  color: var(--el-text-color-regular);
  font-size: 14px;
  text-align: center;
}

.initial-admin-form .el-form-item {
  margin-bottom: 24px;
}

.setup-button {
  width: 100%;
  height: 44px;
  font-size: 16px;
  font-weight: 500;
}

@media (max-width: 768px) {
  .initial-admin-form .el-form-item {
    margin-bottom: 20px;
  }

  .setup-button {
    height: 40px;
    font-size: 15px;
  }
}

@media (max-width: 480px) {
  .setup-button {
    height: 42px;
  }
}
</style>
