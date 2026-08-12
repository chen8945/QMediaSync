<script setup lang="ts">
import type { Component } from 'vue'

interface PageStatItem {
  icon: Component
  value: string | number
  label: string
  tone: string
}

defineProps<{
  items: readonly PageStatItem[]
}>()
</script>

<template>
  <div class="qms-page-stats">
    <div v-for="item in items" :key="item.label" class="qms-page-stats__item">
      <div class="qms-page-stats__icon" :class="`qms-page-stats__icon--${item.tone}`">
        <el-icon aria-hidden="true">
          <component :is="item.icon" />
        </el-icon>
      </div>
      <div class="qms-page-stats__info">
        <span class="qms-page-stats__value">{{ item.value }}</span>
        <span class="qms-page-stats__label">{{ item.label }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.qms-page-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  width: 100%;
}

.qms-page-stats__item {
  display: flex;
  min-width: 140px;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-radius: 8px;
  background: #f5f7fa;
}

.qms-page-stats__icon {
  display: flex;
  flex: 0 0 40px;
  width: 40px;
  height: 40px;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  font-size: 20px;
}

.qms-page-stats__icon--total {
  background: #ecf5ff;
  color: #409eff;
}

.qms-page-stats__icon--authorized,
.qms-page-stats__icon--running {
  background: #f0f9eb;
  color: #67c23a;
}

.qms-page-stats__icon--unauthorized,
.qms-page-stats__icon--waiting {
  background: #fdf6ec;
  color: #e6a23c;
}

.qms-page-stats__icon--failed {
  background: #fef0f0;
  color: #f56c6c;
}

.qms-page-stats__icon--cron {
  background: #f4f4f5;
  color: #909399;
}

.qms-page-stats__info {
  display: flex;
  min-width: 0;
  flex-direction: column;
}

.qms-page-stats__value {
  color: #303133;
  font-size: 20px;
  font-weight: 600;
  line-height: 1.2;
}

.qms-page-stats__label {
  overflow: hidden;
  color: #909399;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 768px) {
  .qms-page-stats {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }

  .qms-page-stats__item {
    min-width: 0;
    gap: 8px;
    padding: 8px 10px;
    border-radius: 6px;
  }

  .qms-page-stats__icon {
    flex-basis: 32px;
    width: 32px;
    height: 32px;
    border-radius: 6px;
    font-size: 16px;
  }

  .qms-page-stats__value {
    font-size: 16px;
  }

  .qms-page-stats__label {
    font-size: 11px;
  }
}
</style>
