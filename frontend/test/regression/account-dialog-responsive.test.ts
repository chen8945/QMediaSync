import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, test } from 'vitest'

import { extractMediaBlock, extractRule } from '../support/css'

const readSource = (path: string) => readFileSync(resolve(path), 'utf8')

describe('账号弹窗响应式布局', () => {
  const cloudAccountsSource = readSource('src/components/AppCloudAccounts.vue')

  test('新增和编辑账号在移动端使用一致的垂直居中表单布局', () => {
    expect(cloudAccountsSource.match(/class="account-dialog"/g) ?? []).toHaveLength(2)
    expect(cloudAccountsSource).not.toContain('add-account-dialog')
    expect(cloudAccountsSource).not.toContain(':top=')
    expect(cloudAccountsSource.match(/:align-center="isMobile"/g) ?? []).toHaveLength(2)
    expect(
      cloudAccountsSource.match(
        /:width="isMobile \? 'min\(500px, calc\(100vw - 32px\)\)' : '500px'"/g,
      ) ?? [],
    ).toHaveLength(2)
    expect(
      cloudAccountsSource.match(/:label-position="isMobile \? 'top' : 'right'"/g) ?? [],
    ).toHaveLength(2)
    expect(
      cloudAccountsSource.match(/:label-width="isMobile \? 'auto' : '100px'"/g) ?? [],
    ).toHaveLength(2)

    const mobileBlock = extractMediaBlock(cloudAccountsSource, '@media (max-width: 768px)')
    expect(extractRule(mobileBlock, '.account-dialog')).toContain(
      'max-height: calc(100dvh - 32px);',
    )
    expect(extractRule(mobileBlock, '.account-dialog')).toContain('display: flex;')
    expect(extractRule(mobileBlock, '.account-dialog :deep(.el-dialog__body)')).toContain(
      'overflow-y: auto;',
    )
    expect(mobileBlock).toContain('.account-dialog :deep(.el-dialog__footer)')
    expect(mobileBlock).toContain('flex-shrink: 0;')
    expect(extractRule(cloudAccountsSource, '.dialog-footer')).toContain('justify-content: center;')
  })
})

describe('网盘状态图标', () => {
  const cloudAccountsSource = readSource('src/components/AppCloudAccounts.vue')
  const statusStart = cloudAccountsSource.indexOf('<template v-if="account.status">')
  const statusEnd = cloudAccountsSource.indexOf('\n              </template>', statusStart)
  const statusSource = cloudAccountsSource.slice(statusStart, statusEnd)

  test('不同状态行使用对应的语义图标', () => {
    for (const icon of ['Postcard', 'Avatar', 'PieChart', 'Medal', 'Calendar']) {
      expect(statusSource.match(new RegExp(`<${icon} \\/>`, 'g')) ?? []).toHaveLength(1)
    }
  })
})

describe('115 二维码授权长错误保护', () => {
  const authorizationSource = readSource('src/components/cloud-auth/V115AuthorizationDialog.vue')

  test('状态标签在有限宽度内换行并在超长错误时独立滚动', () => {
    const statusBlock = extractRule(authorizationSource, '.v115-auth-dialog__status')

    for (const declaration of [
      'width: 100%;',
      'min-width: 0;',
      'height: auto;',
      'white-space: normal;',
      'overflow-wrap: anywhere;',
      'max-height: 96px;',
      'overflow-y: auto;',
      'overscroll-behavior: contain;',
    ]) {
      expect(statusBlock).toContain(declaration)
    }
  })

  test('短状态居中并使用语义色，失败原文保持左对齐', () => {
    expect(authorizationSource).toContain(
      `if (status.value === 'waiting') return 'primary' as const`,
    )
    expect(authorizationSource).toContain(
      `:class="{ 'v115-auth-dialog__status--failed': status === 'failed' }"`,
    )

    const statusBlock = extractRule(authorizationSource, '.v115-auth-dialog__status')
    for (const declaration of [
      'align-items: center;',
      'justify-content: center;',
      'text-align: center;',
    ]) {
      expect(statusBlock).toContain(declaration)
    }

    const failedBlock = extractRule(authorizationSource, '.v115-auth-dialog__status--failed')
    for (const declaration of [
      'align-items: flex-start;',
      'justify-content: flex-start;',
      'text-align: left;',
    ]) {
      expect(failedBlock).toContain(declaration)
    }
  })

  test('账号信息和加载骨架使用紧凑的原风格层级', () => {
    expect(authorizationSource).toContain(
      '<el-skeleton v-else class="v115-auth-dialog__skeleton" animated>',
    )
    expect(authorizationSource.match(/<el-skeleton-item\b/g) ?? []).toHaveLength(3)
    expect(authorizationSource).not.toContain('v115-auth-dialog__skeleton-item--short')

    expect(extractRule(authorizationSource, '.v115-auth-dialog')).toContain('gap: 12px;')

    const nameBlock = extractRule(authorizationSource, '.v115-auth-dialog__name')
    for (const declaration of ['padding:', 'border-radius:', 'background:']) {
      expect(nameBlock).not.toContain(declaration)
    }

    const skeletonBlock = extractRule(authorizationSource, '.v115-auth-dialog__skeleton')
    for (const declaration of ['display: grid;', 'width: 100%;', 'gap: 12px;']) {
      expect(skeletonBlock).toContain(declaration)
    }

    const skeletonItemBlock = extractRule(authorizationSource, '.v115-auth-dialog__skeleton-item')
    expect(skeletonItemBlock).toContain('width: 100%;')
    expect(skeletonItemBlock).toContain('height: 15px;')
  })
})

