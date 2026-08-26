import { ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { isMessageBoxCancelError } from '@/utils/messageBoxUtils'
import type { QueueMutationContextSnapshot } from './useQueueMutationContext'

export interface QueueMutationResponse {
  data?: {
    code?: number
    message?: string
  }
}

export interface QueueMutationOperationOptions {
  endpoint: string
  confirm?: {
    message: string
    title?: string
  }
  successMessage: string
  businessErrorMessage: (message: string | undefined) => string
  requestErrorMessage: string
}

export interface UseQueueMutationsOptions {
  post: (endpoint: string) => Promise<QueueMutationResponse>
  reloadQueue: () => Promise<boolean | void>
  reloadQueueStatus?: () => Promise<boolean | void>
  isContextCurrent: (context: QueueMutationContextSnapshot) => boolean
  startContext: () => QueueMutationContextSnapshot
  finishContext: (context: QueueMutationContextSnapshot) => void
  onClearPendingSuccess?: () => void
  onMutationError?: (error: unknown, message: string) => void
  onSnapshotError?: (message: string) => void
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
  let operationToken = 0

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
          if (error === 'cancel' || error === 'close' || isMessageBoxCancelError(error)) {
            return
          }
          throw error
        }
      }

      if (!isCurrent()) {
        return
      }

      let response: QueueMutationResponse
      try {
        response = await options.post(operation.endpoint)
      } catch (error) {
        if (isCurrent()) {
          options.onMutationError?.(error, operation.requestErrorMessage)
          ElMessage.error(operation.requestErrorMessage)
        }
        return
      }

      if (!isCurrent()) {
        return
      }

      if (response?.data?.code !== 200) {
        const message = operation.businessErrorMessage(response?.data?.message)
        options.onMutationError?.(response, message)
        ElMessage.error(message)
        return
      }

      if (clearPending) {
        options.onClearPendingSuccess?.()
      }
      ElMessage.success(operation.successMessage)

      try {
        const queueReloadResult = await options.reloadQueue()
        if (queueReloadResult === false) {
          throw new Error('queue snapshot reload failed')
        }
        if (clearPending && options.reloadQueueStatus) {
          const statusReloadResult = await options.reloadQueueStatus()
          if (statusReloadResult === false) {
            throw new Error('queue status snapshot reload failed')
          }
        }
      } catch {
        if (isCurrent()) {
          options.onSnapshotError?.(snapshotErrorMessage)
          ElMessage.error(snapshotErrorMessage)
        }
        return
      }

      if (!isCurrent()) {
        return
      }
    } catch (error) {
      if (error === 'cancel' || error === 'close' || isMessageBoxCancelError(error)) {
        return
      }
      options.onMutationError?.(error, operation.requestErrorMessage)
      ElMessage.error(operation.requestErrorMessage)
    } finally {
      // A stale operation may not finish a newer context, but it must not leave
      // this page's button lock stuck after deactivation or unmount.
      if (token === operationToken) {
        if (isCurrent()) {
          options.finishContext(context)
        }
        isQueueMutationPending.value = false
      }
    }
  }

  return {
    isQueueMutationPending,
    clearQueue: (operation: QueueMutationOperationOptions) => run(operation, true),
    runMutation: (operation: QueueMutationOperationOptions) => run(operation),
  }
}
