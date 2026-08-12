<script setup lang="ts">
import DOMPurify from 'dompurify'
import { ref, onMounted, watch } from 'vue'
import { useUpdate } from '@/composables/useUpdate'
import { useVersion } from '@/composables/useVersion'
import { formatFileSize } from '@/utils/fileSizeUtils'
import { formatMaybeUnixDateTime } from '@/utils/timeUtils'
import MarkdownIt from 'markdown-it'
import 'github-markdown-css/github-markdown.css'
import { CircleCheck, Refresh } from '@element-plus/icons-vue'
import { SERVER_URL } from '@/const'
import { useHttpClient } from '@/http/client'
import PageHeader from '@/components/common/PageHeader.vue'

const http = useHttpClient()

const activeNames = ref<string[]>([])
const hasInitializedActiveNames = ref(false)

const { versionInfo, versionLoading } = useVersion()

const {
  updateList,
  updateLoading,
  isUpdating,
  updatingVersion,
  updateProgress,
  showUpdateCompleteDialog,
  countdown,
  updateChannel,
  loadUpdateList,
  updateToVersion,
  cancelUpdate,
  handleDownloadClick,
  manuallyRefresh,
} = useUpdate()

watch(updateList, (updates) => {
  if (hasInitializedActiveNames.value || activeNames.value.length > 0 || updates.length === 0) {
    return
  }

  activeNames.value = [`update-${updates[0].version}`]
  hasInitializedActiveNames.value = true
})

const handleChannelChange = (channel: string) => {
  updateChannel.value = channel as 'github' | 'gitee'
  loadUpdateList(true)
}

const isFnOS = ref(false)
const isFnOSLoading = ref(false)

const checkIsFnOS = async () => {
  try {
    isFnOSLoading.value = true
    const response = await http.get(`${SERVER_URL}/path/is-fn-os`)
    if (response?.data.code === 200) {
      isFnOS.value = response.data.data === true
    }
  } catch {
    isFnOS.value = false
  } finally {
    isFnOSLoading.value = false
  }
}

onMounted(() => {
  checkIsFnOS()
})

const md = new MarkdownIt({
  html: true,
  breaks: true,
  linkify: true,
})

const renderMarkdown = (content: string): string => {
  return DOMPurify.sanitize(md.render(content || ''))
}

const formatVersionBuildTime = (): string => {
  if (!versionInfo.value) {
    return '-'
  }

  return formatMaybeUnixDateTime(versionInfo.value.build_time || versionInfo.value.date)
}

const formatUpdatePublishedAt = (update: { published_at?: number; date?: string }): string => {
  return formatMaybeUnixDateTime(update.published_at || update.date)
}
</script>

