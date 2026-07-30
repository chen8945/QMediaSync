<template>
  <!-- 网络代理设置部分 -->
  <div class="main-content-container proxy-section">
    <el-form
      :model="proxyData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="120"
      class="proxy-form"
    >
      <el-form-item label="代理地址" prop="proxy_url">
        <el-input
          v-model="proxyData.proxy_url"
          placeholder="例如：http://127.0.0.1:7890 或 socks5://127.0.0.1:1080"
          :disabled="proxyLoading"
          clearable
        />
        <div class="form-help">
          支持 HTTP 和 SOCKS5 代理，格式：协议://[用户名:密码@]主机:端口，可用协议为
          http、https、socks5 和 socks5h，留空表示不使用代理
        </div>
      </el-form-item>
      <el-form-item>
        <div class="form-actions">
          <div>
            <el-button
              type="primary"
              size="large"
              :icon="Connection"
              @click="testProxy"
              :loading="testingProxy"
              :disabled="proxyLoading"
            >
              测试
            </el-button>
          </div>
          <div>
            <el-button
              type="success"
              size="large"
              :icon="Check"
              @click="saveProxy"
              :loading="proxyLoading"
              :disabled="testingProxy"
            >
              保存
            </el-button>
          </div>
        </div>
      </el-form-item>
    </el-form>

    <!-- 代理状态显示 -->
    <el-alert
      v-if="proxyStatus"
      :title="proxyStatus.title"
      :type="proxyStatus.type"
      :description="proxyStatus.description"
      :closable="false"
      show-icon
      class="proxy-status"
    />
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import { Connection, Check } from '@element-plus/icons-vue'
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { isMobile } from '@/utils/deviceUtils'
const checkIsMobile = ref(isMobile())
interface ProxyData {
  proxy_url: string
}

interface ProxyStatus {
  title: string
  type: 'success' | 'warning' | 'error' | 'info'
  description: string
}

const http = useHttpClient()

// 代理相关状态
const proxyLoading = ref(false)
const testingProxy = ref(false)
const proxyStatus = ref<ProxyStatus | null>(null)

const proxyData = reactive<ProxyData>({
  proxy_url: '',
})

// 与后端 validation.ProxyURL 保持一致的协议白名单
const PROXY_SCHEMES = ['http:', 'https:', 'socks5:', 'socks5h:']

// 校验代理地址，返回错误提示；地址合法时返回 null
// 规则与后端 validation.ProxyURL 对齐：协议白名单、必须有主机名、端口落在 1-65535
const validateProxyUrl = (raw: string): string | null => {
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return '代理地址格式无效，请填写“协议://主机:端口”'
  }
  if (!parsed.hostname) {
    return '代理地址缺少主机名'
  }
  if (!PROXY_SCHEMES.includes(parsed.protocol)) {
    return '只支持 http、https、socks5 或 socks5h 协议'
  }
  // URL 已挡掉非数字和大于 65535 的端口，这里补上 0 的情况
  if (parsed.port !== '' && Number(parsed.port) < 1) {
    return '端口必须在 1-65535 之间'
  }
  return null
}

// 测试代理连接
const testProxy = async () => {
  const trimmedUrl = proxyData.proxy_url.trim()
  if (!trimmedUrl) {
    ElMessage.warning('请输入代理服务器地址')
    return
  }

  const validationError = validateProxyUrl(trimmedUrl)
  if (validationError) {
    ElMessage.error(validationError)
    return
  }

  try {
    testingProxy.value = true
    proxyStatus.value = null

    // 与校验用的是同一份规范化结果，否则带首尾空白的地址会通过校验但在后端解析失败
    const requestData = {
      http_proxy: trimmedUrl,
    }

    const response = await http.post(`${SERVER_URL}/setting/test-http-proxy`, requestData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      proxyStatus.value = {
        title: '代理测试成功',
        type: 'success',
        description: '代理服务器连接正常，可以正常使用',
      }
    } else {
      proxyStatus.value = {
        title: '代理测试失败',
        type: 'error',
        description: response?.data.message || '无法连接到代理服务器，请检查配置',
      }
    }
  } catch (error) {
    console.error('代理测试错误：', error)
    proxyStatus.value = {
      title: '代理测试出错',
      type: 'error',
      description: '测试过程中发生错误，请检查网络连接和代理设置',
    }
  } finally {
    testingProxy.value = false
  }
}

// 保存代理设置
const saveProxy = async () => {
  const trimmedUrl = proxyData.proxy_url.trim()
  // 留空表示清除代理，不做协议校验
  if (trimmedUrl) {
    const validationError = validateProxyUrl(trimmedUrl)
    if (validationError) {
      ElMessage.error(validationError)
      return
    }
  }

  try {
    proxyLoading.value = true
    proxyStatus.value = null

    const requestData = {
      http_proxy: trimmedUrl,
    }

    const response = await http.post(`${SERVER_URL}/setting/http-proxy`, requestData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      // 已保存的是去除首尾空白后的地址，输入框同步为实际生效值
      proxyData.proxy_url = trimmedUrl
      proxyStatus.value = {
        title: '代理设置已保存',
        type: 'success',
        description: trimmedUrl
          ? `已设置代理服务器：${trimmedUrl}`
          : '已清除代理设置，使用直连网络',
      }
    } else {
      proxyStatus.value = {
        title: '保存代理设置失败',
        type: 'error',
        description: response?.data.message || '保存设置失败，请重试',
      }
    }
  } catch (error) {
    console.error('保存代理设置错误：', error)
    proxyStatus.value = {
      title: '保存设置出错',
      type: 'error',
      description: '保存过程中发生错误，请检查网络连接',
    }
  } finally {
    proxyLoading.value = false
  }
}

// 加载代理设置
const loadProxy = async () => {
  try {
    const response = await http.get(`${SERVER_URL}/setting/http-proxy`)

    if (response?.data.code === 200 && response.data.data) {
      proxyData.proxy_url = response.data.data.http_proxy || ''
    }
  } catch (error) {
    console.error('加载代理设置错误：', error)
  }
}

onMounted(() => {
  loadProxy()
})
</script>

<style scoped>
.proxy-settings-container {
  width: 100%;
  max-width: none;
  display: flex;
  flex-direction: column;
  gap: 20px;
  padding: 0;
}

.proxy-settings-card {
  width: 100%;
  max-width: none;
  margin: 0;
}

.card-title {
  margin: 0 0 8px 0;
  font-size: 24px;
  font-weight: 600;
  color: #303133;
}

.card-subtitle {
  margin: 0;
  font-size: 14px;
  color: #909399;
}

.proxy-content {
  margin-top: 20px;
}

.proxy-section {
  margin-bottom: 24px;
  padding: 0 10px 10px 10px;
}

.section-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 12px 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.section-description {
  margin: 0 0 16px 0;
  font-size: 14px;
  color: #606266;
  line-height: 1.5;
}

.proxy-form {
  margin-top: 16px;
}

.proxy-form .el-form-item {
  margin-bottom: 20px;
}

.form-help {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
  line-height: 1.4;
}

.proxy-status {
  margin-top: 16px;
}
</style>
