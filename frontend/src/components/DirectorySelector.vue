<template>
  <div class="directory-selector">
    <div class="selector-toolbar">
      <el-breadcrumb class="directory-breadcrumb" separator="/" aria-label="当前目录位置">
        <el-breadcrumb-item>{{ rootPathLabel }}</el-breadcrumb-item>
        <el-breadcrumb-item v-for="node in breadcrumbNodes" :key="node.id">
          <button
            type="button"
            class="breadcrumb-button"
            :data-testid="`directory-breadcrumb-${node.id}`"
            @click="handleBreadcrumbNavigate(node)"
          >
            {{ node.name }}
          </button>
        </el-breadcrumb-item>
      </el-breadcrumb>
      <el-button
        plain
        :icon="Refresh"
        :loading="loading"
        :disabled="createLoading"
        aria-label="刷新目录"
        @click="refreshDirectories"
      >
        刷新
      </el-button>
    </div>

    <div
      v-loading="loading"
      data-testid="directory-loading-container"
      class="tree-loading-container"
    >
      <div class="tree-container" role="tree" aria-label="目录列表">
        <div v-if="treeData.length === 0" class="empty-state">
          <el-empty description="暂无目录" />
        </div>
        <div v-else>
          <TreeNode
            v-for="node in treeData"
            :key="node.id"
            :node="node"
            :selected-id="selectedDir?.id"
            :source-type="sourceType"
            :account-id="accountId"
            @select="handleNodeSelect"
            @toggle="handleToggle"
            @retry="retryNode"
          />
        </div>
      </div>
    </div>

    <div class="footer-buttons">
      <el-button :disabled="!createParent || loading" @click="openCreateDialog"
        >新建文件夹</el-button
      >
      <el-button @click="handleCancel">取消</el-button>
      <el-button type="primary" :disabled="!selectedDir || loading" @click="handleButtonSelect"
        >选择</el-button
      >
    </div>

    <el-dialog
      v-model="showCreateDialog"
      title="新建文件夹"
      width="min(400px, calc(100vw - 32px))"
      :close-on-click-modal="false"
      append-to-body
    >
      <el-form ref="createFormRef" :model="createForm" :rules="createRules" label-position="top">
        <el-form-item label="文件夹名称" prop="name">
          <el-input v-model="createForm.name" placeholder="请输入文件夹名称" clearable />
        </el-form-item>
      </el-form>
      <template #footer>
        <span class="dialog-footer">
          <el-button @click="showCreateDialog = false">取消</el-button>
          <el-button
            type="primary"
            :loading="createLoading"
            :disabled="loading"
            @click="handleCreateDirectory"
          >
            确定
          </el-button>
        </span>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, useTemplateRef, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { useHttpClient } from '@/http/client'
import type { DirInfo } from '@/typing'
import TreeNode from './TreeNode.vue'
import { SERVER_URL } from '@/const'

interface Props {
  modelValue?: DirInfo | null
  rootId?: string
  rootPath?: string
  sourceType: string
  accountId?: number
}

type DirectoryLoadState = 'unloaded' | 'loading' | 'loaded' | 'error'

interface TreeNodeData extends DirInfo {
  expanded: boolean
  loadState: DirectoryLoadState
  latestChildLoadId: number
  children: TreeNodeData[]
  isLeaf: boolean
}

interface LoadNodeOptions {
  force?: boolean
  notify?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: null,
  rootId: '',
  rootPath: '',
  accountId: 0,
})

const emit = defineEmits<{
  'update:modelValue': [value: DirInfo | null]
  cancel: []
  select: []
  reset: []
}>()

const http = useHttpClient()

const showCreateDialog = ref(false)
const createLoading = ref(false)
const createFormRef = useTemplateRef<FormInstance>('createFormRef')
const createForm = ref({ name: '' })
const createRules = ref<FormRules>({
  name: [
    { required: true, message: '请输入文件夹名称', trigger: 'blur' },
    { min: 1, max: 255, message: '文件夹名称长度在 1 到 255 个字符', trigger: 'blur' },
  ],
})

const treeData = ref<TreeNodeData[]>([])
const selectedDir = ref<DirInfo | null>(null)
const loading = ref(false)
let latestRootLoadId = 0

const rootPathLabel = computed(() => props.rootPath || '根目录')
const breadcrumbNodes = computed(() => {
  if (!selectedDir.value) return []
  return findNodeAncestors(treeData.value, selectedDir.value.id) || []
})
const createParent = computed<DirInfo | null>(() => {
  if (selectedDir.value) return selectedDir.value
  if (!props.rootId) return null

  return {
    id: props.rootId,
    name: rootPathLabel.value,
    path: props.rootPath,
  }
})

const createNode = (directory: DirInfo): TreeNodeData => ({
  ...directory,
  expanded: false,
  loadState: 'unloaded',
  latestChildLoadId: 0,
  children: [],
  isLeaf: false,
})

