// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { describe, expect, it } from 'vitest'

import App from '@/App.vue'
import { httpKey } from '@/http/client'
import appRouter from '@/router'

// 复用真实路由的 path 和 meta，但替换组件并去掉守卫，只验证菜单展开映射。
const createMenuHarness = async (initialPath: string) => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: appRouter.getRoutes().map((route) => ({
      path: route.path,
      name: route.name,
      meta: route.meta,
      component: { template: '<div />' },
    })),
  })
  await router.push(initialPath)
  await router.isReady()

  const wrapper = mount(App, {
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

  return { router, wrapper }
}

const findSubMenu = (wrapper: VueWrapper, title: string) =>
  wrapper
    .findAll('.el-sub-menu')
    .find((subMenu) => subMenu.find('.el-sub-menu__title').text().includes(title))

const openedSubMenuTitles = (wrapper: VueWrapper) =>
  wrapper
    .findAll('.el-sub-menu.is-opened')
    .map((subMenu) => subMenu.find('.el-sub-menu__title').text().trim())

describe('侧边栏子菜单按 meta.parent 展开', () => {
  // 这四个路由的路径前缀与所属一级菜单不一致，是路径前缀判断会出错的场景。
  it.each([
    ['/settings/strm', 'STRM 同步'],
    ['/settings/tmdb', '刮削 & 整理'],
    ['/settings/ai', '刮削 & 整理'],
    ['/settings/category-strategy', '刮削 & 整理'],
  ])('首屏进入 %s 时展开「%s」而不是路径前缀对应的菜单', async (path, expectedTitle) => {
    const { wrapper } = await createMenuHarness(path)

    expect(openedSubMenuTitles(wrapper)).toEqual([expectedTitle])
    expect(findSubMenu(wrapper, expectedTitle)?.classes()).toContain('is-opened')
    expect(findSubMenu(wrapper, '系统设置')?.classes()).not.toContain('is-opened')

    wrapper.unmount()
  })

  it.each([
    ['/settings/proxy', '系统设置'],
    ['/database/repair', '备份管理'],
    ['/upload-queue', '上传下载'],
    ['/sync-records', 'STRM 同步'],
  ])('首屏进入 %s 时展开「%s」', async (path, expectedTitle) => {
    const { wrapper } = await createMenuHarness(path)

    expect(openedSubMenuTitles(wrapper)).toEqual([expectedTitle])

    wrapper.unmount()
  })

  it('隐藏在菜单外的详情页仍展开所属一级菜单', async () => {
    const { wrapper } = await createMenuHarness('/sync-records/12')

    expect(openedSubMenuTitles(wrapper)).toEqual(['STRM 同步'])

    wrapper.unmount()
  })

  it('没有 meta.parent 的一级页面不展开任何子菜单', async () => {
    const { wrapper } = await createMenuHarness('/accounts')

    expect(openedSubMenuTitles(wrapper)).toEqual([])

    wrapper.unmount()
  })

  // el-menu 只在初始化时读取一次 default-openeds，SPA 内跳转必须由菜单实例的 open() 兜底。
  it('SPA 内跳转到其他一级菜单下的页面时展开目标子菜单', async () => {
    const { router, wrapper } = await createMenuHarness('/settings/tmdb')

    expect(openedSubMenuTitles(wrapper)).toEqual(['刮削 & 整理'])

    await router.push('/settings/user')
    await flushPromises()

    expect(findSubMenu(wrapper, '系统设置')?.classes()).toContain('is-opened')

    await router.push('/database/backup/records')
    await flushPromises()

    expect(findSubMenu(wrapper, '备份管理')?.classes()).toContain('is-opened')

    wrapper.unmount()
  })

  // 菜单外的详情 / 表单页不是 el-menu-item，Element Plus 无法从 default-active 推出应展开的子菜单，
  // 且 default-openeds 只在初始化时读取一次，因此必须由菜单实例的 open() 兜底。
  it('SPA 内从无子菜单页面跳转到菜单外的详情页时展开所属一级菜单', async () => {
    const { router, wrapper } = await createMenuHarness('/accounts')

    expect(openedSubMenuTitles(wrapper)).toEqual([])

    await router.push('/sync-records/12')
    await flushPromises()

    expect(findSubMenu(wrapper, 'STRM 同步')?.classes()).toContain('is-opened')

    await router.push('/scrape-path/add')
    await flushPromises()

    expect(findSubMenu(wrapper, '刮削 & 整理')?.classes()).toContain('is-opened')

    wrapper.unmount()
  })

  it('SPA 内跳转到 /settings 前缀但归属刮削的页面时展开刮削菜单', async () => {
    const { router, wrapper } = await createMenuHarness('/')

    expect(openedSubMenuTitles(wrapper)).toEqual([])

    await router.push('/settings/category-strategy')
    await flushPromises()

    expect(findSubMenu(wrapper, '刮削 & 整理')?.classes()).toContain('is-opened')
    expect(findSubMenu(wrapper, '系统设置')?.classes()).not.toContain('is-opened')

    wrapper.unmount()
  })
})
