<template>
  <div class="cloud-accounts-page">
    <PageHeader>
      <template #actions>
        <div class="header-actions">
          <el-button
            type="primary"
            class="add-btn"
            :icon="Plus"
            @click="showAddAccountDialog = true"
          >
            <span class="btn-text">添加账号</span>
          </el-button>
        </div>
      </template>
      <template #stats>
        <PageStats :items="stats" />
      </template>
    </PageHeader>

    <div class="accounts-content">
      <div class="accounts-grid" v-if="accounts.length > 0">
        <div
          class="account-card"
          v-for="account in accounts"
          :key="account.id"
          :class="getCardStatusClass(account)"
        >
          <div class="card-status-bar" :class="getStatusClass(account)"></div>
          <div class="card-main">
            <div class="card-header">
              <div class="card-title-wrapper">
                <el-tooltip :content="'账号 ID：' + account.id" placement="bottom">
                  <span class="card-id">#{{ account.id }}</span>
                </el-tooltip>
                <span class="card-name">{{ account.name }}</span>
              </div>
              <el-tag
                :type="sourceTypeTagMap[account.source_type]"
                class="source-tag"
                effect="light"
              >
                {{ sourceTypeMap[account.source_type] }}
              </el-tag>
            </div>

            <div class="card-body">
              <div
                class="info-row"
                v-for="appInfo in account.source_type === '115' ? getV115AppInfoRows(account) : []"
                :key="appInfo.label"
              >
                <div class="info-icon">
                  <el-icon>
                    <Key />
                  </el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">{{ appInfo.label }}</span>
                  <span class="info-value">{{ appInfo.value }}</span>
                </div>
              </div>

              <template v-if="account.source_type === 'openlist'">
                <div class="info-row">
                  <div class="info-icon">
                    <el-icon>
                      <Link />
                    </el-icon>
                  </div>
                  <div class="info-content">
                    <span class="info-label">访问地址</span>
                    <span class="info-value path-value">{{ account.base_url }}</span>
                  </div>
                </div>
                <div class="info-row">
                  <div class="info-icon">
                    <el-icon>
                      <Postcard />
                    </el-icon>
                  </div>
                  <div class="info-content">
                    <span class="info-label">用户 ID</span>
                    <span class="info-value">{{ account.user_id || '-' }}</span>
                  </div>
                </div>
              </template>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon>
                    <Calendar />
                  </el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">添加时间</span>
                  <span class="info-value">{{ formatTimestamp(account.created_at) }}</span>
                </div>
              </div>

              <template
                v-if="
                  (account.source_type === '115' || account.source_type === 'baidupan') &&
                  account.token
                "
              >
                <div class="status-divider"></div>
                <div class="disk-status-section">
                  <div class="status-header">
                    <span class="status-title">网盘状态</span>
                    <el-button
                      type="primary"
                      size="small"
                      text
                      :icon="RefreshRight"
                      :loading="account.statusLoading"
                      @click="loadAccountStatus(account)"
                    >
                      刷新
                    </el-button>
                  </div>
                  <template v-if="account.status">
                    <div class="info-row" v-if="account.status.member_level">
                      <div class="info-icon member-icon">
                        <el-icon>
                          <Postcard />
                        </el-icon>
                      </div>
                      <div class="info-content">
                        <span class="info-label">用户 ID</span>
                        <span class="info-value">{{ account.user_id }}</span>
                      </div>
                    </div>
                    <div class="info-row" v-if="account.status.member_level">
                      <div class="info-icon member-icon">
                        <el-icon>
                          <Postcard />
                        </el-icon>
                      </div>
                      <div class="info-content">
                        <span class="info-label">用户名</span>
                        <span class="info-value">{{ account.username }}</span>
                      </div>
                    </div>
                    <div class="info-row space-row">
                      <div class="info-icon space-icon">
                        <el-icon>
                          <Cloudy />
                        </el-icon>
                      </div>
                      <div class="info-content space-content">
                        <span class="info-label">空间使用</span>
                        <div class="space-info">
                          <el-progress
                            style="width: 80%"
                            :percentage="
                              account.status.total_space > 0
                                ? Math.round(
                                    (account.status.used_space / account.status.total_space) * 100,
                                  )
                                : 0
                            "
                            :stroke-width="10"
                            :show-text="false"
                            :color="
                              getSpaceColor(account.status.used_space, account.status.total_space)
                            "
                          />
                          <span class="space-text"
                            >{{ formatFileSize(account.status.used_space) }} /
                            {{ formatFileSize(account.status.total_space) }}</span
                          >
                        </div>
                      </div>
                    </div>
                    <div class="info-row" v-if="account.status.member_level">
                      <div class="info-icon member-icon">
                        <el-icon>
                          <Postcard />
                        </el-icon>
                      </div>
                      <div class="info-content">
                        <span class="info-label">会员等级</span>
                        <el-tag size="small" type="warning" effect="plain">{{
                          account.status.member_level
                        }}</el-tag>
                      </div>
                    </div>
                    <div
                      class="info-row"
                      v-if="
                        account.status.expire_time &&
                        account.status.expire_time !== '0001-01-01T00:00:00Z'
                      "
                    >
                      <div class="info-icon expire-icon">
                        <el-icon>
                          <Calendar />
                        </el-icon>
                      </div>
                      <div class="info-content">
                        <span class="info-label">到期时间</span>
                        <span
                          class="info-value"
                          :class="{ 'expire-warning': isExpiringSoon(account.status.expire_time) }"
                        >
                          {{ formatExpireTime(account.status.expire_time) }}
                        </span>
                      </div>
                    </div>
                  </template>
                  <template v-else-if="!account.statusLoading">
                    <div class="no-status">暂无状态信息</div>
                  </template>
                </div>
              </template>

              <div class="status-row">
                <div class="status-indicator" :class="getStatusClass(account)">
                  <el-icon v-if="account.token_failed_reason && !account.token">
                    <CircleClose />
                  </el-icon>
                  <el-icon v-else-if="account.token">
                    <CircleCheck />
                  </el-icon>
                  <el-icon v-else>
                    <WarningFilled />
                  </el-icon>
                  <span>{{ getStatusText(account) }}</span>
                </div>
                <el-tooltip
                  v-if="account.token_failed_reason && !account.token"
                  :content="account.token_failed_reason"
                  placement="top"
                  popper-class="qms-contained-tooltip"
                  append-to="body"
                >
                  <el-icon class="error-help-icon">
                    <QuestionFilled />
                  </el-icon>
                </el-tooltip>
              </div>
            </div>

            <div class="card-footer">
              <el-button
                type="danger"
                size="small"
                plain
                :icon="Delete"
                @click="handleDelete(account)"
              >
                删除
              </el-button>

              <el-button
                type="warning"
                size="small"
                plain
                :icon="Key"
                @click="handleAuthorize(account)"
                v-if="account.source_type !== 'openlist'"
              >
                授权
              </el-button>

              <el-button
                type="primary"
                size="small"
                plain
                :icon="Edit"
                @click="handleEdit(account)"
              >
                编辑
              </el-button>
            </div>
          </div>
        </div>
      </div>

      <div class="empty-state" v-else-if="!loading">
        <div class="empty-illustration">
          <el-icon class="empty-icon">
            <Cloudy />
          </el-icon>
          <div class="empty-dots">
            <span></span>
            <span></span>
            <span></span>
          </div>
        </div>
        <h3 class="empty-title">暂无网盘账号</h3>
        <p class="empty-description">点击上方按钮添加第一个网盘账号</p>
        <el-button type="primary" :icon="Plus" @click="showAddAccountDialog = true">
          添加账号
        </el-button>
      </div>

      <div class="loading-state" v-if="loading">
        <el-icon class="loading-icon rotating">
          <Loading />
        </el-icon>
        <span>加载中…</span>
      </div>

      <div class="page-footer-tips">
        <div class="tips-header">
          <el-icon class="tips-icon">
            <InfoFilled />
          </el-icon>
          <span>使用说明</span>
        </div>
        <div class="tips-content">
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon>
                <Warning />
              </el-icon>
              <span>操作流程</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item">
                <span class="tip-bullet">1.</span>
                <span>点击“添加账号”，选择网盘类型并填写相关信息</span>
              </div>
              <div class="tip-item">
                <span class="tip-bullet">2.</span>
                <span>添加成功后，点击列表中的“授权”完成账号绑定</span>
              </div>
              <div class="tip-item tip-highlight">
                <span class="tip-bullet">★</span>
                <span>只有已授权的账号才能用于 STRM 同步目录配置</span>
              </div>
            </div>
          </div>
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon>
                <Key />
              </el-icon>
              <span>授权说明</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>115 网盘：通过 OAuth 授权，将跳转到授权页面</span>
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>百度网盘：通过 OAuth 授权，将跳转到授权页面</span>
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>OpenList：支持用户名密码或令牌两种认证方式</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>

  <el-dialog
    v-model="showAddAccountDialog"
    title="添加账号"
    class="account-dialog"
    :width="isMobile ? 'min(500px, calc(100vw - 32px))' : '500px'"
    :align-center="isMobile"
  >
    <el-form
      :model="newAccountForm"
      :label-position="isMobile ? 'top' : 'right'"
      :label-width="isMobile ? 'auto' : '100px'"
    >
      <el-form-item label="网盘类型">
        <el-select v-model="newAccountForm.type" placeholder="请选择网盘类型">
          <template v-for="typeItem in sourceTypeOptions" :key="typeItem.value">
            <el-option
              v-if="typeItem.value !== 'local'"
              :label="typeItem.label"
              :value="typeItem.value"
            ></el-option>
          </template>
        </el-select>
      </el-form-item>
      <el-form-item label="账号备注" v-if="newAccountForm.type !== 'openlist'">
        <el-input v-model="newAccountForm.name" placeholder="请输入账号备注" />
      </el-form-item>
      <el-form-item label="访问地址" v-if="newAccountForm.type === 'openlist'">
        <el-input
          v-model="newAccountForm.base_url"
          placeholder="请输入 OpenList 地址：http://ip:5244"
        />
      </el-form-item>
      <el-form-item label="认证方式" v-if="newAccountForm.type === 'openlist'">
        <el-select v-model="newAccountForm.auth_type" placeholder="请选择认证方式">
          <el-option label="用户名密码" value="password"></el-option>
          <el-option label="令牌" value="token"></el-option>
        </el-select>
      </el-form-item>
      <template
        v-if="newAccountForm.type === 'openlist' && newAccountForm.auth_type === 'password'"
      >
        <el-form-item label="用户名">
          <el-input v-model="newAccountForm.username" placeholder="请输入用户名" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input type="password" v-model="newAccountForm.password" placeholder="请输入密码" />
        </el-form-item>
      </template>
      <el-form-item
        label="令牌"
        v-if="newAccountForm.type === 'openlist' && newAccountForm.auth_type === 'token'"
      >
        <el-input type="password" v-model="newAccountForm.token" placeholder="请输入令牌" />
      </el-form-item>
      <V115AppSelector
        v-if="newAccountForm.type === '115'"
        v-model:auth-mode="newAccountForm.auth_mode"
        v-model:selected-qr-app="newAccountForm.selected_qr_app"
        v-model:selected-web-provider="newAccountForm.selected_web_provider"
        v-model:custom-app-id="newAccountForm.custom_v115_app_id"
        v-model:custom-app-name="newAccountForm.custom_v115_app_name"
      />
    </el-form>
    <template #footer>
      <span class="dialog-footer">
        <el-button @click="showAddAccountDialog = false">取消</el-button>
        <el-button type="primary" @click="handleAddAccount" :loading="addAccountLoading"
          >确定</el-button
        >
      </span>
    </template>
  </el-dialog>

  <el-dialog
    v-model="showEditAccountDialog"
    :title="editDialogTitle"
    class="account-dialog"
    :width="isMobile ? 'min(500px, calc(100vw - 32px))' : '500px'"
    :align-center="isMobile"
  >
    <el-form
      :model="editAccountForm"
      :label-position="isMobile ? 'top' : 'right'"
      :label-width="isMobile ? 'auto' : '100px'"
    >
      <el-form-item label="账号备注">
        <el-input v-model="editAccountForm.name" placeholder="请输入账号备注" />
      </el-form-item>
      <template v-if="canEditCustomAppName">
        <el-form-item label="应用名">
          <el-input
            v-model="editAccountForm.app_id_name"
            placeholder="请输入应用名，可留空"
            clearable
          />
        </el-form-item>
        <el-form-item label="APP ID">
          <el-input v-model="editAccountForm.app_id" disabled />
        </el-form-item>
      </template>
      <template v-if="editAccountForm.source_type === 'openlist'">
        <el-form-item label="访问地址" prop="baseUrl">
          <el-input
            v-model="editAccountForm.base_url"
            placeholder="请输入 OpenList 地址：http://ip:5244"
          />
        </el-form-item>
        <el-form-item label="认证方式">
          <el-select v-model="editAccountForm.auth_type" placeholder="请选择认证方式">
            <el-option label="用户名密码" value="password"></el-option>
            <el-option label="令牌" value="token"></el-option>
          </el-select>
        </el-form-item>
        <template v-if="editAccountForm.auth_type === 'password'">
          <el-form-item label="用户名">
            <el-input v-model="editAccountForm.username" placeholder="请输入用户名" />
          </el-form-item>
          <el-form-item label="密码">
            <el-input
              type="password"
              v-model="editAccountForm.password"
              placeholder="请输入密码（留空则不修改）"
            />
          </el-form-item>
        </template>
        <el-form-item label="令牌" v-if="editAccountForm.auth_type === 'token'">
          <el-input type="password" v-model="editAccountForm.token" placeholder="请输入令牌" />
        </el-form-item>
      </template>
    </el-form>
    <template #footer>
      <span class="dialog-footer">
        <el-button @click="showEditAccountDialog = false">取消</el-button>
        <el-button type="primary" @click="handleUpdateAccount">确定</el-button>
      </span>
    </template>
  </el-dialog>

  <V115AuthorizationDialog
    v-model:visible="showV115AuthDialog"
    :account-id="selectedV115Account?.id ?? null"
    :account-name="selectedV115Account?.name ?? ''"
    @confirmed="loadAccounts"
  />
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import PageHeader from '@/components/common/PageHeader.vue'
import PageStats from '@/components/common/PageStats.vue'
import V115AuthorizationDialog from '@/components/cloud-auth/V115AuthorizationDialog.vue'
import V115AppSelector from '@/components/cloud-auth/V115AppSelector.vue'
import type { AxiosError } from 'axios'
import { ref, computed, onMounted } from 'vue'

