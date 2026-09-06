<template>
  <div class="strm-regex-input">
    <div class="regex-tags limited-width-input">
      <el-tag
        v-for="(pattern, index) in modelValue"
        :key="index"
        closable
        class="regex-tag"
        @close="removePattern(index)"
      >
        <code>{{ pattern }}</code>
      </el-tag>
    </div>
    <el-input
      v-model="draft"
      aria-label="正则排除名称"
      :aria-invalid="!!draftCheck.error"
      placeholder="输入一条正则后按回车添加"
      class="limited-width-input"
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
        匹配文件名（含扩展名）或每一级目录名，默认区分大小写、部分匹配。使用
        <code>(?i)</code> 忽略大小写，使用 <code>^…$</code> 匹配完整名称。
      </p>
      <p>
        每次添加一条原始表达式，不使用 <code>/abc/i</code> 格式；逗号、分号和首尾空格均按原文保留。
        两类排除规则任意命中即排除，目录命中时也排除其下内容。
      </p>
      <p v-if="inherit">列表为空时使用 STRM 设置中的正则；填写后覆盖全局正则列表。</p>
      <p>采用 Go/RE2 语法，保存时由服务器最终校验。</p>
      <details>
        <summary>常用正则示例</summary>
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
import { computed, ref } from 'vue'
import { precheckStrmRegex } from '@/utils/strmRegex'

const props = defineProps<{
  modelValue: string[]
  disabled?: boolean
  inherit?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [patterns: string[]] }>()
const draft = ref('')
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
</script>

<style scoped>
.strm-regex-input {
  width: 100%;
  min-width: 0;
}

.regex-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}

.regex-tag {
  max-width: 100%;
  height: auto;
  padding: 4px 8px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.regex-tag :deep(.el-tag__content) {
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
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
