// @vitest-environment happy-dom
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, nextTick, type Component } from 'vue'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAsyncRouteComponent } from '@/router/asyncRoute'

type Deferred<T> = {
  promise: Promise<T>
  resolve: (value: T) => void
}

const createDeferred = <T>(): Deferred<T> => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })

  return { promise, resolve }
}

const createHarness = (component: Component, title: string) => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div>首页</div>' } },
      { path: '/target', component, meta: { title, requiresAuth: false } },
    ],
  })
  const wrapper = mount(
    defineComponent({ components: { RouterView }, template: '<RouterView />' }),
    {
      global: { plugins: [router] },
    },
  )

  return { router, wrapper }
}

describe('createAsyncRouteComponent', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('在页面模块未完成时立即导航并显示极简加载态', async () => {
    const deferred = createDeferred<Component>()
    const loader = vi.fn(() => deferred.promise)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const { router, wrapper } = createHarness(
      createAsyncRouteComponent('SlowRoutePage', loader),
      '慢速页面',
    )

    await router.push('/')
    await router.isReady()
    await router.push('/target')
    await nextTick()

    expect(router.currentRoute.value.path).toBe('/target')
    expect(loader).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-testid="route-loading"]').text()).toContain('慢速页面')
    expect(wrapper.get('[data-testid="route-loading"]').text()).toContain('加载中…')
    expect(warn.mock.calls.flat().join(' ')).not.toContain('VUE_ROUTER_R0029')

    deferred.resolve(defineComponent({ template: '<div data-testid="loaded-page">已加载</div>' }))
    await flushPromises()

    expect(wrapper.get('[data-testid="loaded-page"]').text()).toBe('已加载')
    wrapper.unmount()
  })

  it('在页面模块加载失败时显示重新加载操作', async () => {
    const { router, wrapper } = createHarness(
      createAsyncRouteComponent('FailedRoutePage', () =>
        Promise.reject(new Error('chunk unavailable')),
      ),
      '失败页面',
    )
    vi.spyOn(console, 'error').mockImplementation(() => undefined)

    await router.push('/')
    await router.isReady()
    await router.push('/target')
    await flushPromises()

    expect(wrapper.get('[data-testid="route-load-error"]').text()).toContain('失败页面')
    expect(wrapper.get('button').text()).toBe('重新加载')
    wrapper.unmount()
  })
})
