<template>
  <div
    class="main-content-container file-manager-container full-width-container"
    ref="pageContainerRef"
  >
    <PageHeader />
    <el-card shadow="never" class="full-width-card">
      <!-- 左右布局 -->
      <div class="file-manager-layout">
        <!-- 左侧：网盘账号列表 -->
        <div class="account-sidebar">
          <div class="sidebar-header">
            <div class="sidebar-title-row">
              <h3>网盘账号</h3>
              <el-popover
                trigger="click"
                placement="bottom-start"
                :width="240"
                popper-class="file-manager-summary-popover"
              >
                <p class="file-manager-summary-popover-text">
                  浏览和管理媒体文件，支持 STRM 生成、移动、复制、重命名和删除操作
                </p>
                <template #reference>
                  <el-button
                    class="mobile-file-manager-info show-on-mobile"
                    link
                    :icon="InfoFilled"
                    aria-label="页面说明"
                  />
                </template>
              </el-popover>
            </div>
          </div>
          <div class="account-list">
            <div
              v-for="account in accountList"
              :key="account.id"
              :class="['account-item', { active: selectedAccountId === account.id }]"
              @click="selectAccount(account)"
            >
              <div class="account-info">
                <el-icon class="account-icon">
                  <component :is="getAccountIcon()" />
                </el-icon>
                <div class="account-details">
                  <div class="account-name">
                    {{ account.username }}
                    <span v-if="account.source_type === '115'">({{ account.user_id }})</span>
                  </div>
                  <div class="account-type">{{ getAccountTypeName(account.source_type) }}</div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- 右侧：文件列表 -->
        <div class="file-content">
          <!-- 未选择账号时的提示 -->
          <div v-if="!selectedAccountId" class="no-account-selected">
            <el-empty description="选择一个网盘账号" />
          </div>

          <!-- 文件列表内容 -->
          <template v-else>
            <!-- 面包屑导航 -->
            <div class="file-manager-toolbar">
              <el-breadcrumb separator="/">
                <el-breadcrumb-item @click="navigateToPath(-1)" style="cursor: pointer"
                  >根目录</el-breadcrumb-item
                >
                <el-breadcrumb-item
                  v-for="(item, index) in pathItems"
                  :key="item.id"
                  @click="navigateToPath(index)"
                  style="cursor: pointer"
                >
                  {{ item.name }}
                </el-breadcrumb-item>
              </el-breadcrumb>
              <div class="file-manager-toolbar-actions">
                <el-checkbox
                  v-model="batchMode"
                  class="file-manager-batch-toggle"
                  :disabled="!selectedAccountId"
                >
                  批量操作
                </el-checkbox>
                <template v-if="isFileManagerSortControlVisible && supportedSortFields.length > 1">
                  <el-select
                    v-model="sortBy"
                    class="file-manager-sort-field"
                    size="small"
                    @change="handleSortChange"
                  >
                    <el-option
                      v-for="field in supportedSortFields"
                      :key="field"
                      :label="getSortFieldLabel(field)"
                      :value="field"
                    />
                  </el-select>
                  <el-select
                    v-model="sortOrder"
                    class="file-manager-sort-order"
                    size="small"
                    @change="handleSortChange"
                  >
                    <el-option label="升序" value="asc" />
                    <el-option label="降序" value="desc" />
                  </el-select>
                </template>
                <el-button
                  :icon="Refresh"
                  size="small"
                  :loading="isRefreshing"
                  :disabled="!selectedAccountId"
                  @click="handleRefreshFileList"
                >
                  刷新
                </el-button>
                <el-button
                  type="primary"
                  :icon="FolderAdd"
                  size="small"
                  @click="openCreateDialog"
                  :disabled="!selectedAccountId"
                >
                  新建文件夹
                </el-button>
              </div>
            </div>

            <!-- 批量操作栏 -->
            <div v-if="batchMode" class="file-manager-batch-bar">
              <span class="file-manager-batch-summary">已选 {{ selectedFileItems.length }} 项</span>
              <div class="file-manager-batch-actions">
                <el-button
                  size="small"
                  :disabled="batchOperateLoading"
                  @click="toggleSelectAllFiles"
                >
                  全选
                </el-button>
                <el-button
                  size="small"
                  type="primary"
                  :disabled="selectedFileItems.length === 0 || batchOperateLoading"
                  @click="openBatchTargetDialog('move')"
                >
                  移动
                </el-button>
                <el-button
                  size="small"
                  :disabled="selectedFileItems.length === 0 || batchOperateLoading"
                  @click="openBatchTargetDialog('copy')"
                >
                  复制
                </el-button>
                <el-button
                  size="small"
                  :disabled="selectedFileItems.length === 0 || batchOperateLoading"
                  @click="openBatchStrmDialog"
                >
                  STRM 生成
                </el-button>
                <el-button
                  size="small"
                  type="danger"
                  :disabled="selectedFileItems.length === 0 || batchOperateLoading"
                  @click="handleBatchDelete"
                >
                  删除
                </el-button>
                <el-button size="small" :disabled="batchOperateLoading" @click="exitBatchMode">
                  退出批量
                </el-button>
              </div>
            </div>

            <!-- 桌面端表格 -->
            <el-table
              v-if="!isMobile"
              ref="fileTableRef"
              v-loading="initialLoading"
              :data="fileList"
              :row-key="(row: FileSystemItem) => String(row.id || row.path)"
              style="width: 100%"
              @row-dblclick="handleRowDoubleClick"
              @selection-change="handleFileSelectionChange"
            >
              <el-table-column v-if="batchMode" type="selection" width="42" reserve-selection />
              <el-table-column label="名称" min-width="300">
                <template #default="{ row }">
                  <div style="display: flex; align-items: center; gap: 8px">
                    <el-icon :size="18">
                      <component :is="getFileIconByName(row.name, row.is_directory)" />
                    </el-icon>
                    <span>{{ row.name }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="大小" width="120" align="right">
                <template #default="{ row }">
                  <span v-if="!row.is_directory">{{ formatFileSize(row.size) }}</span>
                  <span v-else>--</span>
                </template>
              </el-table-column>
              <el-table-column label="修改时间" width="180">
                <template #default="{ row }">
                  {{ formatDateTime(row.modified_time) }}
                </template>
              </el-table-column>
              <el-table-column label="操作" width="120" align="center">
                <template #default="{ row }">
                  <el-dropdown
                    trigger="click"
                    @command="
                      (command: string) => handleSingleOperation(command as FileOperationType, row)
                    "
                  >
                    <el-button type="primary" size="small">
                      操作 <el-icon class="el-icon--right"><arrow-down /></el-icon>
                    </el-button>
                    <template #dropdown>
                      <el-dropdown-menu>
                        <el-dropdown-item command="STRM_GENERATE">STRM 生成</el-dropdown-item>
                        <!--
                          刮削整理与生成 ED2K 尚未实装（点击后仅提示“功能开发中”），
                          暂时隐藏入口避免误导用户；功能实装后恢复以下两个菜单项。
                          处理分支保留在 handleSingleOperation 中，勿直接删除。
                        -->
                        <!-- <el-dropdown-item command="SCRAPE_ORGANIZE">刮削整理</el-dropdown-item> -->
                        <!--
                        <el-dropdown-item
                          v-if="
                            !row.is_directory &&
                            (getFileType(row.name) === 'video' || getFileType(row.name) === 'image')
                          "
                          command="GENERATE_ED2K"
                        >
                          生成 ED2K
                        </el-dropdown-item>
                        -->
                        <el-dropdown-item command="MOVE">移动</el-dropdown-item>
                        <el-dropdown-item command="COPY">复制</el-dropdown-item>
                        <el-dropdown-item command="RENAME">重命名</el-dropdown-item>
                        <el-dropdown-item command="DELETE" divided>删除</el-dropdown-item>
                      </el-dropdown-menu>
                    </template>
                  </el-dropdown>
                </template>
              </el-table-column>
            </el-table>

            <!-- 移动端表格 -->
            <el-table
              v-else
              ref="fileTableRef"
              v-loading="initialLoading"
              :data="fileList"
              :row-key="(row: FileSystemItem) => String(row.id || row.path)"
              :expand-row-keys="pageState.expandedRowKeys"
              @expand-change="handleExpandChange"
              style="width: 100%"
              @row-dblclick="handleRowDoubleClick"
              @selection-change="handleFileSelectionChange"
            >
              <el-table-column v-if="batchMode" type="selection" width="42" reserve-selection />
              <el-table-column type="expand" width="30">
                <template #default="{ row }">
                  <div style="padding: 0 20px">
                    <p>
                      <strong>大小：</strong
                      >{{ row.is_directory ? '--' : formatFileSize(row.size) }}
                    </p>
                    <p><strong>修改时间：</strong>{{ formatDateTime(row.modified_time) }}</p>
                    <div class="file-manager-row-actions">
                      <el-button
                        size="small"
                        type="primary"
                        @click="handleSingleOperation('STRM_GENERATE', row)"
                      >
                        STRM 生成
                      </el-button>
                      <!--
                        刮削整理与生成 ED2K 尚未实装，暂时隐藏入口；功能实装后恢复以下按钮，
                        处理分支保留在 handleSingleOperation 中，勿直接删除。
                      -->
                      <!-- <el-button
                        size="small"
                        type="success"
                        @click="handleSingleOperation('SCRAPE_ORGANIZE', row)"
                      >
                        刮削整理
                      </el-button> -->
                      <!-- <el-button
                        v-if="
                          !row.is_directory &&
                          (getFileType(row.name) === 'video' || getFileType(row.name) === 'image')
                        "
                        size="small"
                        type="warning"
                        @click="handleSingleOperation('GENERATE_ED2K', row)"
                      >
                        生成 ED2K
                      </el-button> -->
                      <el-button size="small" @click="handleSingleOperation('MOVE', row)">
                        移动
                      </el-button>
                      <el-button size="small" @click="handleSingleOperation('COPY', row)">
                        复制
                      </el-button>
                      <el-button size="small" @click="handleSingleOperation('RENAME', row)">
                        重命名
                      </el-button>
                      <el-button
                        size="small"
                        type="danger"
                        @click="handleSingleOperation('DELETE', row)"
                      >
                        删除
                      </el-button>
                    </div>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="文件">
                <template #default="{ row }">
                  <div style="display: flex; align-items: center; gap: 8px">
                    <el-icon :size="18">
                      <component :is="getFileIconByName(row.name, row.is_directory)" />
                    </el-icon>
                    <span>{{ row.name }}</span>
                  </div>
                </template>
              </el-table-column>
            </el-table>

            <!-- 空状态 -->
            <el-empty v-if="!initialLoading && fileList.length === 0" description="当前目录为空" />

            <!-- 分页器 -->
            <ResponsivePagination
              v-model:current-page="currentPage"
              v-model:page-size="pageSize"
              :page-sizes="[50, 100, 200, 500]"
              :total="total"
              :is-mobile="isMobile"
              @size-change="handlePageSizeChange"
              @current-change="handlePageChange"
            />
          </template>
        </div>
      </div>
    </el-card>

    <el-dialog
      v-model="showCreateDialog"
      title="新建文件夹"
      width="400px"
      :close-on-click-modal="false"
      @closed="resetCreateDirectoryDialog"
    >
      <el-form ref="createFormRef" :model="createForm" :rules="createRules" label-width="80px">
        <el-form-item label="文件夹名称" prop="name">
          <el-input v-model="createForm.name" placeholder="请输入文件夹名称" clearable />
        </el-form-item>
      </el-form>
      <template #footer>
        <span class="dialog-footer">
          <el-button @click="resetCreateDirectoryDialog">取消</el-button>
          <el-button type="primary" @click="handleCreateDirectory" :loading="createLoading">
            确定
          </el-button>
        </span>
      </template>
    </el-dialog>

    <el-dialog
      v-model="showStrmTargetDialog"
      :title="strmTargetDialogTitle"
      width="600px"
      :close-on-click-modal="false"
      @closed="resetStrmTargetDialog"
    >
      <div class="strm-target-dialog-content">
        <p class="dialog-tip">请选择 STRM 文件的目标存放目录：</p>
        <div v-if="strmSourceItems.length > 0" class="strm-source-info">
          <span class="source-label">{{ strmSourceInfoLabel }}：</span>
          <span class="source-name">{{ strmSourceInfoText }}</span>
        </div>
        <div class="dir-selector-container">
          <DirectorySelector
            v-model="strmTargetDir"
            source-type="local"
            :reset-on-select="false"
            @cancel="resetStrmTargetDialog"
            @select="confirmStrmGenerate"
          />
        </div>
        <div v-if="strmStorePath" class="strm-store-path">
          <span class="store-label">STRM 存放路径：</span>
          <code class="store-path">{{ strmStorePath }}</code>
        </div>
      </div>
    </el-dialog>

    <el-dialog
      v-model="showFileTargetDialog"
      :title="fileTargetDialogTitle"
      width="min(600px, calc(100vw - 32px))"
      destroy-on-close
      :close-on-click-modal="false"
      :before-close="handleFileTargetDialogClose"
      @closed="resetFileTargetDialog"
    >
      <div class="file-target-dialog-content">
        <p class="dialog-tip">{{ fileTargetDialogTip }}</p>
        <el-button
          :loading="batchOperateLoading"
          :disabled="!isFileTargetOperationContextCurrent(fileTargetOperationContext)"
          @click="confirmFileRootTargetOperation"
        >
          {{ fileTargetOperation === 'copy' ? '复制到根目录' : '移动到根目录' }}
        </el-button>
        <div class="dir-selector-container">
          <DirectorySelector
            v-model="fileTargetDir"
            :source-type="fileTargetSourceType"
            :account-id="fileTargetAccountId"
            :reset-on-select="false"
            @cancel="handleFileTargetDialogClose"
            @select="confirmFileTargetOperation"
          />
        </div>
      </div>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import {
  ref,
  computed,
  onMounted,
  onActivated,
  onDeactivated,
  onUnmounted,
  useTemplateRef,
  watch,
} from 'vue'
import {
  ElMessage,
  ElMessageBox,
  type FormInstance,
  type FormRules,
  type TableInstance,
} from 'element-plus'
import { ArrowDown, Files, FolderAdd, InfoFilled, Refresh } from '@element-plus/icons-vue'
import type { FileSystemItem, FileOperationType, DirInfo } from '@/typing'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { useBackgroundRefresh } from '@/composables/useBackgroundRefresh'
import { useDeviceType } from '@/composables/useDeviceType'
import { useHttpClient } from '@/http/client'
import { notifyHttpError } from '@/utils/httpErrorNotification'
import { accountPublicMessages, listAccounts } from '@/api/accounts'
import { usePageScrollRestore } from '@/composables/usePageScrollRestore'
import { mergeStableList, retainExistingKeys } from '@/composables/useStableList'
import { usePageStateStore } from '@/stores/pageState'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import { getFileType, getFileIconByName } from '@/utils/fileIconUtils'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatDateTime } from '@/utils/timeUtils'
import {
  copyFiles,
  createDirectory,
  deleteFile,
  deleteFiles,
  fetchFiles,
  filePublicMessages,
  generateManualStrm,
  moveFiles,
  renameFile,
  type NetFileListCacheMeta,
  type NetFileListPayload,
  type NetFileListQuery,
  type NetFileSortBy,
  type NetFileSortOrder,
} from '@/api/files'
import PageHeader from '@/components/common/PageHeader.vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import DirectorySelector from './DirectorySelector.vue'

