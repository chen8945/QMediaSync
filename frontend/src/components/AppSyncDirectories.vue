<template>
  <div class="sync-directories-page">
    <PageHeader>
      <template #actions>
        <div class="header-actions">
          <el-button type="primary" class="add-btn" :icon="Plus" @click="handleAdd">
            <span class="btn-text">添加同步目录</span>
          </el-button>
        </div>
      </template>
      <template #stats>
        <PageStats :items="stats" />
      </template>
    </PageHeader>

    <div class="directories-content">
      <div class="directories-grid" v-if="directories.length > 0">
        <div
          class="directory-card"
          v-for="(row, index) in directories"
          :key="row.id || index"
          :class="{ 'is-running': row.is_running === 2, 'is-waiting': row.is_running === 1 }"
        >
          <div class="card-status-bar" :class="getStatusClass(row)"></div>
          <div class="card-main">
            <div class="card-header">
              <div class="card-title-wrapper">
                <el-tooltip :content="'目录 ID：' + row.base_cid" placement="bottom">
                  <span class="card-id">#{{ row.id }}</span>
                </el-tooltip>
                <span class="card-path">{{ row.remote_path }}</span>
              </div>
              <el-tag :type="sourceTypeTagMap[row.source_type]" class="source-tag" effect="light">
                {{ sourceTypeMap[row.source_type] }}
              </el-tag>
            </div>

            <div class="card-body">
              <div class="info-row" v-if="row.source_type !== 'local'">
                <div class="info-icon">
                  <el-icon><User /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">关联账号</span>
                  <span class="info-value">{{ row.account_name }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Folder /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">目标路径</span>
                  <span class="info-value path-value">{{ GetFullPath(row) }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Calendar /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">添加时间</span>
                  <span class="info-value">{{ formatTime(row.created_at) }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Refresh /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">最后同步</span>
                  <span class="info-value">{{ formatTime(row.last_sync_at) || '从未同步' }}</span>
                </div>
              </div>

              <div class="info-row toggle-row">
                <div class="info-icon">
                  <el-icon><Timer /></el-icon>
                </div>
                <div class="info-content">
                  <el-tooltip
                    effect="dark"
                    content="开启后会根据 STRM 设置中的 Cron 表达式定时同步数据，如果该同步目录内的资源变动概率较小，建议关闭定时同步"
                    placement="top"
                    popper-class="qms-contained-tooltip"
                    append-to="body"
                  >
                    <span class="info-label with-tooltip">
                      定时同步
                      <el-icon class="help-icon"><QuestionFilled /></el-icon>
                    </span>
                  </el-tooltip>
                  <el-switch
                    v-model="row.enable_cron"
                    :active-value="true"
                    :inactive-value="false"
                    @change="toggleCron(row)"
                    active-color="#67c23a"
                    inactive-color="#dcdfe6"
                  />
                </div>
              </div>

              <div class="info-row" v-if="row.source_type === '115'">
                <div class="info-icon">
                  <el-icon><Upload /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">目录监控上传</span>
                  <span class="info-value directory-upload-value">
                    <el-tag :type="getDirectoryUploadStatusType(row)" size="small" effect="light">
                      {{ getDirectoryUploadStatusText(row) }}
                    </el-tag>
                  </span>
                </div>
              </div>

              <div class="info-row" v-if="getDirectoryUploadRules(row).length > 0">
                <div class="info-icon">
                  <el-icon><Folder /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">监控目录</span>
                  <span class="info-value path-value">
                    {{ getDirectoryUploadPathText(row) }}
                  </span>
                </div>
              </div>

              <div class="status-row">
                <div class="status-indicator" :class="getStatusClass(row)">
                  <el-icon v-if="row.is_running === 2" class="rotating"><Loading /></el-icon>
                  <el-icon v-else-if="row.is_running === 1"><Clock /></el-icon>
                  <el-icon v-else><CircleCheck /></el-icon>
                  <span>{{ getStatusText(row) }}</span>
                </div>
              </div>
            </div>

            <div class="card-footer">
              <div class="card-footer__primary">
                <el-tooltip
                  v-for="action in getPrimaryActions(row, index)"
                  :key="action.key"
                  :content="action.ariaLabel"
                  placement="top"
                >
                  <el-button
                    :type="action.type"
                    size="small"
                    :plain="action.plain"
                    :circle="action.circle"
                    :icon="action.icon"
                    :loading="action.loading"
                    :aria-label="action.ariaLabel"
                    @click="action.onClick"
                  >
                    {{ action.label }}
                  </el-button>
                </el-tooltip>
              </div>

              <div class="card-footer__secondary">
                <el-tooltip
                  v-for="action in getSecondaryActions(row, index)"
                  :key="action.key"
                  :content="action.ariaLabel"
                  placement="top"
                >
                  <el-button
                    :type="action.type"
                    size="small"
                    :plain="action.plain"
                    :circle="action.circle"
                    :icon="action.icon"
                    :loading="action.loading"
                    :aria-label="action.ariaLabel"
                    @click="action.onClick"
                  />
                </el-tooltip>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="empty-state" v-else-if="!loading">
        <div class="empty-illustration">
          <el-icon class="empty-icon"><FolderOpened /></el-icon>
          <div class="empty-dots">
            <span></span>
            <span></span>
            <span></span>
          </div>
        </div>
        <h3 class="empty-title">暂无同步目录</h3>
        <p class="empty-description">点击上方按钮添加第一个同步目录</p>
        <el-button type="primary" :icon="Plus" @click="handleAdd"> 添加同步目录 </el-button>
      </div>

      <div class="loading-state" v-if="loading">
        <el-icon class="loading-icon rotating"><Loading /></el-icon>
        <span>加载中…</span>
      </div>

      <el-dialog
        v-model="showScrapePathDialog"
        title="关联刮削目录"
        width="500px"
        :close-on-click-modal="false"
      >
        <div class="scrape-path-dialog-content">
          <p class="dialog-tip">请选择要关联的本地类型刮削目录：</p>
          <el-alert
            title="STRM 同步完成 30 秒后，会自动触发已关联的刮削目录执行刮削"
            type="info"
            :closable="false"
            show-icon
            style="margin-bottom: 16px"
          />
          <el-select
            v-model="selectedScrapePathIds"
            multiple
            filterable
            placeholder="请选择刮削目录"
            style="width: 100%"
            :loading="scrapePathsLoading"
          >
            <el-option
              v-for="item in scrapePathOptions"
              :key="item.id"
              :label="item.label"
              :value="item.id"
            />
          </el-select>
        </div>
        <template #footer>
          <span class="dialog-footer">
            <el-button @click="showScrapePathDialog = false">取消</el-button>
            <el-button
              type="primary"
              @click="saveScrapePathRelation"
              :loading="saveScrapePathLoading"
            >
              确定
            </el-button>
          </span>
        </template>
      </el-dialog>

      <div class="page-footer-tips">
        <div class="tips-header">
          <el-icon class="tips-icon"><InfoFilled /></el-icon>
          <span>使用说明</span>
        </div>
        <div class="tips-content">
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon><Warning /></el-icon>
              <span>115 网盘</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >无法感知网盘文件夹重命名等操作；如果同步后的文件夹名称不对，可以手动点击<strong
                    >“全量同步”</strong
                  ></span
                >
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >“全量同步”会删除所有缓存数据（不会删除本地文件），然后重新同步网盘文件变更</span
                >
              </div>
              <div class="tip-item tip-highlight">
                <span class="tip-bullet">★</span>
                <span
                  >请按照电影和电视剧分开添加同步目录，电影的同步速度非常快，电视剧的同步速度较慢</span
                >
              </div>
            </div>
          </div>
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon><Timer /></el-icon>
              <span>百度网盘</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >只查询上次同步时间之后修改的文件列表，不会查询所有文件、无法感知文件和文件夹删除</span
                >
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >增量同步目前只能单线程执行；每分钟最多 8 次请求，每次最多 1000
                  个文件。单次变更超过 8000 个文件时，同步会比较慢</span
                >
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>“全量同步”会删除所有缓存数据（不会删除本地文件），然后递归查询所有文件</span>
              </div>
              <div class="tip-item tip-highlight">
                <span class="tip-bullet">★</span>
                <span>每天第一次同步会执行“全量同步”，后续同步会执行“增量同步”</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import PageHeader from '@/components/common/PageHeader.vue'
import PageStats from '@/components/common/PageStats.vue'
import { useRealtimeEvent } from '@/composables/useRealtimeEvents'
import { useHttpClient } from '@/http/client'
import {
  Calendar,
  CircleCheck,
  Clock,
  Delete,
  Edit,
  Files,
  Folder,
  FolderOpened,
  InfoFilled,
  Link,
  Loading,
  Plus,
  QuestionFilled,
  Refresh,
  RefreshRight,
  Timer,
  User,
  Upload,
  VideoPause,
  VideoPlay,
  Warning,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { computed, onMounted, ref, type Component } from 'vue'
import { useRouter } from 'vue-router'
import { sourceTypeTagMap, sourceTypeMap } from '@/utils/sourceTypeUtils'
import {
  resetSyncTaskEventSequences,
  shouldApplySyncTaskEvent,
} from '@/utils/syncTaskEventSequence'
import { formatTime } from '@/utils/timeUtils'
import {
  formatDirectoryUploadPathSummary,
  formatDirectoryUploadStatus,
  getEnabledDirectoryUploadRules,
  groupDirectoryUploadRulesBySyncPath,
} from '@/utils/directoryUploadRules'
import type { DirectoryUploadRule } from '@/typing'

interface SyncDirectory {
  id: number
  base_cid: string
  local_path: string
  remote_path: string
  strm_path: string
  created_at: number
  updated_at: number
  last_sync_at: number
  deleting?: boolean
  starting?: boolean
  source_type: string
  account_id: number
  account_name: string
  enable_cron: boolean
  directory_upload_enabled: boolean
  is_running: number
  stopping?: boolean
}

type DirectoryUploadRuleState = DirectoryUploadRule & { scanning?: boolean }

interface SyncDirectoryAction {
  key: string
  label: string
  type: 'primary' | 'success' | 'warning' | 'danger' | 'info'
  icon: Component
  plain: boolean
  circle: boolean
  loading?: boolean
  ariaLabel: string
  onClick: () => void
}

interface SyncTaskEventPayload {
  sync_id?: number
  sync_path_id: number
  status?: number
  sequence?: number
  event_time?: number
  is_running?: number
  deleted?: boolean
}

const http = useHttpClient()
const router = useRouter()

const directories = ref<SyncDirectory[]>([])
const directoryUploadRules = ref<Record<number, DirectoryUploadRuleState[]>>({})
const directoryUploadRulesLoadFailed = ref(false)
const loading = ref(false)
const total = ref(0)
const currentPage = ref(1)
const pageSize = ref(9999)

const showScrapePathDialog = ref(false)
const selectedScrapePathIds = ref<number[]>([])
const scrapePathOptions = ref<{ id: number; label: string }[]>([])
const scrapePathsLoading = ref(false)
const saveScrapePathLoading = ref(false)
const currentSyncDirectory = ref<SyncDirectory | null>(null)
const lastSyncPathEventSequence = new Map<number, number>()
const lastSyncPathEventTime = new Map<number, number>()

const runningCount = computed(() => directories.value.filter((d) => d.is_running === 2).length)
const waitingCount = computed(() => directories.value.filter((d) => d.is_running === 1).length)
const cronEnabledCount = computed(() => directories.value.filter((d) => d.enable_cron).length)
const stats = computed(() => [
  { icon: Files, value: directories.value.length, label: '总目录数', tone: 'total' },
  { icon: Loading, value: runningCount.value, label: '运行中', tone: 'running' },
  { icon: Clock, value: waitingCount.value, label: '等待中', tone: 'waiting' },
  { icon: Timer, value: cronEnabledCount.value, label: '定时同步', tone: 'cron' },
])

const getDirectoryUploadRules = (row: SyncDirectory): DirectoryUploadRuleState[] => {
  return directoryUploadRules.value[row.id] || []
}

const getDirectoryUploadStatusText = (row: SyncDirectory): string => {
  return formatDirectoryUploadStatus(
    getDirectoryUploadRules(row),
    row.directory_upload_enabled,
    directoryUploadRulesLoadFailed.value,
  )
}

const getDirectoryUploadStatusType = (
  row: SyncDirectory,
): 'success' | 'info' | 'warning' | 'danger' => {
  if (directoryUploadRulesLoadFailed.value) {
    return 'danger'
  }
  const rules = getDirectoryUploadRules(row)
  if (rules.length === 0) {
    return 'info'
  }
  if (!row.directory_upload_enabled) {
    return 'info'
  }
  return getEnabledDirectoryUploadRules(rules).length > 0 ? 'success' : 'warning'
}

const getDirectoryUploadPathText = (row: SyncDirectory): string => {
  return formatDirectoryUploadPathSummary(getDirectoryUploadRules(row))
}

const getStatusClass = (row: SyncDirectory) => {
  if (row.is_running === 2) return 'status-running'
  if (row.is_running === 1) return 'status-waiting'
  return 'status-idle'
}

const getStatusText = (row: SyncDirectory) => {
  if (row.is_running === 2) return '运行中'
  if (row.is_running === 1) return '等待中'
  return '空闲'
}

const getStartButtonText = (row: SyncDirectory) => {
  return `${row.source_type === '115' || row.source_type === 'baidupan' ? '增量' : ''}同步`
}

const getPrimaryActions = (row: SyncDirectory, index: number): SyncDirectoryAction[] => {
  const actions: SyncDirectoryAction[] = []

  if ((row.source_type === '115' || row.source_type === 'baidupan') && row.is_running === 0) {
    actions.push({
      key: 'full-sync',
      label: '全量同步',
      type: 'warning',
      icon: RefreshRight,
      plain: true,
      circle: false,
      loading: row.starting,
      ariaLabel: '全量同步',
      onClick: () => handleFullStart(row, index),
    })
  }

  if (row.is_running === 0) {
    actions.push({
      key: 'start-sync',
      label: getStartButtonText(row),
      type: 'success',
      icon: VideoPlay,
      plain: true,
      circle: false,
      loading: row.starting,
      ariaLabel: getStartButtonText(row),
      onClick: () => handleStart(row, index),
    })
  } else {
    actions.push({
      key: 'stop-sync',
      label: '停止',
      type: 'info',
      icon: VideoPause,
      plain: true,
      circle: false,
      loading: row.stopping,
      ariaLabel: '停止同步',
      onClick: () => handleStop(row, index),
    })
  }

  return actions
}

const getSecondaryActions = (row: SyncDirectory, index: number): SyncDirectoryAction[] => {
  const actions: SyncDirectoryAction[] = []
  const enabledDirectoryUploadRules = getEnabledDirectoryUploadRules(getDirectoryUploadRules(row))

  if (row.directory_upload_enabled && enabledDirectoryUploadRules.length > 0) {
    actions.push({
      key: 'directory-upload-scan',
      label: '',
      type: 'success',
      icon: Refresh,
      plain: true,
      circle: true,
      loading: enabledDirectoryUploadRules.some((rule) => rule.scanning),
      ariaLabel: '目录监控扫描',
      onClick: () => scanDirectoryUploadRule(row),
    })
  }

  actions.push(
    {
      key: 'edit',
      label: '',
      type: 'primary',
      icon: Edit,
      plain: true,
      circle: true,
      ariaLabel: '编辑同步目录',
      onClick: () => handleEdit(row),
    },
    {
      key: 'delete',
      label: '',
      type: 'danger',
      icon: Delete,
      plain: true,
      circle: true,
      loading: row.deleting,
      ariaLabel: '删除同步目录',
      onClick: () => handleDelete(row, index),
    },
    {
      key: 'scrape-relation',
      label: '',
      type: 'warning',
      icon: Link,
      plain: true,
      circle: true,
      ariaLabel: '关联刮削目录',
      onClick: () => openScrapePathDialog(row),
    },
  )

  return actions
}

const GetFullPath = (row: SyncDirectory) => {
  // const pathSeparator = row.local_path.startsWith('/') ? '/' : '\\'
  if (row.source_type === 'local') {
    return row.local_path
  }
  // let remotePath = row.remote_path
  // if (pathSeparator === '/') {
  //   remotePath = remotePath.replace(/\\/g, pathSeparator)
  // } else {
  //   remotePath = remotePath.replace(/\//g, pathSeparator)
  // }
  return `${row.local_path}/${row.remote_path}`
}

const loadDirectories = async () => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/sync/path-list`, {
      timeout: 5000,
      params: {
        page: currentPage.value,
        page_size: pageSize.value,
      },
    })

    if (response?.data.code === 200) {
      directories.value = response.data.data.list || []
      total.value = response.data.data.total || 0
      await loadDirectoryUploadRules()
    } else {
      ElMessage.error(response?.data.message || '加载同步目录失败')
      directories.value = []
      total.value = 0
      directoryUploadRules.value = {}
      directoryUploadRulesLoadFailed.value = false
    }
  } catch {
    console.error('加载同步目录错误')
    ElMessage.error('加载同步目录失败')
    directories.value = []
    total.value = 0
    directoryUploadRules.value = {}
    directoryUploadRulesLoadFailed.value = false
  } finally {
    loading.value = false
  }
}

const loadDirectoryUploadRules = async () => {
  directoryUploadRulesLoadFailed.value = false
  try {
    const response = await http.get(`${SERVER_URL}/directory-upload/rules`)
    if (response?.data.code !== 200) {
      directoryUploadRules.value = {}
      directoryUploadRulesLoadFailed.value = true
      return
    }
    directoryUploadRules.value = groupDirectoryUploadRulesBySyncPath(response.data.data?.list || [])
  } catch {
    directoryUploadRules.value = {}
    directoryUploadRulesLoadFailed.value = true
  }
}

const updatePathesStatus = async () => {
  const response = await http.get(`${SERVER_URL}/sync/path-list`)

  if (response?.data.code === 200) {
    for (const p of response.data.data.list || []) {
      const path = directories.value.find((pa) => pa.id === p.id)
      if (path) {
        path.is_running = p.is_running
      }
    }
  }
}

const applySyncPathRunningStatus = (syncPathId: number, isRunning: number) => {
  const path = directories.value.find((item) => item.id === syncPathId)
  if (!path) {
    void updatePathesStatus()
    return
  }
  path.is_running = isRunning
}

const patchSyncPathStatus = (raw: Record<string, unknown>) => {
  const payload = raw as unknown as SyncTaskEventPayload
  if (!payload.sync_path_id) {
    return
  }

  const lastEventTime = lastSyncPathEventTime.get(payload.sync_path_id) || 0
  if (payload.event_time && payload.event_time < lastEventTime) {
    return
  }
  if (payload.event_time) {
    lastSyncPathEventTime.set(payload.sync_path_id, payload.event_time)
  }

  const sequenceKey = payload.sync_id || payload.sync_path_id
  if (
    !shouldApplySyncTaskEvent(
      lastSyncPathEventSequence,
      sequenceKey,
      payload.sequence,
      payload.deleted === true,
    )
  ) {
    return
  }

  if (typeof payload.is_running === 'number') {
    applySyncPathRunningStatus(payload.sync_path_id, payload.is_running)
    return
  }

  const path = directories.value.find((item) => item.id === payload.sync_path_id)
  if (!path) {
    void updatePathesStatus()
    return
  }

  if (payload.deleted || payload.status === 2 || payload.status === 3) {
    path.is_running = 0
    return
  }
  if (payload.status === 0) {
    path.is_running = 1
    return
  }
  if (payload.status === 1) {
    path.is_running = 2
  }
}

const handleAdd = () => {
  router.push({ name: 'sync-directory-add' })
}

const handleEdit = (row: SyncDirectory) => {
  router.push({ name: 'sync-directory-edit', params: { id: row.id } })
}

const handleDelete = async (row: SyncDirectory, index: number) => {
  try {
    await ElMessageBox.confirm(
      `不会删除已经同步的元数据和 STRM 文件，确定要删除同步目录“${row.local_path}”吗？`,
      '确认删除',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    directories.value[index].deleting = true

    const formData = {
      id: row.id || '',
    }

    const response = await http.post(`${SERVER_URL}/sync/path-delete`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      ElMessage.success('删除同步目录成功')
      loadDirectories()
    } else {
      ElMessage.error(response?.data.message || '删除同步目录失败')
    }
  } catch (error) {
    if (error !== 'cancel') {
      console.error('删除同步目录错误')
      ElMessage.error('删除同步目录失败')
    }
  } finally {
    if (directories.value[index]) {
      directories.value[index].deleting = false
    }
  }
}

const handleFullStart = async (row: SyncDirectory, index: number) => {
  try {
    directories.value[index].starting = true

    const formData = {
      id: row.id || '',
    }

    const response = await http.post(`${SERVER_URL}/sync/path/full-start`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      const nextStatus = Number(response.data.data?.is_running ?? 1)
      applySyncPathRunningStatus(row.id, nextStatus)
      ElMessage.success(`同步目录“${row.local_path}”启动成功`)
    } else {
      ElMessage.error(response?.data.message || '启动同步目录失败')
    }
  } catch {
    console.error('启动同步目录错误')
    ElMessage.error('启动同步目录失败')
  } finally {
    if (directories.value[index]) {
      directories.value[index].starting = false
    }
  }
}

const handleStart = async (row: SyncDirectory, index: number) => {
  try {
    directories.value[index].starting = true

    const formData = {
      id: row.id || '',
    }

    const response = await http.post(`${SERVER_URL}/sync/path/start`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      const nextStatus = Number(response.data.data?.is_running ?? 1)
      applySyncPathRunningStatus(row.id, nextStatus)
      ElMessage.success(`同步目录“${row.local_path}”启动成功`)
    } else {
      ElMessage.error(response?.data.message || '启动同步目录失败')
    }
  } catch {
    console.error('启动同步目录错误')
    ElMessage.error('启动同步目录失败')
  } finally {
    if (directories.value[index]) {
      directories.value[index].starting = false
    }
  }
}

const handleStop = async (row: SyncDirectory, index: number) => {
  try {
    directories.value[index].stopping = true

    const formData = {
      id: row.id || '',
    }

    const response = await http.post(`${SERVER_URL}/sync/path/stop`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      ElMessage.success(`同步目录“${row.local_path}”停止成功`)
    } else {
      ElMessage.error(response?.data.message || '停止同步目录失败')
    }
  } catch {
    console.error('停止同步目录错误')
    ElMessage.error('停止同步目录失败')
  } finally {
    if (directories.value[index]) {
      directories.value[index].stopping = false
    }
  }
}

const scanDirectoryUploadRule = async (row: SyncDirectory) => {
  if (directoryUploadRulesLoadFailed.value) {
    ElMessage.warning('目录监控上传规则加载失败，请刷新后重试')
    return
  }
  const rules = getDirectoryUploadRules(row)
  if (rules.length === 0) {
    ElMessage.warning('该同步目录尚未配置目录监控上传')
    return
  }
  if (!row.directory_upload_enabled) {
    ElMessage.warning('目录监控上传未启用')
    return
  }
  const enabledRules = getEnabledDirectoryUploadRules(rules)
  if (enabledRules.length === 0) {
    ElMessage.warning('目录监控上传未启用')
    return
  }

  try {
    enabledRules.forEach((rule) => {
      rule.scanning = true
    })
    const response = await http.post(`${SERVER_URL}/directory-upload/sync-paths/${row.id}/scan`)
    if (response?.data.code === 200) {
      const accepted = response.data.data?.accepted
      ElMessage.success(
        typeof accepted === 'number' ? `扫描完成，已加入 ${accepted} 个候选文件` : '扫描已触发',
      )
    } else {
      ElMessage.error(response?.data.message || '触发扫描失败')
    }
  } catch {
    ElMessage.error('触发扫描失败')
  } finally {
    enabledRules.forEach((rule) => {
      rule.scanning = false
    })
  }
}

const toggleCron = async (row: SyncDirectory) => {
  try {
    const formData = {
      id: row.id || '',
    }

    const response = await http.post(`${SERVER_URL}/sync/path/toggle-cron`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      ElMessage.success(row.enable_cron ? '开启定时同步成功' : '关闭定时同步成功')
    } else {
      row.enable_cron = !row.enable_cron
      ElMessage.error(response?.data.message || '切换定时同步状态失败')
    }
  } catch {
    console.error('切换定时同步状态错误')
    row.enable_cron = !row.enable_cron
    ElMessage.error('切换定时同步状态失败')
  }
}

const loadScrapePaths = async () => {
  try {
    scrapePathsLoading.value = true
    const response = await http.get(`${SERVER_URL}/scrape/pathes`, {
      params: { source_type: 'local' },
    })

    if (response?.data.code === 200) {
      scrapePathOptions.value = (response.data.data || []).map(
        (item: { id: number; source_path: string }) => ({
          id: item.id,
          label: `#${item.id} - ${item.source_path}`,
        }),
      )
    } else {
      ElMessage.error(response?.data.message || '加载刮削目录失败')
    }
  } catch {
    console.error('加载刮削目录错误')
    ElMessage.error('加载刮削目录失败')
  } finally {
    scrapePathsLoading.value = false
  }
}

