// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createMemoryHistory, createRouter, type RouteMeta } from 'vue-router'
import { describe, expect, it } from 'vitest'

import PageHeader from '@/components/common/PageHeader.vue'
import { extractMediaBlock, extractRule } from '../../support/css'

const mountPageHeader = async (meta: Partial<RouteMeta> = {}) => {
  const normalizedMeta: RouteMeta = {
    title: '页面',
    requiresAuth: false,
    ...meta,
  }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' }, meta: normalizedMeta }],
  })
  await router.push('/')
  await router.isReady()

  return mount(PageHeader, {
    global: { plugins: [router, ElementPlus] },
  })
}

describe('PageHeader', () => {
  it('移动端默认隐藏共享 identity，并保留带 aside 头部的 actions/stats', () => {
    const source = readFileSync(resolve('src/components/common/PageHeader.vue'), 'utf8')
    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')

    expect(source).not.toContain('home-page-header')
    expect(mobileStyles).not.toContain('home-page-header')
    expect(
      extractRule(mobileStyles, '.qms-page-header:not(.qms-page-header--with-aside)'),
    ).toContain('display: none;')
    expect(extractRule(mobileStyles, '.qms-page-header__identity')).toContain('display: none;')
    expect(extractRule(mobileStyles, '.qms-page-header__actions')).toContain('width: 100%;')
    expect(extractRule(mobileStyles, '.qms-page-header__stats')).toContain('width: 100%;')
    expect(extractRule(mobileStyles, '.qms-page-header__stats')).toContain('margin-top: 0;')
    expect(
      extractRule(
        mobileStyles,
        '.qms-page-header:not(.qms-page-header--with-stats) .qms-page-header__main',
      ),
    ).toContain('display: none;')
  })

  it('允许指定页面在移动端保留 identity', () => {
    const source = readFileSync(resolve('src/components/common/PageHeader.vue'), 'utf8')
    const mobileStyles = extractMediaBlock(source, '@media (max-width: 768px)')

    expect(source).toContain('showIdentityOnMobile?: boolean')
    expect(source).toContain('qms-page-header--show-identity-mobile')
    expect(mobileStyles).toMatch(
      /qms-page-header\.qms-page-header--show-identity-mobile[^}]*qms-page-header__main[\s\S]*?display:\s*block;/,
    )
    expect(mobileStyles).toMatch(
      /qms-page-header\.qms-page-header--show-identity-mobile[^}]*qms-page-header__identity[\s\S]*?display:\s*flex;/,
    )
  })

  it('使用页面展示元信息并保留单一 h1', async () => {
    const wrapper = await mountPageHeader({
      title: '网盘账号',
      requiresAuth: false,
      page: {
        title: '网盘账号管理',
        description: '管理网盘账号授权与绑定',
        icon: 'User',
        variant: 'management',
      },
    })

    expect(wrapper.findAll('h1')).toHaveLength(1)
    expect(wrapper.get('h1').text()).toBe('网盘账号管理')
    expect(wrapper.text()).toContain('管理网盘账号授权与绑定')
    expect(wrapper.get('[aria-hidden="true"]')).toBeTruthy()
    expect(wrapper.classes()).toContain('qms-page-header--management')
    expect(wrapper.classes()).toContain('qms-page-header--actions-end')
  })

  it('没有 page.title 时回退到菜单标题，并渲染页面插槽', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        {
          path: '/',
          component: { template: '<div />' },
          meta: { title: '备份设置', requiresAuth: false },
        },
      ],
    })
    await router.push('/')
    await router.isReady()

    const wrapper = mount(PageHeader, {
      global: { plugins: [router, ElementPlus] },
      slots: {
        actions: '<button type="button">保存设置</button>',
        stats: '<span>统计</span>',
      },
    })

    expect(wrapper.get('h1').text()).toBe('备份设置')
    expect(wrapper.text()).toContain('保存设置')
    expect(wrapper.text()).toContain('统计')
    expect(wrapper.classes()).toContain('qms-page-header--with-aside')
    expect(wrapper.classes()).toContain('qms-page-header--with-actions')
    expect(wrapper.find('.qms-page-header > .qms-page-header__stats').exists()).toBe(true)
    expect(wrapper.find('.qms-page-header__main > .qms-page-header__stats').exists()).toBe(false)
    expect(wrapper.find('.qms-page-header__stats').exists()).toBe(true)
  })

  it('可以把返回类操作固定在标题左侧', async () => {
    const wrapper = await mountPageHeader({
      title: '添加同步目录',
      page: { title: '添加同步目录', variant: 'detail' },
    })

    await wrapper.setProps({ actionsPosition: 'start' })
    expect(wrapper.classes()).toContain('qms-page-header--actions-start')
  })

  it('支持将 actions 堆叠到标题身份上方', async () => {
    const source = readFileSync(resolve('src/components/common/PageHeader.vue'), 'utf8')
    const wrapper = await mountPageHeader()

    await wrapper.setProps({
      actionsLayout: 'stacked',
      actionsPosition: 'start',
      showIdentityOnMobile: true,
    })

    expect(wrapper.classes()).toContain('qms-page-header--actions-layout-stacked')
    expect(source).toContain("actionsLayout?: 'inline' | 'stacked'")
    expect(
      extractRule(source, '.qms-page-header--actions-layout-stacked .qms-page-header__top'),
    ).toContain('flex-direction: column;')
    expect(
      extractRule(
        extractMediaBlock(source, '@media (max-width: 768px)'),
        '.qms-page-header--actions-layout-stacked .qms-page-header__main',
      ),
    ).toContain('order: 2;')
  })
})
