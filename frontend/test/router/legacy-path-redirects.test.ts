// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import router from '@/router'
import { useAuthStore } from '@/stores/auth'

const signIn = () => {
  const authStore = useAuthStore()
  authStore.login({
    user: { id: '1', username: 'tester' },
    csrfToken: 'csrf-token',
  })
  return authStore
}

const signOut = () => {
  const authStore = useAuthStore()
  authStore.clearAuth()
  return authStore
}

describe('旧路径重定向与未知路径兜底', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('旧路径 /proxy 重定向到 /settings/proxy 并更新页面标题', async () => {
    signIn()

    await router.push('/proxy')

    expect(router.currentRoute.value.name).toBe('settings-proxy')
    expect(router.currentRoute.value.path).toBe('/settings/proxy')
    expect(document.title).toBe('网络代理 - QMediaSync')
  })

  it('旧路径 /settings/database-repair 重定向到 /database/repair 并更新页面标题', async () => {
    signIn()

    await router.push('/settings/database-repair')

    expect(router.currentRoute.value.name).toBe('database-repair')
    expect(router.currentRoute.value.path).toBe('/database/repair')
    expect(document.title).toBe('数据库修复 - QMediaSync')
  })

  it('未知路径命中兜底路由，已登录时回到首页而不是空白页', async () => {
    signIn()

    await router.push('/settings/does-not-exist')

    expect(router.currentRoute.value.name).toBe('home')
    expect(router.currentRoute.value.matched.length).toBeGreaterThan(0)
    expect(document.title).toBe('首页 - QMediaSync')
  })

  it('未登录访问未知路径时鉴权守卫仍然生效，不会绕过跳转到登录页', async () => {
    signOut()

    await router.push('/proxy')
    expect(router.currentRoute.value.name).toBe('login')

    await router.push('/totally-unknown/deep/path')
    expect(router.currentRoute.value.name).toBe('login')
  })

  it('兜底路由能解析任意深层未知路径，不会出现无匹配记录', () => {
    const resolved = router.resolve('/unknown/deep/path')

    expect(resolved.matched.length).toBeGreaterThan(0)
  })
})