const openScrapePathDialog = async (row: SyncDirectory) => {
  currentSyncDirectory.value = row
  selectedScrapePathIds.value = []
  showScrapePathDialog.value = true
  await loadScrapePaths()
  if (row.id) {
    try {
      const response = await http.get(`${SERVER_URL}/sync/path/${row.id}/scrape-paths`)
      if (response?.data.code === 200) {
        selectedScrapePathIds.value = response.data.data || []
      }
    } catch {
      console.error('加载已关联刮削目录错误')
    }
  }
}

const saveScrapePathRelation = async () => {
  if (!currentSyncDirectory.value?.id) {
    ElMessage.error('同步目录 ID 不存在')
    return
  }

  try {
    saveScrapePathLoading.value = true
    const response = await http.post(`${SERVER_URL}/sync/path/scrape-paths`, {
      id: currentSyncDirectory.value.id,
      scrape_path_id: selectedScrapePathIds.value,
    })

    if (response?.data.code === 200) {
      ElMessage.success('关联刮削目录成功')
      showScrapePathDialog.value = false
    } else {
      ElMessage.error(response?.data.message || '关联刮削目录失败')
    }
  } catch {
    console.error('关联刮削目录错误')
    ElMessage.error('关联刮削目录失败')
  } finally {
    saveScrapePathLoading.value = false
  }
}

