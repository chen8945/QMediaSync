import { describe, expect, it } from 'vitest'

import router from '@/router'
import { getPageDescription, getPageIcon, getPageTitle, getPageVariant } from '@/router/pageMeta'

describe('页面展示元信息', () => {
  it('优先使用 page.title，空标题回退到 meta.title', () => {
    expect(getPageTitle({ title: '菜单标题', page: { title: '页面标题' } })).toBe('页面标题')
    expect(getPageTitle({ title: '菜单标题', page: { title: '   ' } })).toBe('菜单标题')
    expect(getPageTitle({ title: '菜单标题' })).toBe('菜单标题')
  })

  it('只从 page 元信息读取说明、图标和变体', () => {
    expect(getPageDescription({ title: '页面', page: { description: '说明', icon: 'User' } })).toBe(
      '说明',
    )
    expect(getPageIcon({ title: '页面', page: { icon: 'Film' } })).toBe('Film')
    expect(getPageVariant({ title: '页面' })).toBe('settings')
  })

  it('允许菜单标题、浏览器标题和页面展示标题分层', () => {
    const accounts = router.resolve('/accounts')

    expect(accounts.meta.title).toBe('网盘账号')
    expect(accounts.meta.page?.title).toBe('网盘账号管理')
    expect(accounts.meta.icon).toBe('Cloudy')
    expect(accounts.meta.page?.icon).toBe('Cloudy')
    expect(getPageTitle(accounts.meta)).toBe('网盘账号管理')
  })

  it('飞牛影视反代作为受保护的设置菜单独立接入', () => {
    const fnos = router.resolve('/settings/fnos-proxy')
    expect(fnos.name).toBe('settings-fnos-proxy')
    expect(fnos.meta).toMatchObject({
      title: '飞牛影视',
      requiresAuth: true,
      parent: 'settings',
      showInMenu: true,
    })
    expect(fnos.meta.page?.variant).toBe('settings')
    expect(router.resolve('/settings/emby').name).toBe('settings-emby')
  })
})
