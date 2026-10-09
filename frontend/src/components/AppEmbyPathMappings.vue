<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import type { EmbyPathMapping } from '@/api/emby'
import { fetchSyncPaths, type SyncPath } from '@/api/syncPaths'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'

const props = defineProps<{ modelValue: EmbyPathMapping[]; disabled?: boolean }>()
const emit = defineEmits<{
  'update:modelValue': [value: EmbyPathMapping[]]
  ready: [value: boolean]
}>()
const http = useHttpClient()
const mappingDetailsRef = useTemplateRef<HTMLDetailsElement>('mappingDetailsRef')
const syncPaths = ref<SyncPath[]>([])
const loadState = ref<'unloaded' | 'loading' | 'loaded' | 'error'>('unloaded')
const loadError = ref('')
let active = true
onBeforeUnmount(() => {
  active = false
})

const ready = computed(() => props.modelValue.length === 0 || loadState.value === 'loaded')
watch(ready, (value) => emit('ready', value), { immediate: true })

const loadSyncPaths = async () => {
  if (loadState.value === 'loading') return
  loadState.value = 'loading'
  loadError.value = ''
  try {
    const paths: SyncPath[] = []
    let page = 1
    while (active) {
      const result = await fetchSyncPaths(http, { page, page_size: 100 })
      paths.push(...result.list)
      if (paths.length >= result.total || result.list.length === 0) break
      page += 1
    }
    if (!active) return
    syncPaths.value = paths.filter((path) =>
      ['115', 'baidupan', 'openlist'].includes(path.source_type),
    )
    loadState.value = 'loaded'
  } catch (error) {
    if (!active) return
    loadState.value = 'error'
    const failure = parseHttpError(error, { fallbackMessage: '请重试加载同步目录' })
    loadError.value = failure.shouldNotify ? failure.message : '同步目录尚未加载完成'
  }
}

watch(
  () => props.modelValue.length,
  (length) => {
    if (length > 0 && loadState.value === 'unloaded') void loadSyncPaths()
  },
  { immediate: true },
)

const updateRow = (index: number, change: Partial<EmbyPathMapping>) => {
  emit(
    'update:modelValue',
    props.modelValue.map((row, rowIndex) => (rowIndex === index ? { ...row, ...change } : row)),
  )
}
const addRow = () => {
  emit('update:modelValue', [...props.modelValue, { emby_root: '', local_root: '' }])
}
const removeRow = (index: number) => {
  emit(
    'update:modelValue',
    props.modelValue.filter((_, rowIndex) => rowIndex !== index),
  )
}
const missingSyncPath = (id?: number) => id && !syncPaths.value.some((path) => path.id === id)
const pathLabel = (path: SyncPath) =>
  `${path.account_name || path.source_type} · ${path.remote_path || '/'}（${path.local_path}）`
const rootRules = [
  { required: true, message: '请输入完整目录路径，如 /media/电影', trigger: 'blur' },
  {
    pattern: /^(?:\/|[A-Za-z]:[/\\]|\\\\)/,
    message: '请输入完整目录路径，如 /media/电影',
    trigger: 'blur',
  },
]
const expand = () => {
  if (mappingDetailsRef.value) mappingDetailsRef.value.open = true
}
defineExpose({ expand })
</script>

