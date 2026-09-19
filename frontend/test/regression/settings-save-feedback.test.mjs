import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'vitest'

test('设置页操作反馈和按钮语义保持一致', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const frontendRoot = resolve(__dirname, '../..')

  const readSource = (relativePath) => readFileSync(resolve(frontendRoot, relativePath), 'utf8')

  const listSourceFiles = (relativeDir) => {
    const dir = resolve(frontendRoot, relativeDir)
    return readdirSync(dir).flatMap((entry) => {
      const absolutePath = resolve(dir, entry)
      const relativePath = `${relativeDir}/${entry}`

      if (statSync(absolutePath).isDirectory()) {
        return listSourceFiles(relativePath)
      }

      return /\.(vue|css|scss)$/.test(entry) ? [relativePath] : []
    })
  }

  const listVueSourceFiles = () =>
    listSourceFiles('src').filter((sourcePath) => sourcePath.endsWith('.vue'))

  const findElementPlusButtons = (source) => {
    const buttons = []
    const buttonPattern = /<el-button\b(?![^>]*\/>)\s*[\s\S]*?<\/el-button\s*>/g

    for (const match of source.matchAll(buttonPattern)) {
      buttons.push({
        block: match[0],
        line: source.slice(0, match.index).split('\n').length,
      })
    }

    return buttons
  }

  const assertButtonMatches = (source, label, pattern, message) => {
    assert.match(
      source,
      new RegExp(`<el-button\\b${pattern}[^>]*>[\\s\\S]*?${label}[\\s\\S]*?<\\/el-button\\s*>`),
      message,
    )
  }

  const getLocalFunctionBody = (source, functionName) => {
    const patterns = [
      new RegExp(`const\\s+${functionName}\\s*=\\s*async\\s*\\([^)]*\\)\\s*=>\\s*{`),
      new RegExp(`async\\s+function\\s+${functionName}\\s*\\([^)]*\\)\\s*{`),
    ]

    let start = -1
    for (const pattern of patterns) {
      const match = pattern.exec(source)
      if (match) {
        start = match.index
        break
      }
    }

    assert.notEqual(start, -1, `${functionName} should exist`)

    const bodyStart = source.indexOf('{', start)
    let depth = 0
    for (let index = bodyStart; index < source.length; index += 1) {
      if (source[index] === '{') depth += 1
      if (source[index] === '}') depth -= 1
      if (depth === 0) return source.slice(bodyStart, index + 1)
    }

    throw new Error(`${functionName} body should close`)
  }

  const embySource = readSource('src/components/AppEmbySettings.vue')
  const saveEmbyConfigBody = getLocalFunctionBody(embySource, 'saveEmbyConfig')

  assert.doesNotMatch(
    saveEmbyConfigBody,
    /ElMessage\.success\(\s*['"]Emby 配置已成功保存['"]\s*\)/,
    'AppEmbySettings.vue save success should use the inline status instead of a duplicate toast',
  )

  const userSource = readSource('src/components/AppUserSettings.vue')
  const saveSettingsBody = getLocalFunctionBody(userSource, 'saveSettings')

  assert.doesNotMatch(
    saveSettingsBody,
    /ElMessage\.success\(\s*['"]用户设置已保存['"]\s*\)/,
    'AppUserSettings.vue save success should use the inline status instead of a duplicate toast',
  )

  assert.match(
    userSource,
    /import\s*\{[\s\S]*?\bCheck\b[\s\S]*?\}\s*from\s*['"]@element-plus\/icons-vue['"]/,
    'AppUserSettings.vue should import the Check icon for the save button',
  )

  assert.match(
    userSource,
    /<el-button\b(?=[^>]*type=["']success["'])(?=[^>]*:icon=["']Check["'])(?=[^>]*size=["']large["'])[^>]*>[\s\S]*?保存设置[\s\S]*?<\/el-button>/,
    'AppUserSettings.vue save button should match the green large settings-page save style',
  )

  const backupSettingsSource = readSource('src/components/AppBackupSettings.vue')

  assert.match(
    backupSettingsSource,
    /import\s*\{[\s\S]*?\bCheck\b[\s\S]*?\}\s*from\s*['"]@element-plus\/icons-vue['"]/,
    'AppBackupSettings.vue should import the Check icon for the save button',
  )

  assertButtonMatches(
    backupSettingsSource,
    '保存配置',
    '(?=[^>]*type=["\']success["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']Check["\'])',
    'AppBackupSettings.vue save button should match the green large settings-page save style',
  )

  const proxySource = readSource('src/components/AppProxySettings.vue')

  assertButtonMatches(
    proxySource,
    '测试',
    '(?=[^>]*type=["\']primary["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']Connection["\'])',
    'AppProxySettings.vue test button should be a large primary settings action',
  )

  assertButtonMatches(
    proxySource,
    '保存',
    '(?=[^>]*type=["\']success["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']Check["\'])',
    'AppProxySettings.vue save button should be a large success settings action',
  )

  for (const queuePage of [
    'src/components/AppUploadQueue.vue',
    'src/components/AppDownloadQueue.vue',
  ]) {
    const source = readSource(queuePage)

    assertButtonMatches(
      source,
      '全部暂停',
      '(?=[^>]*type=["\']warning["\'])',
      `${queuePage} pause button should use warning semantics`,
    )

    assertButtonMatches(
      source,
      '全部恢复',
      '(?=[^>]*type=["\']success["\'])',
      `${queuePage} resume button should use success semantics`,
    )
  }

  const backupRestoreSource = readSource('src/components/AppBackupRestore.vue')

  assertButtonMatches(
    backupRestoreSource,
    '开始恢复',
    '(?=[^>]*type=["\']warning["\'])(?=[^>]*size=["\']large["\'])',
    'AppBackupRestore.vue restore button should use warning semantics',
  )

  const databaseRepairSource = readSource('src/components/AppDatabaseRepair.vue')

  assertButtonMatches(
    databaseRepairSource,
    "\\{\\{ loading \\? '修复中…' : '修复数据库' \\}\\}",
    '(?=[^>]*type=["\']warning["\'])(?=[^>]*size=["\']large["\'])',
    'AppDatabaseRepair.vue repair button should use warning semantics',
  )

  const notificationChannelsSource = readSource('src/components/AppNotificationChannels.vue')

  assertButtonMatches(
    notificationChannelsSource,
    '测试',
    '(?=[^>]*type=["\']primary["\'])(?=[^>]*text\\b)',
    'AppNotificationChannels.vue channel test button should not use success before the test result is known',
  )

  const updateSource = readSource('src/components/AppUpdate.vue')

  assert.match(
    updateSource,
    /import\s*\{[\s\S]*?\bRefresh\b[\s\S]*?\}\s*from\s*['"]@element-plus\/icons-vue['"]/,
    'AppUpdate.vue should import Refresh for the refresh button',
  )

  assert.match(
    updateSource,
    /<div\s+class=["']section-header-right update-toolbar["']>[\s\S]*?<el-radio-group\b(?=[^>]*\bsize=["']default["'])[\s\S]*?<el-button\b(?=[^>]*\bsize=["']default["'])(?=[^>]*:icon=["']Refresh["'])[\s\S]*?刷新[\s\S]*?<\/el-button>[\s\S]*?<\/div>/,
    'AppUpdate.vue version channel selector and refresh button should share one coordinated toolbar',
  )

  const updateToolbarBlock =
    updateSource.match(
      /<div\s+class=["']section-header-right update-toolbar["']>[\s\S]*?<\/div>/,
    )?.[0] || ''

  assert.doesNotMatch(
    updateToolbarBlock,
    /<el-button\b[^>]*\bround\b[^>]*>[\s\S]*?刷新[\s\S]*?<\/el-button>/,
    'AppUpdate.vue refresh button should not use round styling next to the segmented channel selector',
  )

  assert.ok(
    embySource.indexOf('class="emby-status-alert"') <
      embySource.indexOf('class="sync-management-card"'),
    'AppEmbySettings.vue save status should appear before the sync management card',
  )

  const twoFactorSource = readSource('src/components/user-settings/TwoFactorSettings.vue')

  assert.match(
    twoFactorSource,
    /import\s*\{[\s\S]*?\bKey\b[\s\S]*?\bCircleCheck\b[\s\S]*?\bLock\b[\s\S]*?\}\s*from\s*['"]@element-plus\/icons-vue['"]/,
    'TwoFactorSettings.vue should import icons for its action buttons',
  )

  assertButtonMatches(
    twoFactorSource,
    '生成配置',
    '(?=[^>]*type=["\']primary["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']Key["\'])',
    'TwoFactorSettings.vue setup button should match the large icon button rhythm',
  )

  assertButtonMatches(
    twoFactorSource,
    '启用两步验证',
    '(?=[^>]*type=["\']success["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']CircleCheck["\'])',
    'TwoFactorSettings.vue enable button should match the large icon button rhythm',
  )

  assertButtonMatches(
    twoFactorSource,
    '关闭两步验证',
    '(?=[^>]*type=["\']danger["\'])(?=[^>]*size=["\']large["\'])(?=[^>]*:icon=["\']Lock["\'])',
    'TwoFactorSettings.vue disable button should match the large icon button rhythm',
  )

  const backupRestoreSourceForIcon = readSource('src/components/AppBackupRestore.vue')

  assertButtonMatches(
    backupRestoreSourceForIcon,
    '开始恢复',
    '(?=[^>]*:icon=["\']CircleCheck["\'])',
    'AppBackupRestore.vue restore button should use Element Plus :icon spacing',
  )

  assert.doesNotMatch(
    backupRestoreSourceForIcon,
    /<el-button\b[^>]*>[\s\S]*?<el-icon><CircleCheck \/><\/el-icon>[\s\S]*?开始恢复[\s\S]*?<\/el-button>/,
    'AppBackupRestore.vue restore button should not mix manual el-icon markup inside the button',
  )

  for (const sourcePath of listSourceFiles('src')) {
    const source = readSource(sourcePath)

    assert.doesNotMatch(
      source,
      /\.el-button[\s\S]{0,120}:deep\(\.el-icon\)\s*\{[\s\S]*?margin-right\s*:/,
      `${sourcePath} should not add extra icon margin inside Element Plus buttons`,
    )
  }

  for (const sourcePath of listVueSourceFiles()) {
    if (sourcePath.endsWith('ResponsiveIconButton.vue')) continue

    const source = readSource(sourcePath).replace(/<!--[\s\S]*?-->/g, '')

    for (const button of findElementPlusButtons(source)) {
      if (!/<el-icon\b/.test(button.block)) continue

      const onlyRightSuffixIcons = [...button.block.matchAll(/<el-icon\b([^>]*)>/g)].every(
        ([, attributes]) => /\bclass=["'][^"']*\bel-icon--right\b/.test(attributes),
      )

      assert.ok(
        onlyRightSuffixIcons,
        `${sourcePath}:${button.line} should use Element Plus :icon for button action icons`,
      )
    }
  }
})
