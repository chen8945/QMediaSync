<script setup lang="ts">
import type { LogLevel } from '@/types/log'
import { LOG_LEVEL_OPTIONS } from '@/utils/logLevel'
import { computed } from 'vue'

const selectedLevels = defineModel<LogLevel[]>({ required: true })

const allLevels = LOG_LEVEL_OPTIONS.map((option) => option.value)
const selectedLevelSet = computed(() => new Set(selectedLevels.value))
const isAllSelected = computed(() => allLevels.every((level) => selectedLevelSet.value.has(level)))

function applyLevels(levels: Iterable<LogLevel>) {
  const nextLevels = new Set(levels)
  selectedLevels.value = allLevels.filter((level) => nextLevels.has(level))
}

function toggleAll() {
  selectedLevels.value = isAllSelected.value ? [] : [...allLevels]
}

function toggleLevel(level: LogLevel) {
  const nextLevels = new Set(selectedLevels.value)
  if (nextLevels.has(level)) {
    nextLevels.delete(level)
  } else {
    nextLevels.add(level)
  }
  applyLevels(nextLevels)
}
</script>

<template>
  <div class="log-level-filter">
    <div class="level-chips" role="group" aria-label="日志等级筛选">
      <button
        type="button"
        class="log-level-chip log-level-chip-all"
        :class="{ 'is-active': isAllSelected }"
        :aria-pressed="isAllSelected"
        @click="toggleAll"
      >
        <span class="level-dot"></span>
        全部
      </button>
      <button
        v-for="option in LOG_LEVEL_OPTIONS"
        :key="option.value"
        type="button"
        class="log-level-chip"
        :class="[`is-${option.value}`, { 'is-active': selectedLevelSet.has(option.value) }]"
        :aria-pressed="selectedLevelSet.has(option.value)"
        @click="toggleLevel(option.value)"
      >
        <span class="level-dot"></span>
        {{ option.label }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.log-level-filter {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.level-chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.log-level-chip {
  --level-color: var(--el-text-color-regular);
  --level-bg: var(--el-color-info-light-9);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-width: 68px;
  min-height: 28px;
  border: 1px solid var(--el-border-color);
  border-radius: 999px;
  padding: 0 10px;
  background: #ffffff;
  color: var(--el-text-color-regular);
  cursor: pointer;
  font-size: 12px;
  font-weight: 600;
  line-height: 1;
  transition:
    background-color 0.15s ease,
    border-color 0.15s ease,
    box-shadow 0.15s ease,
    color 0.15s ease;
}

.log-level-chip:hover {
  border-color: var(--level-color);
  color: var(--level-color);
}

.log-level-chip:focus-visible {
  outline: 2px solid rgba(64, 158, 255, 0.35);
  outline-offset: 2px;
}

.log-level-chip.is-active {
  border-color: var(--level-color);
  background: var(--level-bg);
  color: var(--level-color);
  box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.75);
}

.level-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--level-color);
  opacity: 0.75;
}

.log-level-chip-all {
  --level-color: var(--el-text-color-regular);
  --level-bg: var(--el-color-info-light-9);
}

.log-level-chip.is-debug {
  --level-color: var(--el-text-color-secondary);
  --level-bg: var(--el-color-info-light-9);
}

.log-level-chip.is-info {
  --level-color: var(--el-color-primary);
  --level-bg: var(--el-color-primary-light-9);
}

.log-level-chip.is-warn {
  --level-color: var(--el-color-warning);
  --level-bg: var(--el-color-warning-light-9);
}

.log-level-chip.is-error {
  --level-color: var(--el-color-danger);
  --level-bg: var(--el-color-danger-light-9);
}

@media (max-width: 768px) {
  .log-level-filter {
    align-items: flex-start;
    flex-direction: row;
    width: 100%;
  }

  .level-chips {
    flex: 1;
    gap: 6px;
    min-width: 0;
  }

  .log-level-chip {
    gap: 4px;
    width: auto;
    min-width: 52px;
    min-height: 26px;
    padding: 0 8px;
    font-size: 11px;
  }

  .level-dot {
    width: 6px;
    height: 6px;
  }

  .log-level-chip-all {
    grid-column: auto;
  }
}

@media (max-width: 360px) {
  .log-level-chip {
    min-width: 0;
    padding: 0 7px;
  }
}
</style>
