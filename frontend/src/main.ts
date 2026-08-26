import './assets/main.css'

import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { ElMessage } from 'element-plus'
import 'element-plus/es/components/message/style/css'
import 'element-plus/es/components/message-box/style/css'
import App from './App.vue'

import router from './router/index'
import { useAuthStore } from '@/stores/auth'
import { configureHttpClient, http, httpKey } from '@/http/client'

configureHttpClient({
  getAuthStore: useAuthStore,
  onAuthenticationInvalidated: async () => {
    ElMessage.error('登录已失效，请重新登录')
    await router.replace('/login')
  },
})

const pinia = createPinia()
const app = createApp(App)

app.use(pinia)

const bootstrap = async () => {
  const authStore = useAuthStore()
  await authStore.bootstrapAuth(http)

  app.use(router)
  app.provide(httpKey, http)

  await router.isReady()
  app.mount('#app')
}

void bootstrap()
