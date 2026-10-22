<script setup lang="ts">
import { ref, toRaw, watch } from 'vue'
import type { FnosPathMapping } from '@/api/fnosProxy'
import type { DirInfo } from '@/typing'
import DirectorySelector from '@/components/DirectorySelector.vue'

const mappings = defineModel<FnosPathMapping[]>({ required: true })
const props = defineProps<{ disabled?: boolean }>()
const rowKeys = new WeakMap<FnosPathMapping, number>()
let nextKey = 0
const rowKey = (row: FnosPathMapping) => {
  const raw = toRaw(row)
  if (!rowKeys.has(raw)) rowKeys.set(raw, ++nextKey)
  return rowKeys.get(raw)!
}
const updateRow = (index: number, change: Partial<FnosPathMapping>) => {
  if (props.disabled) return
  mappings.value = mappings.value.map((row, position) => {
    if (position !== index) return row
    const updated = { ...row, ...change }
    rowKeys.set(updated, rowKey(row))
    return updated
  })
}
const addRow = () => {
  if (!props.disabled) mappings.value = [...mappings.value, { fnos_root: '', local_root: '' }]
}

interface Picker {
  id: number
  row: number
  selected: DirInfo | null
  update: (value: DirInfo | null) => void
  confirm: () => void
  cancel: () => void
}
const picker = ref<Picker | null>(null)
let nextPicker = 0
const openPicker = (row: FnosPathMapping) => {
  if (props.disabled) return
  const id = ++nextPicker
  const key = rowKey(row)
  picker.value = {
    id,
    row: key,
    selected: null,
    update: (value) => {
      if (picker.value?.id === id && !props.disabled) picker.value.selected = value
    },
    confirm: () => {
      if (picker.value?.id !== id || props.disabled || !picker.value.selected) return
      const index = mappings.value.findIndex((mapping) => rowKey(mapping) === key)
      if (index >= 0) updateRow(index, { local_root: picker.value.selected.path })
      picker.value = null
    },
    cancel: () => {
      if (picker.value?.id === id) picker.value = null
    },
  }
}
const removeRow = (index: number) => {
  if (props.disabled) return
  if (picker.value?.row === rowKey(mappings.value[index]!)) picker.value = null
  mappings.value = mappings.value.filter((_, position) => position !== index)
}
watch(
  () => props.disabled,
  (disabled) => {
    if (disabled) picker.value = null
  },
)
const rootRules = [
  { required: true, message: '请输入完整文件夹路径', trigger: 'blur' },
  {
    pattern: /^(?:\/|[A-Za-z]:[/\\]|\\\\)/,
    message: '请输入完整文件夹路径，如 /media/strm',
    trigger: 'blur',
  },
]
</script>

<template>
  <section class="fnos-path-mappings" aria-label="STRM 路径映射">
    <h2 class="card-title">STRM 路径映射（可选）</h2>
    <p class="form-help">
      一般可以先留空。找不到 STRM 文件，且飞牛影视与 QMediaSync 中的文件夹路径不同时，
      再填写两边的对应路径。路径映射只用于找到 STRM 文件，不会移动或修改文件。
    </p>
    <details class="mapping-example">
      <summary>两边路径不同，怎样填写？</summary>
      <p class="form-help">
        同一份文件，飞牛影视看到 <code>/vol1/media/strm/电影/示例.strm</code>， QMediaSync 看到
        <code>/media/strm/电影/示例.strm</code>， 两栏分别填写 <code>/vol1/media/strm</code> 和
        <code>/media/strm</code>。 文件夹中的层级和名称应一致。
      </p>
    </details>
    <fieldset
      v-for="(mapping, index) in mappings"
      :key="rowKey(mapping)"
      class="mapping-row"
      :disabled="disabled"
    >
      <legend>路径映射 {{ index + 1 }}</legend>
      <el-form-item
        label="飞牛影视中的文件夹"
        :prop="`path_mappings.${index}.fnos_root`"
        :rules="rootRules"
      >
        <el-input
          :model-value="mapping.fnos_root"
          :aria-label="`映射 ${index + 1} 飞牛影视中的 STRM 文件夹`"
          placeholder="如 /vol1/media/strm"
          :disabled="disabled"
          @update:model-value="updateRow(index, { fnos_root: $event })"
        />
      </el-form-item>
      <el-form-item
        label="QMediaSync 中的文件夹"
        :prop="`path_mappings.${index}.local_root`"
        :rules="rootRules"
      >
        <div class="local-path-input">
          <el-input
            :model-value="mapping.local_root"
            :aria-label="`映射 ${index + 1} QMediaSync 中的 STRM 文件夹`"
            placeholder="如 /media/strm"
            :disabled="disabled"
            @update:model-value="updateRow(index, { local_root: $event })"
          />
          <el-button :disabled="disabled" @click="openPicker(mapping)">选择文件夹</el-button>
        </div>
      </el-form-item>
      <el-button type="danger" plain :disabled="disabled" @click="removeRow(index)">
        删除此映射
      </el-button>
    </fieldset>
    <p class="form-help">请先确认 QMediaSync 能访问这些文件夹；使用 Docker 时填写容器内的路径。</p>
    <el-button :disabled="disabled" @click="addRow">添加路径映射</el-button>

    <el-dialog
      v-if="picker"
      :key="picker.id"
      :model-value="picker !== null"
      title="选择 QMediaSync 中的 STRM 文件夹"
      width="min(640px, calc(100vw - 24px))"
      :close-on-click-modal="false"
      @update:model-value="picker.cancel"
    >
      <DirectorySelector
        :model-value="picker.selected"
        source-type="local"
        :reset-on-select="false"
        @update:model-value="picker.update"
        @select="picker.confirm"
        @cancel="picker.cancel"
      />
    </el-dialog>
  </section>
</template>

<style scoped>
.fnos-path-mappings {
  overflow-wrap: anywhere;
}
.mapping-example {
  margin: 12px 0;
}
.mapping-example summary {
  cursor: pointer;
}
.mapping-row {
  min-width: 0;
  margin: 16px 0;
  padding: 16px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
}
.local-path-input {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  width: 100%;
}
.local-path-input .el-input {
  flex: 1 1 200px;
}
</style>
