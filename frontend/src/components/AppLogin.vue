<script setup lang="ts">
import { computed, onMounted, shallowRef } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import LoginForm, { type LoginSubmitPayload } from '@/components/auth/LoginForm.vue'
import InitialAdminSetupForm, {
  type InitialAdminSubmitPayload,
} from '@/components/auth/InitialAdminSetupForm.vue'
import { createInitialAdmin, fetchSetupStatus } from '@/composables/useInitialAdminSetup'

const router = useRouter()
const authStore = useAuthStore()
const http = useHttpClient()
const loading = shallowRef(false)
const setupRequired = shallowRef(false)
const setupStatusLoaded = shallowRef(false)

const subtitle = computed(() => (setupRequired.value ? '创建管理员' : '系统登录'))

const getErrorMessage = (error: unknown, fallback: string) => {
  if (error instanceof Error && error.message) {
    return error.message
  }
  if (error && typeof error === 'object' && 'response' in error) {
    const response = (error as { response?: { data?: { message?: string } } }).response
    if (response?.data?.message) {
      return response.data.message
    }
  }
  return fallback
}

const loadSetupStatus = async () => {
  if (!http) {
    setupStatusLoaded.value = true
    return
  }
  try {
    const status = await fetchSetupStatus(http)
    setupRequired.value = status.required
  } catch (error) {
    console.error('查询初始化状态失败：', error)
    setupRequired.value = false
  } finally {
    setupStatusLoaded.value = true
  }
}

const handleCreateInitialAdmin = async (payload: InitialAdminSubmitPayload) => {
  if (loading.value || !http) return
  loading.value = true
  try {
    await createInitialAdmin(http, payload)
    ElMessage.success('管理员创建成功，请登录')
    setupRequired.value = false
  } catch (error: unknown) {
    console.error('创建管理员失败：', error)
    ElMessage.error(getErrorMessage(error, '创建管理员失败，请检查网络连接'))
  } finally {
    loading.value = false
  }
}

const handleLogin = async (payload: LoginSubmitPayload) => {
  if (loading.value || !http) return

  try {
    loading.value = true
    // 使用 JSON 格式发送请求，以支持 rememberMe 参数
    const response = await http.post(
      `${SERVER_URL}/login`,
      {
        username: payload.username,
        password: payload.password,
        totp_code: payload.totp_code,
        rememberMe: payload.rememberMe,
      },
      {
        headers: {
          'Content-Type': 'application/json',
        },
        skipAuthInvalidation: true,
      },
    )

    if (response?.data.code === 200) {
      const sessionResult = await authStore.refreshSession(http)
      if (sessionResult.state === 'anonymous') {
        ElMessage.error(
          '登录会话未能建立，请允许本站 Cookie 后重试；若问题持续，请清除本站点数据或停用拦截扩展',
        )
        return
      }
      if (sessionResult.state === 'unavailable') {
        ElMessage.error('登录会话验证失败，请检查网络连接或稍后重试')
        return
      }

      ElMessage.success('登录成功')

      // 跳转到首页或原本要访问的页面
      const redirect = router.currentRoute.value.query.redirect as string
      router.replace(redirect || '/')
    } else {
      ElMessage.error(response?.data.message || '登录失败')
    }
  } catch (error: unknown) {
    console.error('登录错误：', error)
    ElMessage.error(getErrorMessage(error, '登录失败，请检查网络连接'))
  } finally {
    loading.value = false
  }
}

// 检查是否已经登录
onMounted(() => {
  if (authStore.isAuthenticated) {
    router.replace('/')
    return
  }
  void loadSetupStatus()
})
</script>

<template>
  <div class="login-container">
    <div class="login-box">
      <div class="login-header">
        <h1 class="login-title">QMediaSync</h1>
        <p class="login-subtitle">{{ subtitle }}</p>
      </div>

      <InitialAdminSetupForm
        v-if="setupStatusLoaded && setupRequired"
        :loading="loading"
        @submit="handleCreateInitialAdmin"
      />
      <LoginForm v-else-if="setupStatusLoaded" :loading="loading" @submit="handleLogin" />
    </div>
  </div>
</template>

<style scoped>
.login-container {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--qms-gradient-brand);
  padding: 20px;
}

.login-box {
  width: 100%;
  max-width: 500px;
  background: white;
  border-radius: 12px;
  padding: 40px 30px;
  box-shadow: 0 15px 35px rgba(0, 0, 0, 0.1);
}

.login-header {
  text-align: center;
  margin-bottom: 30px;
}

.login-title {
  font-size: 28px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin: 0 0 8px 0;
}

.login-subtitle {
  font-size: 16px;
  color: var(--el-text-color-secondary);
  margin: 0;
}

/* 移动端适配 */
@media (max-width: 768px) {
  .login-container {
    padding: 15px;
  }

  .login-box {
    max-width: 100%;
    padding: 30px 20px;
    border-radius: 8px;
  }

  .login-title {
    font-size: 24px;
  }

  .login-subtitle {
    font-size: 14px;
  }
}

@media (max-width: 480px) {
  .login-container {
    padding: 10px;
  }

  .login-box {
    max-width: 100%;
    padding: 25px 15px;
  }

  .login-title {
    font-size: 22px;
  }
}
</style>