<template>
  <section class="emby-path-mappings" aria-label="STRM 路径映射">
    <details ref="mappingDetailsRef" class="mapping-details">
      <summary class="mapping-summary">
        <span>STRM 路径映射（可选）</span>
        <span class="mapping-summary-note">
          通常无需填写<span v-if="modelValue.length > 0"> · 已配置 {{ modelValue.length }} 条</span>
        </span>
      </summary>
      <p class="mapping-help">
        QMS 同步目录内的 STRM 已有文件记录，且 Emby 路径与记录一致时，通常无需填写。
        两端挂载路径不同，或 STRM 放在原有记录之外、QMS 不知道实际目录时，需要填写。
        仅匹配到网盘视频，还不能确定实际 STRM 的位置。
      </p>
      <p class="mapping-help">
        映射告诉 QMS 到哪里检查原 STRM 是否还在，避免 Emby 暂时看不到文件时误删网盘视频。 原 STRM
        仍在，或所在目录无法访问时，QMS 会保留网盘文件。
      </p>
      <p class="mapping-help">
        填写两端对应的文件夹路径（目录前缀），不要填写单个 .strm 文件。 QMS 中的目录必须能在 QMS
        所在机器或容器内访问。 外部 STRM
        没有对应文件记录、两端路径相同时，可在两栏填写相同的实际目录。
      </p>
      <details class="mapping-example">
        <summary class="mapping-example-summary">示例 1：已有记录且路径一致，无需填写</summary>
        <p class="mapping-help">
          QMS 的同步目录已记录这份 STRM：<code>/strm/电影/示例电影/示例电影.strm</code>， Emby
          使用的也是这个路径，通常无需填写。
        </p>
      </details>
      <details class="mapping-example">
        <summary class="mapping-example-summary">示例 2：容器挂载路径不同，怎样填写</summary>
        <p class="mapping-help">同一个 STRM 文件，在两个容器中看到的路径不同：</p>
        <ul class="mapping-example-paths">
          <li>Emby：<code>/media/电影/示例电影/示例电影.strm</code></li>
          <li>QMS：<code>/strm/电影/示例电影/示例电影.strm</code></li>
        </ul>
        <p class="mapping-help">
          “Emby 中的目录”填 <code>/media/电影</code>，“QMS 中的目录”填 <code>/strm/电影</code>。
          两个目录下的文件夹层级和名称应一致，“对应同步目录”通常留空。
        </p>
      </details>
      <fieldset
        v-for="(mapping, index) in modelValue"
        :key="index"
        class="mapping-row"
        :disabled="disabled"
      >
        <legend class="mapping-row-title">路径映射 {{ index + 1 }}</legend>
        <el-form-item
          label="Emby 中的目录"
          :prop="`path_mappings.${index}.emby_root`"
          :rules="rootRules"
        >
          <el-input
            :model-value="mapping.emby_root"
            :aria-label="`映射 ${index + 1} Emby 中的目录`"
            placeholder="如 /media/电影"
            :disabled="disabled"
            @update:model-value="updateRow(index, { emby_root: $event })"
          />
        </el-form-item>
        <el-form-item
          label="QMS 中的目录"
          :prop="`path_mappings.${index}.local_root`"
          :rules="rootRules"
        >
          <el-input
            :model-value="mapping.local_root"
            :aria-label="`映射 ${index + 1} QMS 中的目录`"
            placeholder="如 /strm/电影"
            :disabled="disabled"
            @update:model-value="updateRow(index, { local_root: $event })"
          />
        </el-form-item>
        <el-form-item label="对应同步目录（可选）">
          <el-select
            :model-value="mapping.sync_path_id || undefined"
            :aria-label="`映射 ${index + 1} 对应同步目录`"
            clearable
            placeholder="通常留空"
            :loading="loadState === 'loading'"
            :disabled="disabled || !ready"
            @update:model-value="updateRow(index, { sync_path_id: $event || undefined })"
          >
            <el-option
              v-if="missingSyncPath(mapping.sync_path_id)"
              :value="mapping.sync_path_id!"
              :label="`同步目录不可用（ID ${mapping.sync_path_id}）`"
              disabled
            />
            <el-option
              v-for="syncPath in syncPaths"
              :key="syncPath.id"
              :value="syncPath.id"
              :label="pathLabel(syncPath)"
            />
          </el-select>
        </el-form-item>
        <p class="mapping-help">
          仅在上面两个目录下的文件夹名称、层级与所选网盘同步目录下完全一致时选择。
          改过名、重新分类过或不确定时留空。
        </p>
        <el-button
          type="danger"
          plain
          :disabled="disabled"
          :aria-label="`移除路径映射 ${index + 1}`"
          @click="removeRow(index)"
          >移除映射</el-button
        >
      </fieldset>
      <p v-if="modelValue.length === 0" class="mapping-help mapping-note">
        未配置路径映射，继续使用已有记录和原路径。
      </p>
      <el-button type="primary" plain :disabled="disabled" @click="addRow">添加路径映射</el-button>
      <p class="mapping-help mapping-note">
        修改后，开启“启用同步”并点击页面下方的“保存设置”，再执行一次“启动全量同步”。
      </p>
    </details>
    <el-alert
      v-if="modelValue.length > 0 && loadState === 'error'"
      class="mapping-error"
      type="error"
      :closable="false"
      show-icon
    >
      <template #title>同步目录加载失败，暂时无法保存设置</template>
      <p>{{ loadError }}</p>
      <el-button :disabled="disabled" @click="loadSyncPaths">重试加载同步目录</el-button>
    </el-alert>
  </section>
</template>

<style scoped>
.mapping-summary {
  padding: 10px 0;
  cursor: pointer;
}
.mapping-summary-note {
  display: inline-block;
  margin-left: 8px;
  color: var(--el-text-color-secondary);
}
.mapping-help {
  margin: 6px 0;
  font-size: inherit;
  line-height: inherit;
}
.mapping-note {
  color: var(--el-text-color-secondary);
}
.mapping-example {
  margin: 8px 0;
}
.mapping-example-summary {
  padding: 6px 0;
  cursor: pointer;
  color: var(--el-text-color-secondary);
}
.mapping-summary:hover,
.mapping-example-summary:hover {
  color: var(--el-color-primary);
}
.mapping-summary:focus-visible,
.mapping-example-summary:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 2px;
  border-radius: 4px;
}
.mapping-example-paths {
  margin: 6px 0;
  padding-left: 20px;
}
.mapping-help code,
.mapping-example-paths code {
  font-size: inherit;
  overflow-wrap: anywhere;
}
.mapping-row {
  min-width: 0;
  margin: 16px 0;
  padding: 16px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
}
.mapping-row-title {
  padding: 0 6px;
  font-size: 14px;
  font-weight: 500;
}
.mapping-row :deep(.el-form-item__content) {
  min-width: 0;
}
.mapping-row :deep(.el-select) {
  width: 100%;
}
.mapping-error {
  margin-top: 12px;
}
@media (max-width: 768px) {
  .mapping-row {
    padding: 12px;
  }
}
</style>