const onLegacySyncEvent = () => {
  void updatePathesStatus()
}

// 全局实时事件监听
useRealtimeEvent('sync_task_created', patchSyncPathStatus, () => {
  resetSyncTaskEventSequences(lastSyncPathEventSequence)
  lastSyncPathEventTime.clear()
  void loadDirectories()
})
useRealtimeEvent('sync_task_updated', patchSyncPathStatus)
useRealtimeEvent('sync_task_deleted', patchSyncPathStatus)
useRealtimeEvent('strm_sync_task_queued', patchSyncPathStatus)
useRealtimeEvent('strm_sync_task_start', onLegacySyncEvent)
useRealtimeEvent('strm_sync_task_complete', onLegacySyncEvent)

onMounted(() => {
  loadDirectories()
})
</script>

<style scoped>
.sync-directories-page {
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

.directories-content {
  padding: 0;
}

.directories-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 20px;
  margin-bottom: 24px;
}

.directory-card {
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

.directory-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.1);
}

.directory-card.is-running {
  border-color: #b3e19d;
  box-shadow: 0 2px 12px rgba(103, 194, 58, 0.2);
}

.directory-card.is-running:hover {
  box-shadow: 0 8px 24px rgba(103, 194, 58, 0.3);
}

.directory-card.is-waiting {
  border-color: #f3d19e;
  box-shadow: 0 2px 12px rgba(230, 162, 60, 0.2);
}