interface NetdiskAccount {
  id: number
  name: string
  username: string
  user_id: string
  source_type: '115' | '123' | 'openlist' | 'baidupan'
  authorized: boolean
  created_at: number
  base_url?: string
  password?: string
  app_id_name?: string
  app_id?: string
  token_failed_reason?: string
}

interface LoadFileListOptions {
  refresh?: boolean
}

const netFileSortFields = ['default', 'name', 'time', 'size', 'type'] as const
const netFileSortOrders = ['asc', 'desc'] as const
const fileManagerPageSizes = [50, 100, 200, 500] as const
// 115 Open API 当前返回顺序与排序参数不一致，OpenList 也只使用默认顺序；
// 排序控件先整体隐藏，待后端全量排序视图缓存完成后再恢复。
const isFileManagerSortControlVisible = false

function isNetFileSortBy(value: unknown): value is NetFileSortBy {
  return typeof value === 'string' && netFileSortFields.includes(value as NetFileSortBy)
}

function isNetFileSortOrder(value: unknown): value is NetFileSortOrder {
  return typeof value === 'string' && netFileSortOrders.includes(value as NetFileSortOrder)
}

function isNetFileListCacheMeta(value: unknown): value is NetFileListCacheMeta {
  if (typeof value !== 'object' || value === null) {
    return false
  }
  const meta = value as Record<string, unknown>
  return (
    ['hit', 'miss', 'partial_hit', 'refresh'].includes(String(meta.status)) &&
    typeof meta.batch_start === 'number' &&
    typeof meta.batch_size === 'number' &&
    typeof meta.cached_at === 'number' &&
    typeof meta.expires_at === 'number'
  )
}

