<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue'

import { useV115AppIdSearch } from '@/composables/useV115AppIdSearch'
import { useHttpClient } from '@/http/client'
import {
  featuredBuiltInAppIDs,
  pinnedBuiltInAppIDs,
  type V115SelectedQrApp,
} from './v115AuthSources'

const selectedQrApp = defineModel<V115SelectedQrApp>('selectedQrApp', { required: true })
const customAppId = defineModel<string>('customAppId', { required: true })
const customAppName = defineModel<string>('customAppName', { required: true })

interface V115AppSelectOption {
  label: string
  value: string
  appName: string
}

const http = useHttpClient()
const { keyword, items, total, loading, hasMore, search, loadMore, reset } = useV115AppIdSearch({
  http,
})
const dropdownVisible = shallowRef(false)
const showDefaultRemoteOptions = shallowRef(false)
const remoteSearchHint = '输入应用名或 APP ID 搜索更多内置应用'
const customOption: V115AppSelectOption = {
  label: '自定义 APP ID',
  value: 'custom',
  appName: '自定义 APP ID',
}

const defaultOptions = computed<V115AppSelectOption[]>(() => [
  ...pinnedBuiltInAppIDs,
  ...featuredBuiltInAppIDs,
])
const defaultOptionValues = computed(() => new Set(defaultOptions.value.map((item) => item.value)))
const remoteOptions = computed<V115AppSelectOption[]>(() =>
  items.value
    .filter((item) => item.deprecated !== true)
    .map((item) => ({
      label: item.display_name || item.app_name,
      value: item.app_id,
      appName: item.app_name,
    })),
)
const defaultRemoteOptions = computed(() =>
  remoteOptions.value.filter((item) => !defaultOptionValues.value.has(item.value)),
)
const selectOptions = computed(() => {
  if (keyword.value.trim()) {
    return [...remoteOptions.value, customOption]
  }
  if (showDefaultRemoteOptions.value) {
    return [...defaultOptions.value, ...defaultRemoteOptions.value, customOption]
  }
  return [...defaultOptions.value, customOption]
})
const visibleRemoteCount = computed(() => {
  if (keyword.value.trim()) {
    return remoteOptions.value.length
  }
  if (showDefaultRemoteOptions.value) {
    return defaultRemoteOptions.value.length
  }
  return 0
})
const resultSummary = computed(() => {
  if (total.value === 0) return ''
  if (!keyword.value.trim() && !showDefaultRemoteOptions.value) return ''
  return `已显示 ${visibleRemoteCount.value} / ${total.value}`
})
const hasBufferedDefaultResults = computed(
  () => !keyword.value.trim() && !showDefaultRemoteOptions.value && items.value.length > 0,
)
const showLoadMoreButton = computed(() => hasBufferedDefaultResults.value || hasMore.value)
const showDropdownFooter = computed(() => Boolean(showLoadMoreButton.value || resultSummary.value))
const selectedValue = computed({
  get: () => selectedQrApp.value.appId,
  set: (value) => {
    const option = [...defaultOptions.value, ...remoteOptions.value, customOption].find(
      (item) => item.value === value,
    )
    selectedQrApp.value = {
      appId: value,
      appName: option?.appName || option?.label || value,
    }
  },
})
const showCustomFields = computed(() => selectedQrApp.value.appId === 'custom')

const normalizeSearchInput = (value: unknown) => {
  if (typeof value === 'string') {
    return value
  }
  if (value instanceof Event) {
    const target = value.target
    if (target instanceof HTMLInputElement) {
      return target.value
    }
  }
  return ''
}

const handleSearch = (value: unknown) => {
  const nextKeyword = normalizeSearchInput(value)
  showDefaultRemoteOptions.value = false
  keyword.value = nextKeyword
  if (!nextKeyword.trim()) {
    reset()
    if (dropdownVisible.value) {
      void search()
    }
    return
  }
  void search()
}

const handleLoadMore = () => {
  if (!keyword.value.trim() && !showDefaultRemoteOptions.value) {
    showDefaultRemoteOptions.value = true
    if (items.value.length === 0) {
      void search()
    }
    return
  }
  void loadMore()
}

const handleVisibleChange = (visible: boolean) => {
  dropdownVisible.value = visible
  if (visible && !keyword.value.trim() && total.value === 0 && !loading.value) {
    showDefaultRemoteOptions.value = false
    void search()
  }
}

watch(showCustomFields, (visible) => {
  if (!visible) return
  if (!customAppName.value) {
    customAppName.value = ''
  }
})
</script>

<template>
  <el-form-item label="APP ID">
    <el-select
      v-model="selectedValue"
      class="v115-app-select"
      filterable
      remote
      clearable
      reserve-keyword
      remote-show-suffix
      placeholder="选择或搜索 115 开放平台 APP ID"
      :remote-method="handleSearch"
      :loading="loading"
      @visible-change="handleVisibleChange"
    >
      <template v-if="dropdownVisible" #header>
        <div class="v115-select-hint">{{ remoteSearchHint }}</div>
      </template>
      <el-option
        v-for="option in selectOptions"
        :key="option.value"
        :label="option.label"
        :value="option.value"
      />
      <template v-if="showDropdownFooter" #footer>
        <div class="v115-select-footer">
          <span class="v115-result-summary">{{ resultSummary }}</span>
          <el-button
            v-if="showLoadMoreButton"
            class="v115-load-more-button"
            text
            type="primary"
            :loading="loading"
            @click.stop="handleLoadMore"
          >
            加载更多
          </el-button>
        </div>
      </template>
    </el-select>
  </el-form-item>
  <template v-if="showCustomFields">
    <el-form-item label="应用名">
      <el-input
        v-model="customAppName"
        name="v115-custom-app-name"
        autocomplete="off"
        placeholder="请输入应用名，可留空"
        clearable
      />
    </el-form-item>
    <el-form-item label="APP ID">
      <el-input
        v-model="customAppId"
        name="v115-app-id"
        autocomplete="off"
        placeholder="请输入 115 开放平台 APP ID"
        clearable
      />
    </el-form-item>
  </template>
</template>

<style scoped>
.v115-app-select {
  width: 100%;
}

.v115-select-hint {
  color: #909399;
  font-size: 12px;
  line-height: 18px;
  padding: 4px 0;
}

.v115-select-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  min-height: 32px;
}

.v115-result-summary {
  color: #909399;
  font-size: 12px;
}

.v115-load-more-button {
  padding-left: 0;
  padding-right: 0;
}
</style>