describe('115 二维码授权画布边框', () => {
  const qrCodeSource = readSource('src/components/cloud-auth/V115QrCode.vue')

  test('二维码容器不额外绘制边框或圆角', () => {
    const qrCodeBlock = extractRule(qrCodeSource, '.v115-qr-code')

    for (const declaration of ['border:', 'border-radius:', 'background:']) {
      expect(qrCodeBlock).not.toContain(declaration)
    }
  })
})

describe('115 授权流程生命周期', () => {
  const cloudAccountsSource = readSource('src/components/AppCloudAccounts.vue')

  test('新授权入口会取消已有流程并暂停隐藏页面的 OAuth 轮询', () => {
    expect(cloudAccountsSource).toContain('const cancelActiveAuthorizationFlow = async () =>')
    expect(
      cloudAccountsSource.match(/await cancelActiveAuthorizationFlow\(\)/g) ?? [],
    ).toHaveLength(2)
    expect(cloudAccountsSource.match(/:disabled="authorizationFlowBusy"/g) ?? []).toHaveLength(2)
    expect(cloudAccountsSource).toContain(
      "document.addEventListener('visibilitychange', oauthPollingVisibilityHandler)",
    )
    expect(cloudAccountsSource).toContain(
      "document.removeEventListener('visibilitychange', oauthPollingVisibilityHandler)",
    )
    expect(cloudAccountsSource).toContain('if (document.hidden) {')
  })

  test('直接跳转 OAuth 会暂存会话并在回调返回后处理失效会话', () => {
    expect(cloudAccountsSource).toContain(`v-if="account.source_type !== 'openlist'"`)
    expect(cloudAccountsSource).not.toContain(
      `v-if="account.source_type !== 'openlist' && !account.deprecated"`,
    )
    expect(cloudAccountsSource).toContain(`v-if="account.source_type === '115'"`)
    expect(cloudAccountsSource).toContain('savePendingV115Authorization')
    expect(cloudAccountsSource).toContain('loadPendingV115Authorization')
    expect(cloudAccountsSource).toContain('clearPendingV115Authorization')
    expect(cloudAccountsSource).toContain(
      'savePendingV115Authorization({ accountId: account.id, authorizationId })',
    )
    expect(cloudAccountsSource).toContain('clearPendingV115Authorization(context.authorizationId)')
    expect(cloudAccountsSource).toContain('const callbackAuthorizationId =')
    expect(cloudAccountsSource).toContain('const callbackAccountId =')
    expect(cloudAccountsSource).toContain('Number.isSafeInteger(callbackAccountId)')
    expect(cloudAccountsSource).toContain(
      'pendingRedirectAuthorization.accountId !== callbackAccountId',
    )
    expect(cloudAccountsSource).toContain('cancelAndClearPendingAuthorization')

    const mismatchBlock = cloudAccountsSource.slice(
      cloudAccountsSource.indexOf(
        'pendingRedirectAuthorization.authorizationId !== callbackAuthorizationId',
      ),
      cloudAccountsSource.indexOf('if (hasValidCallbackAccountId && hasCallbackData)'),
    )
    expect(mismatchBlock).toContain('return')
  })

  test('取消授权会话会校验接口业务码后再清理暂存', () => {
    const cancelBlock = cloudAccountsSource.slice(
      cloudAccountsSource.indexOf('const cancelAuthorizationSession = async'),
      cloudAccountsSource.indexOf('const cancelAndClearPendingAuthorization'),
    )
    expect(cancelBlock).toContain('const response = await http.post')
    expect(cancelBlock).toContain('return response?.data?.code === 200')
  })
})

describe('首页操作区样式隔离', () => {
  const homeSource = readSource('src/components/AppHome.vue')

  test('运行日志操作区不再使用全局 header-actions 类', () => {
    expect(homeSource).toContain('class="home-header__actions"')
    expect(homeSource).not.toContain('class="header-actions"')

    const actionsBlock = extractRule(homeSource, '.home-header__actions')
    expect(actionsBlock).toContain('display: flex;')
    expect(actionsBlock).toContain('align-items: center;')
    expect(actionsBlock).toContain('gap: 12px;')
  })
})