<template>
  <div class="update-page">
    <PageHeader />

    <div class="current-version-section" v-loading="versionLoading">
      <div class="section-card">
        <div class="section-header section-header--current">
          <div class="section-header-left">
            <span class="section-icon">⚙️</span>
            <span>当前版本</span>
          </div>
        </div>
        <div v-if="versionInfo" class="version-info">
          <div class="version-number">{{ versionInfo.version }}</div>
          <div class="version-date">编译时间：{{ formatVersionBuildTime() }}</div>
        </div>
        <div v-else class="empty-state">
          <el-empty description="无法获取版本信息" :image-size="40" />
        </div>
      </div>
    </div>

    <div class="update-section" v-if="!isFnOS">
      <div class="section-card" v-loading="isFnOSLoading">
        <div class="section-header">
          <div class="section-header-left">
            <span class="section-icon">🚀</span>
            <span>可用版本</span>
          </div>
          <div class="section-header-right update-toolbar">
            <el-radio-group
              v-model="updateChannel"
              size="default"
              class="channel-selector"
              @change="handleChannelChange"
            >
              <el-radio-button value="github">GitHub</el-radio-button>
              <!-- Gitee 渠道暂未启用：本仓库尚未在 Gitee 发布，后端 update.go 已注释 Gitee 分支；建立 Gitee 镜像后取消注释即可恢复 -->
              <!-- <el-radio-button value="gitee">Gitee</el-radio-button> -->
            </el-radio-group>
            <el-button
              size="default"
              :icon="Refresh"
              @click="loadUpdateList(true)"
              :loading="updateLoading"
            >
              刷新
            </el-button>
          </div>
        </div>

        <div v-if="updateList.length > 0" class="update-list">
          <el-collapse v-model="activeNames" class="update-collapse">
            <el-collapse-item
              v-for="update in updateList"
              :key="update.version"
              :name="`update-${update.version}`"
            >
              <template #title>
                <div class="update-title-row">
                  <div class="update-version">
                    <span class="version-number">{{ update.version }}</span>
                    <span class="version-date">{{ formatUpdatePublishedAt(update) }}</span>
                  </div>
                  <div class="update-tags">
                    <el-tag v-if="update.latest" type="success" size="small" effect="dark"
                      >最新</el-tag
                    >
                    <el-tag v-if="update.current" type="primary" size="small" effect="dark"
                      >当前</el-tag
                    >
                  </div>
                </div>
              </template>
              <div class="update-detail">
                <!-- eslint-disable-next-line vue/no-v-html -->
                <div class="update-note markdown-body" v-html="renderMarkdown(update.note)"></div>
                <div class="update-actions" v-if="!update.current">
                  <el-button type="default" size="small" @click="handleDownloadClick(update)" round>
                    手动下载
                  </el-button>
                  <el-button
                    type="primary"
                    size="small"
                    @click="updateToVersion(update.version)"
                    :disabled="isUpdating"
                    round
                  >
                    在线更新
                  </el-button>
                </div>

                <div
                  v-if="isUpdating && update.version === updatingVersion"
                  class="update-progress"
                >
                  <el-progress
                    :percentage="updateProgress.progress"
                    :stroke-width="8"
                    :show-text="false"
                  />
                  <div class="progress-info">
                    <span
                      >{{ formatFileSize(updateProgress.downloaded) }} /
                      {{ formatFileSize(updateProgress.total_size) }}</span
                    >
                    <span>{{
                      updateProgress.status === 'downloading'
                        ? '下载中'
                        : updateProgress.status === 'install'
                          ? '安装中'
                          : ''
                    }}</span>
                  </div>
                  <el-button type="danger" size="small" @click="cancelUpdate" round>
                    取消
                  </el-button>
                </div>
              </div>
            </el-collapse-item>
          </el-collapse>
        </div>

        <div v-else class="empty-state">
          <el-empty description="暂无版本信息" :image-size="80" />
        </div>
      </div>
    </div>

    <div class="update-section fnos-tip" v-else>
      <div class="section-card">
        <div class="section-header">
          <span class="section-icon">💡</span>
          <span>提示</span>
        </div>
        <div class="fnos-content">
          <p>检测到您正在使用飞牛 NAS 系统，版本更新请通过飞牛应用商店进行。</p>
        </div>
      </div>
    </div>
  </div>

  <!-- 更新完成弹窗 -->
  <el-dialog
    v-model="showUpdateCompleteDialog"
    title="正在安装更新"
    class="update-complete-dialog"
    width="min(500px, calc(100vw - 32px))"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    show-close="false"
    :destroy-on-close="true"
  >
    <div class="dialog-content">
      <el-icon>
        <CircleCheck />
      </el-icon>
      <h3>安装包已下载，正在更新中</h3>
      <p>
        系统将在 <strong>{{ countdown }}</strong> 秒后自动刷新页面
      </p>
      <div class="dialog-tips">
        <p>
          提示：刷新页面后，新版本将生效。如未生效，请手动刷新或手动下载最新版本；Docker
          部署请更新镜像
        </p>
      </div>
    </div>
    <template #footer>
      <div class="dialog-footer">
        <el-button type="primary" @click="manuallyRefresh"> 立即刷新 </el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.update-page {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 24px;
  padding: 20px 0 0;
}

.update-page :deep(.qms-page-header) {
  margin-bottom: 0;
}

.section-card {
  background: white;
  border-radius: 16px;
  padding: 20px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
  border: 1px solid #f0f0f0;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 16px;
  font-weight: 600;
  color: #303133;
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid #f0f0f0;
}

