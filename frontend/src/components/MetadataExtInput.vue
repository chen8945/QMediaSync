<template>
  <div class="metadata-ext-input-container">
    <div v-if="tags.length || !showInput" class="tag-input-tags">
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
      >
        + 添加
      </el-button>
    </div>
    <el-input
      ref="inputRef"
      v-model="inputValue"
      :placeholder="placeholder"
      @keydown.enter="handleEnter"
      class="limited-width-input"
      size="default"
      v-if="showInput"
    >
      <template #append>
        <el-button @click="addTags" type="primary" :disabled="!inputValue.trim()">添加</el-button>
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
  tags.value.splice(index, 1)
  emit('update:modelValue', tags.value)
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
