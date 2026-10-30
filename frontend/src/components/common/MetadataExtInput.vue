<template>
  <div class="metadata-ext-input-container">
    <div v-if="tags.length || !showInput || clearable || $slots.actions" class="tag-input-tags">
      <el-tag v-for="(tag, index) in tags" :key="index" closable @close="removeTag(index)">
        {{ tag }}
      </el-tag>
      <el-button
        v-if="!showInput"
        ref="addButtonRef"
        @click="showInput = true"
        size="small"
        type="primary"
        plain
        :disabled="disabled"
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
        :disabled="disabled || !tags.length"
        @confirm="clearTags"
      >
        <template #reference>
          <el-button size="small" type="danger" plain :disabled="disabled || !tags.length">
            清空
          </el-button>
        </template>
      </el-popconfirm>
    </div>
    <el-input
      ref="inputRef"
      v-model="inputValue"
      :placeholder="placeholder"
      @keydown.enter="handleEnter"
      class="limited-width-input"
      size="default"
      :disabled="disabled"
      v-if="showInput"
    >
      <template #append>
        <el-button @click="addTags" type="primary" :disabled="disabled || !inputValue.trim()">
          添加
        </el-button>
      </template>
    </el-input>
  </div>
</template>

<script setup lang="ts">
import { nextTick, ref, useTemplateRef, watch, type Ref } from 'vue'
import type { ButtonInstance, InputInstance } from 'element-plus'

// 定义组件属性
interface Props {
  modelValue: string[]
  placeholder?: string
  autoAddDot?: boolean
  disabled?: boolean
  clearable?: boolean
}

// 定义事件发射
const emit = defineEmits<{
  (e: 'update:modelValue', value: string[]): void
}>()

// 设置默认值
const props = withDefaults(defineProps<Props>(), {
  modelValue: () => [],
  placeholder: '输入扩展名后按回车添加，如：jpg 或 jpg,png,gif',
  autoAddDot: true,
  disabled: false,
  clearable: false,
})

// 输入框值
const inputValue: Ref<string> = ref('')

// 是否显示输入框
const showInput: Ref<boolean> = ref(false)
const inputRef = useTemplateRef<InputInstance>('inputRef')
const addButtonRef = useTemplateRef<ButtonInstance>('addButtonRef')

// 标签值
const tags: Ref<string[]> = ref([])

// 监听外部传入的值变化
watch(
  () => props.modelValue,
  (newVal) => {
    tags.value = [...newVal]
  },
  { deep: true, immediate: true },
)

// 添加标签
const addTags = () => {
  if (props.disabled) return
  if (!inputValue.value.trim()) {
    showInput.value = false
    return
  }

  // 分割输入的扩展名（支持逗号或分号分隔）
  const exts = inputValue.value
    .split(/[,;]+/)
    .map((ext) => ext.trim())
    .filter((ext) => ext.length > 0)

  // 处理每个扩展名
  const newTags = [...tags.value]
  exts.forEach((ext) => {
    // 根据属性控制是否自动添加点号
    const formattedExt = props.autoAddDot && !ext.startsWith('.') ? `.${ext}` : ext

    // 避免重复添加
    if (!newTags.includes(formattedExt)) {
      newTags.push(formattedExt)
    }
  })

  // 更新标签
  tags.value = newTags
  emit('update:modelValue', newTags)

  // 清空输入框并隐藏输入框
  inputValue.value = ''
  showInput.value = false
}

const handleEnter = (event: KeyboardEvent) => {
  if (event.isComposing) return
  event.preventDefault()
  addTags()
}

// 删除标签
const removeTag = (index: number) => {
  if (props.disabled) return
  tags.value.splice(index, 1)
  emit('update:modelValue', tags.value)
}

const clearTags = async () => {
  if (props.disabled || !tags.value.length) return
  tags.value = []
  emit('update:modelValue', tags.value)
  inputValue.value = ''
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
.metadata-ext-input-container {
  width: 100%;
  min-width: 0;
}
</style>
