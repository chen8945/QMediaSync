<script setup lang="ts">
import { defineAsyncComponent, ref, useTemplateRef } from 'vue'
import { Document } from '@element-plus/icons-vue'
import PageHeader from '@/components/common/PageHeader.vue'
import VersionManager from './VersionManager.vue'
import QueueStatsCard from './QueueStatsCard.vue'
import AnnouncementCard from './AnnouncementCard.vue'

const HourlyStatsChart = defineAsyncComponent(() => import('./HourlyStatsChart.vue'))
const AppLogViewer = defineAsyncComponent(() => import('./AppLogViewer.vue'))

const showLogDialog = ref(false)
const logViewerRef = useTemplateRef<{ disconnect?: () => void }>('logViewerRef')

const handleLogDialogClose = () => {
  logViewerRef.value?.disconnect?.()
}
</script>

<template>
  <div class="home-container">
    <div class="header-section">
      <PageHeader class="home-page-header" icon="" :show-identity-on-mobile="true">
        <template #actions>
          <div class="home-header__actions">
            <el-button
              type="primary"
              size="large"
              class="home-log-button"
              @click="showLogDialog = true"
              :icon="Document"
              round
            >
              运行日志
            </el-button>
          </div>
        </template>
      </PageHeader>
    </div>
    <AnnouncementCard />

    <div class="stats-section">
      <div class="stats-row">
        <QueueStatsCard />
        <HourlyStatsChart />
      </div>
    </div>

    <div class="info-section">
      <div class="info-grid">
        <VersionManager />

        <div class="info-card notice-card">
          <div class="info-card-header">
            <span class="info-icon">📝</span>
            <span>使用须知</span>
          </div>
          <div class="notice-list">
            <div class="notice-item notice-important">
              <span class="notice-number">1</span>
              <span>本项目使用 115 开放平台，QPS 受限；如果介意限流，请谨慎使用</span>
            </div>
            <div class="notice-item">
              <span class="notice-number">2</span>
              <span>播放、下载、媒体提取等操作的总并发建议不要超过 5</span>
            </div>
            <div class="notice-item">
              <span class="notice-number">3</span>
              <span>建议将神医助手线程数设置为 1 或 2</span>
            </div>
            <div class="notice-item">
              <span class="notice-number">4</span>
              <span>刮削和 STRM 同步是两个独立功能</span>
            </div>
            <div class="notice-item">
              <span class="notice-number">5</span>
              <span
                >问题请在
                <a
                  href="https://github.com/chen8945/QMediaSync"
                  target="_blank"
                  rel="noopener noreferrer"
                  >GitHub</a
                >
                提交 Issue
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>

  <!-- 日志查看弹窗 -->
  <el-dialog
    v-model="showLogDialog"
    title="运行日志"
    class="log-dialog"
    :fullscreen="true"
    :close-on-click-modal="true"
    :close-on-press-escape="true"
    show-close="true"
    :destroy-on-close="true"
    @close="handleLogDialogClose"
  >
    <div class="log-dialog-content">
      <AppLogViewer
        v-if="showLogDialog"
        ref="logViewerRef"
        log-path="app.log"
        :is-real-time="true"
        fullscreen
        height="calc(100dvh - 210px)"
        mobile-height="calc(100dvh - 170px)"
      />
    </div>
  </el-dialog>
</template>

<style scoped>
.home-container {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 24px;
  padding: 0;
}

.header-section {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 20px 24px;
  background: var(--qms-gradient-brand);
  border-radius: 16px;
  color: white;
}

.header-section :deep(.qms-page-header) {
  flex: 1 1 auto;
  min-width: 0;
  margin-bottom: 0;
}

.header-section :deep(.qms-page-header__top) {
  align-items: center;
}

.header-section :deep(.qms-page-header__title) {
  color: white;
}

.header-section :deep(.qms-page-header__description) {
  margin-top: 4px;
  color: white;
  opacity: 0.9;
}

.home-header__actions {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  gap: 12px;
}

.home-log-button {
  min-width: 116px;
  box-shadow: 0 2px 6px rgba(64, 158, 255, 0.22);
  font-weight: 500;
}

.home-log-button:hover {
  box-shadow: 0 4px 10px rgba(64, 158, 255, 0.28);
}

.stats-section {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.stats-row {
  display: grid;
  grid-template-columns: 340px 1fr;
  gap: 20px;
}

.info-section {
  display: flex;
  flex-direction: column;
}

.info-grid {
  display: grid;
  grid-template-columns: 280px 1fr;
  gap: 20px;
}

.info-card {
  background: white;
  border-radius: 16px;
  padding: 20px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
  border: 1px solid #f0f0f0;
}

.info-card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  margin-bottom: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid #f0f0f0;
}

.info-icon {
  font-size: 18px;
  color: var(--el-color-primary);
}

.notice-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.notice-item {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  font-size: 13px;
  color: var(--el-text-color-regular);
  line-height: 1.5;
}

.notice-item.notice-important {
  color: #c62828;
}

.notice-number {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  background: #f0f0f0;
  border-radius: 50%;
  font-size: 11px;
  font-weight: 600;
  color: var(--el-text-color-regular);
  flex-shrink: 0;
}

.notice-item.notice-important .notice-number {
  background: #ffebee;
  color: #c62828;
}

.notice-item a {
  color: var(--el-color-primary);
  text-decoration: none;
}

.notice-item a:hover {
  text-decoration: underline;
}

.log-dialog {
  display: flex;
  align-items: center;
  justify-content: center;
}

.log-dialog-content {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
}

.log-dialog-content :deep(.el-dialog__body) {
  padding: 0;
  overflow: hidden;
  height: calc(100% - 60px);
}

.log-dialog-content :deep(.el-dialog__header) {
  padding: 10px 20px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

@media (max-width: 1200px) {
  .stats-row {
    grid-template-columns: 1fr;
  }

  .info-grid {
    grid-template-columns: 1fr 1fr;
  }
}

@media (max-width: 768px) {
  .header-section {
    flex-direction: column;
    text-align: center;
    padding: 16px;
  }

  .home-header__actions {
    justify-content: center;
    width: 100%;
  }

  .home-page-header :deep(.qms-page-header__identity) {
    justify-content: center;
  }

  .home-page-header :deep(.qms-page-header__copy) {
    text-align: center;
  }

  .info-grid {
    grid-template-columns: 1fr;
  }
}
</style>