import {
  WarningFilled,
  Plus,
  Loading,
  User,
  Key,
  Link,
  Calendar,
  Delete,
  Edit,
  CircleCheck,
  CircleClose,
  InfoFilled,
  QuestionFilled,
  Postcard,
  RefreshRight,
} from '@element-plus/icons-vue'
import { ElMessageBox, ElMessage } from 'element-plus'
import { formatTimestamp } from '@/utils/timeUtils'
import { sourceTypeMap, sourceTypeOptions, sourceTypeTagMap } from '@/utils/sourceTypeUtils'
import { useDeviceType } from '@/composables/useDeviceType'
import { useHttpClient } from '@/http/client'
import { getV115AppInfoRows, isCustomV115App } from '@/utils/cloudAccountUtils'
import { collectOAuthCallbackParams } from '@/utils/oauthCallback'
import {
  buildV115CreatePayload,
  defaultWebAuthProviderValue,
  getV115AuthAction,
  type V115AuthMode,
  type V115AuthProvider,
  type V115AuthSourceType,
  type V115SelectedQrApp,
  type V115WebAuthProviderValue,
} from '@/components/cloud-auth/v115AuthSources'

const { isMobile } = useDeviceType()

interface CloudDiskStatus {
  user_id: string
  username: string
  used_space: number
  total_space: number
  member_level: string
  expire_time: string
}