.directory-card.is-waiting:hover {
  box-shadow: 0 8px 24px rgba(230, 162, 60, 0.3);
}

.card-status-bar {
  height: 4px;
  background: #e4e7ed;
}

.card-status-bar.status-running {
  background: linear-gradient(90deg, #67c23a, #95d475);
  animation: pulse 2s infinite;
}

.card-status-bar.status-waiting {
  background: linear-gradient(90deg, #e6a23c, #f0c78a);
  animation: pulse 2s infinite;
}

.card-status-bar.status-idle {
  background: linear-gradient(90deg, #909399, #c0c4cc);
}

@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.6;
  }
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
  border-bottom: 1px solid #dcdfe6;
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

.card-path {
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
  align-items: center;
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
  min-height: 32px;
  min-width: 0;
}

.info-label {
  font-size: 13px;
  color: #909399;
  display: flex;
  align-items: center;
  gap: 4px;
}

.info-label.with-tooltip {
  cursor: help;
}

.help-icon {
  font-size: 14px;
  color: #c0c4cc;
}

.info-value {
  font-size: 14px;
  color: #303133;
  text-align: right;
}

.path-value {
  /* font-family: 'SF Mono', Monaco, monospace; */
  font-size: 14px;
  word-break: break-all;
  max-width: 200px;
}

.toggle-row .info-content {
  justify-content: space-between;
}

.status-row {
  margin-top: 8px;
  padding-top: 12px;
  border-top: 1px dashed #ebeef5;
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

.status-indicator.status-running {
  background: #f0f9eb;
  color: #67c23a;
}

.status-indicator.status-waiting {
  background: #fdf6ec;
  color: #e6a23c;
}

.status-indicator.status-idle {
  background: #f5f7fa;
  color: #909399;
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
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 8px;
  padding-top: 16px;
  margin-top: 12px;
  border-top: 1px solid #dcdfe6;
}

.card-footer__primary,
.card-footer__secondary {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.card-footer__primary {
  flex-wrap: wrap;
}

.card-footer__secondary {
  justify-content: flex-end;
  flex-wrap: nowrap;
  width: max-content;
}

.card-footer :deep(.el-button + .el-button) {
  margin-left: 0;
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

@media (max-width: 768px) {
  .sync-directories-page {
    padding: 0 8px 8px;
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

  .directories-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .directory-card {
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

  .card-path {
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

  .info-content {
    min-height: 28px;
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

  .card-footer {
    grid-template-columns: 1fr;
    gap: 10px;
  }

  .card-footer__primary {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(112px, 1fr));
    width: 100%;
  }

  .card-footer__secondary {
    display: flex;
    justify-content: flex-end;
    width: 100%;
  }

  .card-footer__primary :deep(.el-button) {
    width: 100%;
  }

  .card-footer__secondary :deep(.el-button.is-circle) {
    flex: 0 0 auto;
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

  .toggle-row .info-content {
    flex-direction: row;
    justify-content: space-between;
    align-items: center;
    width: 100%;
  }
}

.scrape-path-dialog-content {
  padding: 10px 0;
}

.dialog-tip {
  margin: 0 0 12px 0;
  font-size: 14px;
  color: #606266;
}
</style>
