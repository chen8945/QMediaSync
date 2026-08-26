<template>
  <div
    class="main-content-container file-manager-container full-width-container"
    ref="pageContainerRef"
  >
    <PageHeader />
    <el-card shadow="none" class="full-width-card">
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
                  浏览和管理媒体文件，支持 STRM 生成、刮削整理和 ED2K 生成操作
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

            <!-- 桌面端表格 -->
            <el-table
              v-if="!isMobile"
              v-loading="initialLoading"
              :data="fileList"
              :row-key="(row: FileSystemItem) => String(row.id || row.path)"
              style="width: 100%"
              @row-dblclick="handleRowDoubleClick"
            >
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
                        <el-dropdown-item command="SCRAPE_ORGANIZE">刮削整理</el-dropdown-item>
                        <el-dropdown-item
                          v-if="
                            !row.is_directory &&
                            (getFileType(row.name) === 'video' || getFileType(row.name) === 'image')
                          "
                          command="GENERATE_ED2K"
                        >
                          生成 ED2K
                        </el-dropdown-item>
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
              v-loading="initialLoading"
              :data="fileList"
              :row-key="(row: FileSystemItem) => String(row.id || row.path)"
              :expand-row-keys="pageState.expandedRowKeys"
              @expand-change="handleExpandChange"
              style="width: 100%"
              @row-dblclick="handleRowDoubleClick"
            >
              <el-table-column type="expand" width="30">
                <template #default="{ row }">
                  <div style="padding: 0 20px">
                    <p>
                      <strong>大小：</strong
                      >{{ row.is_directory ? '--' : formatFileSize(row.size) }}
                    </p>
                    <p><strong>修改时间：</strong>{{ formatDateTime(row.modified_time) }}</p>
                    <div style="margin-top: 10px">
                      <el-button
                        size="small"
                        type="primary"
                        @click="handleSingleOperation('STRM_GENERATE', row)"
                      >
                        STRM 生成
                      </el-button>
                      <el-button
                        size="small"
                        type="success"
                        @click="handleSingleOperation('SCRAPE_ORGANIZE', row)"
                      >
                        刮削整理
                      </el-button>
                      <el-button
                        v-if="
                          !row.is_directory &&
                          (getFileType(row.name) === 'video' || getFileType(row.name) === 'image')
                        "
                        size="small"
                        type="warning"
                        @click="handleSingleOperation('GENERATE_ED2K', row)"
                      >
                        生成 ED2K
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
      title="选择 STRM 目标目录"
      width="600px"
      :close-on-click-modal="false"
      @closed="resetStrmTargetDialog"
    >
      <div class="strm-target-dialog-content">
        <p class="dialog-tip">请选择 STRM 文件的目标存放目录：</p>
        <div v-if="strmSourceItem" class="strm-source-info">
          <span class="source-label">源文件：</span>
          <span class="source-name">{{ strmSourceItem.name }}</span>
        </div>
        <div class="dir-selector-container">
          <DirectorySelector
            v-model="strmTargetDir"
            source-type="local"
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
} from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { ArrowDown, Files, FolderAdd, InfoFilled, Refresh } from '@element-plus/icons-vue'
import type { FileSystemItem, FileOperationType, DirInfo } from '@/typing'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { useBackgroundRefresh } from '@/composables/useBackgroundRefresh'
import { useDeviceType } from '@/composables/useDeviceType'
import { useHttpClient } from '@/http/client'
import { usePageScrollRestore } from '@/composables/usePageScrollRestore'
import { mergeStableList, retainExistingKeys } from '@/composables/useStableList'
import { usePageStateStore } from '@/stores/pageState'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import { getFileType, getFileIconByName } from '@/utils/fileIconUtils'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatDateTime } from '@/utils/timeUtils'
import { SERVER_URL } from '@/const'
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

type NetFileSortBy = 'default' | 'name' | 'time' | 'size' | 'type'
type NetFileSortOrder = 'asc' | 'desc'

interface NetFileListCacheMeta {
  status: 'hit' | 'miss' | 'partial_hit' | 'refresh'
  batch_start: number
  batch_size: number
  cached_at: number
  expires_at: number
}