function normalizeNetFileListPayload(
  data: unknown,
  fallback: {
    page: number
    pageSize: number
    sortBy: NetFileSortBy
    sortOrder: NetFileSortOrder
  },
): NetFileListPayload {
  if (Array.isArray(data)) {
    return {
      list: data as FileSystemItem[],
      total: data.length,
      total_exact: true,
      has_more: false,
      page: fallback.page,
      page_size: fallback.pageSize,
      sort_by: fallback.sortBy,
      sort_order: fallback.sortOrder,
    }
  }

  if (typeof data !== 'object' || data === null) {
    return {
      list: [],
      total: 0,
      total_exact: true,
      has_more: false,
      page: fallback.page,
      page_size: fallback.pageSize,
      sort_by: fallback.sortBy,
      sort_order: fallback.sortOrder,
    }
  }

  const payload = data as Record<string, unknown>
  const list = Array.isArray(payload.list) ? (payload.list as FileSystemItem[]) : []
  const total = typeof payload.total === 'number' ? payload.total : list.length

  return {
    list,
    total: Math.max(total, list.length),
    total_exact: typeof payload.total_exact === 'boolean' ? payload.total_exact : true,
    has_more: typeof payload.has_more === 'boolean' ? payload.has_more : total > list.length,
    page: typeof payload.page === 'number' ? payload.page : fallback.page,
    page_size: typeof payload.page_size === 'number' ? payload.page_size : fallback.pageSize,
    sort_by: isNetFileSortBy(payload.sort_by) ? payload.sort_by : fallback.sortBy,
    sort_order: isNetFileSortOrder(payload.sort_order) ? payload.sort_order : fallback.sortOrder,
    cache: isNetFileListCacheMeta(payload.cache) ? payload.cache : undefined,
  }
}

// 响应式数据
const pageStateStore = usePageStateStore()
const pageState = pageStateStore.getPageState('file-manager', {
  currentPage: 1,
  pageSize: 50,
  filters: {
    currentPath: '',
    pathItems: '[]',
    selectedAccountId: null,
    sortBy: 'name',
    sortOrder: 'asc',
  },
})
if (!fileManagerPageSizes.includes(pageState.pageSize as (typeof fileManagerPageSizes)[number])) {
  pageStateStore.setPagination('file-manager', pageState.currentPage, 50)
}
const { initialLoading, isRefreshing, runRefresh } = useBackgroundRefresh()
const pageContainerRef = useTemplateRef<HTMLElement>('pageContainerRef')
const getPageScrollContainer = () =>
  pageContainerRef.value?.closest<HTMLElement>('.main-content') ?? pageContainerRef.value
const currentPath = computed({
  get: () => String(pageState.filters.currentPath ?? ''),
  set: (value) => pageStateStore.setFilter('file-manager', 'currentPath', value),
})
const currentPage = computed({
  get: () => pageState.currentPage,
  set: (value) => pageStateStore.setPagination('file-manager', value, pageState.pageSize),
})
const pageSize = computed({
  get: () => pageState.pageSize,
  set: (value) => pageStateStore.setPagination('file-manager', pageState.currentPage, value),
})
const total = ref(0)
const fileList = ref<FileSystemItem[]>([])
const { isMobile } = useDeviceType()

