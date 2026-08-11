<template>
  <div class="scrape-pathes-page">
    <PageHeader>
      <template #actions>
        <div class="header-actions">
          <el-button type="primary" class="add-btn" :icon="Plus" @click="goToAdd">
            <span class="btn-text">添加刮削目录</span>
          </el-button>
        </div>
      </template>
      <template #stats>
        <div class="stats-bar">
          <div class="stat-item">
            <div class="stat-icon total">
              <el-icon><FolderOpened /></el-icon>
            </div>
            <div class="stat-info">
              <span class="stat-value">{{ pathes.length }}</span>
              <span class="stat-label">总目录数</span>
            </div>
          </div>
          <div class="stat-item">
            <div class="stat-icon running">
              <el-icon><Loading /></el-icon>
            </div>
            <div class="stat-info">
              <span class="stat-value">{{ runningCount }}</span>
              <span class="stat-label">运行中</span>
            </div>
          </div>
          <div class="stat-item">
            <div class="stat-icon waiting">
              <el-icon><Clock /></el-icon>
            </div>
            <div class="stat-info">
              <span class="stat-value">{{ waitingCount }}</span>
              <span class="stat-label">等待中</span>
            </div>
          </div>
          <div class="stat-item">
            <div class="stat-icon cron">
              <el-icon><Timer /></el-icon>
            </div>
            <div class="stat-info">
              <span class="stat-value">{{ cronEnabledCount }}</span>
              <span class="stat-label">定时任务</span>
            </div>
          </div>
        </div>
      </template>
    </PageHeader>

    <div class="pathes-content">
      <div class="pathes-grid" v-if="pathes.length > 0">
        <div
          class="path-card"
          v-for="(row, index) in pathes"
          :key="row.id || index"
          :class="{ 'is-running': row.is_running === 2, 'is-waiting': row.is_running === 1 }"
        >
          <div class="card-status-bar" :class="getStatusClass(row)"></div>
          <div class="card-main">
            <div class="card-header">
              <div class="card-title-wrapper">
                <el-tooltip :content="'目录 ID：' + row.id" placement="bottom">
                  <span class="card-id">#{{ row.id }}</span>
                </el-tooltip>
                <span class="card-path">{{ row.source_path }}</span>
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
                  <span class="info-value">{{ getAccountName(row.account_id) }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><VideoCamera /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">媒体类型</span>
                  <span class="info-value">{{ getMediaTypeText(row.media_type) }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Folder /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">目标路径</span>
                  <span class="info-value path-value">{{
                    row.scrape_type === 'only_scrape' ? '-' : row.dest_path
                  }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Operation /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">操作方式</span>
                  <span class="info-value">{{ getScrapeTypeText(row.scrape_type) }}</span>
                </div>
              </div>

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Sort /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">整理方式</span>
                  <span class="info-value">{{ getRenameTypeText(row.rename_type) }}</span>
                </div>
              </div>

              <div class="info-row toggle-row">
                <div class="info-icon">
                  <el-icon><Timer /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">定时任务</span>
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

              <div class="info-row">
                <div class="info-icon">
                  <el-icon><Calendar /></el-icon>
                </div>
                <div class="info-content">
                  <span class="info-label">创建时间</span>
                  <span class="info-value">{{ formatTime(row.created_at || 0) }}</span>
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
              <el-button
                type="success"
                size="small"
                plain
                :icon="VideoPlay"
                @click="handleScan(row)"
                :loading="row.scanning"
                v-if="row.is_running === 0"
              >
                启动
              </el-button>

              <el-button
                type="info"
                size="small"
                plain
                :icon="VideoPause"
                @click="handleStop(row)"
                :loading="row.scanning"
                v-if="row.is_running !== 0"
              >
                停止
              </el-button>

              <el-button type="primary" size="small" plain :icon="Edit" @click="goToEdit(row)">
                编辑
              </el-button>

              <el-button
                type="danger"
                size="small"
                plain
                :icon="Delete"
                @click="handleDelete(row, index)"
                :loading="row.deleting"
              >
                删除
              </el-button>

              <el-button
                v-if="row.source_type !== 'local'"
                type="warning"
                size="small"
                plain
                :icon="Link"
                @click="openSyncPathDialog(row)"
              >
                关联
              </el-button>
            </div>
          </div>
        </div>
      </div>

      <div class="empty-state" v-else-if="!loading">
        <div class="empty-illustration">
          <el-icon class="empty-icon"><Film /></el-icon>
          <div class="empty-dots">
            <span></span>
            <span></span>
            <span></span>
          </div>
        </div>
        <h3 class="empty-title">暂无刮削目录</h3>
        <p class="empty-description">点击上方按钮添加第一个刮削目录</p>
        <el-button type="primary" :icon="Plus" @click="goToAdd"> 添加刮削目录 </el-button>
      </div>

      <div class="loading-state" v-if="loading">
        <el-icon class="loading-icon rotating"><Loading /></el-icon>
        <span>加载中…</span>
      </div>

      <div class="page-footer-tips">
        <div class="tips-header">
          <el-icon class="tips-icon"><InfoFilled /></el-icon>
          <span>使用说明</span>
        </div>
        <div class="tips-content">
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon><Warning /></el-icon>
              <span>基本规则</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item tip-highlight">
                <span class="tip-bullet">★</span>
                <span>来源路径请按照电影和电视剧区分，不要设置成同一个文件夹</span>
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>字幕等其他文件的处理方式都是移动到目标位置，不受整理方式的影响</span>
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span>开启定时任务后，每 13 分钟执行一次</span>
              </div>
            </div>
          </div>
          <div class="tip-group">
            <div class="tip-group-title">
              <el-icon><Setting /></el-icon>
              <span>性能设置</span>
            </div>
            <div class="tip-group-items">
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >默认刮削并发数为 5；并发越高速度越快，但触发 TMDB 或网盘限流的概率也会增加</span
                >
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >如需调整并发数，请在
                  <router-link to="/settings/tmdb">刮削设置</router-link> 中配置自己的 TMDB API
                  Key；否则会使用默认并发数</span
                >
              </div>
              <div class="tip-item">
                <span class="tip-bullet">•</span>
                <span
                  >刮削流程：扫描待刮削文件 → 刮削 →
                  整理。文件较多时，首次刮削可能需要较长时间</span
                >
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <el-dialog
      v-model="showSyncPathDialog"
      title="关联 STRM 同步目录"
      width="500px"
      :close-on-click-modal="false"
    >
      <div class="sync-path-dialog-content">
        <p class="dialog-tip">请选择要关联的 STRM 同步目录：</p>
        <el-alert
          title="刮削完成后，会自动将元数据放入 STRM 同步目录并生成 STRM 文件"
          type="info"
          :closable="false"
          show-icon
          style="margin-bottom: 16px"
        />
        <el-select
          v-model="selectedSyncPathIds"
          multiple
          filterable
          placeholder="请选择同步目录"
          style="width: 100%"
          :loading="syncPathsLoading"
        >
          <el-option
            v-for="item in syncPathOptions"
            :key="item.id"
            :label="item.label"
            :value="item.id"
          />
        </el-select>
      </div>
      <template #footer>
        <span class="dialog-footer">
          <el-button @click="showSyncPathDialog = false">取消</el-button>
          <el-button type="primary" @click="saveSyncPathRelation" :loading="saveSyncPathLoading">
            确定
          </el-button>
        </span>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { SERVER_URL } from '@/const'
import PageHeader from '@/components/common/PageHeader.vue'
import { useHttpClient } from '@/http/client'
import { onMounted, ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Plus,
  Loading,
  Folder,
  Edit,
  Delete,
  VideoPlay,
  VideoPause,
  InfoFilled,
  Timer,
  FolderOpened,
  Clock,
  User,
  Calendar,
  Film,
  VideoCamera,
  Operation,
  Sort,
  CircleCheck,
  Setting,
  Warning,
  Link,
} from '@element-plus/icons-vue'
import { formatTime } from '@/utils/timeUtils'
import { sourceTypeTagMap, sourceTypeMap } from '@/utils/sourceTypeUtils'

interface ScrapePath {
  id?: number
  source_type: string
  account_id?: number
  media_type: string
  source_path: string
  dest_path: string
  scrape_type: string
  rename_type: string
  enable_category: boolean
  created_at?: number
  deleting: boolean
  editing: boolean
  scanning: boolean
  enable_cron?: boolean
  is_running: number
}

interface CloudAccount {
  id: number
  name: string
  source_type: string
}

const http = useHttpClient()
const router = useRouter()

const pathes = ref<ScrapePath[]>([])
const loading = ref(false)
const accounts = ref<CloudAccount[]>([])
const accountsLoading = ref(false)
const showSyncPathDialog = ref(false)
const selectedSyncPathIds = ref<number[]>([])
const syncPathOptions = ref<{ id: number; label: string }[]>([])
const syncPathsLoading = ref(false)
const saveSyncPathLoading = ref(false)
const currentScrapePath = ref<ScrapePath | null>(null)

const runningCount = computed(() => pathes.value.filter((p) => p.is_running === 2).length)
const waitingCount = computed(() => pathes.value.filter((p) => p.is_running === 1).length)
const cronEnabledCount = computed(() => pathes.value.filter((p) => p.enable_cron).length)

const getStatusClass = (row: ScrapePath) => {
  if (row.is_running === 2) return 'status-running'
  if (row.is_running === 1) return 'status-waiting'
  return 'status-idle'
}

const getStatusText = (row: ScrapePath) => {
  if (row.is_running === 2) return '运行中'
  if (row.is_running === 1) return '等待中'
  return '空闲'
}

const goToAdd = () => {
  router.push('/scrape-path/add')
}

const goToEdit = (row: ScrapePath) => {
  router.push(`/scrape-path/edit/${row.id}`)
}

const getAccountName = (accountId?: number): string => {
  if (!accountId) return ''
  const account = accounts.value.find((a) => a.id === accountId)
  return account ? account.name : ''
}

const getMediaTypeText = (mediaType: string): string => {
  const typeMap: Record<string, string> = {
    movie: '电影',
    tvshow: '电视剧',
    other: '其他',
  }
  return typeMap[mediaType] || mediaType
}

const getScrapeTypeText = (scrapeType: string): string => {
  const typeMap: Record<string, string> = {
    only_scrape: '仅刮削',
    scrape_and_rename: '刮削和整理',
    only_rename: '仅整理',
  }
  return typeMap[scrapeType] || scrapeType
}

const getRenameTypeText = (renameType: string): string => {
  const typeMap: Record<string, string> = {
    move: '移动',
    copy: '复制',
    soft_symlink: '软链接',
    hard_symlink: '硬链接',
    same: '-',
  }
  return typeMap[renameType] || renameType
}

const loadPathes = async () => {
  try {
    loading.value = true
    const response = await http.get(`${SERVER_URL}/scrape/pathes`)

    if (response?.data.code === 200) {
      pathes.value = response.data.data || []
    } else {
      ElMessage.error(response?.data.message || '加载刮削目录失败')
      pathes.value = []
    }
  } catch {
    console.error('加载刮削目录错误')
    ElMessage.error('加载刮削目录失败')
    pathes.value = []
  } finally {
    loading.value = false
  }
}

const updatePathesStatus = async () => {
  const response = await http.get(`${SERVER_URL}/scrape/pathes`)

  if (response?.data.code === 200) {
    for (const p of response?.data?.data || []) {
      const path = pathes.value.find((pa) => pa.id === p.id)
      if (path) {
        path.is_running = p.is_running
      }
    }
  }
}

const loadAccounts = async (sourceType?: string) => {
  try {
    accountsLoading.value = true
    const response = await http.get(`${SERVER_URL}/account/list`, {
      params: { source_type: sourceType },
    })

    if (response?.data.code === 200) {
      accounts.value = response.data.data || []
    } else {
      ElMessage.error(response?.data.message || '加载账号列表失败')
      accounts.value = []
    }
  } catch {
    console.error('加载账号列表错误')
    ElMessage.error('加载账号列表失败')
    accounts.value = []
  } finally {
    accountsLoading.value = false
  }
}

const handleDelete = async (row: ScrapePath, index: number) => {
  try {
    await ElMessageBox.confirm('确定要删除这个刮削目录吗？', '确认删除', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    if (pathes.value[index]) {
      pathes.value[index].deleting = true
    }

    const response = await http.delete(`${SERVER_URL}/scrape/pathes/${row.id}`)

    if (response?.data.code === 200) {
      ElMessage.success('删除刮削目录成功')
      loadPathes()
    } else {
      ElMessage.error(response?.data.message || '删除刮削目录失败')
    }
  } catch (error) {
    if (error !== 'cancel') {
      console.error('删除刮削目录错误')
      ElMessage.error('删除刮削目录失败')
    }
  } finally {
    if (pathes.value[index]) {
      pathes.value[index].deleting = false
    }
  }
}

const handleScan = async (row: ScrapePath) => {
  if (!http) return

  try {
    row.scanning = true
    await http.post(`${SERVER_URL}/scrape/pathes/start`, { id: row.id })
    ElMessage.success('刮削任务已开始')
  } catch (error) {
    ElMessage.error('启动刮削任务失败')
    console.error('启动刮削任务错误：', error)
  } finally {
    row.scanning = false
  }
}

const handleStop = async (row: ScrapePath) => {
  if (!http) return

  try {
    row.scanning = true
    await http.post(`${SERVER_URL}/scrape/pathes/stop`, { id: row.id })
    ElMessage.success('刮削任务已停止')
  } catch (error) {
    ElMessage.error('停止刮削任务失败')
    console.error('停止刮削任务错误：', error)
  } finally {
    row.scanning = false
  }
}

const toggleCron = async (row: ScrapePath) => {
  try {
    const formData = {
      id: row.id || 0,
    }

    const response = await http.post(`${SERVER_URL}/scrape/pathes/toggle-cron`, formData, {
      headers: {
        'Content-Type': 'application/json',
      },
    })

    if (response?.data.code === 200) {
      ElMessage.success(row.enable_cron ? '开启定时任务成功' : '关闭定时任务成功')
    } else {
      row.enable_cron = !row.enable_cron
      ElMessage.error(response?.data.message || '切换定时任务状态失败')
    }
  } catch {
    console.error('切换定时任务状态错误')
    row.enable_cron = !row.enable_cron
    ElMessage.error('切换定时任务状态失败')
  }
}

const loadSyncPaths = async (sourceType?: string) => {
  try {
    syncPathsLoading.value = true
    const response = await http.get(`${SERVER_URL}/sync/path-list`, {
      params: { page: 1, page_size: 9999, source_type: sourceType },
    })

    if (response?.data.code === 200) {
      syncPathOptions.value = (response.data.data.list || []).map(
        (item: { id: number; source_type: string; remote_path: string }) => ({
          id: item.id,
          label: `#${item.id} - ${item.source_type} - ${item.remote_path}`,
        }),
      )
    } else {
      ElMessage.error(response?.data.message || '加载同步目录失败')
    }
  } catch {
    console.error('加载同步目录错误')
    ElMessage.error('加载同步目录失败')
  } finally {
    syncPathsLoading.value = false
  }
}

const openSyncPathDialog = async (row: ScrapePath) => {
  currentScrapePath.value = row
  selectedSyncPathIds.value = []
  showSyncPathDialog.value = true
  await loadSyncPaths(row.source_type)
  if (row.id) {
    try {
      const response = await http.get(`${SERVER_URL}/scrape/sync-pathes`, {
        params: { scrape_path_id: row.id },
      })
      if (response?.data.code === 200) {
        selectedSyncPathIds.value = response.data.data || []
      }
    } catch {
      console.error('加载已关联同步目录错误')
    }
  }
}

const saveSyncPathRelation = async () => {
  if (!currentScrapePath.value?.id) {
    ElMessage.error('刮削目录 ID 不存在')
    return
  }

  try {
    saveSyncPathLoading.value = true
    const response = await http.post(`${SERVER_URL}/scrape/sync-pathes`, {
      scrape_path_id: currentScrapePath.value.id,
      sync_path_ids: selectedSyncPathIds.value,
    })

    if (response?.data.code === 200) {
      ElMessage.success('关联同步目录成功')
      showSyncPathDialog.value = false
    } else {
      ElMessage.error(response?.data.message || '关联同步目录失败')
    }
  } catch {
    console.error('关联同步目录错误')
    ElMessage.error('关联同步目录失败')
  } finally {
    saveSyncPathLoading.value = false
  }
}

// 全局实时事件监听
import { useRealtimeEvent } from '@/composables/useRealtimeEvents'

const onScraperEvent = () => {
  updatePathesStatus()
}

useRealtimeEvent('scraper_task_start', onScraperEvent, () => void loadPathes())
useRealtimeEvent('scraper_task_complete', onScraperEvent)
useRealtimeEvent('scraper_item_complete', onScraperEvent)

onMounted(async () => {
  await loadPathes()
  await loadAccounts()
})
</script>

<style scoped>
.scrape-pathes-page {
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

.stats-bar {
  display: flex;
  gap: 16px;
  flex-wrap: wrap;
}

.stat-item {
  display: flex;
  align-items: center;
  gap: 12px;
  background: #f5f7fa;
  padding: 12px 16px;
  border-radius: 8px;
  min-width: 140px;
}

.stat-icon {
  width: 40px;
  height: 40px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 20px;
}

.stat-icon.total {
  background: #ecf5ff;
  color: #409eff;
}

.stat-icon.running {
  background: #f0f9eb;
  color: #67c23a;
}

.stat-icon.waiting {
  background: #fdf6ec;
  color: #e6a23c;
}

.stat-icon.cron {
  background: #f4f4f5;
  color: #909399;
}

.stat-info {
  display: flex;
  flex-direction: column;
}

.stat-value {
  font-size: 20px;
  font-weight: 600;
  line-height: 1.2;
  color: #303133;
}

.stat-label {
  font-size: 12px;
  color: #909399;
}

.pathes-content {
  padding: 0;
}

.pathes-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 20px;
  margin-bottom: 24px;
}

.path-card {
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

.path-card:hover {
  transform: translateY(-4px);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.1);
}

.path-card.is-running {
  border-color: #b3e19d;
  box-shadow: 0 2px 12px rgba(103, 194, 58, 0.2);
}

.path-card.is-running:hover {
  box-shadow: 0 8px 24px rgba(103, 194, 58, 0.3);
}

.path-card.is-waiting {
  border-color: #f3d19e;
  box-shadow: 0 2px 12px rgba(230, 162, 60, 0.2);
}

.path-card.is-waiting:hover {
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
  align-items: flex-start;
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
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 16px;
  margin-top: 12px;
  border-top: 1px solid #dcdfe6;
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
  background: #409eff;
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
  color: #409eff;
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

.tip-highlight {
  background: #fdf6ec;
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
  .scrape-pathes-page {
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

  .pathes-grid {
    grid-template-columns: 1fr;
    gap: 12px;
  }

  .path-card {
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

  .card-footer {
    grid-template-columns: 1fr;
    gap: 6px;
  }
}

.sync-path-dialog-content {
  padding: 10px 0;
}

.dialog-tip {
  margin: 0 0 12px 0;
  font-size: 14px;
  color: #606266;
}
</style>