interface NetFileListPayload {
  list: FileSystemItem[]
  total: number
  total_exact: boolean
  has_more: boolean
  page: number
  page_size: number
  sort_by: NetFileSortBy
  sort_order: NetFileSortOrder
  cache?: NetFileListCacheMeta
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
const strmSourceItem = ref<FileSystemItem | null>(null)
const strmOperationContext = ref<FileOperationContextSnapshot | null>(null)
const createDirectoryOperationContext = ref<FileOperationContextSnapshot | null>(null)
const strmGenerateLoading = ref(false)
const contextVersion = ref(0)

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

function resetStrmTargetDialog() {
  showStrmTargetDialog.value = false
  strmSourceItem.value = null
  strmTargetDir.value = null
  strmOperationContext.value = null
  strmGenerateLoading.value = false
}

function resetCreateDirectoryDialog() {
  showCreateDialog.value = false
  createForm.value.name = ''
  createDirectoryOperationContext.value = null
  createLoading.value = false
}

function invalidateFileOperationContext() {
  contextVersion.value += 1
  resetStrmTargetDialog()
  resetCreateDirectoryDialog()
}

function clearFileListForContextSwitch() {
  invalidateFileOperationContext()
  fileListRequestGate.invalidate()
  fileList.value = []
  total.value = 0
  pageStateStore.setExpandedRowKeys('file-manager', [])
}

function clearFileListForPageChange() {
  fileListRequestGate.invalidate()
  fileList.value = []
  pageStateStore.setExpandedRowKeys('file-manager', [])
}

// 计算属性
const strmStorePath = computed(() => {
  if (!strmTargetDir.value || !strmSourceItem.value) return ''
  const currentPathStr = pathItems.value.map((p) => p.name).join('/')
  const itemPath = currentPathStr
    ? `${currentPathStr}/${strmSourceItem.value.name}`
    : strmSourceItem.value.name
  return `${strmTargetDir.value.path}/${itemPath}`
})

// const isMobileDevice = computed(() => isMobile())

// 加载网盘账号列表
async function loadAccountList() {
  const requestId = accountListRequestGate.next()

  if (!accountListRequestGate.isCurrent(requestId)) {
    return
  }

  if (!http) {
    console.warn('HTTP 客户端未注入，无法加载账号列表')
    return
  }

  try {
    const response = await http.get(`${SERVER_URL}/account/list`)

    if (!accountListRequestGate.isCurrent(requestId)) {
      return
    }

    if (response?.data.code === 200) {
      const data = response.data.data
      accountList.value = data.map((item: NetdiskAccount) => ({
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
    } else {
      console.error('加载账号列表失败：', response?.data.message || '未知错误')
      accountList.value = []
    }
  } catch (error) {
    if (!accountListRequestGate.isCurrent(requestId)) {
      return
    }
    console.error('加载账号列表失败：', error)
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

  if (!http) {
    console.warn('HTTP 客户端未注入，无法加载文件列表')
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

      const requestParams: Record<string, string | number> = {
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

      const response = await http.get(`${SERVER_URL}/path/files`, {
        params: requestParams,
        timeout: 60000,
      })

      if (!fileListRequestGate.isCurrent(requestId)) {
        return
      }

      if (response?.data.code === 200) {
        const {
          list: items,
          total: responseTotal,
          sort_by: responseSortBy,
          sort_order: responseSortOrder,
        } = normalizeNetFileListPayload(response.data.data, {
          page: currentPage.value,
          pageSize: pageSize.value,
          sortBy: sortBy.value,
          sortOrder: sortOrder.value,
        })

        const pageStart = (currentPage.value - 1) * pageSize.value
        if (responseTotal > 0 && pageStart >= responseTotal) {
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
        pageStateStore.setExpandedRowKeys(
          'file-manager',
          retainExistingKeys(
            pageState.expandedRowKeys,
            fileList.value,
            (row) => row.id || row.path,
          ),
        )
        total.value = responseTotal
        sortBy.value = responseSortBy
        sortOrder.value = responseSortOrder
      } else {
        console.error('加载文件列表失败：', response?.data.message || '未知错误')
        fileList.value = []
        total.value = 0
      }
    })
  } catch {
    if (!fileListRequestGate.isCurrent(requestId)) {
      return
    }
    ElMessage.error('加载文件列表失败')
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

  if (operation === 'STRM_GENERATE') {
    const operationContext = createFileOperationContextSnapshot()
    if (!operationContext.accountId) {
      ElMessage.warning('请先选择网盘账号')
      return
    }

    strmSourceItem.value = item
    strmTargetDir.value = null
    strmOperationContext.value = operationContext
    showStrmTargetDialog.value = true
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

    const response = await http.delete(`${SERVER_URL}/path`, {
      params: {
        parent_id: operationContext.parentId,
        file_id: item.id,
        account_id: operationContext.accountId,
      },
    })

    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('删除成功')
      await loadFileList({ refresh: true })
    } else {
      ElMessage.error(response?.data.message || '删除失败')
    }
  } catch (error) {
    if (!isFileOperationContextCurrent(operationContext)) {
      return
    }

    if (!isMessageBoxCancelError(error)) {
      console.error('删除失败：', error)
      ElMessage.error('删除失败')
    }
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

    const response = await http.post(`${SERVER_URL}/path/create`, {
      parent_id: operationContext.parentId,
      parent_path: operationContext.parentPath,
      name: createForm.value.name.trim(),
      source_type: operationContext.sourceType,
      account_id: operationContext.accountId,
    })

    if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('创建文件夹成功')
      resetCreateDirectoryDialog()
      await loadFileList({ refresh: true })
    } else {
      ElMessage.error(response?.data.message || '创建文件夹失败')
    }
  } catch {
    if (!isCreateDirectoryOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.error('创建文件夹失败')
  } finally {
    if (createDirectoryOperationContext.value === operationContext) {
      createLoading.value = false
    }
  }
}

async function confirmStrmGenerate() {
  if (!strmTargetDir.value || !strmSourceItem.value) {
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

  try {
    strmGenerateLoading.value = true

    // const currentPathStr = pathItems.value.map(p => p.name).join('/')
    // const itemPath = currentPathStr ? `${currentPathStr}/${strmSourceItem.value.name}` : strmSourceItem.value.name

    const response = await http.post(`${SERVER_URL}/sync/manual`, {
      path_id: strmSourceItem.value.id,
      // path: itemPath,
      target_path: strmTargetDir.value.path,
      // is_file: !strmSourceItem.value.is_directory,
      account_id: operationContext.accountId,
    })

    if (!isStrmOperationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('STRM 生成任务已提交')
      resetStrmTargetDialog()
    } else {
      ElMessage.error(response?.data.message || 'STRM 生成失败')
    }
  } catch {
    if (!isStrmOperationContextCurrent(operationContext)) {
      return
    }

    ElMessage.error('STRM 生成失败')
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
  background: #f5f7fa;
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

.sidebar-header {
  padding: 16px;
  background: #fff;
  border-bottom: 1px solid #e4e7ed;
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
  color: #303133;
}

.mobile-file-manager-info {
  width: 24px;
  height: 24px;
  padding: 0;
  color: #909399;
}

:global(.file-manager-summary-popover) {
  max-width: calc(100vw - 32px);
}

:global(.file-manager-summary-popover-text) {
  margin: 0;
  color: #606266;
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
  border-bottom: 1px solid #e4e7ed;
  cursor: pointer;
  transition:
    background-color 0.2s ease,
    border-color 0.2s ease;
}

.account-item:hover {
  background: #ecf5ff;
}

.account-item.active {
  background: #e6f7ff;
  border-left: 3px solid #409eff;
}

.account-info {
  display: flex;
  align-items: center;
  gap: 12px;
}

.account-icon {
  font-size: 24px;
  color: #409eff;
}

.account-details {
  flex: 1;
}

.account-name {
  font-size: 14px;
  font-weight: 500;
  color: #303133;
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
}

.strm-target-dialog-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.strm-target-dialog-content .dialog-tip {
  margin: 0;
  color: #606266;
  font-size: 14px;
}

.strm-source-info {
  padding: 12px;
  background: #f5f7fa;
  border-radius: 4px;
  font-size: 14px;
}

.strm-source-info .source-label {
  color: #909399;
  margin-right: 8px;
}

.strm-source-info .source-name {
  color: #303133;
  font-weight: 500;
}

.strm-store-path {
  padding: 12px;
  background: #f0f9eb;
  border-radius: 4px;
  font-size: 14px;
  border: 1px solid #e1f3d8;
}

.strm-store-path .store-label {
  color: #67c23a;
  margin-right: 8px;
  font-weight: 500;
}

.strm-store-path .store-path {
  color: #303133;
  background: #fff;
  padding: 2px 6px;
  border-radius: 2px;
  border: 1px solid #dcdfe6;
  font-family: 'Monaco', 'Menlo', 'Ubuntu Mono', monospace;
  word-break: break-all;
}

.dir-selector-container {
  height: 400px;
  border: 1px solid #e4e7ed;
  border-radius: 4px;
  padding: 12px;
}
</style>
