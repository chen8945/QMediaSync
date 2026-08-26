import { nextTick, onActivated, onDeactivated } from 'vue'
import { usePageStateStore } from '@/stores/pageState'

interface UsePageScrollRestoreOptions {
  /** 页面状态存储键，与页面自身 `getPageState` 使用的键一致。 */
  pageKey: string
  /** 返回需要保存 / 恢复滚动位置的滚动容器。 */
  getScrollContainer: () => HTMLElement | null
}

/**
 * keep-alive 页面离开时保存滚动位置，重新激活时恢复到离开前的位置。
 *
 * 恢复在 `nextTick` 后执行，等待页面内容重新渲染出实际高度；保存读取
 * 容器当前的 `scrollTop`，容器不可用时按 0 处理。页面的数据加载、请求
 * 失效和状态清理仍由页面自身的激活 / 停用逻辑负责。
 */
export function usePageScrollRestore(options: UsePageScrollRestoreOptions) {
  const pageStateStore = usePageStateStore()

  onActivated(() => {
    nextTick(() => {
      const scrollContainer = options.getScrollContainer()
      if (scrollContainer) {
        scrollContainer.scrollTop = pageStateStore.getPageState(options.pageKey).scrollTop
      }
    })
  })

  onDeactivated(() => {
    const scrollContainer = options.getScrollContainer()
    pageStateStore.setScrollTop(options.pageKey, scrollContainer?.scrollTop || 0)
  })
}