const http = useHttpClient()
const accountList = ref<NetdiskAccount[]>([])
const selectedAccountId = computed<number | null>({
  get: () => {
    const value = pageState.filters.selectedAccountId
    return typeof value === 'number' ? value : null
  },
  set: (value) => pageStateStore.setFilter('file-manager', 'selectedAccountId', value),
})
const selectedAccount = computed(() =>
  accountList.value.find((account) => account.id === selectedAccountId.value),
)
const supportedSortFields = computed(() =>
  getSupportedSortFields(selectedAccount.value?.source_type),
)
const defaultSortByForSelectedAccount = computed<NetFileSortBy>(() => {
  const fields = supportedSortFields.value
  return fields[0] ?? 'name'
})
const sortBy = computed<NetFileSortBy>({
  get: () => {
    const stored = pageState.filters.sortBy
    if (isNetFileSortBy(stored) && supportedSortFields.value.includes(stored)) {
      return stored
    }
    return defaultSortByForSelectedAccount.value
  },
  set: (value) => pageStateStore.setFilter('file-manager', 'sortBy', value),
})
const sortOrder = computed<NetFileSortOrder>({
  get: () =>
    isNetFileSortOrder(pageState.filters.sortOrder) ? pageState.filters.sortOrder : 'asc',
  set: (value) => pageStateStore.setFilter('file-manager', 'sortOrder', value),
})
const pendingFileListRefresh = ref<LoadFileListOptions | null>(null)
let isPageActive = false
const accountListRequestGate = createActiveRequestGate(() => isPageActive)
const fileListRequestGate = createActiveRequestGate(() => isPageActive)

const showCreateDialog = ref(false)
const createLoading = ref(false)
const createFormRef = useTemplateRef<FormInstance>('createFormRef')
const createForm = ref({ name: '' })
const createRules = ref<FormRules>({
  name: [
    { required: true, message: '请输入文件夹名称', trigger: 'blur' },
    { min: 1, max: 255, message: '文件夹名称长度在 1 到 255 个字符', trigger: 'blur' },
  ],
})

const showStrmTargetDialog = ref(false)
const strmTargetDir = ref<DirInfo | null>(null)
// 提交目标在打开弹窗时快照；单项为单个生成，多项为批量逐项提交。
const strmSourceItems = ref<FileSystemItem[]>([])
const strmOperationContext = ref<FileOperationContextSnapshot | null>(null)
const createDirectoryOperationContext = ref<FileOperationContextSnapshot | null>(null)
const strmGenerateLoading = ref(false)
const isBatchStrmOperation = ref(false)
const contextVersion = ref(0)

type BatchFileTargetOperation = 'move' | 'copy'

const fileTableRef = useTemplateRef<TableInstance>('fileTableRef')
const batchMode = ref(false)
const selectedFileItems = ref<FileSystemItem[]>([])
const batchOperateLoading = ref(false)
let batchDeleteConfirmationPending = false
const showFileTargetDialog = ref(false)
const fileTargetOperation = ref<BatchFileTargetOperation | null>(null)
const fileTargetDir = ref<DirInfo | null>(null)
const fileTargetOperationContext = ref<FileOperationContextSnapshot | null>(null)
const singleTargetItem = ref<FileSystemItem | null>(null)
// 目录选择器的来源信息在打开弹窗时快照，关闭弹窗后保持不变：
// DirectorySelector 监听 sourceType/accountId 变化会重新加载目录，
// 若随弹窗关闭一起重置会以空 source_type 发出无效请求并误报“未知的同步源类型”。
const fileTargetSourceType = ref('')
const fileTargetAccountId = ref(0)

const fileTargetDialogTitle = computed(() => {
  const action = fileTargetOperation.value === 'copy' ? '复制' : '移动'
  return singleTargetItem.value ? `${action}到` : `批量${action}`
})
const fileTargetDialogTip = computed(() => {
  const action = fileTargetOperation.value === 'copy' ? '复制' : '移动'
  if (singleTargetItem.value) {
    return `请选择“${singleTargetItem.value.name}”的${action}目标目录：`
  }
  return `请选择${action} ${selectedFileItems.value.length} 项的目标目录：`
})

const strmTargetDialogTitle = computed(() =>
  strmSourceItems.value.length > 1 ? '批量 STRM 生成' : '选择 STRM 目标目录',
)
const strmSourceInfoLabel = computed(() => (strmSourceItems.value.length > 1 ? '已选' : '源文件'))
const strmSourceInfoText = computed(() => {
  if (strmSourceItems.value.length === 1) {
    return strmSourceItems.value[0].name
  }
  return `${strmSourceItems.value.length} 项`
})

interface FileOperationContextSnapshot {
  accountId: number | null
  parentId: string
  parentPath: string
  sourceType: NetdiskAccount['source_type'] | null
  contextVersion: number
}

function parseStoredPathItems(): FileSystemItem[] {
  const value = pageState.filters.pathItems
  if (typeof value !== 'string' || !value) {
    return []
  }

  try {
    const items = JSON.parse(value)
    if (!Array.isArray(items)) {
      return []
    }

    return items
      .filter((item): item is Record<string, unknown> => typeof item === 'object' && item !== null)
      .filter((item) => typeof item.name === 'string')
      .map((item) => ({
        id: typeof item.id === 'string' ? item.id : String(item.id ?? ''),
        name: item.name as string,
        path: typeof item.path === 'string' ? item.path : (item.name as string),
        type: item.type === 'directory' ? 'directory' : getFileType(item.name as string),
        size: typeof item.size === 'number' ? item.size : 0,
        modified_time: typeof item.modified_time === 'number' ? item.modified_time : 0,
        is_directory: item.is_directory === true,
      }))
  } catch {
    return []
  }
}

const pathItems = ref<FileSystemItem[]>(parseStoredPathItems())

function setPathItems(items: FileSystemItem[]) {
  pathItems.value = items
  currentPath.value = items.map((item) => item.name).join('/')
  pageStateStore.setFilter('file-manager', 'pathItems', JSON.stringify(items))
}

function getCurrentParentId() {
  return pathItems.value.length > 0 ? pathItems.value[pathItems.value.length - 1].id : ''
}

function getCurrentParentPath() {
  return pathItems.value.length > 0 ? pathItems.value[pathItems.value.length - 1].path : ''
}

function getSelectedAccount() {
  return selectedAccount.value
}

function createFileOperationContextSnapshot(): FileOperationContextSnapshot {
  const account = getSelectedAccount()

  return {
    accountId: selectedAccountId.value,
    parentId: getCurrentParentId(),
    parentPath: getCurrentParentPath(),
    sourceType: account?.source_type ?? null,
    contextVersion: contextVersion.value,
  }
}

function isFileOperationContextCurrent(
  snapshot: FileOperationContextSnapshot | null,
): snapshot is FileOperationContextSnapshot {
  return (
    isPageActive &&
    !!snapshot &&
    snapshot.accountId === selectedAccountId.value &&
    snapshot.parentId === getCurrentParentId() &&
    snapshot.contextVersion === contextVersion.value
  )
}

function isStrmOperationContextCurrent(
  snapshot: FileOperationContextSnapshot | null,
): snapshot is FileOperationContextSnapshot {
  return strmOperationContext.value === snapshot && isFileOperationContextCurrent(snapshot)
}

function isCreateDirectoryOperationContextCurrent(
  snapshot: FileOperationContextSnapshot | null,
): snapshot is FileOperationContextSnapshot {
  return (
    createDirectoryOperationContext.value === snapshot && isFileOperationContextCurrent(snapshot)
  )
}

function isFileTargetOperationContextCurrent(
  snapshot: FileOperationContextSnapshot | null,
): snapshot is FileOperationContextSnapshot {
  return fileTargetOperationContext.value === snapshot && isFileOperationContextCurrent(snapshot)
}