interface CloudAccount {
  id: number
  source_type: string
  name: string
  user_id: string
  username: string
  password: string
  base_url: string
  created_at: number
  token: string
  auth_type?: string
  app_id_name?: string
  app_name?: string
  display_name?: string
  app_id?: string
  auth_source_type?: V115AuthSourceType
  auth_provider?: V115AuthProvider
  requires_encryption_key?: boolean
  token_failed_reason?: string
  status?: CloudDiskStatus
  statusLoading?: boolean
}

interface V115OAuthURLData {
  auth_url?: string
  state?: string
  polling?: boolean
}

const http = useHttpClient()

const accounts = ref<CloudAccount[]>([])
const loading = ref(false)
const showAddAccountDialog = ref(false)
const addAccountLoading = ref(false)

const newAccountForm = ref({
  type: '',
  name: '',
  base_url: '',
  username: '',
  password: '',
  token: '',
  auth_type: 'password',
  auth_mode: 'qr' as V115AuthMode,
  selected_qr_app: { appId: '100197849', appName: 'QMediaSync' } as V115SelectedQrApp,
  selected_web_provider: defaultWebAuthProviderValue as V115WebAuthProviderValue,
  custom_v115_app_id: '',
  custom_v115_app_name: '',
})

const showEditAccountDialog = ref(false)
const currentEditAccount = ref<CloudAccount | null>(null)
const editAccountForm = ref({
  id: 0,
  source_type: '',
  base_url: '',
  username: '',
  password: '',
  token: '',
  auth_type: 'password',
  token_failed_reason: '',
  name: '',
  app_id: '',
  app_id_name: '',
})

