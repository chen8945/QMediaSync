<script setup lang="ts">
import { onMounted, shallowRef } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Refresh } from '@element-plus/icons-vue'
import { useHttpClient } from '@/http/client'
import { SERVER_URL } from '@/const'
import { formatDateTime } from '@/utils/timeUtils'
import PageHeader from '@/components/common/PageHeader.vue'

interface LoginSession {
  session_id: string
  current: boolean
  ip_address: string
  user_agent: string
  created_at: number
  last_seen_at: number
  expires_at: number
}

const http = useHttpClient()
const sessions = shallowRef<LoginSession[]>([])
const loading = shallowRef(false)

const loadSessions = async () => {
  loading.value = true
  try {
    const response = await http.get(`${SERVER_URL}/user/sessions`)
    if (response?.data.code === 200) {
      sessions.value = response.data.data || []
    } else {
      sessions.value = []
      ElMessage.error(response?.data.message || '加载登录设备失败')
    }
  } finally {
    loading.value = false
  }
}

const revokeSession = async (session: LoginSession) => {
  if (session.current) {
    ElMessage.warning('当前设备请使用退出登录')
    return
  }
  await ElMessageBox.confirm('确定撤销该登录设备吗？', '撤销登录设备', {
    type: 'warning',
    confirmButtonText: '撤销',
    cancelButtonText: '取消',
  })
  const response = await http.delete(`${SERVER_URL}/user/sessions/${session.session_id}`)
  if (response?.data.code === 200) {
    ElMessage.success('登录设备已撤销')
    await loadSessions()
  } else {
    ElMessage.error(response?.data.message || '撤销失败')
  }
}

const revokeOthers = async () => {
  await ElMessageBox.confirm('确定撤销除当前设备外的所有登录设备吗？', '撤销其他设备', {
    type: 'warning',
    confirmButtonText: '撤销',
    cancelButtonText: '取消',
  })
  const response = await http.post(`${SERVER_URL}/user/sessions/revoke-others`)
  if (response?.data.code === 200) {
    ElMessage.success('其他登录设备已撤销')
    await loadSessions()
  } else {
    ElMessage.error(response?.data.message || '撤销失败')
  }
}

onMounted(() => {
  void loadSessions()
})
</script>

<template>
  <div class="main-content-container login-sessions-container">
    <PageHeader>
      <template #actions>
        <div class="action-bar">
          <el-button :icon="Refresh" :loading="loading" @click="loadSessions">刷新</el-button>
          <el-button type="danger" plain @click="revokeOthers">撤销其他设备</el-button>
        </div>
      </template>
    </PageHeader>

    <el-table :data="sessions" v-loading="loading" border stripe empty-text="暂无登录设备">
      <el-table-column label="状态" width="120">
        <template #default="{ row }">
          <el-tag :type="row.current ? 'success' : 'info'">
            {{ row.current ? '当前设备' : '其他设备' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="ip_address" label="登录 IP" width="150" />
      <el-table-column
        prop="user_agent"
        label="设备"
        min-width="260"
        :show-overflow-tooltip="{ popperClass: 'qms-contained-tooltip', strategy: 'fixed' }"
      />
      <el-table-column label="最后活跃" width="180">
        <template #default="{ row }">{{ formatDateTime(row.last_seen_at) }}</template>
      </el-table-column>
      <el-table-column label="过期时间" width="180">
        <template #default="{ row }">{{ formatDateTime(row.expires_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <el-button
            type="danger"
            size="small"
            :icon="Delete"
            :disabled="row.current"
            @click="revokeSession(row)"
          >
            撤销
          </el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.login-sessions-container {
  display: grid;
  gap: 16px;
  padding: 0 10px 10px;
}

.login-sessions-container :deep(.qms-page-header) {
  margin-bottom: 0;
}

.action-bar {
  display: flex;
  gap: 12px;
  align-items: center;
  flex-wrap: wrap;
}

.action-bar :deep(.el-button + .el-button) {
  margin-left: 0;
}
</style>
