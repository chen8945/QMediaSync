<script setup lang="ts">
import { onBeforeUnmount, shallowRef, watch } from 'vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import { fetchSyncTaskFailures } from '@/api/syncTasks'
import { useDeviceType } from '@/composables/useDeviceType'
import { parseHttpError } from '@/http/errors'
import type { SyncScanFailure } from '@/types/syncTaskStream'

const props = defineProps<{ syncId: number; status: number }>()
const { isMobile } = useDeviceType()
const entries = shallowRef<SyncScanFailure[]>([])
const total = shallowRef(0)
const recorded = shallowRef(false)
const page = shallowRef(1)
const loading = shallowRef(false)
const errorMessage = shallowRef('')
let controller: AbortController | null = null
let requestedPage = 1
const load = async (nextPage = 1) => {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  errorMessage.value = ''
  requestedPage = nextPage
  try {
    const data = await fetchSyncTaskFailures(props.syncId, (nextPage - 1) * 100, current.signal)
    if (current.signal.aborted) return
    entries.value = data.entries
    total.value = data.total
    recorded.value = data.recorded
    page.value = nextPage
  } catch (error) {
    if (current.signal.aborted) return
    const failure = parseHttpError(error, { fallbackMessage: '加载失败范围失败' })
    if (failure.shouldNotify) errorMessage.value = failure.message
  } finally {
    if (controller === current) loading.value = false
  }
}
watch(
  [() => props.syncId, () => props.status],
  () => {
    controller?.abort()
    entries.value = []
    total.value = 0
    recorded.value = false
    page.value = 1
    loading.value = false
    errorMessage.value = ''
    if (props.status >= 2) void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => controller?.abort())
</script>

<template>
  <div v-loading="loading">
    <span v-if="errorMessage"
      >{{ errorMessage }} <el-button link @click="load(requestedPage)">重试</el-button></span
    >
    <ul v-if="entries.length" class="scan-failures">
      <li v-for="(failure, index) in entries" :key="index">
        {{ failure.kind === 'directory' ? '目录' : '文件' }} {{ failure.path }}：{{
          failure.reason
        }}
      </li>
    </ul>
    <span v-else-if="!loading && !errorMessage">{{ recorded ? '无失败范围' : '未记录' }}</span>
    <ResponsivePagination
      v-if="total > 100"
      :is-mobile="isMobile"
      mobile-layout="prev, pager, next, total"
      desktop-layout="prev, pager, next, total"
      :background="false"
      :current-page="page"
      :page-size="100"
      :total="total"
      @current-change="load"
    />
  </div>
</template>

<style scoped>
.scan-failures {
  margin: 0;
  padding-left: 18px;
  overflow-wrap: anywhere;
}
</style>