const selectedAccountId = ref<number | undefined>(undefined)
const show123AuthDialog = ref(false)
const selectedV115Account = ref<CloudAccount | null>(null)
const showV115AuthDialog = ref(false)

const authorizedCount = computed(
  () => accounts.value.filter((a) => a.token && !a.token_failed_reason).length,
)
const unauthorizedCount = computed(
  () => accounts.value.filter((a) => !a.token && !a.token_failed_reason).length,
)
const failedCount = computed(
  () => accounts.value.filter((a) => a.token_failed_reason && !a.token).length,
)
const stats = computed(() => [
  { icon: User, value: accounts.value.length, label: '总账号数', tone: 'total' },
  { icon: CircleCheck, value: authorizedCount.value, label: '已授权', tone: 'authorized' },
  { icon: WarningFilled, value: unauthorizedCount.value, label: '未授权', tone: 'unauthorized' },
  { icon: CircleClose, value: failedCount.value, label: '授权失败', tone: 'failed' },
])
const editDialogTitle = computed(() =>
  editAccountForm.value.source_type === 'openlist' ? '编辑 OpenList 账号' : '编辑账号',
)
const canEditCustomAppName = computed(() =>
  isCustomV115App({
    source_type: editAccountForm.value.source_type,
    app_id: editAccountForm.value.app_id,
  }),
)

const getStatusClass = (account: CloudAccount) => {
  if (account.token_failed_reason && !account.token) return 'status-failed'
  if (account.token) return 'status-authorized'
  return 'status-unauthorized'
}

const getStatusText = (account: CloudAccount) => {
  if (account.token_failed_reason && !account.token) return '授权失败'
  if (account.token) return '已授权'
  return '未授权'
}

const getCardStatusClass = (account: CloudAccount) => {
  if (account.token_failed_reason && !account.token) return 'is-failed'
  if (account.token) return 'is-authorized'
  return 'is-unauthorized'
}

