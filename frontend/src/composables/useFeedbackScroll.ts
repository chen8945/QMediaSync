import { nextTick, onScopeDispose, shallowRef, toValue, type MaybeRefOrGetter } from 'vue'

/** 仅由用户操作调用，确保新反馈出现在当前内容视口中。 */
export function useFeedbackScroll(status: MaybeRefOrGetter<unknown>) {
  const feedbackRef = shallowRef<HTMLElement | { $el: HTMLElement } | null>(null)
  let active = true
  let lastFeedback: unknown
  onScopeDispose(() => {
    active = false
  })

  const revealFeedback = async () => {
    const current = toValue(status)
    if (!active || !current || current === lastFeedback) return
    lastFeedback = current
    await nextTick()
    if (!active || toValue(status) !== current) return
    const target = feedbackRef.value
    const element = target instanceof HTMLElement ? target : target?.$el
    if (!element?.isConnected) return
    const bounds = element.getBoundingClientRect()
    const container = element.closest('.main-content')?.getBoundingClientRect()
    const top = Math.max(0, container?.top ?? 0)
    const bottom = Math.min(window.innerHeight, container?.bottom ?? window.innerHeight)
    if (bounds.top < top || bounds.bottom > bottom) {
      element.scrollIntoView({ block: 'nearest' })
    }
  }

  return { feedbackRef, revealFeedback }
}