.section-header-left {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.section-icon {
  font-size: 20px;
}

.update-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.channel-selector {
  margin-left: 0;
}

.version-info {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.version-info .version-number {
  font-size: 24px;
  font-weight: 700;
  color: #303133;
}

.version-info .version-date {
  font-size: 14px;
  color: #909399;
}

.update-collapse {
  border: none;
}

.update-collapse :deep(.el-collapse-item__header) {
  background: #f8f9fa;
  border-radius: 12px;
  padding: 0 16px;
  margin-bottom: 8px;
  border: none;
  height: 56px;
}

.update-collapse :deep(.el-collapse-item__wrap) {
  border: none;
  transition: height 0.22s ease;
  will-change: height;
}

.update-collapse :deep(.el-collapse-item__content) {
  padding: 16px;
}

@media (prefers-reduced-motion: reduce) {
  .update-collapse :deep(.el-collapse-item__wrap) {
    transition: none;
  }
}

.update-title-row {
  display: flex;
  min-width: 0;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  width: 100%;
}

.update-version {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: center;
  gap: 12px;
}

.update-version .version-number {
  font-size: 16px;
  font-weight: 600;
  color: #303133;
}

.update-version .version-date {
  overflow-wrap: anywhere;
  font-size: 13px;
  color: #909399;
}

.update-tags {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.update-detail {
  background: #fafafa;
  border-radius: 12px;
  padding: 16px;
}

.update-note {
  background: white;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
  font-size: 14px;
  line-height: 1.6;
  color: #606266;
}

.update-actions {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

.update-progress {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #f0f0f0;
}

.update-progress .el-progress {
  flex: 1;
}

.progress-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
  color: #909399;
  min-width: 120px;
}

.empty-state {
  padding: 40px 20px;
  text-align: center;
}

:deep(.update-complete-dialog.el-dialog),
:deep(.update-complete-dialog .el-dialog) {
  border-radius: 16px;
}

.dialog-content {
  text-align: center;
  padding: 30px 20px;
}

.dialog-content .el-icon {
  font-size: 48px;
  color: #67c23a;
  margin-bottom: 20px;
}

.dialog-content h3 {
  font-size: 20px;
  font-weight: 600;
  margin-bottom: 12px;
  color: #303133;
}

.dialog-content p {
  font-size: 15px;
  color: #606266;
  margin-bottom: 16px;
}

.dialog-tips {
  padding: 12px 16px;
  background: #f0f9ff;
  border-radius: 8px;
}

.dialog-tips p {
  font-size: 13px;
  color: #909399;
  margin: 0;
}

.dialog-footer {
  display: flex;
  justify-content: center;
  padding: 16px;
  border-top: 1px solid #ebeef5;
}

@media (max-width: 768px) {
  .update-page {
    padding-top: 10px;
  }

  .section-card {
    padding: 16px;
  }

  .section-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .section-header--current {
    flex-direction: row;
    align-items: center;
  }

  .section-header-left,
  .section-header-right,
  .update-toolbar {
    width: 100%;
  }

  .section-header--current .section-header-left {
    width: auto;
  }

  .update-toolbar {
    justify-content: flex-start;
  }

  .update-title-row {
    align-items: center;
    gap: 8px;
  }

  .update-collapse :deep(.el-collapse-item__header) {
    height: auto;
    min-height: 56px;
    padding-top: 12px;
    padding-bottom: 12px;
  }

  .update-version {
    min-width: 0;
    flex-wrap: wrap;
    row-gap: 2px;
  }

  .update-actions {
    justify-content: flex-start;
    flex-wrap: wrap;
  }

  .dialog-content {
    padding: 24px 12px;
  }

  .dialog-footer {
    padding: 12px;
  }
}

.update-note :deep(.markdown-body) {
  font-size: 14px;
  line-height: 1.6;
}

.update-note :deep(.markdown-body pre) {
  background-color: #f6f8fa;
  border-radius: 6px;
  padding: 12px;
  overflow: auto;
}

.update-note :deep(.markdown-body code) {
  background-color: #f1f1f1;
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 13px;
}

.update-note :deep(.markdown-body pre code) {
  background-color: transparent;
  padding: 0;
}

.update-note :deep(.markdown-body a) {
  color: #409eff;
  text-decoration: none;
}

.update-note :deep(.markdown-body a:hover) {
  text-decoration: underline;
}

.update-note :deep(.markdown-body ul),
.update-note :deep(.markdown-body ol) {
  padding-left: 1.5em;
  margin: 8px 0;
}

.update-note :deep(.markdown-body li) {
  margin-bottom: 4px;
}

.fnos-content {
  padding: 20px;
  text-align: center;
}

.fnos-content p {
  font-size: 15px;
  color: #606266;
  margin: 0;
  line-height: 1.6;
}
</style>