const loadAccounts = async () => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/account/list`)

    if (response?.data.code === 200) {
      const data = response.data.data
      accounts.value = data.map((item: CloudAccount) => ({
        id: item.id,
        source_type: item.source_type,
        name: item.name,
        user_id: item.user_id,
        username: item.username,
        created_at: item.created_at,
        token: item.token,
        base_url: item.base_url,
        password: item.password,
        auth_type: item.auth_type,
        app_id_name: item.app_id_name,
        app_name: item.app_name,
        display_name: item.display_name,
        app_id: item.app_id,
        auth_source_type: item.auth_source_type,
        auth_provider: item.auth_provider,
        requires_encryption_key: item.requires_encryption_key,
        token_failed_reason: item.token_failed_reason || '',
        status: undefined,
        statusLoading: false,
      }))
      accounts.value.forEach((account) => {
        if (
          (account.source_type === '115' || account.source_type === 'baidupan') &&
          account.token
        ) {
          loadAccountStatus(account)
        }
      })
    } else {
      console.error('加载账号列表失败：', response?.data.message || '未知错误')
      accounts.value = []
    }
  } catch (error) {
    console.error('加载账号列表失败：', error)
    accounts.value = []
  } finally {
    loading.value = false
  }
}

const loadAccountStatus = async (account: CloudAccount) => {
  const index = accounts.value.findIndex((a) => a.id === account.id)
  if (index === -1) return

  accounts.value[index].statusLoading = true

  try {
    let url = ''
    if (account.source_type === '115') {
      url = `${SERVER_URL}/115/status`
    } else if (account.source_type === 'baidupan') {
      url = `${SERVER_URL}/baidupan/status`
    } else {
      return
    }

    const response = await http.get(url, {
      params: { account_id: account.id },
    })

    if (response?.data.code === 200 && response.data.data) {
      accounts.value[index].status = response.data.data
    }
  } catch (error) {
    console.error(`获取 ${account.source_type} 状态失败：`, error)
  } finally {
    accounts.value[index].statusLoading = false
  }
}

const formatFileSize = (bytes: number): string => {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const k = 1024
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + units[i]
}

const formatExpireTime = (expireTime: string): string => {
  if (
    !expireTime ||
    expireTime === '0001-01-01T00:00:00Z' ||
    expireTime === '1970-01-01T00:00:00Z'
  ) {
    return '-'
  }
  try {
    const date = new Date(expireTime)
    if (isNaN(date.getTime())) return '-'
    return date.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
  } catch {
    return '-'
  }
}

const getSpaceColor = (used: number, total: number): string => {
  if (total === 0) return '#67c23a'
  const percentage = used / total
  if (percentage < 0.5) return '#67c23a'
  if (percentage < 0.8) return '#e6a23c'
  return '#f56c6c'
}

const isExpiringSoon = (expireTime: string): boolean => {
  if (!expireTime) return false
  try {
    const date = new Date(expireTime)
    if (isNaN(date.getTime())) return false
    const now = new Date()
    const diffDays = Math.ceil((date.getTime() - now.getTime()) / (1000 * 60 * 60 * 24))
    return diffDays <= 30
  } catch {
    return false
  }
}

const handleDelete = async (row: CloudAccount) => {
  try {
    await ElMessageBox.confirm(`确定要删除账号 "${row.name}" 吗？此操作不可恢复。`, '确认删除', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    const response = await http.post(
      `${SERVER_URL}/account/delete`,
      { id: row.id },
      {
        headers: {
          'Content-Type': 'application/json',
        },
      },
    )

    if (response?.data.code === 200) {
      ElMessage.success('账号删除成功')
      loadAccounts()
    } else {
      ElMessage.error(response?.data.message || '删除账号失败')
    }
  } catch (error) {
    if (error !== 'cancel' && error !== 'close') {
      console.error('删除账号失败：', error)
      ElMessage.error('删除账号失败')
    }
  }
}

const handleEdit = (account: CloudAccount) => {
  currentEditAccount.value = account

  const authType =
    account.auth_type || (account.username && account.password ? 'password' : 'token')

  editAccountForm.value = {
    id: account.id,
    source_type: account.source_type,
    name: account.name,
    base_url: account.base_url,
    username: account.username,
    password: account.password || '',
    token: account.token || '',
    auth_type: authType,
    token_failed_reason: account.token_failed_reason || '',
    app_id: account.app_id || '',
    app_id_name: account.app_id_name || '',
  }
  showEditAccountDialog.value = true
}

const hasOpenListConfigChanged = (): boolean => {
  const original = currentEditAccount.value
  if (!original || editAccountForm.value.source_type !== 'openlist') {
    return false
  }
  return (
    editAccountForm.value.base_url !== original.base_url ||
    editAccountForm.value.auth_type !== original.auth_type ||
    editAccountForm.value.username !== original.username ||
    editAccountForm.value.token !== (original.token || '') ||
    editAccountForm.value.password !== ''
  )
}

const handleUpdateAccount = async () => {
  try {
    if (hasOpenListConfigChanged()) {
      const openListRequestData = {
        id: editAccountForm.value.id,
        base_url: editAccountForm.value.base_url,
        auth_type: editAccountForm.value.auth_type,
        ...(editAccountForm.value.auth_type === 'token'
          ? { token: editAccountForm.value.token }
          : {
              username: editAccountForm.value.username,
              password: editAccountForm.value.password,
            }),
      }

      const openListResponse = await http.post(
        `${SERVER_URL}/account/openlist`,
        openListRequestData,
        {
          headers: {
            'Content-Type': 'application/json',
          },
        },
      )

      if (openListResponse?.data.code !== 200) {
        console.error('更新 OpenList 账号失败：', openListResponse?.data.message || '未知错误')
        ElMessage.error(openListResponse?.data.message || '更新账号失败')
        return
      }
    }

    const response = await http.post(
      `${SERVER_URL}/account/update`,
      {
        id: editAccountForm.value.id,
        name: editAccountForm.value.name,
        app_id_name: editAccountForm.value.app_id_name,
      },
      {
        headers: {
          'Content-Type': 'application/json',
        },
      },
    )

    if (response?.data.code === 200) {
      showEditAccountDialog.value = false
      loadAccounts()
      ElMessage.success('账号更新成功')
    } else {
      console.error('更新账号失败：', response?.data.message || '未知错误')
      ElMessage.error(response?.data.message || '更新账号失败')
    }
  } catch (error) {
    console.error('更新账号错误：', error)
    ElMessage.error('更新账号失败')
  }
}

const handleAuthorize = (row: CloudAccount) => {
  if (row.source_type === '115') {
    const action = getV115AuthAction(row)
    if (action === 'pkce') {
      selectedV115Account.value = row
      showV115AuthDialog.value = true
      return
    }
    if (action === 'oauth') {
      void handle115OAuth(row.id)
      return
    }
    ElMessage.error('不支持的 115 开放平台应用')
    return
  }
  if (row.source_type === '123') {
    selectedAccountId.value = row.id
    show123AuthDialog.value = true
    return
  }
  if (row.source_type === 'baidupan') {
    void handleBaiduOAuth(row.id)
  }
}

const handle115OAuth = async (accountId?: number) => {
  try {
    await ElMessageBox.confirm(
      '即将跳转到 115 网盘授权页面，请在新页面完成授权后返回本页面。',
      '授权提示',
      {
        confirmButtonText: '前往授权',
        cancelButtonText: '取消',
        type: 'info',
      },
    )

    const redirectUrl = window.location.href.split('?')[0]
    const response = await http.get(`${SERVER_URL}/115/oauth-url`, {
      params: {
        account_id: accountId,
        redirect_url: redirectUrl,
      },
    })

    if (response?.data.code === 200 && response.data.data) {
      const data = response.data.data as V115OAuthURLData | string
      if (typeof data === 'string') {
        window.location.href = data
        return
      }
      if (data.polling && data.state) {
        if (data.auth_url) {
          window.open(data.auth_url, '_blank', 'noopener,noreferrer')
        }
        poll115OAuthStatus(accountId, data.state)
        return
      }
      if (data.auth_url) {
        window.location.href = data.auth_url
        return
      }
      ElMessage.error('授权服务未返回授权地址')
    } else {
      ElMessage.error(response?.data.message || '获取授权地址失败')
    }
  } catch (error) {
    if (error !== 'cancel') {
      console.error('115 OAuth 授权错误：', error)
      ElMessage.error('获取授权地址失败')
    }
  }
}

const poll115OAuthStatus = (accountId: number | undefined, state: string) => {
  if (!accountId) return
  let retries = 0
  const maxRetries = 60
  const timer = window.setInterval(async () => {
    retries += 1
    try {
      const response = await http.get(`${SERVER_URL}/115/oauth-status`, {
        params: { account_id: accountId, state },
      })
      if (response?.data.code === 200 && response.data.data?.done) {
        window.clearInterval(timer)
        ElMessage.success('授权成功')
        await loadAccounts()
        return
      }
      if (response?.data.code !== 200) {
        window.clearInterval(timer)
        ElMessage.error(response?.data.message || '授权状态查询失败')
        return
      }
      if (retries >= maxRetries) {
        window.clearInterval(timer)
        ElMessage.error('授权等待超时')
      }
    } catch (error) {
      window.clearInterval(timer)
      console.error('115 OAuth 状态查询错误：', error)
      ElMessage.error('授权状态查询失败')
    }
  }, 3000)
}

const handleBaiduOAuth = async (accountId?: number) => {
  try {
    await ElMessageBox.confirm(
      '即将跳转到百度网盘授权页面，请在新页面完成授权后返回本页面。',
      '授权提示',
      {
        confirmButtonText: '前往授权',
        cancelButtonText: '取消',
        type: 'info',
      },
    )

    const redirectUrl = window.location.href.split('?')[0]
    const response = await http.get(`${SERVER_URL}/baidupan/oauth-url`, {
      params: {
        account_id: accountId,
        redirect_url: redirectUrl,
      },
    })

    if (response?.data.code === 200 && response.data.data) {
      window.location.href = response.data.data
    } else {
      ElMessage.error(response?.data.message || '获取授权地址失败')
    }
  } catch (error) {
    if (error !== 'cancel') {
      console.error('百度网盘 OAuth 授权错误：', error)
      ElMessage.error('获取授权地址失败')
    }
  }
}

const resetForm = () => {
  newAccountForm.value = {
    type: '',
    name: '',
    base_url: '',
    username: '',
    password: '',
    token: '',
    auth_type: 'password',
    auth_mode: 'qr',
    selected_qr_app: { appId: '100197849', appName: 'QMediaSync' },
    selected_web_provider: defaultWebAuthProviderValue,
    custom_v115_app_id: '',
    custom_v115_app_name: '',
  }
}

const getAddAccountValidationMessage = (): string | null => {
  const form = newAccountForm.value
  const sourceType = form.type.trim()
  if (!sourceType) {
    return '请先选择网盘类型'
  }

  if (sourceType === 'openlist') {
    if (!form.base_url.trim()) {
      return '请先填写 OpenList 访问地址'
    }
    if (form.auth_type === 'token') {
      return form.token.trim() ? null : '请先填写 OpenList 令牌'
    }
    if (!form.username.trim()) {
      return '请先填写 OpenList 用户名'
    }
    if (!form.password.trim()) {
      return '请先填写 OpenList 密码'
    }
    return null
  }

  const accountName = form.name.trim()
  if (!accountName) {
    return '请先填写账号备注'
  }
  if (Array.from(accountName).length > 64) {
    return '账号备注不能超过 64 个字符'
  }
  return null
}

const handleAddAccount = async () => {
  try {
    const validationMessage = getAddAccountValidationMessage()
    if (validationMessage) {
      ElMessage.warning(validationMessage)
      return
    }

    const data: Record<string, string | number> = {
      source_type: newAccountForm.value.type,
      name: newAccountForm.value.name,
    }
    let url = `${SERVER_URL}/account/add`
    if (newAccountForm.value.type === '115') {
      Object.assign(data, {
        ...buildV115CreatePayload({
          authMode: newAccountForm.value.auth_mode,
          selectedQrApp: newAccountForm.value.selected_qr_app,
          selectedWebProvider: newAccountForm.value.selected_web_provider,
          customAppId: newAccountForm.value.custom_v115_app_id,
          customAppName: newAccountForm.value.custom_v115_app_name,
        }),
      })
    } else if (newAccountForm.value.type === 'openlist') {
      url = `${SERVER_URL}/account/openlist`
      Object.assign(data, {
        base_url: newAccountForm.value.base_url,
        auth_type: newAccountForm.value.auth_type,
      })
      if (newAccountForm.value.auth_type === 'token') {
        data.token = newAccountForm.value.token
      } else {
        data.username = newAccountForm.value.username
        data.password = newAccountForm.value.password
      }
    }

    const response = await http.post(url, data)

    if (response?.data.code === 200) {
      ElMessage.success('添加账号成功')
      showAddAccountDialog.value = false
      loadAccounts()
      resetForm()
    } else {
      ElMessage.error(`添加账号失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    console.error('添加账号失败：', error)
    const err: AxiosError = error as AxiosError
    const errData = err.response?.data as { message?: string }
    const status = err.response?.status || err.status
    const message = status
      ? `HTTP ${status}，${errData.message || err.message}`
      : errData.message || err.message
    ElMessage.error(`添加账号失败：${message}`)
  }
}

