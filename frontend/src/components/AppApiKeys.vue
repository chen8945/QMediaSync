<template>
  <div class="main-content-container api-keys-container">
    <PageHeader>
      <template #actions>
        <div class="action-bar">
          <el-button type="primary" :icon="Plus" @click="openCreateDialog">
            生成 API Key
          </el-button>
          <el-button :icon="Refresh" @click="loadKeys()" :loading="loading"> 刷新 </el-button>
        </div>
      </template>
    </PageHeader>

    <el-alert type="info" :closable="false" show-icon>
      <template #title>
        API Key 认证已启用，推荐在请求头中使用 X-API-Key，也可以在 URL 中追加 api_key 参数。
      </template>
      <p class="alert-tip">完整密钥仅在创建时显示一次，请妥善保存。</p>
    </el-alert>

    <el-table
      :data="apiKeys"
      v-loading="loading"
      border
      stripe
      style="width: 100%"
      empty-text="暂无 API Key"
    >
      <el-table-column prop="name" label="名称" min-width="80" />
      <el-table-column prop="key_prefix" label="API Key 前缀" width="160">
        <template #default="{ row }">
          <el-tag type="info">{{ row.key_prefix }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="is_active" label="状态" width="200">
        <template #default="{ row }">
          <el-switch
            :model-value="row.is_active"
            :loading="row._updating"
            active-text="启用"
            inactive-text="停用"
            @change="toggleStatus(row, Boolean($event))"
          />
        </template>
      </el-table-column>
      <el-table-column label="最后使用" min-width="200">
        <template #default="{ row }">
          {{ formatDateSafe(row.last_used_at) }}
        </template>
      </el-table-column>
      <el-table-column label="创建时间" min-width="200">
        <template #default="{ row }">
          {{ formatDateSafe(row.created_at) }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="140" fixed="right">
        <template #default="{ row }">
          <el-button type="danger" size="small" :icon="Delete" @click="confirmDelete(row)">
            删除
          </el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>

  <el-dialog
    v-model="createDialogVisible"
    title="生成 API Key"
    :width="isMobileView ? '90%' : '480px'"
    :close-on-click-modal="false"
  >
    <el-form
      :model="createForm"
      :label-position="isMobileView ? 'top' : 'left'"
      label-width="100px"
    >
      <el-form-item label="名称" required>
        <el-input
          v-model="createForm.name"
          placeholder="用于区分用途的名称，例如：CI 脚本"
          maxlength="60"
          show-word-limit
        />
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="createDialogVisible = false">取消</el-button>
      <el-button type="primary" :loading="creating" @click="createKey"> 生成 </el-button>
    </template>
  </el-dialog>

  <el-dialog
    v-model="createdKeyDialogVisible"
    title="请立即保存新密钥"
    :width="isMobileView ? '90%' : '520px'"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
  >
    <el-alert type="warning" show-icon :closable="false" class="created-warning">
      <template #title> 只会显示一次，关闭后无法再次查看完整密钥。 </template>
      <p class="alert-tip">
        推荐：X-API-Key: YOUR_API_KEY；兼容：/api/user/info?api_key=YOUR_API_KEY
      </p>
    </el-alert>

    <div class="created-key-box" v-if="createdKey">
      <div class="key-row">
        <span class="key-label">完整密钥</span>
        <div class="key-value">
          <el-input v-model="createdKey.key" readonly />
          <el-button type="primary" plain :icon="CopyDocument" @click="copyContent(createdKey.key)">
            复制
          </el-button>
        </div>
      </div>
      <div class="key-meta">
        <span>名称：{{ createdKey.name }}</span>
        <span>前缀：{{ createdKey.key_prefix }}</span>
        <span>创建时间：{{ formatDateTime(createdKey.created_at) }}</span>
      </div>
    </div>

    <template #footer>
      <el-button type="primary" @click="createdKeyDialogVisible = false"> 我已妥善保存 </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Delete, CopyDocument } from '@element-plus/icons-vue'
import { useHttpClient } from '@/http/client'
import {
  apiKeyPublicMessages,
  createApiKey,
  deleteApiKey,
  fetchApiKeys,
  updateApiKeyStatus,
  type ApiKey,
  type CreatedApiKey,
} from '@/api/apiKeys'
import { notifyHttpError } from '@/utils/httpErrorNotification'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import { copyText } from '@/utils/clipboard'
import { formatDateTime } from '@/utils/timeUtils'
import { useDeviceType } from '@/composables/useDeviceType'
import PageHeader from '@/components/common/PageHeader.vue'

type ApiKeyItem = ApiKey & { _updating?: boolean }

const http = useHttpClient()
const apiKeys = ref<ApiKeyItem[]>([])
const loading = ref(false)
const createDialogVisible = ref(false)
const createdKeyDialogVisible = ref(false)
const createForm = reactive({ name: '' })
const creating = ref(false)
const createdKey = ref<CreatedApiKey | null>(null)
const { isMobile: isMobileView } = useDeviceType()

const formatDateSafe = (value?: number | null) => {
  return formatDateTime(value || 0)
}

const loadKeys = async (refreshAfterSuccess = false) => {
  try {
    loading.value = true
    apiKeys.value = (await fetchApiKeys(http)).map((item) => ({ ...item, _updating: false }))
  } catch (error) {
    const messagePrefix = refreshAfterSuccess ? '操作已成功，但刷新 API Key 列表失败' : undefined
    notifyHttpError(error, '加载 API Key 列表失败', {
      fallbackMessage: messagePrefix ?? '加载 API Key 列表失败',
      publicMessages: apiKeyPublicMessages,
      messagePrefix,
    })
  } finally {
    loading.value = false
  }
}

const openCreateDialog = () => {
  createForm.name = ''
  createDialogVisible.value = true
}

const createKey = async () => {
  if (!createForm.name.trim()) {
    ElMessage.warning('请输入 API Key 名称')
    return
  }
  try {
    creating.value = true
    createdKey.value = await createApiKey(http, createForm.name.trim())
    createDialogVisible.value = false
    createdKeyDialogVisible.value = true
    ElMessage.success('API Key 创建成功')
    await loadKeys(true)
  } catch (error) {
    notifyHttpError(error, '创建 API Key 失败', {
      fallbackMessage: '创建 API Key 失败',
      publicMessages: apiKeyPublicMessages,
    })
  } finally {
    creating.value = false
  }
}

const toggleStatus = async (row: ApiKeyItem, isActive: boolean) => {
  if (row._updating) return
  const original = row.is_active
  row.is_active = isActive
  row._updating = true
  try {
    await updateApiKeyStatus(http, row.id, isActive)
    ElMessage.success(isActive ? '已启用' : '已禁用')
    // 刷新时间等可能变化；回读失败不撤销已经成功的状态更新。
    await loadKeys(true)
  } catch (error) {
    row.is_active = original
    notifyHttpError(error, '更新 API Key 状态失败', {
      fallbackMessage: '更新 API Key 状态失败',
      publicMessages: apiKeyPublicMessages,
    })
  } finally {
    row._updating = false
  }
}

const confirmDelete = async (row: ApiKey) => {
  try {
    await ElMessageBox.confirm(
      `确认删除“${row.name}”（前缀 ${row.key_prefix}）吗？删除后无法恢复。`,
      '删除确认',
      {
        type: 'warning',
        confirmButtonText: '删除',
        cancelButtonText: '取消',
      },
    )

    await deleteApiKey(http, row.id)
    ElMessage.success('删除成功')
    await loadKeys(true)
  } catch (error) {
    if (!isMessageBoxCancelError(error)) {
      notifyHttpError(error, '删除 API Key 失败', {
        fallbackMessage: '删除 API Key 失败',
        publicMessages: apiKeyPublicMessages,
      })
    }
  }
}

const copyContent = async (content?: string) => {
  if (!content) return
  if (await copyText(content)) {
    ElMessage.success('已复制到剪贴板')
    return
  }
  ElMessage.error('复制失败，请手动复制')
}

onMounted(() => {
  loadKeys()
})
</script>

<style scoped>
.api-keys-container {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 0 10px 10px 10px;
}

.api-keys-container :deep(.qms-page-header) {
  margin-bottom: 0;
}

.action-bar {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}

.action-bar :deep(.el-button + .el-button) {
  margin-left: 0;
}

.alert-tip {
  margin: 4px 0 0;
  color: var(--el-text-color-regular);
  font-size: 13px;
}

.created-warning {
  margin-bottom: 12px;
}

.created-key-box {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.key-row {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.key-label {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.key-value {
  display: flex;
  gap: 10px;
  align-items: center;
}

.key-meta {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
  color: var(--el-text-color-regular);
  font-size: 13px;
}
</style>
