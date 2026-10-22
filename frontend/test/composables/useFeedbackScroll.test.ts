import { enableAutoUnmount, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, shallowRef } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useFeedbackScroll } from '@/composables/useFeedbackScroll'

const mountFeedback = (componentTarget = false) => {
  const status = shallowRef<{ message: string } | null>(null)
  let feedback!: ReturnType<typeof useFeedbackScroll>
  const Alert = defineComponent({ setup: () => () => h('div', { class: 'feedback' }) })
  const wrapper = mount(
    defineComponent({
      setup() {
        feedback = useFeedbackScroll(() => status.value)
        return () =>
          h('main', { class: 'main-content' }, [
            h('button', '保存'),
            status.value
              ? componentTarget
                ? h(Alert, { class: 'feedback', ref: feedback.feedbackRef })
                : h('div', { class: 'feedback', ref: feedback.feedbackRef })
              : null,
          ])
      },
    }),
    { attachTo: document.body },
  )
  return { wrapper, status, getFeedback: () => feedback }
}

const mockBounds = (
  top: number,
  height = 60,
  containerTop = 0,
  containerBottom = window.innerHeight,
) =>
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
    this: HTMLElement,
  ) {
    return this.classList.contains('main-content')
      ? DOMRect.fromRect({ y: containerTop, width: 400, height: containerBottom - containerTop })
      : DOMRect.fromRect({ y: top, width: 400, height })
  })

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('设置页操作反馈滚动', () => {
  it('仅显式操作后等待新提示渲染，再将越界提示滚入视口，不改变焦点', async () => {
    const { wrapper, status, getFeedback } = mountFeedback(true)
    mockBounds(window.innerHeight + 20)
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    const button = wrapper.get('button').element
    button.focus()
    status.value = { message: '初次读取或后台状态' }
    await nextTick()
    expect(scroll).not.toHaveBeenCalled()
    status.value = { message: '保存成功' }
    await getFeedback().revealFeedback()
    expect(scroll).toHaveBeenCalledExactlyOnceWith({ block: 'nearest' })
    expect(scroll.mock.contexts[0]).toBe(wrapper.get('.feedback').element)
    expect(document.activeElement).toBe(button)
  })

  it.each([
    { top: 80, height: 60, containerTop: 40, containerBottom: 600, scrolls: false },
    { top: 20, height: 60, containerTop: 40, containerBottom: 600, scrolls: true },
    { top: 580, height: 60, containerTop: 40, containerBottom: 600, scrolls: true },
    { top: -10, height: 60, containerTop: -100, containerBottom: 600, scrolls: true },
    {
      top: window.innerHeight - 20,
      height: 60,
      containerTop: 0,
      containerBottom: window.innerHeight + 100,
      scrolls: true,
    },
  ])('只在窗口与实际滚动容器的交集之外滚动 %j', async (bounds) => {
    const { status, getFeedback } = mountFeedback()
    mockBounds(bounds.top, bounds.height, bounds.containerTop, bounds.containerBottom)
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    status.value = { message: '保存结果' }
    await getFeedback().revealFeedback()
    expect(scroll).toHaveBeenCalledTimes(bounds.scrolls ? 1 : 0)
  })

  it('同一条提示只处理一次，新结果出现后才再次处理', async () => {
    const { status, getFeedback } = mountFeedback()
    mockBounds(window.innerHeight + 20)
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    status.value = { message: '保存成功' }
    await getFeedback().revealFeedback()
    await getFeedback().revealFeedback()
    expect(scroll).toHaveBeenCalledTimes(1)
    status.value = { message: '保存后刷新失败' }
    await getFeedback().revealFeedback()
    expect(scroll).toHaveBeenCalledTimes(2)
  })

  it.each(['close', 'replace', 'unmount'])('渲染前 %s 时不滚动旧提示', async (action) => {
    const { wrapper, status, getFeedback } = mountFeedback()
    mockBounds(window.innerHeight + 20)
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => {})
    status.value = { message: '保存结果' }
    const pending = getFeedback().revealFeedback()
    if (action === 'close') status.value = null
    if (action === 'replace') status.value = { message: '其他提示' }
    if (action === 'unmount') wrapper.unmount()
    await pending
    await getFeedback().revealFeedback()
    expect(scroll).toHaveBeenCalledTimes(action === 'replace' ? 1 : 0)
  })
})