const confirmOAuth = async (
  source: string,
  accountId: number,
  tokenData: string,
  payload: Record<string, string> = {},
): Promise<void> => {
  try {
    let url = ''
    if (source === '' || source === '115') {
      url = `${SERVER_URL}/115/oauth-confirm`
    } else if (source === 'baidupan') {
      url = `${SERVER_URL}/baidupan/oauth-confirm`
    } else {
      return
    }

    const response = await http.post(
      url,
      {
        account_id: accountId,
        ...(tokenData ? { data: tokenData } : { payload }),
      },
      {
        headers: {
          'Content-Type': 'application/json',
        },
      },
    )

    if (response?.data.code === 200) {
      ElMessage.success({ message: '授权成功，2 秒后将自动刷新页面', duration: 2000 })
      setTimeout(() => {
        const hash = window.location.hash
        const cleanHash = hash.split('?')[0]
        window.location.href = window.location.origin + cleanHash
      }, 2000)
    } else {
      ElMessage.error(response?.data.message || '授权确认失败')
    }
  } catch (error) {
    console.error('OAuth 确认错误：', error)
    ElMessage.error('授权确认失败')
  }
}

const checkOAuthCallback = async () => {
  const urlParams = collectOAuthCallbackParams(window.location.search, window.location.hash)
  const accountId = urlParams.get('account_id')
  const tokenData = urlParams.get('token_data')
  const source = urlParams.get('source') || '115'

  if (accountId && (tokenData || urlParams.get('access_token') || urlParams.get('state'))) {
    const payload = Object.fromEntries(urlParams.entries())
    await confirmOAuth(source, parseInt(accountId), tokenData || '', payload)
  }
}

