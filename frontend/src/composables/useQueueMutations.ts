import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { parseHttpError, type ParsedHttpError } from '@/http/errors'
import { notifyHttpError } from '@/utils/httpErrorNotification'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import type { QueueMutationContextSnapshot } from './useQueueMutationContext'

export type QueueSnapshotResult =
  { status: 'loaded' } | { status: 'stale' } | { status: 'failed'; error: ParsedHttpError }

export interface QueueMutationOperationOptions {
  execute: () => Promise<void>
  confirm?: {
    message: string
    title?: string
  }
  successMessage: string
  requestErrorMessage: string
}

export interface UseQueueMutationsOptions {
  reloadQueue: () => Promise<QueueSnapshotResult>
  reloadQueueStatus?: () => Promise<QueueSnapshotResult>
  isContextCurrent: (context: QueueMutationContextSnapshot) => boolean
  startContext: () => QueueMutationContextSnapshot
  finishContext: (context: QueueMutationContextSnapshot) => void
  onClearPendingSuccess?: () => void
}

const snapshotErrorMessage = '操作已成功，但刷新队列快照失败，请手动刷新。'

/**
 * 页面范围内的队列批量操作协调器。
 *
 * pending 从确认框打开前开始，直到 mutation 以及必要快照完成后才释放。
 * 该状态只属于当前页面实例，不是跨页面或服务端锁；context 仍负责屏蔽
 * 页面失活后的异步回调。
 */
export function useQueueMutations(options: UseQueueMutationsOptions) {
  const isQueueMutationPending = ref(false)
  // 加载函数仍返回错误类别；此阶段的提示统一由协调器负责。
  const snapshotContext = ref<QueueMutationContextSnapshot | null>(null)
  const isReloadingQueueSnapshot = computed(
    () => snapshotContext.value !== null && options.isContextCurrent(snapshotContext.value),
  )
  let operationToken = 0

  const reportSnapshotError = (error: ParsedHttpError) => {
    if (!error.shouldNotify) return
    const message =
      error.kind === 'unknown' || error.kind === 'application'
        ? snapshotErrorMessage
        : `${snapshotErrorMessage}${error.message}`
    console.error('刷新队列快照失败', error.diagnostics)
    ElMessage.error(message)
  }

  const run = async (operation: QueueMutationOperationOptions, clearPending = false) => {
    if (isQueueMutationPending.value) {
      return
    }

    const token = ++operationToken
    isQueueMutationPending.value = true
    const context = options.startContext()
    const isCurrent = () => token === operationToken && options.isContextCurrent(context)

    try {
      if (operation.confirm) {
        try {
          await ElMessageBox.confirm(operation.confirm.message, operation.confirm.title ?? '提示', {
            confirmButtonText: '确定',
            cancelButtonText: '取消',
            type: 'warning',
          })
        } catch (error) {
          if (isMessageBoxCancelError(error)) {
            return
          }
          throw error
        }
      }

      if (!isCurrent()) return

      // 领域 API 在返回前完成业务成功校验。
      await operation.execute()
      if (!isCurrent()) return

      if (clearPending) {
        options.onClearPendingSuccess?.()
      }
      ElMessage.success(operation.successMessage)

      snapshotContext.value = context
      try {
        const queueReloadResult = await options.reloadQueue()
        if (!isCurrent() || queueReloadResult.status === 'stale') return
        if (queueReloadResult.status === 'failed') {
          reportSnapshotError(queueReloadResult.error)
          return
        }
        if (clearPending && options.reloadQueueStatus) {
          const statusReloadResult = await options.reloadQueueStatus()
          if (!isCurrent() || statusReloadResult.status === 'stale') return
          if (statusReloadResult.status === 'failed') {
            reportSnapshotError(statusReloadResult.error)
          }
        }
      } catch (error) {
        if (isCurrent()) {
          reportSnapshotError(parseHttpError(error, { fallbackMessage: snapshotErrorMessage }))
        }
      }
    } catch (error) {
      if (!isCurrent() || isMessageBoxCancelError(error)) {
        return
      }
      notifyHttpError(error, operation.requestErrorMessage, {
        fallbackMessage: operation.requestErrorMessage,
        messagePrefix: operation.requestErrorMessage,
      })
    } finally {
      // 旧操作不能结束新 context，但失活或卸载后仍须释放当前页面的按钮锁。
      if (token === operationToken) {
        if (isCurrent()) {
          options.finishContext(context)
        }
        snapshotContext.value = null
        isQueueMutationPending.value = false
      }
    }
  }

  return {
    isQueueMutationPending,
    isReloadingQueueSnapshot,
    clearQueue: (operation: QueueMutationOperationOptions) => run(operation, true),
    runMutation: (operation: QueueMutationOperationOptions) => run(operation),
  }
}
