<script setup lang="ts">
import { useQueueStats } from '@/composables/useQueueStats'

const { queueStats, queueStatsLoading } = useQueueStats()
</script>

<template>
  <div class="stats-card-main" v-loading="queueStatsLoading">
    <div class="stats-card-header">
      <div class="stats-card-title">
        <span class="title-icon">📊</span>
        <span>115 接口监控</span>
      </div>
      <div
        class="status-badge"
        :class="queueStats?.is_throttled ? 'status-warning' : 'status-success'"
      >
        {{ queueStats?.is_throttled ? '限流中' : '运行正常' }}
      </div>
    </div>

    <div v-if="queueStats" class="stats-content">
      <div v-if="queueStats.is_throttled" class="throttle-warning">
        <div class="throttle-icon">⚠️</div>
        <div class="throttle-details">
          <div class="throttle-item">
            <span class="label">等待时间</span>
            <span class="value">{{ queueStats.throttle_wait_time }}</span>
          </div>
          <div class="throttle-item">
            <span class="label">已过时间</span>
            <span class="value">{{ queueStats.throttled_elapsed_time }}</span>
          </div>
          <div class="throttle-item">
            <span class="label">剩余时间</span>
            <span class="value">{{ queueStats.throttled_remaining_time }}</span>
          </div>
        </div>
      </div>

      <div class="metrics-grid">
        <div class="metric-item" :class="{ 'metric-warning': queueStats.qps_count > 3 }">
          <div class="metric-value">{{ queueStats.qps_count }}</div>
          <div class="metric-label">QPS</div>
        </div>
        <div class="metric-item">
          <div class="metric-value">{{ queueStats.qpm_count }}</div>
          <div class="metric-label">QPM</div>
        </div>
        <div class="metric-item">
          <div class="metric-value">{{ queueStats.qph_count }}</div>
          <div class="metric-label">QPH</div>
        </div>
        <div class="metric-item">
          <div class="metric-value">{{ queueStats.avg_response_time_ms }}</div>
          <div class="metric-label">响应 (ms)</div>
        </div>
        <div class="metric-item" :class="{ 'metric-danger': queueStats.throttled_count > 0 }">
          <div class="metric-value">{{ queueStats.throttled_count }}</div>
          <div class="metric-label">限流次数</div>
        </div>
      </div>
    </div>

    <div v-else class="empty-state">
      <el-empty description="暂无统计数据" :image-size="60" />
    </div>
  </div>
</template>

<style scoped>
.stats-card-main {
  background: white;
  border-radius: 16px;
  padding: 20px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.08);
  border: 1px solid #f0f0f0;
}

.stats-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.stats-card-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 16px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.title-icon {
  font-size: 20px;
  color: var(--el-color-primary);
}

.status-badge {
  padding: 4px 12px;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 500;
}

.status-success {
  background: #e8f5e9;
  color: #2e7d32;
}

.status-warning {
  background: #fff3e0;
  color: #e65100;
  animation: pulse-bg 2s ease-in-out infinite;
}

@keyframes pulse-bg {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.7;
  }
}

.throttle-warning {
  display: flex;
  gap: 16px;
  padding: 16px;
  background: linear-gradient(135deg, #fff8e1 0%, #ffecb3 100%);
  border-radius: 12px;
  margin-bottom: 16px;
}

.throttle-icon {
  font-size: 24px;
  color: var(--el-color-warning);
}

.throttle-details {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  flex: 1;
}

.throttle-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.throttle-item .label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.throttle-item .value {
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.metrics-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(80px, 1fr));
  gap: 12px;
}

.metric-item {
  text-align: center;
  padding: 16px 8px;
  background: #f8f9fa;
  border-radius: 12px;
  transition:
    transform 0.2s ease,
    box-shadow 0.2s ease;
}

.metric-item:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
}

.metric-item.metric-warning {
  background: linear-gradient(135deg, #fff8e1 0%, #ffe082 100%);
}

.metric-item.metric-danger {
  background: linear-gradient(135deg, #ffebee 0%, #ffcdd2 100%);
}

.metric-value {
  font-size: 24px;
  font-weight: 700;
  color: var(--el-text-color-primary);
  font-family: 'SF Mono', 'Monaco', 'Menlo', monospace;
}

.metric-item.metric-warning .metric-value {
  color: #e65100;
}

.metric-item.metric-danger .metric-value {
  color: #c62828;
}

.metric-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

.empty-state {
  padding: 40px 20px;
  text-align: center;
}

@media (max-width: 768px) {
  .metrics-grid {
    grid-template-columns: repeat(3, 1fr);
  }

  .metric-value {
    font-size: 20px;
  }
}
@media (prefers-reduced-motion: reduce) {
  .status-warning {
    animation: none;
  }
}
</style>