const toDirInfo = (node: TreeNodeData): DirInfo => ({
  id: node.id,
  name: node.name,
  path: node.path,
})

const requestDirectories = async (parentID: string, parentPath: string): Promise<DirInfo[]> => {
  const response = await http.get(`${SERVER_URL}/path/list`, {
    timeout: 60000,
    params: {
      parent_id: parentID,
      parent_path: parentPath,
      source_type: props.sourceType,
      account_id: props.accountId || 0,
    },
  })

  if (response?.data.code === 200) {
    return (response.data.data || []) as DirInfo[]
  }

  throw new Error(response?.data.message || '加载目录失败')
}

const loadNodeChildren = async (
  node: TreeNodeData,
  { force = false, notify = true }: LoadNodeOptions = {},
): Promise<boolean> => {
  if (node.loadState === 'loading' || (!force && node.loadState === 'loaded')) return true

  const childLoadId = ++node.latestChildLoadId
  node.loadState = 'loading'
  try {
    const directories = await requestDirectories(node.id, node.path)
    if (childLoadId !== node.latestChildLoadId) return false

    node.children = directories.map(createNode)
    node.isLeaf = node.children.length === 0
    node.loadState = 'loaded'
    return true
  } catch (error) {
    if (childLoadId !== node.latestChildLoadId) return false

    node.loadState = 'error'
    node.isLeaf = false
    if (notify) {
      ElMessage.error(error instanceof Error ? error.message : '加载子目录失败')
    }
    return false
  }
}

const findNode = (nodes: TreeNodeData[], id: string): TreeNodeData | null => {
  for (const node of nodes) {
    if (node.id === id) return node
    const child = findNode(node.children, id)
    if (child) return child
  }
  return null
}

const findNodeAncestors = (
  nodes: TreeNodeData[],
  id: string,
  ancestors: DirInfo[] = [],
): DirInfo[] | null => {
  for (const node of nodes) {
    const currentAncestors = [...ancestors, toDirInfo(node)]
    if (node.id === id) return currentAncestors
    const result = findNodeAncestors(node.children, id, currentAncestors)
    if (result) return result
  }
  return null
}

const restoreSelection = async (
  ancestors: DirInfo[],
  rootNodes: TreeNodeData[],
): Promise<TreeNodeData | null> => {
  let nodes = rootNodes
  let lastExisting: TreeNodeData | null = null

  for (const ancestor of ancestors) {
    const node = nodes.find(({ id }) => id === ancestor.id)
    if (!node) return lastExisting

    lastExisting = node
    const loaded = await loadNodeChildren(node, { notify: false })
    if (!loaded) {
      throw new Error('加载目录失败')
    }
    node.expanded = !node.isLeaf
    nodes = node.children
  }

  return lastExisting
}

const selectDirectory = (node: TreeNodeData) => {
  const directory = toDirInfo(node)
  selectedDir.value = directory
  emit('update:modelValue', directory)
}

const loadRootDirectories = async () => {
  const rootLoadId = ++latestRootLoadId
  loading.value = true
  try {
    const directories = await requestDirectories(props.rootId || '', props.rootPath || '')
    if (rootLoadId !== latestRootLoadId) return
    treeData.value = directories.map(createNode)
  } catch (error) {
    if (rootLoadId !== latestRootLoadId) return
    treeData.value = []
    ElMessage.error(error instanceof Error ? error.message : '加载目录失败')
  } finally {
    if (rootLoadId === latestRootLoadId) {
      loading.value = false
    }
  }
}

const refreshDirectories = async () => {
  if (loading.value || createLoading.value) return

  const ancestors = selectedDir.value
    ? findNodeAncestors(treeData.value, selectedDir.value.id)
    : null

  const rootLoadId = ++latestRootLoadId
  loading.value = true
  try {
    const directories = await requestDirectories(props.rootId || '', props.rootPath || '')
    if (rootLoadId !== latestRootLoadId) return
    const refreshedTree = directories.map(createNode)

    if (!ancestors?.length) {
      treeData.value = refreshedTree
      return
    }

    const restoredNode = await restoreSelection(ancestors, refreshedTree)
    if (rootLoadId !== latestRootLoadId) return
    treeData.value = refreshedTree

    if (restoredNode) {
      selectDirectory(restoredNode)
      return
    }

    selectedDir.value = null
    emit('update:modelValue', null)
    ElMessage.warning('所选目录已不存在，已回到根目录，请重新选择')
  } catch (error) {
    if (rootLoadId === latestRootLoadId) {
      ElMessage.error(error instanceof Error ? error.message : '加载目录失败')
    }
  } finally {
    if (rootLoadId === latestRootLoadId) {
      loading.value = false
    }
  }
}

