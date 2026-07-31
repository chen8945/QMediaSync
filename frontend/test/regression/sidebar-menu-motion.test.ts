// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import App from '@/App.vue'
import { httpKey } from '@/http/client'
import appRouter from '@/router'
import { extractMediaBlock, extractRule } from '../support/css'

const appVue = readFileSync(resolve(__dirname, '../../src/App.vue'), 'utf8')

// 与 test/router/menu-parent-openeds.test.ts 相同的挂载方式：复用真实路由的 path 和 meta，
// 但替换组件并去掉守卫。
const mountApp = async () => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: appRouter.getRoutes().map((route) => ({
      path: route.path,
      name: route.name,
      meta: route.meta,
      component: { template: '<div />' },
    })),
  })
  await router.push('/')
  await router.isReady()

  return mount(App, {
    global: {
      plugins: [createPinia(), router, ElementPlus],
      provide: {
        [httpKey]: {},
      },
      stubs: {
        RouterView: { template: '<div />' },
        ElDialog: { template: '<div />' },
      },
    },
  })
}

describe('侧边栏滚轮滚动期间关闭菜单命中测试', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('滚轮事件后进入滚动态，连续滚动期间保持，空闲后自动退出', async () => {
    const wrapper = await mountApp()
    const aside = wrapper.find('.el-aside')
    expect(aside.classes()).not.toContain('is-menu-scrolling')

    vi.useFakeTimers()
    await aside.trigger('wheel')
    expect(aside.classes()).toContain('is-menu-scrolling')

    // 空闲计时未到就再次滚动时必须重新计时，不能中途恢复命中测试
    vi.advanceTimersByTime(119)
    await aside.trigger('wheel')
    vi.advanceTimersByTime(119)
    await nextTick()
    expect(aside.classes()).toContain('is-menu-scrolling')

    vi.advanceTimersByTime(1)
    await nextTick()
    expect(aside.classes()).not.toContain('is-menu-scrolling')

    wrapper.unmount()
  })

  // passive 无法从挂载后的组件观测，只能锁源码：非 passive 的 wheel 监听会阻塞滚动。
  it('wheel 监听为 passive', () => {
    expect(appVue).toMatch(/@wheel\.passive=/)
  })

  it('滚动态下菜单关闭命中测试', () => {
    expect(extractRule(appVue, '.el-aside.is-menu-scrolling .el-menu-vertical')).toContain(
      'pointer-events: none',
    )
  })
})

// 以下动效只有真实浏览器才会计算，happy-dom 下无法观测，只能锁源码契约。
describe('侧边栏菜单动效源码契约', () => {
  it('子菜单展开与收起不逐帧动画 max-height', () => {
    const enter = extractRule(appVue, '.el-aside .el-collapse-transition-enter-active')
    expect(enter).toContain('transition: none')
    expect(enter).toMatch(/animation:\s*submenu-fade var\(--submenu-fade-in\)/)

    const leave = extractRule(appVue, '.el-aside .el-collapse-transition-leave-active')
    expect(leave).toMatch(/max-height 0s var\(--submenu-fade-out\)/)
    expect(leave).toMatch(/animation:\s*submenu-fade var\(--submenu-fade-out\)/)
  })

  it('reduced-motion 下同时关闭 animation 和 transition', () => {
    const reduced = extractMediaBlock(appVue, '@media (prefers-reduced-motion: reduce)')
    const collapse = extractRule(reduced, '.el-aside .el-collapse-transition-leave-active')
    expect(collapse).toContain('transition: none')
    expect(collapse).toContain('animation: none')
  })

  it('菜单容器保留合成层提升，规避 Chromium 图标绘制缺陷', () => {
    expect(appVue).toMatch(/\.el-menu-vertical\s*\{[^}]*will-change:\s*transform/)
  })
})