onMounted(() => {
  checkOAuthCallback()
  loadAccounts()
})
</script>

<style scoped>
.cloud-accounts-page {
  position: relative;
  min-height: 100%;
  padding: 0 24px 24px;
}

.header-actions {
  display: flex;
}

.add-btn {
  background: #409eff !important;
  border-color: #409eff !important;
  transition:
    background-color 0.2s ease,
    border-color 0.2s ease;
}

.add-btn:hover {
  background: #66b1ff !important;
  border-color: #66b1ff !important;
}

.accounts-content {
  padding: 0;
}

.accounts-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 20px;
  margin-bottom: 24px;
}

.account-card {
  background: #fff;
  border-radius: 16px;
  overflow: hidden;
  border: 1px solid #dcdfe6;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.06);
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
  position: relative;
}

.account-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.1);
}

.account-card.is-authorized {
  border-color: #b3e19d;
  box-shadow: 0 2px 12px rgba(103, 194, 58, 0.2);
}

.account-card.is-authorized:hover {
  box-shadow: 0 8px 24px rgba(103, 194, 58, 0.3);
}

.account-card.is-unauthorized {
  border-color: #f3d19e;
  box-shadow: 0 2px 12px rgba(230, 162, 60, 0.2);
}

.account-card.is-unauthorized:hover {
  box-shadow: 0 8px 24px rgba(230, 162, 60, 0.3);
}

.account-card.is-failed {
  border-color: #f5b5b5;
  box-shadow: 0 2px 12px rgba(245, 108, 108, 0.2);
}

.account-card.is-failed:hover {
  box-shadow: 0 8px 24px rgba(245, 108, 108, 0.3);
}

.card-status-bar {
  height: 4px;
  background: #e4e7ed;
}

.card-status-bar.status-authorized {
  background: linear-gradient(90deg, #67c23a, #95d475);
}

.card-status-bar.status-unauthorized {
  background: linear-gradient(90deg, #e6a23c, #f0c78a);
}

.card-status-bar.status-failed {
  background: linear-gradient(90deg, #f56c6c, #fab6b6);
}

.card-main {
  padding: 16px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid #f0f2f5;
}

.card-title-wrapper {
  flex: 1;
  min-width: 0;
}

.card-id {
  display: inline-block;
  font-size: 12px;
  color: #909399;
  background: #f5f7fa;
  padding: 2px 8px;
  border-radius: 4px;
  margin-right: 8px;
}

.card-name {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  word-break: break-all;
}

.source-tag {
  flex-shrink: 0;
  margin-left: 8px;
}

.card-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.info-row {
  display: flex;
  align-items: middle;
  gap: 12px;
}

.info-icon {
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: #f5f7fa;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #909399;
  flex-shrink: 0;
}

.info-content {
  flex: 1;
  display: flex;
  justify-content: space-between;
  align-items: center;
  min-width: 0;
}

.info-label {
  font-size: 13px;
  color: #909399;
}

.info-value {
  font-size: 14px;
  color: #303133;
  text-align: right;
}

.path-value {
  font-family: 'SF Mono', Monaco, monospace;
  font-size: 12px;
  word-break: break-all;
  max-width: 200px;
}

.status-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  padding-top: 12px;
  border-top: 1px dashed #ebeef5;
}

.status-divider {
  height: 1px;
  background: #ebeef5;
  margin: 8px 0;
}

.disk-status-section {
  background: #fafbfc;
  border-radius: 8px;
  padding: 12px;
  margin-top: 4px;
}

.status-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.status-title {
  font-size: 13px;
  font-weight: 600;
  color: #606266;
}

.no-status {
  text-align: center;
  color: #909399;
  font-size: 13px;
  padding: 8px 0;
}

.space-info {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  flex: 1;
}

.space-text {
  font-size: 12px;
  color: #606266;
}

.expire-warning {
  color: #f56c6c;
  font-weight: 500;
}

.status-indicator {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: 20px;
  font-size: 13px;
  font-weight: 500;
}

.status-indicator.status-authorized {
  background: #f0f9eb;
  color: #67c23a;
}

.status-indicator.status-unauthorized {
  background: #fdf6ec;
  color: #e6a23c;
}

.status-indicator.status-failed {
  background: #fef0f0;
  color: #f56c6c;
}

.error-help-icon {
  font-size: 16px;
  color: #f56c6c;
  cursor: help;
}

.rotating {
  animation: rotate 1s linear infinite;
}

@keyframes rotate {
  from {
    transform: rotate(0deg);
  }

  to {
    transform: rotate(360deg);
  }
}

.card-footer {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 16px;
  margin-top: 12px;
  border-top: 1px solid #f0f2f5;
}

.card-footer .el-button {
  flex: 1;
  min-width: 70px;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  background: #fff;
  border-radius: 16px;
  border: 1px solid #dcdfe6;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.06);
  margin-bottom: 24px;
}

.empty-illustration {
  position: relative;
  margin-bottom: 24px;
}

.empty-icon {
  font-size: 80px;
  color: #dcdfe6;
}

.empty-dots {
  display: flex;
  justify-content: center;
  gap: 8px;
  margin-top: 16px;
}

.empty-dots span {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #dcdfe6;
  animation: bounce 1.4s infinite ease-in-out both;
}

.empty-dots span:nth-child(1) {
  animation-delay: -0.32s;
}

.empty-dots span:nth-child(2) {
  animation-delay: -0.16s;
}

@keyframes bounce {
  0%,
  80%,
  100% {
    transform: scale(0);
  }

  40% {
    transform: scale(1);
  }
}

.empty-title {
  margin: 0 0 8px 0;
  font-size: 18px;
  font-weight: 600;
  color: #303133;
}

.empty-description {
  margin: 0 0 24px 0;
  font-size: 14px;
  color: #909399;
}

.loading-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 20px;
  background: #fff;
  border-radius: 16px;
  color: #909399;
  gap: 12px;
}

.loading-icon {
  font-size: 32px;
  color: #409eff;
}

.page-footer-tips {
  border: none;
  border-radius: 16px;
  overflow: hidden;
  background: #fff;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.06);
}

.tips-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 14px 20px;
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  color: #fff;
  font-size: 15px;
  font-weight: 600;
}