const handleToggle = async (node: TreeNodeData) => {
  if (node.isLeaf) return

  if (node.expanded) {
    node.expanded = false
    return
  }

  node.expanded = true
  const loaded = await loadNodeChildren(node)
  if (loaded && node.isLeaf) {
    node.expanded = false
  }
}

const handleNodeSelect = (node: TreeNodeData) => {
  selectDirectory(node)
}

const handleBreadcrumbNavigate = (directory: DirInfo) => {
  const node = findNode(treeData.value, directory.id)
  if (!node) return

  node.expanded = !node.isLeaf
  selectDirectory(node)
}

const retryNode = async (node: TreeNodeData) => {
  await loadNodeChildren(node, { force: true })
}

const handleCancel = () => {
  resetState()
  emit('cancel')
}

const handleButtonSelect = () => {
  if (loading.value || !selectedDir.value) return

  emit('select')
  resetState()
}

const resetState = () => {
  selectedDir.value = null
  emit('update:modelValue', null)
  void loadRootDirectories()
  emit('reset')
}

watch(
  () => props.sourceType,
  () => {
    void loadRootDirectories()
  },
)

watch(
  () => props.accountId,
  () => {
    void loadRootDirectories()
  },
)

watch(
  () => props.rootPath,
  () => {
    void loadRootDirectories()
  },
)

watch(
  () => props.rootId,
  () => {
    void loadRootDirectories()
  },
)

watch(
  () => props.modelValue,
  (newValue) => {
    selectedDir.value = newValue
  },
  { immediate: true },
)

onMounted(() => {
  void loadRootDirectories()
})

const openCreateDialog = () => {
  if (!createParent.value || loading.value) return

  createForm.value.name = ''
  showCreateDialog.value = true
}

const handleCreateDirectory = async () => {
  const parent = createParent.value
  if (!createFormRef.value || !parent || loading.value || createLoading.value) return

  try {
    createLoading.value = true
    await createFormRef.value.validate()

    const response = await http.post(`${SERVER_URL}/path/create`, {
      parent_id: parent.id,
      parent_path: parent.path,
      name: createForm.value.name.trim(),
      source_type: props.sourceType,
      account_id: props.accountId,
    })

    if (response?.data.code !== 200) {
      ElMessage.error(response?.data.message || '创建文件夹失败')
      return
    }

    ElMessage.success('创建文件夹成功')
    showCreateDialog.value = false
    createForm.value.name = ''

    const newDirectory = response.data.data as DirInfo
    const parentNode = findNode(treeData.value, parent.id)
    if (parentNode) {
      parentNode.latestChildLoadId += 1
      parentNode.children.push(createNode(newDirectory))
      parentNode.isLeaf = false
      parentNode.loadState = 'loaded'
      parentNode.expanded = true
    } else if (parent.id === props.rootId) {
      latestRootLoadId += 1
      loading.value = false
      treeData.value.push(createNode(newDirectory))
    }

    selectedDir.value = newDirectory
    emit('update:modelValue', newDirectory)
  } catch {
    ElMessage.error('创建文件夹失败')
  } finally {
    createLoading.value = false
  }
}

defineExpose({
  refresh: refreshDirectories,
})
</script>

<style scoped>
.directory-selector {
  display: flex;
  flex-direction: column;
  height: min(100%, calc(100dvh - 32px));
  gap: 12px;
}

.selector-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.directory-breadcrumb {
  flex: 1 1 auto;
  min-width: 0;
  white-space: normal;
  overflow-wrap: anywhere;
}

.directory-breadcrumb :deep(.el-breadcrumb__item) {
  max-width: 100%;
}

.directory-breadcrumb :deep(.el-breadcrumb__inner) {
  max-width: 100%;
  white-space: normal;
  overflow-wrap: anywhere;
}

.breadcrumb-button {
  max-width: 100%;
  padding: 0;
  font: inherit;
  line-height: inherit;
  vertical-align: baseline;
  color: inherit;
  text-align: left;
  white-space: normal;
  overflow-wrap: anywhere;
  cursor: pointer;
  background: transparent;
  border: 0;
}

.breadcrumb-button:hover {
  color: var(--el-color-primary);
}

.breadcrumb-button:focus-visible {
  outline: 2px solid var(--el-color-primary);
  outline-offset: 1px;
}

.tree-loading-container {
  flex: 1;
  min-height: 180px;
  position: relative;
}

.tree-container {
  height: 100%;
  overflow-y: auto;
}

.empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 20px;
}

.footer-buttons {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 10px;
  padding-top: 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}

@media (max-width: 767px) {
  .selector-toolbar {
    align-items: flex-start;
  }

  .footer-buttons {
    justify-content: stretch;
  }

  .footer-buttons :deep(.el-button) {
    flex: 1 1 auto;
    min-width: 0;
  }
}
</style>
