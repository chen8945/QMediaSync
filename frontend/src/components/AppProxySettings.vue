<template>
  <!-- 网络代理设置部分 -->
  <div class="main-content-container proxy-section">
    <PageHeader />

    <el-form
      :model="proxyData"
      :label-position="checkIsMobile ? 'top' : 'left'"
      :label-width="120"
      class="proxy-form"
    >
      <el-form-item label="代理地址" prop="proxy_url">
        <el-input
          v-model="proxyData.proxy_url"
          @update:model-value="markProxyCredentialsEdited"
          :placeholder="PROXY_URL_PLACEHOLDER"
          :disabled="proxyLoading"
          clearable
        />
        <div class="form-help">{{ PROXY_URL_HELP }}</div>
        <div v-if="credentialsMasked" class="form-help proxy-credentials-masked">
          {{ PROXY_CREDENTIALS_MASKED_HINT }}
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
import {
  PROXY_CREDENTIALS_MASKED_HINT,
  PROXY_PORT_RANGE,
  PROXY_SCHEMES,
  PROXY_URL_HELP,
  PROXY_URL_MESSAGES,
  PROXY_URL_PLACEHOLDER,
} from '@/constants/validation'
import { useHttpClient } from '@/http/client'
import { Connection, Check } from '@element-plus/icons-vue'
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { isMobile } from '@/utils/deviceUtils'
import PageHeader from '@/components/common/PageHeader.vue'
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
// 已保存的凭据是否被后端脱敏，决定是否展示占位串说明
const credentialsMasked = ref(false)
// 初始脱敏值尚未编辑时可自动请求保留当前凭据；用户输入后，xxxxx 是可保存的普通凭据。
const proxyCredentialsEdited = ref(false)

const proxyData = reactive<ProxyData>({
  proxy_url: '',
})

// 后端 net/url 拒绝码位小于 0x20 或等于 0x7f 的字符，而 new URL 会把 \t、\n 静默删掉，
// 不先挡住的话，从换行终端粘贴的地址能过前端校验，却被服务端用通用错误拒绝，用户看不出哪里错了。
// 空格一并拒绝：代理地址里出现空格必定是误输入
const PROXY_CONTROL_CHAR_PATTERN = /[\u0000-\u0020\u007f]/
// 协议必须写成“协议://”：new URL('proxy.example.com:8080') 不抛错，会把 proxy.example.com 当协议，
// 直接用 new URL 会把漏写协议的输入误判成缺少主机名。
// 一次切出协议、主机和端口：userinfo 取到最后一个 @，主机兼容 [::1] 形式，端口单独捕获以区分格式错误和越界
const PROXY_URL_PATTERN =
  /^([A-Za-z][A-Za-z0-9+.-]*):\/\/(?:[^/?#]*@)?(\[[^\]]*\]|[^:/?#]*)(?::([^/?#]*))?(?:[/?#]|$)/
const PROXY_PORT_PATTERN = /^\d+$/

// 校验代理地址，返回错误提示；地址合法时返回 null
// 规则与后端 validation.ProxyURL 对齐，判断顺序也一致：先是地址本身能否解析，再是协议白名单，最后是端口范围。
// 省略主机名（如 http://:7890）是合法的本机代理简写，Go 解析后按本机出站，前后端都必须放行。
const validateProxyUrl = (raw: string): string | null => {
  if (PROXY_CONTROL_CHAR_PATTERN.test(raw)) {
    return PROXY_URL_MESSAGES.whitespace
  }
  const matched = PROXY_URL_PATTERN.exec(raw)
  if (!matched) {
    return PROXY_URL_MESSAGES.format
  }
  const [, scheme, host, port] = matched
  // 后端要求 Host 段非空，只有协议、连端口都没写的地址没有拨号目标
  if (!host && port === undefined) {
    return PROXY_URL_MESSAGES.format
  }
  // 端口非数字属于解析失败，与后端一样归入格式错误
  if (port && !PROXY_PORT_PATTERN.test(port)) {
    return PROXY_URL_MESSAGES.format
  }
  // 主机名语法交给 URL 解析判断，空主机名用占位地址代入，保持“省略主机名即本机”的语义
  try {
    new URL(`http://${host || '127.0.0.1'}`)
  } catch {
    return PROXY_URL_MESSAGES.format
  }
  if (!(PROXY_SCHEMES as readonly string[]).includes(scheme.toLowerCase())) {
    return PROXY_URL_MESSAGES.scheme
  }
  // URL 构造器会直接对越界端口抛错，端口必须自己判断，否则用户看到的是格式错误，会去改协议而不是改端口
  if (port) {
    const portNumber = Number(port)
    if (portNumber < PROXY_PORT_RANGE.min || portNumber > PROXY_PORT_RANGE.max) {
      return PROXY_URL_MESSAGES.port
    }
  }
  return null
}

const markProxyCredentialsEdited = (): void => {
  if (credentialsMasked.value) {
    proxyCredentialsEdited.value = true
    credentialsMasked.value = false
  }
}

const shouldPreserveProxyCredentials = (): boolean =>
  credentialsMasked.value && !proxyCredentialsEdited.value

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
      preserve_proxy_credentials: shouldPreserveProxyCredentials(),
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
      preserve_proxy_credentials: shouldPreserveProxyCredentials(),
    }

    const response = await http.post(`${SERVER_URL}/setting/http-proxy`, requestData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      // 重新拉取而不是直接回填 trimmedUrl：接口只回传脱敏地址，
      // 这样输入框和提示都不会继续留着刚输入的明文凭据，也能刷新脱敏标记
      await loadProxy()
      proxyStatus.value = {
        title: '代理设置已保存',
        type: 'success',
        description: proxyData.proxy_url
          ? `已设置代理服务器：${proxyData.proxy_url}`
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
      // 接口不回传明文凭据，凭据被脱敏时要提示用户输入框里的 xxxxx 是占位串
      credentialsMasked.value = response.data.data.credentials_masked === '1'
      proxyCredentialsEdited.value = false
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