.tips-icon {
  font-size: 18px;
}

.tips-content {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
}

.tip-group {
  flex: 1;
  min-width: 300px;
  padding: 20px;
  border-right: 1px solid #f0f2f5;
}

.tip-group:last-child {
  border-right: none;
}

.tip-group-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
  padding-bottom: 10px;
  border-bottom: 2px solid #f0f2f5;
  font-size: 15px;
  font-weight: 600;
  color: #303133;
}

.tip-group-title .el-icon {
  color: #e6a23c;
  font-size: 18px;
}

.tip-group-items {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.tip-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 13px;
  color: #606266;
  line-height: 1.6;
}

.tip-bullet {
  flex-shrink: 0;
  width: 16px;
  color: #c0c4cc;
  text-align: center;
}

.tip-item strong {
  color: #409eff;
}

.tip-highlight {
  background: linear-gradient(135deg, #fdf6ec 0%, #fef8eb 100%);
  margin: 6px -12px;
  padding: 12px;
  border-radius: 8px;
  border-left: 3px solid #e6a23c;
}

.tip-highlight .tip-bullet {
  color: #e6a23c;
}

.tip-highlight span:last-child {
  color: #8b6b3d;
}

.dialog-footer {
  display: flex;
  justify-content: center;
  gap: 12px;
}

.oauth-iframe-container {
  width: 100%;
  height: 500px;
  overflow: hidden;
}

.oauth-iframe {
  width: 100%;
  height: 100%;
  border: none;
}

@media (max-width: 768px) {
  .cloud-accounts-page {
    padding: 0 8px 8px;
  }

  .account-dialog {
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - 32px);
  }

  .account-dialog :deep(.el-dialog__body) {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    overscroll-behavior: contain;
  }

  .account-dialog :deep(.el-dialog__header),
  .account-dialog :deep(.el-dialog__footer) {
    flex-shrink: 0;
  }

  .header-actions {
    width: max-content;
    justify-content: stretch;
  }

  .header-actions .add-btn {
    width: auto;
    background: #409eff !important;
    border-color: #409eff !important;
    color: #fff !important;
  }

  .header-actions .add-btn:hover {
    background: #66b1ff !important;
    border-color: #66b1ff !important;
    transform: none;
  }

  .accounts-content {
    padding: 0;
  }

  .accounts-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .account-card {
    border-radius: 12px;
  }

  .card-main {
    padding: 12px;
  }

  .card-header {
    margin-bottom: 12px;
    padding-bottom: 10px;
  }

  .card-id {
    font-size: 11px;
    padding: 2px 6px;
  }

  .card-name {
    font-size: 14px;
  }

  .source-tag {
    font-size: 11px;
  }

  .card-body {
    gap: 10px;
  }

  .info-row {
    gap: 10px;
  }

  .info-icon {
    width: 28px;
    height: 28px;
    font-size: 14px;
  }

  .info-label {
    font-size: 12px;
  }

  .info-value {
    font-size: 13px;
  }

  .path-value {
    font-size: 11px;
    max-width: 140px;
  }

  .status-row {
    margin-top: 6px;
    padding-top: 10px;
  }

  .status-indicator {
    padding: 5px 10px;
    font-size: 12px;
  }

  .disk-status-section {
    padding: 10px;
  }

  .status-header {
    margin-bottom: 10px;
  }

  .status-title {
    font-size: 12px;
  }

  .space-info {
    align-items: flex-start;
  }

  .space-text {
    font-size: 11px;
  }

  .card-footer {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 8px;
    padding-top: 12px;
    margin-top: 10px;
  }

  .card-footer .el-button {
    flex: none;
    min-width: 0;
    width: 100%;
    margin: 0;
  }

  .empty-state {
    padding: 40px 16px;
    border-radius: 12px;
  }

  .empty-icon {
    font-size: 60px;
  }

  .empty-title {
    font-size: 16px;
  }

  .empty-description {
    font-size: 13px;
    margin-bottom: 20px;
  }

  .page-footer-tips {
    border-radius: 12px;
  }

  .tips-header {
    padding: 12px 14px;
    font-size: 14px;
  }

  .tip-group {
    padding: 14px;
    border-right: none;
    border-bottom: 1px solid #f0f2f5;
  }

  .tip-group:last-child {
    border-bottom: none;
  }

  .tip-group-title {
    font-size: 14px;
    margin-bottom: 12px;
    padding-bottom: 8px;
  }

  .tip-group-items {
    gap: 8px;
  }

  .tip-item {
    font-size: 12px;
  }

  .tip-highlight {
    margin: 4px -8px;
    padding: 10px;
  }

  .oauth-iframe-container {
    height: calc(100vh - 120px);
    min-height: 400px;
  }
}

@media (max-width: 480px) {
  .info-content {
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
  }

  .info-value {
    text-align: left;
  }

  .path-value {
    max-width: 100%;
  }

  .card-footer {
    grid-template-columns: 1fr;
    gap: 6px;
  }
}
</style>