function resetStrmTargetDialog() {
  showStrmTargetDialog.value = false
  strmSourceItems.value = []
  strmTargetDir.value = null
  strmOperationContext.value = null
  strmGenerateLoading.value = false
  isBatchStrmOperation.value = false
}

function resetCreateDirectoryDialog() {
  showCreateDialog.value = false
  createForm.value.name = ''
  createDirectoryOperationContext.value = null
  createLoading.value = false
}

function resetFileTargetDialog() {
  showFileTargetDialog.value = false
  fileTargetOperation.value = null
  fileTargetDir.value = null
  fileTargetOperationContext.value = null
  singleTargetItem.value = null
}

function handleFileTargetDialogClose(done?: () => void) {
  if (batchOperateLoading.value) return
  done?.()
  resetFileTargetDialog()
}

function invalidateFileOperationContext() {
  contextVersion.value += 1
  resetStrmTargetDialog()
  resetCreateDirectoryDialog()
  resetFileTargetDialog()
}

function clearFileSelection() {
  selectedFileItems.value = []
  fileTableRef.value?.clearSelection?.()
}

function handleFileSelectionChange(rows: FileSystemItem[]) {
  selectedFileItems.value = rows
}

function toggleSelectAllFiles() {
  fileTableRef.value?.toggleAllSelection?.()
}

function exitBatchMode() {
  batchMode.value = false
}

watch(batchMode, (enabled) => {
  if (!enabled) {
    clearFileSelection()
  }
})

watch(isMobile, () => {
  clearFileSelection()
  if (batchDeleteConfirmationPending) {
    batchDeleteConfirmationPending = false
    ElMessageBox.close()
  }
  if (!batchOperateLoading.value && !singleTargetItem.value) {
    resetFileTargetDialog()
  }
  if (!strmGenerateLoading.value && isBatchStrmOperation.value) {
    resetStrmTargetDialog()
  }
})

function clearFileListForContextSwitch() {
  invalidateFileOperationContext()
  fileListRequestGate.invalidate()
  fileList.value = []
  total.value = 0
  clearFileSelection()
  pageStateStore.setExpandedRowKeys('file-manager', [])
}

function clearFileListForPageChange() {
  fileListRequestGate.invalidate()
  fileList.value = []
  clearFileSelection()
  pageStateStore.setExpandedRowKeys('file-manager', [])
}

// 计算属性
// 存放路径预览只在单个生成时展示；批量提交逐项解析路径，预览没有意义。
const strmStorePath = computed(() => {
  if (!strmTargetDir.value || strmSourceItems.value.length !== 1) return ''
  const sourceName = strmSourceItems.value[0].name
  const currentPathStr = pathItems.value.map((p) => p.name).join('/')
  const itemPath = currentPathStr ? `${currentPathStr}/${sourceName}` : sourceName
  return `${strmTargetDir.value.path}/${itemPath}`
})

// const isMobileDevice = computed(() => isMobile())

// 加载网盘账号列表
async function loadAccountList() {
  const requestId = accountListRequestGate.next()

  if (!accountListRequestGate.isCurrent(requestId)) {
    return
  }

  try {
    const data = await listAccounts(http)

    if (!accountListRequestGate.isCurrent(requestId)) {
      return
    }

    accountList.value = data.map((item) => ({
      id: item.id,
      name: item.name,
      username: item.username,
      user_id: item.user_id,
      source_type: item.source_type,
      authorized: item.authorized,
      created_at: item.created_at,
      base_url: item.base_url,
      password: item.password,
      app_id_name: item.app_id_name,
      app_id: item.app_id,
      token_failed_reason: item.token_failed_reason || '',
    }))

    if (
      selectedAccountId.value &&
      !accountList.value.some((account) => account.id === selectedAccountId.value)
    ) {
      selectedAccountId.value = null
      setPathItems([])
      clearFileListForContextSwitch()
    }
  } catch (error) {
    if (!accountListRequestGate.isCurrent(requestId)) {
      return
    }
    notifyHttpError(error, '加载账号列表失败：', {
      publicMessages: accountPublicMessages,
      fallbackMessage: '加载账号列表失败',
    })
    accountList.value = []
  }
}

// 选择账号
function selectAccount(account: NetdiskAccount) {
  selectedAccountId.value = account.id
  setPathItems([])
  pageStateStore.setPagination('file-manager', 1, pageState.pageSize)
  loadFileListForContextSwitch()
}

// 获取账号图标
function getAccountIcon() {
  return Files
}

// 获取账号类型名称
function getAccountTypeName(sourceType: string): string {
  switch (sourceType) {
    case '115':
      return '115 网盘'
    case '123':
      return '123 网盘'
    case 'openlist':
      return 'OpenList'
    case 'baidupan':
      return '百度网盘'
    default:
      return '其他'
  }
}

function getSupportedSortFields(sourceType?: NetdiskAccount['source_type']): NetFileSortBy[] {
  switch (sourceType) {
    case '115':
      return ['name', 'size', 'time', 'type']
    case 'baidupan':
      return ['name', 'size', 'time']
    case 'openlist':
      return ['default']
    default:
      return ['name']
  }
}

function getSortFieldLabel(field: NetFileSortBy): string {
  switch (field) {
    case 'default':
      return '默认'
    case 'name':
      return '名称'
    case 'time':
      return '时间'
    case 'size':
      return '大小'
    case 'type':
      return '类型'
    default:
      return field
  }
}

function reportFileError(error: unknown, fallbackMessage: string, isRead = false) {
  notifyHttpError(error, fallbackMessage, {
    fallbackMessage,
    publicMessages: filePublicMessages,
    messagePrefix: isRead ? fallbackMessage : undefined,
  })
}

