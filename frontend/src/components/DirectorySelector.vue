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
      <div class="selector-toolbar-actions">
        <BrowseSortControl
          v-if="sortReady && sortCapabilities"
          :options="sortCapabilities"
          :model-value="sortSelection"
          :disabled="createLoading"
          @change="handleSortChange"
        />
        <el-button
          plain
          size="small"
          :icon="Refresh"
          :loading="loading"
          :disabled="createLoading"
          aria-label="刷新目录"
          @click="refreshDirectories"
        >
          刷新
        </el-button>
      </div>
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
import { computed, onBeforeUnmount, ref, useTemplateRef, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { useHttpClient } from '@/http/client'
import type { DirInfo } from '@/typing'
import TreeNode from './TreeNode.vue'
import {
  createDirectory,
  fetchDirectories,
  filePublicMessages,
  type BrowseSortValue,
} from '@/api/files'
import { browseSortQuery, sortLocalDirectories, useBrowseSort } from '@/composables/useBrowseSort'
import BrowseSortControl from './BrowseSortControl.vue'
import { notifyHttpError } from '@/utils/httpErrorNotification'

interface Props {
  modelValue?: DirInfo | null
  rootId?: string
  rootPath?: string
  sourceType: string
  accountId?: number
  resetOnSelect?: boolean
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
  refresh?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  modelValue: null,
  rootId: '',
  rootPath: '',
  accountId: 0,
  resetOnSelect: true,
})

const emit = defineEmits<{
  'update:modelValue': [value: DirInfo | null]
  cancel: []
  select: []
  reset: []
}>()

const http = useHttpClient()
const {
  capabilities: sortCapabilities,
  selection: sortSelection,
  ready: sortReady,
  contextKey: sortContextKey,
  prepare: prepareSort,
  choose: chooseSort,
  commit: commitSort,
  rollback: rollbackSort,
} = useBrowseSort(
  http,
  () => props.sourceType,
  () => props.accountId,
  'directories',
)
const requests = new Set<AbortController>()
let disposed = false

function invalidateRequests() {
  latestRootLoadId += 1
  for (const request of requests) request.abort()
  requests.clear()
  const invalidateChildren = (nodes: TreeNodeData[]) => {
    for (const node of nodes) {
      node.latestChildLoadId += 1
      if (node.loadState === 'loading') node.loadState = 'unloaded'
      invalidateChildren(node.children)
    }
  }
  invalidateChildren(treeData.value)
}

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

const requestDirectories = async (
  parentID: string,
  parentPath: string,
  refresh = false,
): Promise<DirInfo[]> => {
  const sourceType = props.sourceType
  const sort = { ...sortSelection.value }
  const controller = new AbortController()
  requests.add(controller)
  try {
    const directories =
      (await fetchDirectories(
        http,
        {
          parent_id: parentID,
          parent_path: parentPath,
          source_type: sourceType,
          account_id: props.accountId || 0,
          ...browseSortQuery(sort),
          refresh: refresh ? 1 : 0,
        },
        controller.signal,
      )) || []
    return sourceType === 'local' ? sortLocalDirectories(directories, sort) : directories
  } finally {
    requests.delete(controller)
  }
}

const reportDirectoryError = (error: unknown, fallbackMessage: string, includeContext = false) => {
  notifyHttpError(error, fallbackMessage, {
    fallbackMessage,
    publicMessages: filePublicMessages,
    messagePrefix: includeContext ? fallbackMessage : undefined,
  })
}

