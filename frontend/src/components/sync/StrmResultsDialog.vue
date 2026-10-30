<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { fetchStrmResults, strmResultStatus, type StrmResult } from '@/api/strmResults'
import { useHttpClient } from '@/http/client'
import { notifyHttpError } from '@/utils/httpErrorNotification'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'

const visible = defineModel<boolean>({ required: true })
const props = defineProps<{ uploadTaskId?: number; syncPathId?: number }>()
const http = useHttpClient()
const items = ref<StrmResult[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const parent = ref<StrmResult>()
const loading = ref(false)
let request = 0
onUnmounted(() => {
  ++request
})
async function load() {
  const current = ++request
  loading.value = true
  try {
    const result = await fetchStrmResults(http, {
      page: page.value,
      page_size: pageSize.value,
      upload_task_id: props.uploadTaskId,
      sync_path_id: props.syncPathId,
      parent_task_id: parent.value?.id,
    })
    if (current === request) {
      items.value = result.items
      total.value = result.total
    }
  } catch (error) {
    if (current === request) {
      items.value = []
      total.value = 0
      notifyHttpError(error, '读取 STRM 结果失败')
    }
  } finally {
    if (current === request) loading.value = false
  }
}
function resizePage() {
  page.value = 1
  void load()
}
function showChildren(item?: StrmResult) {
  parent.value = item
  page.value = 1
  void load()
}
watch(
  () => [visible.value, props.uploadTaskId, props.syncPathId],
  () => {
    ++request
    if (visible.value) {
      parent.value = undefined
      page.value = 1
      items.value = []
      void load()
    }
  },
  { immediate: true },
)
</script>

<template>
  <el-dialog v-model="visible" title="STRM 后处理结果" width="min(960px, 95vw)">
    <p>上传完成或 Webhook 接收成功只表示进入处理队列。已跳过的文件会保留上传源文件。</p>
    <el-button v-if="parent" @click="showChildren()">返回任务列表</el-button>
    <el-button :loading="loading" @click="load">刷新结果</el-button>
    <p v-if="parent">任务 #{{ parent.id }} 的子项</p>
    <el-table :data="items" v-loading="loading" empty-text="暂无 STRM 后处理记录">
      <el-table-column prop="id" label="任务 ID" width="90" />
      <el-table-column label="文件或目录" min-width="180"
        ><template #default="{ row }">{{
          row.file_name || row.directory_path || '批量文件'
        }}</template></el-table-column
      >
      <el-table-column label="状态" min-width="130"
        ><template #default="{ row }">{{ strmResultStatus(row.status) }}</template></el-table-column
      >
      <el-table-column label="跳过原因" min-width="140"
        ><template #default="{ row }">{{
          row.skip_reason || (row.status === 'skipped' ? '未记录' : '—')
        }}</template></el-table-column
      >
      <el-table-column label="目录 / 批量统计" min-width="220"
        ><template #default="{ row }"
          ><template v-if="row.task_type !== 'file'"
            >总计 {{ row.total_items ?? '未记录' }}，完成 {{ row.accepted_items ?? '未记录' }}，失败
            {{ row.failed_items ?? '未记录' }}，跳过 {{ row.skipped_items ?? '未记录'
            }}<el-button link type="primary" @click="showChildren(row)"
              >查看子项</el-button
            ></template
          ><span v-else>—</span></template
        ></el-table-column
      >
    </el-table>
    <ResponsivePagination
      v-model:current-page="page"
      v-model:page-size="pageSize"
      :total="total"
      @current-change="load"
      @size-change="resizePage"
    />
  </el-dialog>
</template>