// 加载文件列表
async function loadFileList(options: LoadFileListOptions = {}) {
  if (!isPageActive) {
    return
  }

  const requestId = fileListRequestGate.next()

  if (isRefreshing.value) {
    pendingFileListRefresh.value = {
      refresh: pendingFileListRefresh.value?.refresh === true || options.refresh === true,
    }
    return
  }

  if (!selectedAccountId.value) {
    fileList.value = []
    total.value = 0
    pageStateStore.setExpandedRowKeys('file-manager', [])
    return
  }

  try {
    await runRefresh(async () => {
      const accountId = selectedAccountId.value
      if (!accountId) {
        return
      }

      const currentItemId =
        pathItems.value.length > 0 ? pathItems.value[pathItems.value.length - 1].id : ''

      const requestParams: NetFileListQuery = {
        account_id: accountId,
        path: currentItemId,
        page: currentPage.value,
        page_size: pageSize.value,
        refresh: options.refresh ? 1 : 0,
      }
      if (isFileManagerSortControlVisible) {
        requestParams.sort_by = sortBy.value
        requestParams.sort_order = sortOrder.value
      }

      const data = await fetchFiles(http, requestParams)

      if (!fileListRequestGate.isCurrent(requestId)) {
        return
      }

      const {
        list: items,
        total: responseTotal,
        sort_by: responseSortBy,
        sort_order: responseSortOrder,
      } = normalizeNetFileListPayload(data, {
        page: currentPage.value,
        pageSize: pageSize.value,
        sortBy: sortBy.value,
        sortOrder: sortOrder.value,
      })

      const pageStart = (currentPage.value - 1) * pageSize.value
      if (responseTotal > 0 && pageStart >= responseTotal) {
        clearFileSelection()
        pageStateStore.setPagination('file-manager', 1, pageSize.value)
        await loadFileList({ refresh: options.refresh })
        return
      }

      const rows = items.map((item: FileSystemItem) => ({
        id: item.id,
        name: item.name,
        path: currentPath.value ? `${currentPath.value}/${item.name}` : item.name,
        type: item.is_directory ? 'directory' : getFileType(item.name),
        size: item.size,
        modified_time: item.modified_time,
        is_directory: item.is_directory,
      }))

      fileList.value = mergeStableList(fileList.value, rows, (row) => row.id || row.path)
      const existingIds = new Set(fileList.value.map((row) => row.id || row.path))
      for (const selected of selectedFileItems.value) {
        if (!existingIds.has(selected.id || selected.path)) {
          fileTableRef.value?.toggleRowSelection(selected, false)
        }
      }
      pageStateStore.setExpandedRowKeys(
        'file-manager',
        retainExistingKeys(pageState.expandedRowKeys, fileList.value, (row) => row.id || row.path),
      )
      total.value = responseTotal
      sortBy.value = responseSortBy
      sortOrder.value = responseSortOrder
    })
  } catch (error) {
    if (!fileListRequestGate.isCurrent(requestId)) {
      return
    }
    reportFileError(error, '加载文件列表失败', true)
  } finally {
    if (pendingFileListRefresh.value && isPageActive) {
      const pendingOptions = pendingFileListRefresh.value
      pendingFileListRefresh.value = null
      await loadFileList(pendingOptions)
    }
  }
}

function loadFileListForContextSwitch() {
  clearFileListForContextSwitch()
  loadFileList()
}

function loadFileListForPageChange() {
  clearFileListForPageChange()
  loadFileList()
}

async function handleRefreshFileList() {
  clearFileSelection()
  await loadFileList({ refresh: true })
}

function handleSortChange() {
  pageStateStore.setPagination('file-manager', 1, pageState.pageSize)
  loadFileListForContextSwitch()
}

// 导航到指定路径
function navigateToPath(index: number) {
  setPathItems(pathItems.value.slice(0, index + 1))
  pageStateStore.setPagination('file-manager', 1, pageState.pageSize)
  loadFileListForContextSwitch()
}

// 处理行双击事件（进入目录）
function handleRowDoubleClick(row: FileSystemItem) {
  if (row.is_directory) {
    setPathItems([...pathItems.value, row])
    pageStateStore.setPagination('file-manager', 1, pageState.pageSize)
    loadFileListForContextSwitch()
  }
}

const handleExpandChange = (row: FileSystemItem, expandedRows: FileSystemItem[]) => {
  pageStateStore.setExpandedRowKeys(
    'file-manager',
    expandedRows.map((item) => String(item.id || item.path)),
  )
}

// 处理分页大小变化
function handlePageSizeChange(newSize: number) {
  pageStateStore.setPagination('file-manager', 1, newSize)
  loadFileListForPageChange()
}

// 处理页码变化
function handlePageChange(newPage: number) {
  pageStateStore.setPagination('file-manager', newPage, pageState.pageSize)
  loadFileListForPageChange()
}

// 处理单个操作
async function handleSingleOperation(operation: FileOperationType, item: FileSystemItem) {
  if (operation === 'DELETE') {
    await handleDeleteItem(item)
    return
  }

  if (operation === 'RENAME') {
    await handleRenameItem(item)
    return
  }

  if (operation === 'MOVE' || operation === 'COPY') {
    openSingleTargetDialog(operation === 'COPY' ? 'copy' : 'move', item)
    return
  }

  if (operation === 'STRM_GENERATE') {
    openStrmTargetDialog([item])
    return
  }

  try {
    const operationMap = {
      STRM_GENERATE: 'STRM 生成',
      SCRAPE_ORGANIZE: '刮削整理',
      GENERATE_ED2K: '生成 ED2K',
    }

    await ElMessageBox.confirm(
      `确认对文件“${item.name}”执行 ${operationMap[operation]} 操作吗？`,
      '确认操作',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    ElMessage.info(`${operationMap[operation]} 功能开发中…`)
  } catch {}
}

async function handleDeleteItem(item: FileSystemItem) {
  const operationContext = createFileOperationContextSnapshot()

  try {
    await ElMessageBox.confirm(
      `确认删除“${item.name}”吗？${item.is_directory ? '文件夹内的所有内容也将被删除。' : ''}`,
      '确认删除',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!operationContext.accountId) {
      ElMessage.warning('请先选择网盘账号')
      return
    }

    await deleteFile(http, {
      parent_id: operationContext.parentId,
      file_id: item.id,
      account_id: operationContext.accountId,
    })

    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.success('删除成功')
    await loadFileList({ refresh: true })
  } catch (error) {
    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!isMessageBoxCancelError(error)) {
      reportFileError(error, '删除失败')
    }
  }
}

async function handleRenameItem(item: FileSystemItem) {
  const operationContext = createFileOperationContextSnapshot()

  try {
    await ElMessageBox.prompt(`将“${item.name}”重命名为：`, '重命名', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      inputValue: item.name,
      inputPattern: /\S+/,
      inputErrorMessage: '请输入新名称',
      beforeClose: async (action, instance, done) => {
        if (instance.confirmButtonLoading) return
        if (action !== 'confirm' || !isFileOperationContextCurrent(operationContext)) {
          done()
          return
        }
        if (!operationContext.accountId) {
          ElMessage.warning('请先选择网盘账号')
          return
        }

        instance.confirmButtonLoading = true
        try {
          await renameFile(http, {
            parent_id: operationContext.parentId,
            file_id: item.id,
            new_name: instance.inputValue.trim(),
            account_id: operationContext.accountId,
          })
          // 提交期间被拦截的取消动作不能把已完成的写入变成取消结果。
          instance.action = 'confirm'
          done()
        } catch (error) {
          if (isFileOperationContextCurrent(operationContext)) {
            reportFileError(error, '重命名失败')
          } else {
            done()
          }
        } finally {
          instance.confirmButtonLoading = false
        }
      },
    })

    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.success('重命名成功')
    await loadFileList({ refresh: true })
  } catch (error) {
    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!isMessageBoxCancelError(error)) {
      reportFileError(error, '重命名失败')
    }
  }
}