const loadNodeChildren = async (
  node: TreeNodeData,
  { force = false, notify = true, refresh = false }: LoadNodeOptions = {},
): Promise<boolean> => {
  if (!force && node.loadState === 'loaded') return true
  if (!force && node.loadState === 'loading') return false
  const rootLoadId = latestRootLoadId

  const childLoadId = ++node.latestChildLoadId
  node.loadState = 'loading'
  try {
    const directories = await requestDirectories(node.id, node.path, refresh)
    if (disposed || rootLoadId !== latestRootLoadId || childLoadId !== node.latestChildLoadId)
      return false

    node.children = directories.map(createNode)
    node.isLeaf = node.children.length === 0
    node.loadState = 'loaded'
    return true
  } catch (error) {
    if (disposed || rootLoadId !== latestRootLoadId || childLoadId !== node.latestChildLoadId)
      return false

    node.loadState = 'error'
    node.isLeaf = false
    if (notify) {
      reportDirectoryError(error, '加载子目录失败')
    } else {
      throw error
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
  refresh: boolean,
): Promise<TreeNodeData | null> => {
  let nodes = rootNodes
  let lastExisting: TreeNodeData | null = null

  for (const ancestor of ancestors) {
    const node = nodes.find(({ id }) => id === ancestor.id)
    if (!node) return lastExisting

    lastExisting = node
    const loaded = await loadNodeChildren(node, { notify: false, refresh })
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

const reloadDirectories = async (preserveSelection = false, refresh = false): Promise<boolean> => {
  const ancestors =
    preserveSelection && selectedDir.value
      ? findNodeAncestors(treeData.value, selectedDir.value.id)
      : null
  invalidateRequests()
  const rootLoadId = latestRootLoadId
  loading.value = true
  try {
    if (!(await prepareSort()) || disposed || rootLoadId !== latestRootLoadId) return false
    const requestedSort = { ...sortSelection.value }
    const directories = await requestDirectories(props.rootId || '', props.rootPath || '', refresh)
    if (disposed || rootLoadId !== latestRootLoadId) return false
    const refreshedTree = directories.map(createNode)
    const restoredNode = ancestors?.length
      ? await restoreSelection(ancestors, refreshedTree, refresh)
      : null
    if (disposed || rootLoadId !== latestRootLoadId) return false
    treeData.value = refreshedTree
    commitSort(requestedSort)
    if (ancestors?.length) {
      if (restoredNode) {
        selectDirectory(restoredNode)
      } else {
        selectedDir.value = null
        emit('update:modelValue', null)
        ElMessage.warning('所选目录已不存在，已回到根目录，请重新选择')
      }
    }
    return true
  } catch (error) {
    if (!disposed && rootLoadId === latestRootLoadId) {
      rollbackSort()
      reportDirectoryError(error, '加载目录失败')
    }
    return false
  } finally {
    if (rootLoadId === latestRootLoadId) loading.value = false
  }
}

const loadRootDirectories = () => reloadDirectories()
const refreshDirectories = () => {
  if (loading.value || createLoading.value) return
  return reloadDirectories(true, true)
}
const handleSortChange = (value: BrowseSortValue) => {
  chooseSort(value)
  void reloadDirectories(true)
}

const handleToggle = async (node: TreeNodeData) => {
  if (loading.value || node.isLeaf) return

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
  if (loading.value) return
  selectDirectory(node)
}

const handleBreadcrumbNavigate = (directory: DirInfo) => {
  if (loading.value) return
  const node = findNode(treeData.value, directory.id)
  if (!node) return

  node.expanded = !node.isLeaf
  selectDirectory(node)
}

const retryNode = async (node: TreeNodeData) => {
  if (loading.value) return
  await loadNodeChildren(node, { force: true })
}

const handleCancel = () => {
  resetState()
  emit('cancel')
}

const handleButtonSelect = () => {
  if (loading.value || !selectedDir.value) return

  emit('select')
  if (props.resetOnSelect) resetState()
}

const resetState = () => {
  selectedDir.value = null
  emit('update:modelValue', null)
  void loadRootDirectories()
  emit('reset')
}

watch(
  () => [sortContextKey.value, props.rootPath, props.rootId],
  (_value, previous) => {
    treeData.value = []
    if (previous) {
      selectedDir.value = null
      emit('update:modelValue', null)
      showCreateDialog.value = false
      createLoading.value = false
    }
    void loadRootDirectories()
  },
  { immediate: true },
)

watch(
  () => props.modelValue,
  (newValue) => {
    selectedDir.value = newValue
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  disposed = true
  invalidateRequests()
})

const openCreateDialog = () => {
  if (!createParent.value || loading.value) return

  createForm.value.name = ''
  showCreateDialog.value = true
}

const handleCreateDirectory = async () => {
  const parent = createParent.value
  if (!createFormRef.value || !parent || loading.value || createLoading.value) return

  const contextId = latestRootLoadId
  try {
    createLoading.value = true
    await createFormRef.value.validate()
    if (disposed || contextId !== latestRootLoadId) return

    const newDirectory = await createDirectory(http, {
      parent_id: parent.id,
      parent_path: parent.path,
      name: createForm.value.name.trim(),
      source_type: props.sourceType,
      account_id: props.accountId,
    })

    if (disposed || contextId !== latestRootLoadId) return
    ElMessage.success('创建文件夹成功')
    showCreateDialog.value = false
    createForm.value.name = ''

    // 创建成功后重新读取父目录，让新目录遵守当前排序，并废弃创建前的子目录请求。
    try {
      const parentNode = findNode(treeData.value, parent.id)
      if (parentNode) {
        const loaded = await loadNodeChildren(parentNode, {
          force: true,
          notify: false,
          refresh: true,
        })
        if (!loaded) return
        parentNode.expanded = !parentNode.isLeaf
      } else if (parent.id === props.rootId) {
        const directories = await requestDirectories(props.rootId, props.rootPath, true)
        if (disposed || contextId !== latestRootLoadId) return
        treeData.value = directories.map(createNode)
      }
      const createdNode = findNode(treeData.value, newDirectory.id)
      if (createdNode) selectDirectory(createdNode)
    } catch (error) {
      if (!disposed && contextId === latestRootLoadId) {
        reportDirectoryError(error, '文件夹已创建，但刷新目录失败，请点击刷新重试', true)
      }
    }
  } catch (error) {
    if (!disposed && contextId === latestRootLoadId) reportDirectoryError(error, '创建文件夹失败')
  } finally {
    if (contextId === latestRootLoadId) createLoading.value = false
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
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
}

.selector-toolbar-actions {
  display: flex;
  align-items: flex-start;
  max-width: 100%;
  gap: 8px;
}

.selector-toolbar-actions > .el-button {
  flex-shrink: 0;
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

@media (max-width: 768px) {
  .directory-breadcrumb {
    flex-basis: 100%;
  }

  .selector-toolbar-actions {
    width: 100%;
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
