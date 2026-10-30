<template>
  <div class="strm-regex-input">
    <div
      v-if="modelValue.length || !showInput || clearable || $slots.actions"
      class="tag-input-tags limited-width-input"
    >
      <el-tag
        v-for="(pattern, index) in modelValue"
        :key="index"
        closable
        @close="removePattern(index)"
      >
        <code>{{ pattern }}</code>
      </el-tag>
      <el-button
        v-if="!showInput"
        ref="addButtonRef"
        size="small"
        type="primary"
        plain
        :disabled="disabled"
        @click="showInput = true"
      >
        + 添加
      </el-button>
      <slot name="actions" />
      <el-popconfirm
        v-if="clearable"
        title="清空当前列表？保存后生效。"
        confirm-button-text="清空"
        cancel-button-text="取消"
        confirm-button-type="danger"
        :disabled="disabled || !modelValue.length"
        @confirm="clearPatterns"
      >
        <template #reference>
          <el-button size="small" type="danger" plain :disabled="disabled || !modelValue.length">
            清空
          </el-button>
        </template>
      </el-popconfirm>
    </div>
    <el-input
      v-if="showInput"
      ref="inputRef"
      v-model="draft"
      aria-label="正则排除名称"
      :aria-invalid="!!draftCheck.error"
      placeholder="输入一条正则后按回车添加"
      class="limited-width-input"
      size="default"
      :disabled="disabled"
      @keydown.enter="handleEnter"
    >
      <template #append>
        <el-button :disabled="disabled || draft === ''" @click="addPattern">添加</el-button>
      </template>
    </el-input>
    <p v-if="draftCheck.error" class="regex-error" role="alert">{{ draftCheck.error }}</p>
    <p v-if="notice" class="form-help" role="status">{{ notice }}</p>
    <div class="form-help">
      <p>
        匹配文件名（含扩展名）或每一级目录名，默认区分大小写、部分匹配。
        两类排除规则任意命中即排除，目录命中时也排除其下内容。
      </p>
      <p v-if="showInput">每次添加一条原始表达式；逗号、分号和首尾空格均按原文保留。</p>
      <p v-if="inherit">列表为空时使用 STRM 设置中的正则；填写后覆盖全局正则列表。</p>
      <p v-else>列表为空时不按正则排除名称。</p>
      <p>采用 Go/RE2 语法，保存时由服务器最终校验。</p>
      <details>
        <summary>正则语法与常用示例</summary>
        <p>
          使用 <code>(?i)</code> 忽略大小写，使用 <code>^…$</code> 匹配完整名称；不使用
          <code>/abc/i</code> 格式。
        </p>
        <ul>
          <li><code>sample</code>：名称包含 sample，区分大小写</li>
          <li><code>(?i)(sample|trailer)</code>：名称包含 sample 或 trailer，忽略大小写</li>
          <li><code>(?i)^extras$</code>：完整名称为 extras，忽略大小写</li>
          <li><code>(?i)^sample\.[^.]+$</code>：文件名为 sample 加扩展名，忽略大小写</li>
          <li><code>^\.</code>：名称以点开头</li>
        </ul>
      </details>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useTemplateRef, watch } from 'vue'
import type { ButtonInstance, InputInstance } from 'element-plus'
import { precheckStrmRegex } from '@/utils/strmRegex'

const props = defineProps<{
  modelValue: string[]
  disabled?: boolean
  clearable?: boolean
  inherit?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [patterns: string[]] }>()
const draft = ref('')
const showInput = ref(false)
const inputRef = useTemplateRef<InputInstance>('inputRef')
const addButtonRef = useTemplateRef<ButtonInstance>('addButtonRef')
const draftCheck = computed(() => (draft.value === '' ? {} : precheckStrmRegex(draft.value)))
const notice = computed(
  () =>
    draftCheck.value.notice ||
    props.modelValue.map(precheckStrmRegex).find((check) => check.notice)?.notice,
)

function addPattern() {
  if (props.disabled || draft.value === '' || draftCheck.value.error) return
  if (!props.modelValue.includes(draft.value)) {
    emit('update:modelValue', [...props.modelValue, draft.value])
  }
  draft.value = ''
  showInput.value = false
}

function handleEnter(event: KeyboardEvent) {
  if (event.isComposing) return
  event.preventDefault()
  addPattern()
}

function removePattern(index: number) {
  if (!props.disabled) {
    emit(
      'update:modelValue',
      props.modelValue.filter((_, itemIndex) => itemIndex !== index),
    )
  }
}

async function clearPatterns() {
  if (props.disabled || !props.modelValue.length) return
  emit('update:modelValue', [])
  draft.value = ''
  showInput.value = false
  await nextTick()
  addButtonRef.value?.ref?.focus()
}

watch(showInput, async (visible) => {
  await nextTick()
  if (visible) inputRef.value?.focus()
  else addButtonRef.value?.ref?.focus()
})
</script>

<style scoped>
.strm-regex-input {
  width: 100%;
  min-width: 0;
}

.regex-error {
  color: var(--el-color-danger);
  font-size: 12px;
  margin: 4px 0;
}

summary {
  cursor: pointer;
}

code {
  font-size: inherit;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
