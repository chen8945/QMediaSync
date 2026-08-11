import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import router from '@/router'

const readSource = (path: string) => readFileSync(resolve(path), 'utf8')

const routeViewFiles = [
  'AppHome.vue',
  'AppCloudAccounts.vue',
  'AppSyncDirectories.vue',
  'AppSyncDirectoryForm.vue',
  'AppSyncRecords.vue',
  'AppSyncTaskDetail.vue',
  'AppScrapePathes.vue',
  'AppScrapePathForm.vue',
  'AppScrapeRecords.vue',
  'AppTmdbSettings.vue',
  'AppAiSettings.vue',
  'AppCategoryStrategy.vue',
  'AppUploadQueue.vue',
  'AppDownloadQueue.vue',
  'AppFileManager.vue',
  'AppBackupSettings.vue',
  'AppBackupRecords.vue',
  'AppBackupRestore.vue',
  'AppDatabaseRepair.vue',
  'AppUserSettings.vue',
  'AppApiKeys.vue',
  'AppNotificationChannels.vue',
  'AppEmbySettings.vue',
  'AppThreadSettings.vue',
  'AppLogSettings.vue',
  'AppProxySettings.vue',
  'AppUpdate.vue',
  'user-settings/LoginSessions.vue',
]

describe('页面头部统一契约', () => {
  it('所有业务路由视图都接入共享 PageHeader', () => {
    for (const file of routeViewFiles) {
      const source = readSource(`src/components/${file}`)
      expect(source, file).toContain('PageHeader')
    }
  })

  it('共享页面头部和页面根容器不引入灰色卡片底', () => {
    const pageHeaderSource = readSource('src/components/common/PageHeader.vue')

    expect(pageHeaderSource).toContain('background: transparent;')
    expect(pageHeaderSource).toContain('border-bottom: 0;')
    expect(pageHeaderSource).not.toContain('background: var(--el-color-primary-light-9')

    for (const [file, selector] of [
      ['AppCloudAccounts.vue', '\\.cloud-accounts-page'],
      ['AppScrapePathes.vue', '\\.scrape-pathes-page'],
      ['AppSyncDirectories.vue', '\\.sync-directories-page'],
    ]) {
      expect(readSource(`src/components/${file}`), file).not.toMatch(
        new RegExp(`${selector}\\s*\\{[^}]*background\\s*:`),
      )
    }

    const embySource = readSource('src/components/AppEmbySettings.vue')
    expect(embySource).toContain('background: transparent;')
    expect(embySource).not.toContain('linear-gradient(135deg, #f5f7fa 0%, #e4e7ed 100%)')
  })

  it('业务路由都有可选的页面展示元信息，重定向和登录路由除外', () => {
    for (const route of router.getRoutes()) {
      if (!route.components?.default || route.name === 'login') {
        continue
      }

      expect(route.meta.page, String(route.name)).toBeDefined()
    }
  })

  it('Emby 保留页面级 h1、内容级 h2/h3 和静止卡片边界', () => {
    const source = readSource('src/components/AppEmbySettings.vue')

    expect(source).toContain('<PageHeader />')
    expect(source).toContain('<h2 class="card-title">Emby 服务器配置</h2>')
    expect(source).toContain('<h3 class="card-title">通知链接配置</h3>')
    expect(source).not.toContain('<h1 class="card-title">')
    expect(source).toMatch(
      /\.settings-card,\s*\.sync-management-card\s*\{[\s\S]*?border:\s*1px solid #dcdfe6;[\s\S]*?box-shadow:/,
    )
    expect(source).toMatch(
      /\.settings-card :deep\(\.el-card__header\),\s*\.sync-management-card :deep\(\.el-card__header\)\s*\{[\s\S]*?border-bottom:\s*1px solid #dcdfe6;/,
    )
  })

  it('数据库修复使用共享页面头部，并保留完整内容卡片', () => {
    const source = readSource('src/components/AppDatabaseRepair.vue')

    expect(source).toContain('<PageHeader />')
    expect(source).toContain('<div class="section-card">')
    expect(source).not.toContain('<h1 class="section-title">数据库修复</h1>')
    expect(source).not.toContain('section-subtitle')
  })

  it('文件管理器只保留共享页面身份，账号标题仍属于内容分区', () => {
    const source = readSource('src/components/AppFileManager.vue')

    expect(source).toContain('<PageHeader />')
    expect(source).not.toContain('<h2 class="card-title hide-on-mobile">')
    expect(source).toContain('<h3>网盘账号</h3>')
  })

  it('页面级 API、备份和登录设备操作通过头部 actions slot 提供', () => {
    for (const file of [
      'AppApiKeys.vue',
      'AppBackupRecords.vue',
      'user-settings/LoginSessions.vue',
    ]) {
      expect(readSource(`src/components/${file}`), file).toContain('<template #actions>')
    }
  })

  it('PageHeader 覆盖共享样式的旧间距而不改变日志子组件的全局兼容间距', () => {
    const pageHeaderSource = readSource('src/components/common/PageHeader.vue')
    const sharedStyles = readSource('src/assets/components.css')

    expect(sharedStyles).toMatch(/\.header-actions\s*\{[\s\S]*?margin-top:\s*16px;/)
    expect(pageHeaderSource).toMatch(
      /\.qms-page-header__actions :deep\(\.header-actions\)\s*\{[\s\S]*?margin-top:\s*0;/,
    )
  })

  it('页面级移动端重复标题结构已从迁移页面移除', () => {
    for (const file of [
      'AppUploadQueue.vue',
      'AppDownloadQueue.vue',
      'AppSyncRecords.vue',
      'AppScrapeRecords.vue',
      'AppSyncDirectoryForm.vue',
      'AppScrapePathForm.vue',
    ]) {
      expect(readSource(`src/components/${file}`), file).not.toContain('hide-on-mobile')
      expect(readSource(`src/components/${file}`), file).not.toContain('mobile-form-header')
    }
  })
})
