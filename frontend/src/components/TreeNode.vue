<template>
  <div
    class="tree-node"
    role="treeitem"
    tabindex="0"
    :aria-selected="nodeId === selectedId"
    :aria-expanded="isLeaf ? undefined : node.expanded"
    :aria-busy="loadState === 'loading'"
    @click="handleActivate"
    @keydown="handleKeydown"
  >
    <div class="node-content" :class="{ 'is-selected': nodeId === selectedId }">
      <span v-if="isLeaf" class="expand-placeholder" aria-hidden="true"></span>
      <button
        v-else
        class="expand-button"
        type="button"
        :aria-label="node.expanded ? '收起目录' : '展开目录'"
        :aria-expanded="node.expanded"
        :disabled="loadState === 'loading'"
        @click.stop="handleToggle"
        @keydown.stop
      >
        <el-icon>
          <ArrowRight v-if="!node.expanded" />
          <ArrowDown v-else />
        </el-icon>
      </button>
      <el-icon class="folder-icon">
        <Folder />
      </el-icon>
      <span class="node-label" :title="node.path || node.name">{{ node.name }}</span>
      <el-icon v-if="loadState === 'loading'" class="is-loading loading-icon" aria-hidden="true">
        <Loading />
      </el-icon>
      <button
        v-if="loadState === 'error'"
        class="retry-button"
        type="button"
        aria-label="重试加载目录"
        @click.stop="emit('retry', node)"
        @keydown.stop
      >
        重试
      </button>
    </div>
    <div
      v-if="node.expanded && node.children && node.children.length > 0"
      class="node-children"
      role="group"
      @click.stop
    >
      <TreeNode
        v-for="child in node.children"
        :key="child.id"
        :node="child"
        :selected-id="selectedId"
        :source-type="sourceType"
        :account-id="accountId"
        @select="$emit('select', $event)"
        @toggle="$emit('toggle', $event)"
        @retry="$emit('retry', $event)"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ArrowRight, ArrowDown, Folder, Loading } from '@element-plus/icons-vue'

interface TreeNodeData {
  id: string
  name: string
  path: string
  expanded: boolean
  loadState: 'unloaded' | 'loading' | 'loaded' | 'error'
  latestChildLoadId: number
  children: TreeNodeData[]
  isLeaf: boolean
}

interface Props {
  node: TreeNodeData
  selectedId?: string
  sourceType: string
  accountId?: number
}

const props = defineProps<Props>()

const emit = defineEmits<{
  select: [node: TreeNodeData]
  toggle: [node: TreeNodeData]
  retry: [node: TreeNodeData]
}>()

const nodeId = computed(() => props.node.id)
const loadState = computed(() => props.node.loadState)
const isLeaf = computed(() => props.node.isLeaf === true)

const handleActivate = () => {
  emit('select', props.node)
  emit('toggle', props.node)
}

const handleToggle = () => {
  emit('toggle', props.node)
}

const handleKeydown = (event: KeyboardEvent) => {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  event.stopPropagation()
  handleActivate()
}
</script>

<style scoped>
.tree-node {
  user-select: none;
}

.node-content {
  display: flex;
  align-items: center;
  gap: 6px;
  min-height: 36px;
  padding: 4px 8px;
  cursor: pointer;
  border-radius: 4px;
  transition:
    background-color 0.2s,
    color 0.2s;
}

.node-content:hover {
  background-color: var(--el-fill-color-light);
}

.node-content.is-selected {
  background-color: var(--el-color-primary-light-9);
}

.tree-node:focus-visible > .node-content {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 1px;
}

.expand-button,
.expand-placeholder {
  flex: 0 0 32px;
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.expand-button {
  padding: 0;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  background: transparent;
  border: 0;
  border-radius: 4px;
  transition:
    color 0.2s,
    background-color 0.2s;
}

.expand-button:hover:not(:disabled) {
  color: var(--el-color-primary);
  background-color: var(--el-color-primary-light-9);
}

.expand-button:focus-visible,
.retry-button:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 1px;
}

.expand-button:disabled {
  cursor: wait;
}

.folder-icon {
  color: var(--el-color-primary);
}

.node-label {
  flex: 1;
  min-width: 0;
  font-size: 14px;
  color: var(--el-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.loading-icon {
  font-size: 14px;
  color: var(--el-color-primary);
}

.retry-button {
  padding: 2px 4px;
  color: var(--el-color-danger);
  cursor: pointer;
  background: transparent;
  border: 0;
  border-radius: 4px;
}

.retry-button:hover {
  background-color: var(--el-color-danger-light-9);
}

.node-children {
  padding-left: 20px;
}
</style>
