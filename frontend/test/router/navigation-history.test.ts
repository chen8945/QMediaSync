// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'

import router from '@/router'
import { hasAppBackHistory, navigateBackOrReplace } from '../../src/utils/navigation'

const readSource = (path: string) => readFileSync(resolve(path), 'utf-8')

describe('router history behavior', () => {
  it('导航完成后再更新页面标题，避免浏览器历史标题错位', () => {
    const source = readSource('src/router/index.ts')
    const beforeEachStart = source.indexOf('router.beforeEach')
    const afterEachStart = source.indexOf('router.afterEach')
    const beforeEachBlock =
      afterEachStart === -1
        ? source.slice(beforeEachStart)
        : source.slice(beforeEachStart, afterEachStart)

    expect(afterEachStart).toBeGreaterThan(beforeEachStart)
    expect(beforeEachBlock).not.toContain('document.title')
    expect(source.slice(afterEachStart)).toContain('document.title')
  })

  it('登录态跳转使用 replace，不把登录页和失效页留在历史栈里', () => {
    const mainSource = readSource('src/main.ts')
    const loginSource = readSource('src/views/auth/AppLogin.vue')
    const routerSource = readSource('src/router/index.ts')
    const appSource = readSource('src/App.vue')

    expect(mainSource).not.toContain("router.push('/login')")
    expect(mainSource).toContain("router.replace('/login')")
    expect(loginSource).not.toContain("router.push(redirect || '/')")
    expect(loginSource).not.toContain("router.push('/')")
    expect(loginSource).toContain("router.replace(redirect || '/')")
    expect(loginSource).toContain("router.replace('/')")
    expect(routerSource).toContain('replace: true')
    expect(appSource).toContain("router.replace('/login')")
  })

  it('有应用内上一页时回退，没有上一页时替换到兜底路由', async () => {
    const router = {
      back: vi.fn(),
      replace: vi.fn().mockResolvedValue(undefined),
    }
    const fallback = { name: 'sync-records' }

    expect(hasAppBackHistory({ back: '/sync-records' })).toBe(true)
    expect(hasAppBackHistory({ back: null })).toBe(false)
    expect(hasAppBackHistory({})).toBe(false)

    await navigateBackOrReplace(router, fallback, {
      historyState: { back: '/sync-records' },
    })
    expect(router.back).toHaveBeenCalledTimes(1)
    expect(router.replace).not.toHaveBeenCalled()

    router.back.mockClear()

    await navigateBackOrReplace(router, fallback, {
      historyState: { back: null },
    })
    expect(router.back).not.toHaveBeenCalled()
    expect(router.replace).toHaveBeenCalledWith(fallback)
  })

  it('详情和表单页取消返回使用回退兜底，保存成功使用 replace 回列表', () => {
    const syncTaskDetailSource = readSource('src/views/sync/AppSyncTaskDetail.vue')
    const syncDirectoryFormSource = readSource('src/views/sync/AppSyncDirectoryForm.vue')
    const scrapePathFormSource = readSource('src/views/scrape/AppScrapePathForm.vue')

    expect(syncTaskDetailSource).toContain('navigateBackOrReplace')
    expect(syncTaskDetailSource).not.toContain("router.push({ name: 'sync-records' })")
    expect(syncDirectoryFormSource).toContain('navigateBackOrReplace')
    expect(syncDirectoryFormSource).toContain("router.replace({ name: 'sync-directories' })")
    expect(scrapePathFormSource).toContain('navigateBackOrReplace')
    expect(scrapePathFormSource).toContain("router.replace({ name: 'scrape-pathes' })")
  })

  it('业务路由注册同步路由壳，导航不等待动态导入', () => {
    const pageRoutes = router.getRoutes().filter((route) => route.components?.default)

    expect(pageRoutes.length).toBeGreaterThan(0)
    for (const route of pageRoutes) {
      expect(typeof route.components?.default, String(route.name)).toBe('object')
    }
  })
})