async function handleBatchDelete() {
  const items = selectedFileItems.value
  if (items.length === 0) {
    return
  }

  const operationContext = createFileOperationContextSnapshot()
  const folderCount = items.filter((item) => item.is_directory).length
  let submitted = false

  try {
    batchDeleteConfirmationPending = true
    await ElMessageBox.confirm(
      `确认删除选中的 ${items.length} 项吗？${folderCount > 0 ? '所选文件夹内的所有内容也将被删除。' : ''}`,
      '确认批量删除',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )
    batchDeleteConfirmationPending = false

    if (items !== selectedFileItems.value || !isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!operationContext.accountId) {
      ElMessage.warning('请先选择网盘账号')
      return
    }

    batchOperateLoading.value = true

    submitted = true
    await deleteFiles(http, {
      parent_id: operationContext.parentId,
      file_ids: items.map((item) => item.id),
      account_id: operationContext.accountId,
    })

    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.success(`已删除 ${items.length} 项`)
    clearFileSelection()
    await loadFileList({ refresh: true })
  } catch (error) {
    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!isMessageBoxCancelError(error)) {
      reportFileError(error, '批量删除失败')
      if (submitted) {
        clearFileSelection()
        await loadFileList({ refresh: true })
      }
    }
  } finally {
    batchDeleteConfirmationPending = false
    batchOperateLoading.value = false
  }
}

// 单个和批量 STRM 生成共用目标目录弹窗；提交列表在打开时快照，
// 弹窗打开期间的选择变化不影响本次提交范围。
function openStrmTargetDialog(items: FileSystemItem[], isBatch = false) {
  const operationContext = createFileOperationContextSnapshot()
  if (!operationContext.accountId) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  strmSourceItems.value = items
  isBatchStrmOperation.value = isBatch
  strmTargetDir.value = null
  strmOperationContext.value = operationContext
  showStrmTargetDialog.value = true
}

function openBatchStrmDialog() {
  if (selectedFileItems.value.length === 0) {
    return
  }

  openStrmTargetDialog([...selectedFileItems.value], true)
}

function openBatchTargetDialog(operation: BatchFileTargetOperation) {
  if (selectedFileItems.value.length === 0) {
    return
  }

  const operationContext = createFileOperationContextSnapshot()
  if (!operationContext.accountId || !operationContext.sourceType) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  fileTargetOperation.value = operation
  fileTargetDir.value = null
  fileTargetOperationContext.value = operationContext
  singleTargetItem.value = null
  fileTargetSourceType.value = operationContext.sourceType ?? ''
  fileTargetAccountId.value = operationContext.accountId ?? 0
  showFileTargetDialog.value = true
}

function openSingleTargetDialog(operation: BatchFileTargetOperation, item: FileSystemItem) {
  const operationContext = createFileOperationContextSnapshot()
  if (!operationContext.accountId || !operationContext.sourceType) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  fileTargetOperation.value = operation
  fileTargetDir.value = null
  fileTargetOperationContext.value = operationContext
  singleTargetItem.value = item
  fileTargetSourceType.value = operationContext.sourceType
  fileTargetAccountId.value = operationContext.accountId
  showFileTargetDialog.value = true
}

async function confirmFileRootTargetOperation() {
  if (
    batchOperateLoading.value ||
    !isFileTargetOperationContextCurrent(fileTargetOperationContext.value)
  ) {
    return
  }

  fileTargetDir.value = {
    id: fileTargetSourceType.value === '115' ? '0' : '/',
    name: '根目录',
    path: '/',
  }
  await confirmFileTargetOperation()
}

async function confirmFileTargetOperation() {
  if (batchOperateLoading.value) {
    return
  }

  const operation = fileTargetOperation.value
  const targetDir = fileTargetDir.value
  if (!operation || !targetDir) {
    ElMessage.warning('请选择目标目录')
    return
  }

  const operationContext = fileTargetOperationContext.value
  if (!isFileTargetOperationContextCurrent(operationContext)) {
    resetFileTargetDialog()
    return
  }

  if (!operationContext.accountId) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  const isSingleOperation = singleTargetItem.value !== null
  const items = isSingleOperation ? [singleTargetItem.value!] : selectedFileItems.value
  try {
    batchOperateLoading.value = true

    const payload = {
      parent_id: operationContext.parentId,
      file_ids: items.map((item) => item.id),
      target_parent_id: targetDir.id,
      account_id: operationContext.accountId,
    }
    const result =
      operation === 'move' ? await moveFiles(http, payload) : await copyFiles(http, payload)

    if (!isFileTargetOperationContextCurrent(operationContext)) {
      return
    }

    if (result?.status === 'submitted') {
      ElMessage.info('已提交到 OpenList，请查看任务结果，完成后点击刷新更新文件列表')
    } else {
      ElMessage.success(operation === 'move' ? '移动成功' : '复制成功')
    }
    resetFileTargetDialog()
    if (!isSingleOperation) {
      clearFileSelection()
    }
    await loadFileList({ refresh: true })
  } catch (error) {
    if (!isFileTargetOperationContextCurrent(operationContext)) {
      return
    }

    reportFileError(error, operation === 'move' ? '移动失败' : '复制失败')
    resetFileTargetDialog()
    clearFileSelection()
    await loadFileList({ refresh: true })
  } finally {
    // 成功路径会先 resetFileTargetDialog 清掉上下文，这里必须无条件复位，
    // 否则 batchOperateLoading 卡在 true 导致批量操作栏按钮全部禁用。
    batchOperateLoading.value = false
  }
}

function openCreateDialog() {
  const operationContext = createFileOperationContextSnapshot()

  if (!operationContext.accountId || !operationContext.sourceType) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  createForm.value.name = ''
  createDirectoryOperationContext.value = operationContext
  showCreateDialog.value = true
}

async function handleCreateDirectory() {
  if (!createFormRef.value) return

  const operationContext = createDirectoryOperationContext.value
  if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
    resetCreateDirectoryDialog()
    return
  }

  if (!operationContext.accountId || !operationContext.sourceType) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  try {
    await createFormRef.value.validate()

    if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
      return
    }

    createLoading.value = true

    await createDirectory(http, {
      parent_id: operationContext.parentId,
      parent_path: operationContext.parentPath,
      name: createForm.value.name.trim(),
      source_type: operationContext.sourceType,
      account_id: operationContext.accountId,
    })

    if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.success('创建文件夹成功')
    resetCreateDirectoryDialog()
    await loadFileList({ refresh: true })
  } catch (error) {
    if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
      return
    }

    reportFileError(error, '创建文件夹失败')
  } finally {
    if (createDirectoryOperationContext.value === operationContext) {
      createLoading.value = false
    }
  }
}

