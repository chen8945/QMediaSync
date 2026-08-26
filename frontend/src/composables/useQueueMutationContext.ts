import { ref } from 'vue'

export interface QueueMutationContextSnapshot {
  contextVersion: number
}

interface UseQueueMutationContextOptions {
  /** 返回页面当前是否处于激活状态；失活后上下文一律视为过期。 */
  isPageActive: () => boolean
}

/**
 * 队列清空、重试等变更操作的失效上下文。
 *
 * 变更开始时 `startQueueMutationContext` 使之前的上下文失效并取得新快照；
 * 操作完成回写快照前用 `isQueueMutationContextCurrent` 确认期间没有发生
 * 页面失活、路由切换或其他变更，避免旧响应覆盖新状态。
 */
export function useQueueMutationContext(options: UseQueueMutationContextOptions) {
  const queueMutationContextVersion = ref(0)
  const activeQueueMutationContext = ref<QueueMutationContextSnapshot | null>(null)

  const invalidateQueueMutationContext = () => {
    queueMutationContextVersion.value += 1
    activeQueueMutationContext.value = null
  }

  const startQueueMutationContext = (): QueueMutationContextSnapshot => {
    invalidateQueueMutationContext()
    const snapshot = {
      contextVersion: queueMutationContextVersion.value,
    }
    activeQueueMutationContext.value = snapshot
    return snapshot
  }

  const isQueueMutationContextCurrent = (snapshot: QueueMutationContextSnapshot | null) => {
    return (
      options.isPageActive() &&
      !!snapshot &&
      !!activeQueueMutationContext.value &&
      activeQueueMutationContext.value.contextVersion === snapshot.contextVersion &&
      snapshot.contextVersion === queueMutationContextVersion.value
    )
  }

  const finishQueueMutationContext = (snapshot: QueueMutationContextSnapshot) => {
    if (isQueueMutationContextCurrent(snapshot)) {
      activeQueueMutationContext.value = null
    }
  }

  return {
    invalidateQueueMutationContext,
    startQueueMutationContext,
    isQueueMutationContextCurrent,
    finishQueueMutationContext,
  }
}
