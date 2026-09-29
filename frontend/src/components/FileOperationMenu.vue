<script setup lang="ts">
import { ArrowDown } from '@element-plus/icons-vue'
import type { FileOperationType, FileSystemItem } from '@/typing'

defineProps<{ row: FileSystemItem }>()
const emit = defineEmits<{
  command: [operation: FileOperationType, row: FileSystemItem]
}>()
</script>

<template>
  <el-dropdown trigger="click" @command="emit('command', $event, row)">
    <el-button type="primary" size="small" :aria-label="`操作 ${row.name}`">
      操作 <el-icon class="el-icon--right"><ArrowDown /></el-icon>
    </el-button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item command="STRM_GENERATE">STRM 生成</el-dropdown-item>
        <!-- 刮削整理与生成 ED2K 尚未实装，入口保持隐藏；处理分支留在父组件。 -->
        <el-dropdown-item command="MOVE">移动</el-dropdown-item>
        <el-dropdown-item command="COPY">复制</el-dropdown-item>
        <el-dropdown-item command="RENAME">重命名</el-dropdown-item>
        <el-dropdown-item command="DELETE" divided>删除</el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>