async function confirmStrmGenerate() {
  if (strmGenerateLoading.value) return

  const items = strmSourceItems.value
  if (!strmTargetDir.value || items.length === 0) {
    ElMessage.warning('请选择目标目录')
    return
  }

  const operationContext = strmOperationContext.value
  if (!isStrmOperationContextCurrent(operationContext)) {
    resetStrmTargetDialog()
    return
  }

  if (!operationContext.accountId) {
    ElMessage.warning('请先选择网盘账号')
    return
  }

  const targetPath = strmTargetDir.value.path

  try {
    strmGenerateLoading.value = true

    let succeededCount = 0
    let firstError: unknown = null
    for (const item of items) {
      // 提交期间目录或账号被切换时中止剩余提交，避免向新上下文误发任务
      if (!isStrmOperationContextCurrent(operationContext)) {
        return
      }

      try {
        await generateManualStrm(http, {
          path_id: item.id,
          target_path: targetPath,
          account_id: operationContext.accountId,
        })
        succeededCount += 1
      } catch (error) {
        if (!isStrmOperationContextCurrent(operationContext)) {
          return
        }
        firstError ??= error
      }
    }

    if (!isStrmOperationContextCurrent(operationContext)) {
      return
    }

    if (succeededCount === items.length) {
      ElMessage.success(
        items.length > 1 ? `已提交 ${items.length} 项 STRM 生成任务` : 'STRM 生成任务已提交',
      )
      resetStrmTargetDialog()
      return
    }

    if (succeededCount > 0) {
      // 部分成功时已入队任务无法撤回，关闭弹窗并汇总结果；
      // 失败项可重新选择后再次提交，队列会拒绝重复任务。
      ElMessage.warning(
        `STRM 生成任务提交：成功 ${succeededCount} 项，失败 ${items.length - succeededCount} 项`,
      )
      resetStrmTargetDialog()
      return
    }

    // 全部失败时保留弹窗和已选目标目录，便于修正后重试，与单个提交一致
    reportFileError(firstError, items.length > 1 ? '批量 STRM 生成失败' : 'STRM 生成失败')
  } finally {
    if (strmOperationContext.value === operationContext) {
      strmGenerateLoading.value = false
    }
  }
}

async function activateFileManagerPage() {
  if (isPageActive) {
    return
  }
  isPageActive = true
  await loadAccountList()
  if (selectedAccountId.value) {
    await loadFileList()
  }
}

function deactivateFileManagerPage() {
  isPageActive = false
  pendingFileListRefresh.value = null
  accountListRequestGate.invalidate()
  fileListRequestGate.invalidate()
  clearFileSelection()
  invalidateFileOperationContext()
}

// 页面生命周期
onMounted(activateFileManagerPage)

onActivated(activateFileManagerPage)

usePageScrollRestore({
  pageKey: 'file-manager',
  getScrollContainer: getPageScrollContainer,
})

onDeactivated(deactivateFileManagerPage)

onUnmounted(() => {
  deactivateFileManagerPage()
  accountListRequestGate.invalidate()
  fileListRequestGate.invalidate()
})
</script>

<style scoped>
.file-manager-container {
  padding: 20px;
}

.file-manager-layout {
  display: flex;
  width: 100%;
  gap: 20px;
  min-height: calc(100vh - 300px);
}

.account-sidebar {
  width: 280px;
  flex-shrink: 0;
  background: var(--el-fill-color-light);
  border-radius: 4px;
  overflow: hidden;
}

.file-content {
  flex: 1;
  min-width: 0;
  background: #fff;
  border-radius: 4px;
  padding: 20px;
}

.file-manager-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.file-manager-toolbar-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 6px;
}

.file-manager-sort-field {
  width: 82px;
}

.file-manager-sort-order {
  width: 76px;
}

.file-manager-batch-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 16px;
  padding: 8px 12px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
}

.file-manager-batch-summary {
  color: var(--el-text-color-regular);
  font-size: 14px;
}

.file-manager-batch-actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.file-manager-row-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 10px;
}

.file-manager-row-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.sidebar-header {
  padding: 16px;
  background: #fff;
  border-bottom: 1px solid var(--el-border-color-light);
}

.sidebar-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.sidebar-header h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.mobile-file-manager-info {
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--el-text-color-secondary);
}

:global(.file-manager-summary-popover) {
  max-width: calc(100vw - 32px);
}

:global(.file-manager-summary-popover-text) {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 12px;
  line-height: 1.45;
}

.account-list {
  max-height: calc(100vh - 400px);
  overflow-y: auto;
}

.account-item {
  padding: 16px;
  background: #fff;
  border-bottom: 1px solid var(--el-border-color-light);
  cursor: pointer;
  transition:
    background-color 0.2s ease,
    border-color 0.2s ease;
}

.account-item:hover {
  background: var(--el-color-primary-light-9);
}

.account-item.active {
  background: #e6f7ff;
  border-left: 3px solid var(--el-color-primary);
}

.account-info {
  display: flex;
  align-items: center;
  gap: 12px;
}

.account-icon {
  font-size: 24px;
  color: var(--el-color-primary);
}

.account-details {
  flex: 1;
}

.account-name {
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-primary);
  margin-bottom: 4px;
}

.no-account-selected {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  min-height: 400px;
}

@media (max-width: 768px) {
  .file-manager-layout {
    flex-direction: column;
    gap: 8px;
  }

  .account-sidebar {
    width: 100%;
    max-height: 128px;
  }

  .file-manager-container {
    padding: 6px;
  }

  .file-manager-container.full-width-container {
    margin-top: -20px !important;
    padding-top: 12px !important;
    padding-bottom: 12px !important;
  }

  .file-manager-container :deep(.el-card__header) {
    display: none;
  }

  .file-manager-container :deep(.el-card__body) {
    padding: 0 10px 10px;
  }

  .sidebar-header {
    padding: 4px 10px 8px;
  }

  .sidebar-header h3 {
    font-size: 14px;
  }

  .account-list {
    max-height: 82px;
  }

  .account-item {
    padding: 7px 10px;
  }

  .account-info {
    gap: 8px;
  }

  .account-icon {
    font-size: 18px;
  }

  .account-name {
    margin-bottom: 2px;
  }

  .file-content {
    padding: 8px;
    min-height: 55vh;
  }

  .file-manager-toolbar {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 10px;
  }

  .file-manager-toolbar-actions {
    justify-content: flex-start;
    width: 100%;
  }

  .file-manager-sort-field {
    width: 76px;
  }

  .file-manager-sort-order {
    width: 70px;
  }

  .file-manager-batch-bar {
    align-items: flex-start;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 10px;
  }

  .file-manager-batch-actions {
    justify-content: flex-start;
    width: 100%;
  }
}

.strm-target-dialog-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.strm-target-dialog-content .dialog-tip {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 14px;
}

.file-target-dialog-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.file-target-dialog-content .dialog-tip {
  margin: 0;
  color: var(--el-text-color-regular);
  font-size: 14px;
}

.strm-source-info {
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 4px;
  font-size: 14px;
}

.strm-source-info .source-label {
  color: var(--el-text-color-secondary);
  margin-right: 8px;
}

.strm-source-info .source-name {
  color: var(--el-text-color-primary);
  font-weight: 500;
}

.strm-store-path {
  padding: 12px;
  background: var(--el-color-success-light-9);
  border-radius: 4px;
  font-size: 14px;
  border: 1px solid #e1f3d8;
}

.strm-store-path .store-label {
  color: var(--el-color-success);
  margin-right: 8px;
  font-weight: 500;
}

.strm-store-path .store-path {
  color: var(--el-text-color-primary);
  background: #fff;
  padding: 2px 6px;
  border-radius: 2px;
  border: 1px solid var(--el-border-color);
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  word-break: break-all;
}

.dir-selector-container {
  height: 400px;
  border: 1px solid var(--el-border-color-light);
  border-radius: 4px;
  padding: 12px;
}
</style>
