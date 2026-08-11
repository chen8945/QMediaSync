import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import router from '@/router'
import { getIconComponent } from '@/components/common/iconRegistry'

describe('App 菜单图标结构', () => {
  it('菜单数据预解析图标组件，避免初始展开时动态函数调用导致图标延迟显示', () => {
    const source = readFileSync(resolve('src/App.vue'), 'utf8')

    expect(source).toContain('iconComponent')
    expect(source).toContain("from '@/components/common/iconRegistry'")
    expect(source).toContain(':is="menu.iconComponent"')
    expect(source).toContain(':is="child.iconComponent"')
    expect(source).not.toContain(':is="getMenuIcon(menu.meta.icon)"')
    expect(source).not.toContain(':is="getMenuIcon(child.meta.icon)"')
    expect(source).toContain(":aria-label=\"isMenuOpen ? '关闭菜单' : '打开菜单'\"")
    expect(source).toContain(':aria-expanded="isMenuOpen"')
    expect(source).toContain('aria-controls="mobile-navigation"')
    expect(source).toContain('aria-label="打开用户菜单"')
    expect(source).toContain('aria-haspopup="menu"')
    expect(source).toContain('<h1 class="page-title">')
    expect(source).toContain("typeof route.meta.title === 'string'")
    expect(source).not.toContain("getPageTitle(route.meta, '首页')")
    expect(source).toContain('flex: 0 0 auto;')
    expect(source).toContain('word-break: break-word;')
  })

  it('网盘账号菜单使用云服务图标，并与页面头部图标字段分别保留', () => {
    const accounts = router.resolve('/accounts')

    expect(accounts.meta.icon).toBe('Cloudy')
    expect(accounts.meta.page?.icon).toBe('Cloudy')
    expect(getIconComponent(accounts.meta.icon)).toBe(getIconComponent('Cloudy'))
  })
})
