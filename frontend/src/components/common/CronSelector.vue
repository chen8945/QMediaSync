<template>
  <div class="cron-selector">
    <el-select
      v-model="selectedPreset"
      placeholder="选择定时策略"
      @change="handlePresetChange"
      style="width: 100%"
    >
      <el-option label="每天凌晨 2 点" value="0 2 * * *" />
      <el-option label="每天凌晨 3 点" value="0 3 * * *" />
      <el-option label="每 4 小时" value="0 */4 * * *" />
      <el-option label="每 12 小时" value="0 */12 * * *" />
      <el-option label="每周日凌晨 0 点" value="0 0 * * 0" />
      <el-option label="自定义" value="custom" />
    </el-select>

    <!-- 自定义输入框 -->
    <el-input
      v-if="selectedPreset === 'custom'"
      v-model="customCron"
      placeholder="请输入 Cron 表达式，如：0 2 * * *"
      @input="handleCustomCronChange"
      style="margin-top: 12px"
    >
      <template #prepend>Cron</template>
    </el-input>
  </div>
</template>

<script setup lang="ts">
import { shallowRef, watch } from 'vue'

const cronValue = defineModel<string>({ required: true })
const customCron = defineModel<string>('customValue', { default: '' })

const selectedPreset = shallowRef<string>('0 2 * * *')

const defaultCustomCron = '0 2 * * *'
const presetValues = new Set(['0 2 * * *', '0 3 * * *', '0 */4 * * *', '0 */12 * * *', '0 0 * * 0'])

const handlePresetChange = (value: string) => {
  if (value === 'custom') {
    const nextCron = customCron.value || cronValue.value || defaultCustomCron
    customCron.value = nextCron
    cronValue.value = nextCron
  } else {
    cronValue.value = value
  }
}

const handleCustomCronChange = (value: string) => {
  customCron.value = value
  cronValue.value = value
}

watch(
  cronValue,
  (newValue) => {
    if (!newValue) return

    if (presetValues.has(newValue)) {
      if (selectedPreset.value !== 'custom') {
        selectedPreset.value = newValue
      }
    } else {
      if (selectedPreset.value !== 'custom' || customCron.value !== newValue) {
        selectedPreset.value = 'custom'
        customCron.value = newValue
      }
    }
  },
  { immediate: true },
)
</script>

<style scoped>
.cron-selector {
  width: 100%;
}
</style>
