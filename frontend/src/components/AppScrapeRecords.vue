<template>
  <div class="scrape-records-container" ref="pageContainerRef">
    <div class="header-section">
      <div class="card-header">
        <div>
          <h2 class="hide-on-mobile">刮削记录</h2>
          <p class="queue-description">
            刮削时会把临时文件放在 config/tmp/刮削临时文件/，方便排查问题，刮削完成后会自动删除。
          </p>
        </div>
      </div>

      <div class="top-actions">
        <div class="action-group">
          <div class="action-buttons">
            <el-tooltip
              content="将所选识别错误记录导出为文件，方便反馈问题"
              placement="top"
              popper-class="qms-contained-tooltip"
              :append-to="pageContainerRef ?? undefined"
            >
              <el-button
                type="primary"
                @click="handleExportErrors"
                :disabled="selectedRecords.length === 0"
                >导出识别错误</el-button
              >
            </el-tooltip>
            <el-tooltip
              content="删除刮削记录后，对应文件会在下次扫描时重新识别和刮削。"
              placement="top"
              popper-class="qms-contained-tooltip"
              :append-to="pageContainerRef ?? undefined"
            >
              <el-button
                type="danger"
                @click="handleDeleteSelectedRecords"
                :disabled="selectedRecords.length === 0"
                >删除所选记录</el-button
              >
            </el-tooltip>
            <el-tooltip
              content="请选择整理失败的记录；所选记录会被标记为待整理，并在下次整理时重试。"
              placement="top"
              popper-class="qms-contained-tooltip"
              :append-to="pageContainerRef ?? undefined"
            >
              <el-button
                type="warning"
                @click="handleRename"
                :disabled="selectedRecords.length === 0"
                >重新整理所选</el-button
              >
            </el-tooltip>
          </div>
          <div class="selected-count">已选择 {{ selectedRecords.length }} 条记录</div>
        </div>
        <div class="action-group">
          <div class="action-buttons">
            <el-tooltip content="将同一部电视剧的所有集合并为一条记录，方便查看" placement="top">
              <el-button type="primary" @click="toggleMergeEpisodes">
                {{ isMerged ? '显示电视剧集' : '合并电视剧集' }}
              </el-button>
            </el-tooltip>
            <el-button type="warning" @click="handleDeleteFailedRecords">清除失败记录</el-button>
            <el-tooltip content="删除所有刮削记录，慎用！" placement="top">
              <el-button type="danger" @click="handleTruncateAll">清空记录</el-button>
            </el-tooltip>
          </div>
        </div>
      </div>

      <div class="search-filter-section">
        <el-input
          v-model="nameFilter"
          placeholder="按文件名模糊搜索"
          class="filter-input"
          @keyup.enter="applyFilter"
        />
        <el-select v-model="statusFilter" placeholder="筛选状态" class="filter-select">
          <el-option label="全部" value=""></el-option>
          <el-option label="未刮削" value="scanned"></el-option>
          <el-option label="刮削中" value="scraping"></el-option>
          <el-option label="已刮削" value="scraped"></el-option>
          <el-option label="刮削失败" value="scrape_failed"></el-option>
          <el-option label="整理中" value="renaming"></el-option>
          <el-option label="已整理" value="renamed"></el-option>
          <el-option label="整理失败" value="rename_failed"></el-option>
        </el-select>
        <el-select v-model="typeFilter" placeholder="筛选类型" class="filter-select-small">
          <el-option label="全部" value=""></el-option>
          <el-option label="电影" value="movie"></el-option>
          <el-option label="电视剧" value="tvshow"></el-option>
          <el-option label="其他" value="other"></el-option>
        </el-select>
        <el-button type="primary" @click="applyFilter">筛选</el-button>
        <el-button @click="resetFilter">重置</el-button>
      </div>
    </div>

    <div class="table-section">
      <div class="table-container" v-loading="queryLoading">
        <ResponsiveRecordTable
          class="scrape-record-table"
          :rows="records"
          :columns="scrapeRecordColumns"
          :actions="scrapeRecordActions"
          :row-key="getScrapeRecordRowKey"
          :loading="initialLoading || queryLoading"
          :is-mobile="isMobileView"
          :expanded-row-keys="pageState.expandedRowKeys"
          show-all-details
          show-selection
          @selection-change="handleSelectionChange"
          @expand-change="handleExpandChange"
          @action="handleScrapeRecordAction"
        >
          <template #cell-path_is_scraping="{ row }">
            <span class="info-value" v-if="row.path_is_scraping">
              <el-icon class="is-loading">
                <Loading />
              </el-icon>
              <el-text class="mx-1" type="primary">刮削中…</el-text>
            </span>
            <span class="info-value" v-else>
              <el-text class="mx-1" type="info" size="small">未执行</el-text>
            </span>
          </template>
          <template #cell-status="{ row }">
            <div class="scrape-status-cell">
              <el-tooltip
                :content="getStatusTooltip(row.status)"
                placement="top"
                popper-class="qms-contained-tooltip"
                :append-to="pageContainerRef ?? undefined"
              >
                <el-tag :type="getStatusTagType(row.status)" class="scrape-status-tag">
                  {{ getStatusName(row.status) }}
                </el-tag>
              </el-tooltip>
            </div>
          </template>
          <template #cell-type="{ row }">
            <el-tag :type="getTypeTagType(row.type)">
              {{ getTypeName(row.type) }}
            </el-tag>
          </template>
          <template #cell-source_type="{ row }">
            <el-tag :type="getSourceTypeTagType(row.source_type)">
              {{ getSourceTypeName(row.source_type) }}
            </el-tag>
          </template>
          <template #cell-path="{ row }">
            <el-tooltip
              :content="getPathTooltip(row)"
              placement="top"
              popper-class="qms-contained-tooltip"
              :append-to="pageContainerRef ?? undefined"
            >
              <div class="scrape-path-cell">
                <p class="path-text">{{ row.source_full_path }}</p>
                <p class="scrape-path-cell__rename">
                  <el-tag :type="getRenameTypeTagType(row.rename_type)">
                    {{ getRenameTypeName(row.rename_type) }}
                  </el-tag>
                  <span>到</span>
                </p>
                <p class="path-text">{{ row.dest_full_path }}</p>
              </div>
            </el-tooltip>
          </template>
        </ResponsiveRecordTable>

        <ResponsivePagination
          v-model:current-page="currentPage"
          v-model:page-size="pageSize"
          :page-sizes="[20, 100, 200, 500]"
          :total="total"
          :is-mobile="isMobileView"
          @size-change="handleSizeChange"
          @current-change="handleCurrentChange"
        />
      </div>
    </div>

    <!-- 详情对话框 -->
    <el-dialog v-model="showDetailDialog" title="刮削详情" width="600px">
      <div v-if="selectedRecord" class="detail-content">
        <el-descriptions border direction="vertical">
          <el-descriptions-item label="原始路径">
            <el-tooltip :content="selectedRecord.path" placement="top">
              <pre
                style="
                  margin: 0;
                  white-space: pre-wrap;
                  word-break: break-all;
                  max-height: 100px;
                  overflow: auto;
                "
                >{{ selectedRecord.path }}</pre>
            </el-tooltip>
          </el-descriptions-item>
          <el-descriptions-item label="原始文件名">
            <el-tooltip :content="selectedRecord.file_name" placement="top">
              <pre
                style="
                  margin: 0;
                  white-space: pre-wrap;
                  word-break: break-all;
                  max-height: 100px;
                  overflow: auto;
                "
                >{{ selectedRecord.file_name }}</pre>
            </el-tooltip>
          </el-descriptions-item>
          <el-descriptions-item label="识别名称">{{
            selectedRecord.media_name || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="识别年份">{{
            selectedRecord.year || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="识别类型">{{
            getTypeName(selectedRecord.type)
          }}</el-descriptions-item>
          <el-descriptions-item label="二级分类">{{
            selectedRecord.category_name || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="新文件夹">{{
            selectedRecord.new_dest_path || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="新文件名">{{
            selectedRecord.new_dest_name || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="TMDB ID">{{
            selectedRecord.tmdb_id || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="季集">
            <span v-if="selectedRecord.type == 'tvshow'">
              S{{ selectedRecord.season_number }}E{{ selectedRecord.episode_number }}
            </span>
            <span v-else>-</span>
          </el-descriptions-item>
          <el-descriptions-item label="集名称">{{
            selectedRecord.episode_name || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="音轨数量">{{
            selectedRecord.audio_count || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="字幕数量">{{
            selectedRecord.subtitle_count || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="分辨率">{{
            selectedRecord.resolution || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="分辨率等级">{{
            selectedRecord.resolution_level || '-'
          }}</el-descriptions-item>
          <el-descriptions-item label="HDR">{{
            selectedRecord.is_hdr ? '是' : '否'
          }}</el-descriptions-item>
          <el-descriptions-item label="状态">{{
            getStatusName(selectedRecord.status)
          }}</el-descriptions-item>
          <el-descriptions-item label="识别时间">{{
            formatTimestamp(selectedRecord.scanned_at)
          }}</el-descriptions-item>
          <el-descriptions-item label="刮削时间">{{
            formatTimestamp(selectedRecord.scraped_at)
          }}</el-descriptions-item>
          <el-descriptions-item label="失败原因">
            <pre
              v-if="selectedRecord.failed_reason"
              style="
                margin: 0;
                white-space: pre-wrap;
                word-break: break-all;
                max-height: 100px;
                overflow: auto;
              "
              >{{ selectedRecord.failed_reason }}</pre>
            <span v-else>-</span>
          </el-descriptions-item>
        </el-descriptions>
      </div>
    </el-dialog>

    <!-- 重新识别对话框 -->
    <el-dialog
      v-model="showReScrapeDialog"
      title="重新识别"
      width="700px"
      :close-on-click-modal="false"
      :before-close="handleReScrapeDialogBeforeClose"
    >
      <div class="re-scrape-dialog-content">
        <div class="search-section">
          <el-form label-width="80px" @submit.prevent>
            <el-form-item label="文件名">
              <el-text>{{ reScrapeForm.originalFileName }}</el-text>
            </el-form-item>
            <el-form-item label="搜索方式">
              <el-radio-group v-model="searchMode" @change="handleSearchModeChange">
                <el-radio value="name">名称+年份</el-radio>
                <el-radio value="tmdb">TMDB ID</el-radio>
              </el-radio-group>
            </el-form-item>
            <div v-if="searchMode === 'name'" class="search-row">
              <el-form-item label="名称" class="name-input">
                <el-input v-model="reScrapeForm.name" placeholder="请输入影视剧名称" />
              </el-form-item>
              <el-form-item label="年份" class="year-input">
                <el-input
                  v-model="reScrapeForm.year"
                  placeholder="年份"
                  type="number"
                  :min="1900"
                  :max="new Date().getFullYear() + 5"
                />
              </el-form-item>
            </div>
            <div v-if="searchMode === 'tmdb'" class="search-row">
              <el-form-item label="TMDB ID" class="tmdb-input">
                <el-input
                  v-model="reScrapeForm.tmdb_id"
                  placeholder="请输入 TMDB ID"
                  type="number"
                  :min="1"
                />
              </el-form-item>
            </div>
            <el-form-item
              label="季"
              v-if="reScrapeForm.type == 'tvshow'"
              class="season-episode-row"
            >
              <el-input
                v-model="reScrapeForm.season"
                placeholder="季数"
                type="number"
                :min="0"
                :max="100"
                style="width: 100px"
              />
              <span class="episode-label">集</span>
              <el-input
                v-model="reScrapeForm.episode"
                placeholder="集数"
                type="number"
                :min="0"
                :max="10000"
                style="width: 100px"
              />
            </el-form-item>
          </el-form>
        </div>

        <div class="search-results-section" v-if="searchResults.length > 0">
          <div class="results-header">
            <el-icon>
              <Film />
            </el-icon>
            <span>搜索结果（{{ searchResults.length }}）</span>
          </div>
          <div class="results-list">
            <div class="result-item" v-for="item in searchResults" :key="item.tmdb_id">
              <div class="result-poster">
                <el-image :src="item.poster_url" fit="cover" lazy>
                  <template #error>
                    <div class="poster-placeholder">
                      <el-icon>
                        <Picture />
                      </el-icon>
                    </div>
                  </template>
                </el-image>
              </div>
              <div class="result-info">
                <div class="result-title">{{ item.title }}</div>
                <div class="result-original-title" v-if="item.original_title !== item.title">
                  原名：{{ item.original_title }}
                </div>
                <div class="result-meta">
                  <el-tag size="small" type="info">{{ item.year || '未知年份' }}</el-tag>
                  <el-tag size="small" type="warning">TMDB ID：{{ item.tmdb_id }}</el-tag>
                </div>
                <div class="result-overview" v-if="item.overview">
                  {{ item.overview }}
                </div>
              </div>
              <div class="result-action">
                <el-button
                  type="primary"
                  size="small"
                  @click="selectSearchResult(item)"
                  :loading="item.selecting"
                >
                  选择
                </el-button>
              </div>
            </div>
          </div>
        </div>

        <div class="empty-results" v-if="hasSearched && searchResults.length === 0">
          <el-empty description="未找到匹配结果" :image-size="80" />
        </div>
      </div>
      <template #footer>
        <div class="dialog-footer">
          <el-button @click="closeReScrapeDialog">取消</el-button>
          <el-button type="primary" :icon="Search" @click="searchTmdb" :loading="searchLoading">
            搜索
          </el-button>
        </div>
      </template>
    </el-dialog>

    <!-- 回滚对话框 -->
    <el-dialog
      v-model="showRollbackDialog"
      title="注意"
      width="320px"
      :before-close="handleRollbackDialogBeforeClose"
    >
      <p>
        确认回滚这条刮削记录吗？回滚后，视频和字幕会放回原目录，并根据已查询到的 TMDB
        信息重命名；这条记录会被删除，后续扫描时会重新刮削该影片。
      </p>
      <template #footer>
        <div class="dialog-footer">
          <el-button @click="invalidateRecordActionContext">取消</el-button>
          <el-button type="primary" @click="openRollbackReScrapeDialog"> 确认 </el-button>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import ResponsiveRecordTable from '@/components/records/ResponsiveRecordTable.vue'
import { SERVER_URL } from '@/const'
import { createActiveRequestGate } from '@/composables/useActiveRequestGate'
import { useBackgroundRefresh } from '@/composables/useBackgroundRefresh'
import { useDeviceType } from '@/composables/useDeviceType'
import { useHttpClient } from '@/http/client'
import { mergeStableList, retainExistingKeys } from '@/composables/useStableList'
import { useRealtimeEvent } from '@/composables/useRealtimeEvents'
import { usePageStateStore } from '@/stores/pageState'
import type { RecordAction, RecordActionPayload, RecordColumn } from '@/types/recordTable'
import { formatTimestamp } from '@/utils/timeUtils'
import {
  computed,
  nextTick,
  onActivated,
  onDeactivated,
  onMounted,
  onUnmounted,
  ref,
  useTemplateRef,
} from 'vue'
import { Film, Finished, Picture, Refresh, Search, View } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'

const http = useHttpClient()

// 定义刮削记录接口
interface ScrapeRecord {
  id: number
  type: 'movie' | 'tvshow' | 'other'
  path: string
  file_name: string
  media_name: string
  original_name: string
  year: number
  tmdb_id: number
  season_number: number
  episode_number: number
  episode_name?: string
  status:
    'scanned' | 'scraping' | 'scraped' | 'scrape_failed' | 'renaming' | 'renamed' | 'rename_failed'
  failed_reason: string
  created_at: number
  updated_at: number
  scanned_at: number
  scraped_at: number
  renamed_at: number
  audio_count: number
  subtitle_count: number
  resolution: string
  resolution_level: string
  is_hdr: boolean
  category_name: string
  new_dest_path: string
  new_dest_name: string
  path_is_scraping: boolean
  source_full_path: string
  dest_full_path: string
  source_type: string
  rename_type: string
  scrape_type: string
}

interface TmdbSearchResult {
  tmdb_id: number
  title: string
  original_title: string
  year: string
  poster_url: string
  overview: string
  selecting?: boolean
}

interface ReScrapeFormState {
  type: ScrapeRecord['type'] | ''
  id: number
  name: string
  year: string
  tmdb_id: string
  originalFileName: string
  season: number
  episode: number
  status: ScrapeRecord['status'] | ''
}

interface RecordActionContextSnapshot {
  recordId: number
  contextVersion: number
}

interface ScrapeRecordsMutationContextSnapshot {
  contextVersion: number
}

type DialogCloseDone = () => void

// 状态变量
const pageStateStore = usePageStateStore()
const pageState = pageStateStore.getPageState('scrape-records', {
  currentPage: 1,
  pageSize: 20,
  filters: {
    status: '',
    type: '',
    name: '',
  },
})
const { initialLoading, isRefreshing, runRefresh } = useBackgroundRefresh()
const pageContainerRef = useTemplateRef<HTMLElement>('pageContainerRef')
const getPageScrollContainer = () =>
  pageContainerRef.value?.closest<HTMLElement>('.main-content') ?? pageContainerRef.value
const records = ref<ScrapeRecord[]>([])
const originalRecords = ref<ScrapeRecord[]>([])
const isMerged = ref(false)
const selectedRecords = ref<ScrapeRecord[]>([])
const queryLoading = ref(false)
const { isMobile: isMobileView } = useDeviceType()
const statusFilter = computed({
  get: () => String(pageState.filters.status ?? ''),
  set: (value) => pageStateStore.setFilter('scrape-records', 'status', value),
})
const typeFilter = computed({
  get: () => String(pageState.filters.type ?? ''),
  set: (value) => pageStateStore.setFilter('scrape-records', 'type', value),
})
const nameFilter = computed({
  get: () => String(pageState.filters.name ?? ''),
  set: (value) => pageStateStore.setFilter('scrape-records', 'name', value),
})
const showDetailDialog = ref(false)
const selectedRecord = ref<ScrapeRecord | null>(null)
const showRollbackDialog = ref(false)
const recordActionContextVersion = ref(0)
const activeRecordActionContext = ref<RecordActionContextSnapshot | null>(null)
const scrapeRecordsMutationContextVersion = ref(0)
const activeScrapeRecordsMutationContext = ref<ScrapeRecordsMutationContextSnapshot | null>(null)
const pendingScrapeRecordsRefresh = ref(false)
let isPageActive = false
const scrapeRecordsRequestGate = createActiveRequestGate(() => isPageActive)

// 分页相关
const currentPage = computed({
  get: () => pageState.currentPage,
  set: (value) => pageStateStore.setPagination('scrape-records', value, pageState.pageSize),
})
const pageSize = computed({
  get: () => pageState.pageSize,
  set: (value) => pageStateStore.setPagination('scrape-records', pageState.currentPage, value),
})
const total = ref(0)

function createDefaultReScrapeForm(): ReScrapeFormState {
  return {
    type: '',
    id: 0,
    name: '',
    year: '',
    tmdb_id: '',
    originalFileName: '',
    season: -1,
    episode: -1,
    status: '',
  }
}

function createRecordActionContextSnapshot(record: ScrapeRecord): RecordActionContextSnapshot {
  return {
    recordId: record.id,
    contextVersion: recordActionContextVersion.value,
  }
}

function startRecordActionContext(record: ScrapeRecord): RecordActionContextSnapshot {
  invalidateRecordActionContext()
  const snapshot = createRecordActionContextSnapshot(record)
  activeRecordActionContext.value = snapshot
  return snapshot
}

function isRecordActionContextCurrent(
  snapshot: RecordActionContextSnapshot | null,
  recordId?: number,
): snapshot is RecordActionContextSnapshot {
  const activeSnapshot = activeRecordActionContext.value

  return (
    isPageActive &&
    !!snapshot &&
    !!activeSnapshot &&
    activeSnapshot.recordId === snapshot.recordId &&
    activeSnapshot.contextVersion === snapshot.contextVersion &&
    snapshot.contextVersion === recordActionContextVersion.value &&
    (recordId === undefined || snapshot.recordId === recordId) &&
    records.value.some((record) => record.id === snapshot.recordId)
  )
}

function resetRecordActionState() {
  showDetailDialog.value = false
  selectedRecord.value = null
  showReScrapeDialog.value = false
  showRollbackDialog.value = false
  activeRecordActionContext.value = null
  reScrapeForm.value = createDefaultReScrapeForm()
  searchResults.value = []
  searchLoading.value = false
  hasSearched.value = false
  searchMode.value = 'name'
}

function invalidateRecordActionContext() {
  recordActionContextVersion.value += 1
  resetRecordActionState()
}

function invalidateScrapeRecordsMutationContext() {
  scrapeRecordsMutationContextVersion.value += 1
  activeScrapeRecordsMutationContext.value = null
}

function startScrapeRecordsMutationContext(): ScrapeRecordsMutationContextSnapshot {
  invalidateScrapeRecordsMutationContext()
  const snapshot = {
    contextVersion: scrapeRecordsMutationContextVersion.value,
  }
  activeScrapeRecordsMutationContext.value = snapshot
  return snapshot
}

function isScrapeRecordsMutationContextCurrent(
  snapshot: ScrapeRecordsMutationContextSnapshot | null,
): snapshot is ScrapeRecordsMutationContextSnapshot {
  return (
    isPageActive &&
    !!snapshot &&
    !!activeScrapeRecordsMutationContext.value &&
    activeScrapeRecordsMutationContext.value.contextVersion === snapshot.contextVersion &&
    snapshot.contextVersion === scrapeRecordsMutationContextVersion.value
  )
}

function finishScrapeRecordsMutationContext(snapshot: ScrapeRecordsMutationContextSnapshot) {
  if (isScrapeRecordsMutationContextCurrent(snapshot)) {
    activeScrapeRecordsMutationContext.value = null
  }
}

const handleExpandChange = (row: ScrapeRecord, expandedRows: ScrapeRecord[]) => {
  pageStateStore.setExpandedRowKeys(
    'scrape-records',
    expandedRows.map((item) => String(item.id)),
  )
}

const getScrapeRecordRowKey = (row: ScrapeRecord) => row.id

function canReScrape(row: ScrapeRecord) {
  return (
    (row.type === 'movie' &&
      (row.status === 'scrape_failed' || row.status === 'scanned' || row.status === 'renamed')) ||
    (row.type === 'tvshow' && row.status === 'scrape_failed')
  )
}

function canMarkAsFinished(row: ScrapeRecord) {
  return row.status === 'renaming' || row.status === 'scraped'
}

const scrapeRecordColumns: RecordColumn<ScrapeRecord>[] = [
  { key: 'path_is_scraping', label: '运行状态', priority: 'primary', width: 96, align: 'center' },
  {
    key: 'status',
    label: '文件状态',
    priority: 'primary',
    width: 132,
    align: 'center',
    showOverflowTooltip: false,
    detailField: {
      key: 'status',
      label: '文件状态',
      value: (row) => getStatusName(row.status),
    },
  },
  { key: 'type', label: '类型', priority: 'secondary', width: 88, align: 'center' },
  { key: 'source_type', label: '来源', priority: 'secondary', width: 88, align: 'center' },
  {
    key: 'path',
    label: '文件路径',
    priority: 'primary',
    minWidth: 320,
    showOverflowTooltip: false,
    detailField: {
      key: 'path',
      label: '文件路径',
      value: (row) => row.path,
      span: 2,
      isLongText: true,
    },
  },
  {
    key: 'media_name',
    label: '识别名称',
    priority: 'detail',
    detailField: {
      key: 'media_name',
      label: '识别名称',
      value: (row) => row.media_name || '-',
      span: 2,
      isLongText: true,
    },
  },
  {
    key: 'original_name',
    label: '原始名称',
    priority: 'detail',
    detailField: {
      key: 'original_name',
      label: '原始名称',
      value: (row) => row.original_name || '-',
      span: 2,
      isLongText: true,
    },
  },
  {
    key: 'tmdb_id',
    label: 'TMDB ID',
    priority: 'detail',
    detailField: { key: 'tmdb_id', label: 'TMDB ID', value: (row) => row.tmdb_id || '-' },
  },
  {
    key: 'year',
    label: '年份',
    priority: 'detail',
    detailField: { key: 'year', label: '年份', value: (row) => row.year || '-' },
  },
  {
    key: 'times',
    label: '时间',
    priority: 'detail',
    detailField: {
      key: 'times',
      label: '时间',
      value: (row) =>
        `创建 ${formatTimestamp(row.created_at)}，刮削 ${formatTimestamp(row.scraped_at)}，整理 ${formatTimestamp(row.renamed_at)}`,
      span: 2,
    },
  },
  {
    key: 'failed_reason',
    label: '失败原因',
    priority: 'detail',
    detailField: {
      key: 'failed_reason',
      label: '失败原因',
      value: (row) => row.failed_reason || '-',
      span: 2,
      isLongText: true,
    },
  },
]

const scrapeRecordActions: RecordAction<ScrapeRecord>[] = [
  { key: 'detail', label: '详情', type: 'primary', icon: View },
  {
    key: 'rescrape',
    label: '重新识别',
    type: 'warning',
    icon: Refresh,
    visible: canReScrape,
  },
  {
    key: 'mark-finished',
    label: '标记已整理',
    type: 'success',
    icon: Finished,
    visible: canMarkAsFinished,
  },
]

const handleScrapeRecordAction = ({ actionKey, row }: RecordActionPayload<ScrapeRecord>) => {
  if (actionKey === 'detail') {
    handleDetail(row)
    return
  }
  if (actionKey === 'rescrape') {
    reScrape(row)
    return
  }
  if (actionKey === 'mark-finished') {
    void markAsFinished(row)
  }
}

function cloneScrapeRecords(rows: ScrapeRecord[]): ScrapeRecord[] {
  return rows.map((item: ScrapeRecord) => ({ ...item }))
}

function mergeEpisodeRecords(sourceRecords: ScrapeRecord[]): ScrapeRecord[] {
  const mergedMap = new Map<string, ScrapeRecord>()

  sourceRecords.forEach((record) => {
    if (record.type === 'tvshow' && record.tmdb_id && record.season_number) {
      const key = `${record.tmdb_id}-${record.season_number}`
      if (!mergedMap.has(key)) {
        mergedMap.set(key, record)
      }
    } else {
      mergedMap.set(`unique-${record.id}`, record)
    }
  })

  return Array.from(mergedMap.values())
}

function applyLoadedScrapeRecords(rows: ScrapeRecord[]) {
  originalRecords.value = cloneScrapeRecords(rows)
  const visibleRows = isMerged.value ? mergeEpisodeRecords(rows) : rows
  records.value = mergeStableList(records.value, visibleRows, (row) => row.id)
}

function clearRecordsForReload({ resetTotal }: { resetTotal: boolean }) {
  queryLoading.value = true
  scrapeRecordsRequestGate.invalidate()
  invalidateRecordActionContext()
  invalidateScrapeRecordsMutationContext()
  records.value = []
  originalRecords.value = []
  if (resetTotal) {
    total.value = 0
  }
  selectedRecords.value = []
  isMerged.value = false
  pageStateStore.setExpandedRowKeys('scrape-records', [])
}

function finishRecordsQuerySwitchLoading() {
  if (!isRefreshing.value && !pendingScrapeRecordsRefresh.value) {
    queryLoading.value = false
  }
}

async function loadRecordsForQuerySwitch() {
  clearRecordsForReload({ resetTotal: true })
  await loadRecords()
}

async function loadRecordsForPageChange() {
  clearRecordsForReload({ resetTotal: false })
  await loadRecords()
}

// 加载刮削记录
const loadRecords = async () => {
  if (!isPageActive) {
    finishRecordsQuerySwitchLoading()
    return
  }

  const requestId = scrapeRecordsRequestGate.next()

  if (isRefreshing.value) {
    pendingScrapeRecordsRefresh.value = true
    return
  }

  try {
    await runRefresh(async () => {
      try {
        // 构建查询参数
        const params: Record<string, string | number> = {
          page: currentPage.value,
          pageSize: pageSize.value,
        }

        // 根据需求，将 statusFilter 映射到 media_type 参数
        if (statusFilter.value) {
          params.status = statusFilter.value
        }

        // 添加类型筛选参数
        if (typeFilter.value) {
          params.type = typeFilter.value
        }

        // 添加名称搜索参数
        if (nameFilter.value) {
          params.name = nameFilter.value
        }

        const response = await http.get(`${SERVER_URL}/scrape/records`, { params })

        if (!scrapeRecordsRequestGate.isCurrent(requestId)) {
          return
        }

        if (response?.data.code === 200) {
          const rows = response.data.data.list || []
          applyLoadedScrapeRecords(rows)
          pageStateStore.setExpandedRowKeys(
            'scrape-records',
            retainExistingKeys(pageState.expandedRowKeys, records.value, (row) => row.id),
          )
          total.value = response.data.data.total
        } else {
          ElMessage.error(`加载刮削记录失败：${response?.data.message || '未知错误'}`)
        }
      } catch (error) {
        if (!scrapeRecordsRequestGate.isCurrent(requestId)) {
          return
        }
        console.error('加载刮削记录失败：', error)
        ElMessage.error('加载刮削记录失败：网络错误')
      }
    })
  } finally {
    if (pendingScrapeRecordsRefresh.value && isPageActive) {
      pendingScrapeRecordsRefresh.value = false
      await loadRecords()
    }
    finishRecordsQuerySwitchLoading()
  }
}

// 合并/显示电视剧集
const toggleMergeEpisodes = () => {
  if (!isMerged.value) {
    isMerged.value = true
    const visibleRows = mergeEpisodeRecords(originalRecords.value)
    records.value = mergeStableList(records.value, visibleRows, (row) => row.id)
  } else {
    isMerged.value = false
    const rows = originalRecords.value
    originalRecords.value = rows.map((item: ScrapeRecord) => ({ ...item }))
    records.value = mergeStableList(records.value, originalRecords.value, (row) => row.id)
  }
  pageStateStore.setExpandedRowKeys(
    'scrape-records',
    retainExistingKeys(pageState.expandedRowKeys, records.value, (row) => row.id),
  )
}

// 应用筛选
const applyFilter = () => {
  pageStateStore.setPagination('scrape-records', 1, pageState.pageSize)
  loadRecordsForQuerySwitch() // 重新加载数据
  // 重置合并状态
  if (isMerged.value) {
    isMerged.value = false
  }
}

// // 处理搜索
// const handleSearch = () => {
//   // 搜索逻辑
//   if (searchKeyword.value) {
//     // 如果有搜索关键词，使用本地过滤
//     if (isMerged.value) {
//       // 如果处于合并状态，先还原再过滤
//       records.value = JSON.parse(JSON.stringify(originalRecords.value))
//     }

//     const keyword = searchKeyword.value.toLowerCase()
//     records.value = originalRecords.value.filter(record =>
//       (record.file_name && record.file_name.toLowerCase().includes(keyword)) ||
//       (record.path && record.path.toLowerCase().includes(keyword))
//     )
//   } else {
//     // 如果没有搜索关键词，重新加载
//     if (isMerged.value) {
//       // 如果处于合并状态，保持合并状态
//       toggleMergeEpisodes()
//       toggleMergeEpisodes()
//     } else {
//       loadRecords()
//     }
//   }
// }

// 重置筛选
const resetFilter = () => {
  statusFilter.value = ''
  typeFilter.value = ''
  nameFilter.value = '' // 重置名称搜索过滤器
  pageStateStore.setPagination('scrape-records', 1, pageState.pageSize)
  loadRecordsForQuerySwitch() // 重新加载数据
}

// 分页处理
const handleSizeChange = (size: number) => {
  pageStateStore.setPagination('scrape-records', 1, size)
  loadRecordsForPageChange()
}

const handleCurrentChange = (current: number) => {
  pageStateStore.setPagination('scrape-records', current, pageState.pageSize)
  loadRecordsForPageChange()
}

// 处理选择变化
const handleSelectionChange = (selection: ScrapeRecord[]) => {
  selectedRecords.value = selection
}

// 导出识别错误文件
const handleExportErrors = async () => {
  try {
    // 移除筛选条件，导出所有已选择的记录
    if (selectedRecords.value.length === 0) {
      ElMessage.warning('请选择记录')
      return
    }

    const ids = selectedRecords.value.map((record) => record.id)
    // 构造 URL，将 ids 作为 GET 参数传递
    const idsQuery = ids.join(',')
    const downloadUrl = `${SERVER_URL}/scrape/records/export?ids=${idsQuery}`

    // 在新窗口打开下载
    window.open(downloadUrl, '_blank')
    ElMessage.success('导出请求已发送')
  } catch (error) {
    console.error('导出失败：', error)
    ElMessage.error('导出失败：网络错误')
  }
}

// 删除所选刮削记录
const handleDeleteSelectedRecords = async () => {
  const operationContext = startScrapeRecordsMutationContext()

  try {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (selectedRecords.value.length === 0) {
      ElMessage.warning('请选择记录')
      return
    }

    try {
      await ElMessageBox.confirm(
        `确定要删除选中的 ${selectedRecords.value.length} 条记录吗？`,
        '确认删除',
        {
          confirmButtonText: '确定',
          cancelButtonText: '取消',
          type: 'warning',
        },
      )
    } catch {
      return
    }

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    const ids = selectedRecords.value.map((record) => record.id)
    // 发送 DELETE 请求，参数与导出识别错误文件接口一致
    // 构造 URL，将 ids 作为 GET 参数传递
    const idsQuery = ids.join(',')
    const response = await http.delete(`${SERVER_URL}/scrape/records?ids=${idsQuery}`)

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('删除成功')
      // 清空选择
      selectedRecords.value = []
      // 刷新记录列表
      loadRecords()
    } else {
      ElMessage.error(`删除失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }
    console.error('删除失败：', error)
    ElMessage.error('删除失败：网络错误')
  } finally {
    if (isScrapeRecordsMutationContextCurrent(operationContext)) {
      finishScrapeRecordsMutationContext(operationContext)
    }
  }
}

const handleRename = async () => {
  const operationContext = startScrapeRecordsMutationContext()

  try {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (selectedRecords.value.length === 0) {
      ElMessage.warning('请选择记录')
      return
    }

    try {
      await ElMessageBox.confirm(
        `确定要重新整理选中的 ${selectedRecords.value.length} 条记录吗？`,
        '确认重新整理',
        {
          confirmButtonText: '确定',
          cancelButtonText: '取消',
          type: 'warning',
        },
      )
    } catch {
      return
    }

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    const ids = selectedRecords.value.map((record) => record.id)
    // 发送 DELETE 请求，参数与导出识别错误文件接口一致
    // 构造 URL，将 ids 作为 GET 参数传递
    const idsQuery = ids.join(',')
    const response = await http.post(`${SERVER_URL}/scrape/rename-failed?ids=${idsQuery}`)

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('重新整理成功')
      // 清空选择
      selectedRecords.value = []
      // 刷新记录列表
      loadRecords()
    } else {
      ElMessage.error(`重新整理失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }
    console.error('重新整理失败：', error)
    ElMessage.error('重新整理失败：网络错误')
  } finally {
    if (isScrapeRecordsMutationContextCurrent(operationContext)) {
      finishScrapeRecordsMutationContext(operationContext)
    }
  }
}

// 查看详情
const handleDetail = (record: ScrapeRecord) => {
  startRecordActionContext(record)
  selectedRecord.value = record
  showDetailDialog.value = true
}

// 重识别相关变量
const showReScrapeDialog = ref(false)
const reScrapeForm = ref<ReScrapeFormState>(createDefaultReScrapeForm())
const searchResults = ref<TmdbSearchResult[]>([])
const searchLoading = ref(false)
const hasSearched = ref(false)
const searchMode = ref<'name' | 'tmdb'>('name')

const handleSearchModeChange = () => {
  searchResults.value = []
  hasSearched.value = false
  if (searchMode.value === 'name') {
    reScrapeForm.value.tmdb_id = ''
  } else {
    reScrapeForm.value.name = ''
    reScrapeForm.value.year = ''
  }
}

const closeReScrapeDialog = () => {
  invalidateRecordActionContext()
}

const handleReScrapeDialogBeforeClose = (done: DialogCloseDone) => {
  done()
  invalidateRecordActionContext()
}

const handleRollbackDialogBeforeClose = (done: DialogCloseDone) => {
  done()
  invalidateRecordActionContext()
}

const searchTmdb = async () => {
  const operationContext = activeRecordActionContext.value
  const recordId = reScrapeForm.value.id
  if (!isRecordActionContextCurrent(operationContext, recordId)) {
    return
  }

  if (searchMode.value === 'name') {
    if (!reScrapeForm.value.name) {
      ElMessage.warning('请输入影视剧名称')
      return
    }
  } else {
    if (!reScrapeForm.value.tmdb_id) {
      ElMessage.warning('请输入 TMDB ID')
      return
    }
  }

  try {
    searchLoading.value = true
    hasSearched.value = false
    searchResults.value = []

    const params: Record<string, string | number> = {
      type: reScrapeForm.value.type,
    }

    if (searchMode.value === 'name') {
      params.name = reScrapeForm.value.name
      if (reScrapeForm.value.year) {
        params.year = reScrapeForm.value.year
      }
    } else {
      params.tmdb_id = reScrapeForm.value.tmdb_id
    }

    const response = await http.get(`${SERVER_URL}/scrape/tmdb-search`, { params, timeout: 30000 })

    if (!isRecordActionContextCurrent(operationContext, recordId)) {
      return
    }

    if (response?.data.code === 200) {
      searchResults.value = (response.data.data || []).map((item: TmdbSearchResult) => ({
        ...item,
        selecting: false,
      }))
      hasSearched.value = true
    } else {
      ElMessage.error(response?.data.message || '搜索失败')
    }
  } catch (error) {
    if (!isRecordActionContextCurrent(operationContext, recordId)) {
      return
    }
    console.error('TMDB 搜索失败：', error)
    ElMessage.error('搜索失败：网络错误')
  } finally {
    if (isRecordActionContextCurrent(operationContext, recordId)) {
      searchLoading.value = false
    }
  }
}

const selectSearchResult = async (item: TmdbSearchResult) => {
  const operationContext = activeRecordActionContext.value
  const recordId = reScrapeForm.value.id
  if (!isRecordActionContextCurrent(operationContext, recordId)) {
    return
  }

  try {
    item.selecting = true

    const params = {
      id: recordId,
      tmdb_id: item.tmdb_id,
      season: reScrapeForm.value.season >= 0 ? parseInt(reScrapeForm.value.season + '') : -1,
      episode: reScrapeForm.value.episode >= 0 ? parseInt(reScrapeForm.value.episode + '') : -1,
    }

    if (!isRecordActionContextCurrent(operationContext, recordId)) {
      return
    }

    const response = await http.post(`${SERVER_URL}/scrape/re-scrape`, params, { timeout: 60000 })

    if (!isRecordActionContextCurrent(operationContext, recordId)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('重新识别请求已发送')
      invalidateRecordActionContext()
      loadRecords()
    } else {
      ElMessage.error(response?.data.message || '重新识别失败')
    }
  } catch (error) {
    if (!isRecordActionContextCurrent(operationContext, recordId)) {
      return
    }
    console.error('重新识别失败：', error)
    ElMessage.error('重新识别失败：网络错误')
  } finally {
    item.selecting = false
  }
}

// 处理重新识别
const reScrape = (record: ScrapeRecord) => {
  startRecordActionContext(record)
  searchResults.value = []
  hasSearched.value = false
  searchMode.value = 'name'
  reScrapeForm.value = {
    type: record.type,
    id: record.id,
    name: record.media_name || '',
    year: record.year?.toString() || '',
    tmdb_id: '',
    originalFileName: record.file_name || '',
    season: record.season_number || -1,
    episode: parseInt(record.episode_number + '') || -1,
    status: record.status || '',
  }
  if (record.status === 'renamed') {
    showRollbackDialog.value = true
  } else {
    showReScrapeDialog.value = true
  }
}

const openRollbackReScrapeDialog = () => {
  const operationContext = activeRecordActionContext.value
  if (!isRecordActionContextCurrent(operationContext, reScrapeForm.value.id)) {
    invalidateRecordActionContext()
    return
  }

  showRollbackDialog.value = false
  showReScrapeDialog.value = true
}

const handleDeleteFailedRecords = async () => {
  const operationContext = startScrapeRecordsMutationContext()

  try {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    const response = await http.post(`${SERVER_URL}/scrape/clear-failed`)

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('刮削失败记录已清除')
      loadRecords()
    } else {
      ElMessage.error(`清除刮削失败记录失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }
    console.error('清除刮削失败记录失败：', error)
    ElMessage.error('清除刮削失败记录失败：网络错误')
  } finally {
    if (isScrapeRecordsMutationContextCurrent(operationContext)) {
      finishScrapeRecordsMutationContext(operationContext)
    }
  }
}

const handleTruncateAll = async () => {
  const operationContext = startScrapeRecordsMutationContext()

  try {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    // 第一次确认
    await ElMessageBox.confirm('此操作将删除所有刮削记录，是否继续？', '警告', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    // 第二次确认
    await ElMessageBox.confirm('确认要清空所有刮削记录吗？此操作不可恢复！', '二次确认', {
      confirmButtonText: '确认清空',
      cancelButtonText: '取消',
      type: 'error',
    })

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    // 发送请求
    const response = await http.post(`${SERVER_URL}/scrape/truncate-all`)

    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('清空记录成功')
      // 清空选择
      selectedRecords.value = []
      // 刷新记录列表
      loadRecords()
    } else {
      ElMessage.error(`清空记录失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    if (!isScrapeRecordsMutationContextCurrent(operationContext)) {
      return
    }
    // 如果用户取消操作，不显示错误消息
    if (!isMessageBoxCancelError(error)) {
      console.error('清空记录失败：', error)
      ElMessage.error('清空记录失败：网络错误')
    }
  } finally {
    if (isScrapeRecordsMutationContextCurrent(operationContext)) {
      finishScrapeRecordsMutationContext(operationContext)
    }
  }
}

const isMessageBoxCancelError = (error: unknown): boolean => {
  if (error === 'cancel' || error === 'close') {
    return true
  }

  const errorMessage = error instanceof Error ? error.message : String(error)
  return errorMessage.includes('用户取消操作')
}

const markAsFinished = async (record: ScrapeRecord) => {
  const operationContext = startRecordActionContext(record)

  try {
    // 显示确认对话框
    await ElMessageBox.confirm(
      '请确保文件已在目标位置存在，变为已整理的文件不会继续整理',
      '确认操作',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning',
      },
    )

    if (!isRecordActionContextCurrent(operationContext, record.id)) {
      return
    }

    // 发送 POST 请求到/scrape/finish 接口
    const response = await http.post(`${SERVER_URL}/scrape/finish`, { id: record.id })

    if (!isRecordActionContextCurrent(operationContext, record.id)) {
      return
    }

    if (response?.data.code === 200) {
      ElMessage.success('标记为已整理成功')
      // 刷新记录列表
      loadRecords()
    } else {
      ElMessage.error(`标记为已整理失败：${response?.data.message || '未知错误'}`)
    }
  } catch (error) {
    if (!isRecordActionContextCurrent(operationContext, record.id)) {
      return
    }
    // 如果用户取消操作，不显示错误消息
    if (!isMessageBoxCancelError(error)) {
      console.error('标记为已整理失败：', error)
      ElMessage.error('标记为已整理失败：网络错误')
    }
  } finally {
    if (isRecordActionContextCurrent(operationContext, record.id)) {
      invalidateRecordActionContext()
    }
  }
}

const getStatusTooltip = (status: string): string => {
  switch (status) {
    case 'scanned':
      return '文件已扫描入库，等待刮削'
    case 'scraping':
      return '正在刮削中…'
    case 'scraped':
      return '刮削成功，等待整理。如果本次没有成功，下次任务启动时会继续处理'
    case 'scrape_failed':
      return '刮削失败，需要重新识别'
    case 'renaming':
      return '正在整理…'
    case 'renamed':
      return '整理成功，无需额外处理'
    case 'rename_failed':
      return '整理失败，可删除刮削记录，或标记为待整理后等待下次任务重新处理'
    case 'rollbacking':
      return '等待回滚任务执行时会将视频和字幕放回原目录，然后删除该刮削记录'
    default:
      return '未知状态'
  }
}

const getPathTooltip = (row: ScrapeRecord): string =>
  [row.source_full_path || '-', '到', row.dest_full_path || '-'].join('\n')

// 获取类型标签类型
const getTypeTagType = (type: string): string => {
  switch (type) {
    case 'movie':
      return 'primary'
    case 'tvshow':
      return 'success'
    default:
      return 'info'
  }
}

// 获取类型名称
const getTypeName = (type: string): string => {
  switch (type) {
    case 'movie':
      return '电影'
    case 'tvshow':
      return '电视剧'
    default:
      return '其他'
  }
}

const getSourceTypeName = (type: string): string => {
  switch (type) {
    case 'local':
      return '本地文件'
    case '115':
      return '115 网盘'
    case 'openlist':
      return 'OpenList'
    case '123':
      return '123 网盘'
    case 'baidupan':
      return '百度网盘'
    default:
      return '其他'
  }
}

// 获取类型标签类型
const getSourceTypeTagType = (type: string): string => {
  switch (type) {
    case 'local':
      return 'warning'
    case '115':
      return 'primary'
    case 'openlist':
      return 'success'
    case '123':
      return 'info'
    case 'baidupan':
      return 'danger'
    default:
      return 'info'
  }
}

// 获取类型标签类型
const getRenameTypeTagType = (type: string): string => {
  switch (type) {
    case 'move':
      return 'warning'
    case 'copy':
      return 'primary'
    case 'hard_symlink':
      return 'success'
    case 'soft_symlink':
      return 'info'
    default:
      return 'info'
  }
}

const getRenameTypeName = (type: string): string => {
  switch (type) {
    case 'move':
      return '移动'
    case 'copy':
      return '复制'
    case 'hard_symlink':
      return '硬链接'
    case 'soft_symlink':
      return '软链接'
    default:
      return '其他'
  }
}

// 获取状态标签类型
const getStatusTagType = (status: string): string => {
  switch (status) {
    case 'scanned':
      return 'info'
    case 'scraping':
      return 'info'
    case 'scraped':
      return 'primary'
    case 'scrape_failed':
      return 'danger'
    case 'renaming':
      return 'primary'
    case 'renamed':
      return 'success'
    case 'rename_failed':
      return 'danger'
    case 'rollbacking':
      return 'danger'
    default:
      return 'info'
  }
}

// 获取状态名称
const getStatusName = (status: string): string => {
  switch (status) {
    case 'scanned':
      return '未刮削'
    case 'scraping':
      return '刮削中'
    case 'scraped':
      return '已刮削'
    case 'scrape_failed':
      return '刮削失败'
    case 'renaming':
      return '整理中'
    case 'renamed':
      return '已整理'
    case 'rename_failed':
      return '整理失败'
    case 'rollbacking':
      return '回滚中'
    default:
      return '未知'
  }
}

// 全局实时事件监听：刮削单项完成时刷新记录列表
useRealtimeEvent(
  'scraper_item_complete',
  () => {
    loadRecords()
  },
  () => {
    if (isPageActive) void loadRecords()
  },
)

// 全局实时事件监听：刮削任务完成时刷新记录列表
useRealtimeEvent('scraper_task_complete', () => {
  loadRecords()
})

const activateScrapeRecordsPage = () => {
  if (isPageActive) {
    return
  }
  isPageActive = true
  loadRecords()
}

const deactivateScrapeRecordsPage = () => {
  if (!isPageActive) {
    return
  }
  isPageActive = false
  pendingScrapeRecordsRefresh.value = false
  queryLoading.value = false
  scrapeRecordsRequestGate.invalidate()
  invalidateRecordActionContext()
  invalidateScrapeRecordsMutationContext()
}

// 页面生命周期
onMounted(activateScrapeRecordsPage)

onActivated(activateScrapeRecordsPage)

onActivated(() => {
  nextTick(() => {
    const scrollContainer = getPageScrollContainer()
    if (scrollContainer) {
      scrollContainer.scrollTop = pageState.scrollTop
    }
  })
})

onDeactivated(() => {
  const scrollContainer = getPageScrollContainer()
  pageStateStore.setScrollTop('scrape-records', scrollContainer?.scrollTop || 0)
})

onDeactivated(deactivateScrapeRecordsPage)

onUnmounted(() => {
  isPageActive = false
  pendingScrapeRecordsRefresh.value = false
  queryLoading.value = false
  scrapeRecordsRequestGate.invalidate()
  invalidateRecordActionContext()
  invalidateScrapeRecordsMutationContext()
})
</script>

<style scoped>
.scrape-records-container {
  position: relative;
  padding: 20px;
  display: flex;
  flex-direction: column;
  height: calc(100vh - 40px);
}

.header-section {
  flex-shrink: 0;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.card-header h2 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
}

.queue-description {
  margin: 0;
  font-size: 14px;
  color: #909399;
}

.top-actions {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
  padding: 16px;
  background: linear-gradient(135deg, #f5f7fa 0%, #e4e7ed 100%);
  border-radius: 8px;
  flex-wrap: wrap;
}

.action-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.action-buttons {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.selected-count {
  color: #606266;
  font-size: 13px;
}

.search-filter-section {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.filter-input {
  width: 200px;
}

.filter-select {
  width: 150px;
}

.filter-select-small {
  width: 120px;
}

.table-section {
  display: block;
}

.table-container {
  background: #fff;
  border-radius: 4px;
  padding: 0;
  display: block;
}

.scrape-record-table {
  width: 100%;
}

.scrape-path-cell {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.scrape-status-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  min-height: 24px;
}

.scrape-status-tag {
  width: 84px;
  justify-content: center;
}

.path-text {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
}

.scrape-path-cell__rename {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0;
}

.error-reason-text {
  color: #f56c6c;
  cursor: pointer;
}

.detail-content {
  max-height: 400px;
  overflow-y: auto;
}

.detail-item {
  display: flex;
  gap: 10px;
  margin-bottom: 10px;
}

.detail-label {
  color: #969797;
}

.detail-value {
  font-size: 16px;
}

.re-scrape-dialog-content {
  max-height: 60vh;
  overflow-y: auto;
}

.search-section {
  margin-bottom: 20px;
}

.search-row {
  display: flex;
  gap: 12px;
}

.search-row .name-input {
  flex: 1;
}

.search-row .year-input {
  width: 150px;
}

.search-row .tmdb-input {
  flex: 1;
  max-width: 250px;
}

.season-episode-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.episode-label {
  color: #606266;
  font-size: 14px;
}

.search-results-section {
  margin-top: 16px;
  border-top: 1px solid #ebeef5;
  padding-top: 16px;
}

.results-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
  font-size: 15px;
  font-weight: 600;
  color: #303133;
}

.results-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-height: 300px;
  overflow-y: auto;
}

.result-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #fafafa;
  transition:
    border-color 0.2s ease,
    background-color 0.2s ease;
}

.result-item:hover {
  border-color: #409eff;
  background: #f5f7fa;
}

.result-poster {
  width: 60px;
  height: 90px;
  flex-shrink: 0;
  border-radius: 4px;
  overflow: hidden;
  background: #e4e7ed;
}

.result-poster .el-image {
  width: 100%;
  height: 100%;
}

.poster-placeholder {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #c0c4cc;
  font-size: 24px;
}

.result-info {
  flex: 1;
  min-width: 0;
}

.result-title {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.result-original-title {
  font-size: 13px;
  color: #909399;
  margin-bottom: 8px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.result-meta {
  display: flex;
  gap: 8px;
}

.result-overview {
  margin-top: 8px;
  font-size: 12px;
  color: #909399;
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  text-overflow: ellipsis;
}

.result-action {
  flex-shrink: 0;
}

.empty-results {
  padding: 20px 0;
}

@media (max-width: 768px) {
  .scrape-records-container {
    padding: 12px;
  }

  .top-actions {
    flex-direction: column;
    padding: 12px;
  }

  .action-group {
    width: 100%;
  }

  .action-buttons {
    width: 100%;
  }

  .action-buttons .el-button {
    flex: 1;
    min-width: 0;
    font-size: 13px;
    padding: 8px 12px;
  }

  .search-filter-section {
    flex-direction: column;
    align-items: stretch;
  }

  .filter-input,
  .filter-select,
  .filter-select-small {
    width: 100%;
  }

  .search-filter-section .el-button {
    width: 100%;
  }

  .card-header p {
    margin: 0;
    font-size: 12px;
    line-height: 1.4;
  }

  .card-header {
    flex-direction: column;
    align-items: flex-start;
  }

  .scrape-records-container {
    height: auto;
    display: block;
  }

  .header-section {
    margin-bottom: 0;
  }
}

@media (max-width: 600px) {
  .search-row {
    flex-direction: column;
  }

  .search-row .year-input {
    width: 100%;
  }

  .result-item {
    flex-direction: column;
    align-items: flex-start;
  }

  .result-poster {
    width: 100%;
    height: 150px;
  }

  .result-action {
    width: 100%;
    margin-top: 8px;
  }

  .result-action .el-button {
    width: 100%;
  }
}
</style>
e>
