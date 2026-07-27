import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'vitest'

test('队列页在刷新、查询切换和停用时保持页面状态一致', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const frontendRoot = resolve(__dirname, '../..')

  const readSource = (relativePath) => readFileSync(resolve(frontendRoot, relativePath), 'utf8')

  const getLocalFunctionBody = (source, functionName) => {
    const patterns = [
      new RegExp(`const\\s+${functionName}\\s*=\\s*async\\s*\\([^)]*\\)\\s*=>\\s*{`),
      new RegExp(`const\\s+${functionName}\\s*=\\s*\\([^)]*\\)\\s*=>\\s*{`),
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
    assert.notEqual(bodyStart, -1, `${functionName} should have a body`)

    let depth = 0
    for (let index = bodyStart; index < source.length; index += 1) {
      if (source[index] === '{') depth += 1
      if (source[index] === '}') depth -= 1
      if (depth === 0) {
        return source.slice(bodyStart, index + 1)
      }
    }

    throw new Error(`${functionName} body should close`)
  }

  const extractBalancedBlock = (source, bodyStart, label) => {
    assert.notEqual(bodyStart, -1, `${label} should have a body`)

    let depth = 0
    for (let index = bodyStart; index < source.length; index += 1) {
      if (source[index] === '{') depth += 1
      if (source[index] === '}') depth -= 1
      if (depth === 0) {
        return {
          body: source.slice(bodyStart, index + 1),
          end: index + 1,
        }
      }
    }

    throw new Error(`${label} body should close`)
  }

  const getActivatedCallbackBodyContaining = (source, needle) => {
    let searchFrom = 0

    while (searchFrom < source.length) {
      const activatedIndex = source.indexOf('onActivated', searchFrom)
      assert.notEqual(activatedIndex, -1, `onActivated callback containing ${needle} should exist`)

      const bodyStart = source.indexOf('{', activatedIndex)
      const { body, end } = extractBalancedBlock(source, bodyStart, 'onActivated callback')

      if (body.includes(needle)) {
        return body
      }

      searchFrom = end
    }

    throw new Error(`onActivated callback containing ${needle} should exist`)
  }

  const countMatches = (source, pattern) => source.match(pattern)?.length ?? 0
  const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

  const assertImportsNamed = (source, names, moduleName, messagePrefix) => {
    for (const name of names) {
      assert.match(
        source,
        new RegExp(
          `import\\s*{[\\s\\S]*?\\b${name}\\b[\\s\\S]*?}\\s*from\\s*['"]${moduleName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}['"]`,
        ),
        `${messagePrefix} should import ${name} from ${moduleName}`,
      )
    }
  }

  const assertComputedPageState = (source, pageKey, messagePrefix) => {
    assert.match(
      source,
      new RegExp(
        `const\\s+pageStateStore\\s*=\\s*usePageStateStore\\s*\\(\\s*\\)[\\s\\S]*?const\\s+pageState\\s*=\\s*pageStateStore\\.getPageState\\s*\\(\\s*['"]${pageKey}['"]`,
      ),
      `${messagePrefix} should create page state with ${pageKey}`,
    )
    assert.match(
      source,
      /const\s+currentPage\s*=\s*computed\s*\(\s*{[\s\S]*?get:\s*\(\)\s*=>\s*pageState\.currentPage[\s\S]*?set:\s*\(\s*value\s*\)\s*=>\s*pageStateStore\.setPagination/,
      `${messagePrefix} currentPage should be a computed page-state proxy`,
    )
    assert.match(
      source,
      /const\s+pageSize\s*=\s*computed\s*\(\s*{[\s\S]*?get:\s*\(\)\s*=>\s*pageState\.pageSize[\s\S]*?set:\s*\(\s*value\s*\)\s*=>\s*pageStateStore\.setPagination/,
      `${messagePrefix} pageSize should be a computed page-state proxy`,
    )
    assert.match(
      source,
      /const\s+statusFilter\s*=\s*computed\s*\(\s*{[\s\S]*?get:\s*\(\)\s*=>\s*Number\s*\(\s*pageState\.filters\.status\s*\?\?\s*-1\s*\)[\s\S]*?set:\s*\(\s*value\s*\)\s*=>\s*pageStateStore\.setFilter/,
      `${messagePrefix} statusFilter should be a computed page-state proxy`,
    )
  }

  const assertTablesUseControlledExpansion = (source, rowType, messagePrefix) => {
    assert.equal(
      countMatches(
        source,
        new RegExp(
          `:row-key\\s*=\\s*["']\\(row:\\s*${rowType}\\)\\s*=>\\s*String\\(row\\.id\\)["']`,
          'g',
        ),
      ),
      2,
      `${messagePrefix} should set row-key on both tables`,
    )
    assert.equal(
      countMatches(source, /:expand-row-keys\s*=\s*["']pageState\.expandedRowKeys["']/g),
      2,
      `${messagePrefix} should bind expanded row keys on both tables`,
    )
    assert.equal(
      countMatches(source, /@expand-change\s*=\s*["']handleExpandChange["']/g),
      2,
      `${messagePrefix} should handle expand changes on both tables`,
    )
    assert.equal(
      countMatches(source, /v-loading\s*=\s*["']initialLoading\s*\|\|\s*queryLoading["']/g),
      2,
      `${messagePrefix} should show table loading during initial loads and query switches`,
    )
    assert.equal(
      countMatches(source, /v-if\s*=\s*["']isMobileView["']/g),
      1,
      `${messagePrefix} should only mount the mobile table on mobile`,
    )
    assert.equal(
      countMatches(source, /<el-table\s*\n\s*v-else/g),
      1,
      `${messagePrefix} should mount the desktop table as the alternative branch`,
    )
    assert.doesNotMatch(
      source,
      new RegExp(`<el-table\\s+v-if="isMobileView"[\\s\\S]*?class="hidden-md-and-up"`),
      `${messagePrefix} mobile table should not use a mismatched Element Plus breakpoint class`,
    )
    assert.doesNotMatch(
      source,
      new RegExp(`<el-table\\s+v-else[\\s\\S]*?class="hidden-md-and-down"`),
      `${messagePrefix} desktop table should not use a mismatched Element Plus breakpoint class`,
    )
  }

  const assertQueueDetailPresentation = (source, messagePrefix) => {
    assert.equal(
      countMatches(source, /^\s+flexible$/gm),
      2,
      `${messagePrefix} should let both responsive tables shrink to their available container width`,
    )
    assert.equal(
      countMatches(source, /^\s+class-name="queue-expand-carrier"$/gm),
      2,
      `${messagePrefix} should keep a hidden expand carrier for both tables`,
    )
    assert.equal(
      countMatches(source, /^\s+label-class-name="queue-expand-carrier"$/gm),
      2,
      `${messagePrefix} should hide both expand carrier headers`,
    )
    assert.equal(
      countMatches(source, /<QueueTaskExpandButton\s+/g),
      2,
      `${messagePrefix} should provide one explicit expansion control per responsive table`,
    )
    assert.equal(
      countMatches(source, /:expanded="is(?:Download|Upload)RowExpanded\(scope\.row\)"/g),
      2,
      `${messagePrefix} should pass the controlled expansion state to both controls`,
    )
    assert.equal(
      countMatches(source, /@toggle="toggle(?:Download|Upload)RowExpansion\(scope\.row\)"/g),
      2,
      `${messagePrefix} should connect both controls to their table expansion action`,
    )
    assert.equal(
      countMatches(source, /^\s+class-name="queue-expand-column"$/gm),
      2,
      `${messagePrefix} should reserve a left-hand control column for both tables`,
    )
    assert.equal(
      countMatches(source, /<QueueTaskDetails\s+:groups=/g),
      2,
      `${messagePrefix} should render shared details from both responsive tables`,
    )
    assert.equal(
      countMatches(source, /^\s*<QueueTaskDetails\s+:groups=.*?:max-columns="2"\s*\/>$/gm),
      1,
      `${messagePrefix} should cap each detail group at two columns on mobile`,
    )
    assert.equal(
      countMatches(source, /^\s*<QueueTaskDetails\s+:groups=.*?:max-columns="5"\s*\/>$/gm),
      1,
      `${messagePrefix} should preserve each detail group's desktop column layout`,
    )
    const isUploadQueue = source.includes('<el-table-column prop="upload_phase"')
    const locationLabel = isUploadQueue ? '上传位置' : '下载位置'
    const progressLabel = isUploadQueue ? '进度' : '大小'
    const summaryLabels = [
      '任务',
      '状态',
      progressLabel,
      ...(isUploadQueue ? ['结果'] : []),
      locationLabel,
      '时间',
    ]
    for (const label of summaryLabels) {
      assert.match(
        source,
        new RegExp(`<el-table-column[^>]*label="${label}"`),
        `${messagePrefix} desktop table should include ${label} summary column`,
      )
    }
    assert.match(
      source,
      /<el-table-column\s+label="任务"\s+width="320">/,
      `${messagePrefix} should give the task column enough room for long file names`,
    )
    assert.match(
      source,
      new RegExp(`<el-table-column\\s+label="${locationLabel}"\\s+min-width="200">`),
      `${messagePrefix} should let the location column consume the remaining desktop width`,
    )
    assert.doesNotMatch(
      source,
      new RegExp(`<el-table-column\\s+label="${locationLabel}"\\s+width=`),
      `${messagePrefix} should not impose a fixed width on the location column`,
    )
    for (const [label, width] of [
      ['状态', '104'],
      ['时间', '260'],
      ...(isUploadQueue
        ? [
            ['进度', '160'],
            ['结果', '180'],
          ]
        : [['大小', '128']]),
    ]) {
      assert.match(
        source,
        new RegExp(`<el-table-column[^>]*label="${label}"[^>]*\\bwidth="${width}"`),
        `${messagePrefix} ${label} should use its compact fixed width`,
      )
    }
    assert.doesNotMatch(
      source,
      /class="mobile-task-location"/,
      `${messagePrefix} should keep detailed locations out of the collapsed mobile summary`,
    )
    assert.match(
      source,
      />开始时间：\{\{ formatDateTime\(scope\.row\.start_time\) \}\}<\/span\s*>/,
      `${messagePrefix} should label the start timestamp clearly`,
    )
    assert.match(
      source,
      /结束时间：\{\{ formatDateTime\(scope\.row\.end_time\) \}\}<\/span\s*>/,
      `${messagePrefix} should label the end timestamp clearly`,
    )
    assert.match(
      source,
      /:deep\(\.queue-expand-carrier\)\s*{\s*width:\s*1px\s*!important;\s*min-width:\s*1px\s*!important;\s*max-width:\s*1px\s*!important;\s*padding:\s*0\s*!important;/,
      `${messagePrefix} should keep the hidden expand carrier in the table layout`,
    )
    assert.match(
      source,
      /:deep\(\.queue-expand-carrier\s+\.cell\)\s*{\s*display:\s*none;/,
      `${messagePrefix} should hide only the expand carrier's generated control`,
    )
    assert.doesNotMatch(
      source,
      /:deep\(\.queue-expand-carrier\)\s*{\s*display:\s*none/,
      `${messagePrefix} should not remove a structural table column from browser layout`,
    )
    assert.match(
      source,
      /\.desktop-task-summary\s*:deep\(\.el-tooltip__trigger\)\s*{\s*display:\s*block;\s*flex:\s*1\s+1\s+0;\s*min-width:\s*0;/,
      `${messagePrefix} should let the tooltip trigger shrink with its task-summary text`,
    )
    assert.match(
      source,
      /:deep\(\.queue-table-desktop\s+\.el-table__body\s+tr\s*>\s*td\.el-table__cell\)\s*{\s*height:\s*110px;/,
      `${messagePrefix} should reserve roughly five lines of height for desktop task rows`,
    )
    assert.match(
      source,
      /:deep\(\.queue-expand-column\s+\.cell\)\s*{\s*display:\s*flex;\s*align-items:\s*center;\s*justify-content:\s*center;\s*height:\s*100%;\s*padding:\s*0;/,
      `${messagePrefix} should center its left-hand expansion control vertically`,
    )
  }

  const assertLoadQueueData = (
    source,
    pageKey,
    counterField,
    failureMessage,
    legacyFailureMessage,
    messagePrefix,
  ) => {
    const body = getLocalFunctionBody(source, 'loadQueueData')

    assert.match(body, /runRefresh\s*\(/, `${messagePrefix} loadQueueData should use runRefresh`)
    assert.match(
      body,
      /queueDataRequestGate\.next\s*\(\s*\)/,
      `${messagePrefix} loadQueueData should keep request gate ids`,
    )
    assert.match(
      body,
      /queueDataRequestGate\.isCurrent\s*\(/,
      `${messagePrefix} loadQueueData should ignore stale responses`,
    )
    assert.match(
      body,
      /mergeStableList\s*\(\s*queueData\.value\s*,\s*rows\s*,\s*\(\s*row\s*\)\s*=>\s*row\.id\s*\)/,
      `${messagePrefix} should merge rows by stable id`,
    )
    assert.match(
      body,
      /pruneExpandedRowsAfterLoad\s*\(\s*\)/,
      `${messagePrefix} should prune controlled expanded rows after refresh`,
    )
    assert.match(
      body,
      new RegExp(`${counterField}\\.value\\s*=\\s*response\\.data\\.data\\.${counterField}`),
      `${messagePrefix} should update ${counterField} count from the response`,
    )
    assert.doesNotMatch(
      body,
      new RegExp(
        `ElMessage\\.error\\s*\\(\\s*['"]${escapeRegExp(legacyFailureMessage)}['"]\\s*\\)`,
      ),
      `${messagePrefix} loadQueueData should not use the legacy failure message`,
    )
    assert.equal(
      countMatches(
        body,
        new RegExp(
          `ElMessage\\.error\\s*\\(\\s*['"]${escapeRegExp(failureMessage)}['"]\\s*\\)`,
          'g',
        ),
      ),
      2,
      `${messagePrefix} loadQueueData should use the specified failure message for response and catch failures`,
    )
    assert.match(
      body,
      new RegExp(
        `catch\\s*\\([^)]*\\)\\s*{[\\s\\S]*?ElMessage\\.error\\s*\\(\\s*['"]${escapeRegExp(failureMessage)}['"]\\s*\\)`,
      ),
      `${messagePrefix} loadQueueData catch branch should use the specified failure message`,
    )
  }

  const assertLoadQueueDataCoalescesInFlightChanges = (source, messagePrefix) => {
    const body = getLocalFunctionBody(source, 'loadQueueData')
    const requestIdIndex = body.indexOf('queueDataRequestGate.next()')
    const isRefreshingIndex = body.indexOf('isRefreshing.value')
    const runRefreshIndex = body.indexOf('runRefresh')

    assert.match(
      source,
      /const\s+{\s*initialLoading\s*,\s*backgroundRefreshing\s*,\s*isRefreshing\s*,\s*runRefresh\s*}\s*=\s*useBackgroundRefresh\s*\(\s*\)/,
      `${messagePrefix} should read isRefreshing from useBackgroundRefresh to coalesce refreshes`,
    )
    assert.match(
      source,
      /const\s+pendingQueueDataRefresh\s*=\s*ref\s*\(\s*false\s*\)/,
      `${messagePrefix} should keep a pending queue refresh flag`,
    )
    assert.match(
      body,
      /if\s*\(\s*!\s*isPageActive\s*\)\s*{\s*return\s*}/,
      `${messagePrefix} loadQueueData should skip inactive pages before creating requests`,
    )
    assert.notEqual(requestIdIndex, -1, `${messagePrefix} loadQueueData should create a request id`)
    assert.notEqual(
      isRefreshingIndex,
      -1,
      `${messagePrefix} loadQueueData should check for an in-flight refresh`,
    )
    assert.notEqual(runRefreshIndex, -1, `${messagePrefix} loadQueueData should use runRefresh`)
    assert.ok(
      requestIdIndex < isRefreshingIndex && isRefreshingIndex < runRefreshIndex,
      `${messagePrefix} loadQueueData should invalidate stale responses before coalescing in-flight refreshes`,
    )
    assert.match(
      body,
      /if\s*\(\s*isRefreshing\.value\s*\)\s*{[\s\S]*?pendingQueueDataRefresh\.value\s*=\s*true[\s\S]*?return\s*}/,
      `${messagePrefix} loadQueueData should record one pending refresh while a request is in flight`,
    )
    assert.match(
      body,
      /finally\s*{[\s\S]*?if\s*\(\s*pendingQueueDataRefresh\.value\s*&&\s*isPageActive\s*\)\s*{[\s\S]*?pendingQueueDataRefresh\.value\s*=\s*false[\s\S]*?await\s+loadQueueData\s*\(\s*\)/,
      `${messagePrefix} loadQueueData should replay one pending refresh after the current refresh finishes`,
    )
  }

  const assertActivationRepair = (source, pageKey, messagePrefix) => {
    const body = getActivatedCallbackBodyContaining(source, 'pruneExpandedRowKeys')
    const guardIndex = body.search(/if\s*\(\s*queueData\.value\.length\s*>\s*0\s*\)\s*{/)
    assert.notEqual(
      guardIndex,
      -1,
      `${messagePrefix} onActivated prune should be guarded by non-empty queue data`,
    )

    const guardBodyStart = body.indexOf('{', guardIndex)
    const { body: guardedBody, end: guardEnd } = extractBalancedBlock(
      body,
      guardBodyStart,
      `${messagePrefix} onActivated queue data guard`,
    )

    assert.match(
      guardedBody,
      new RegExp(
        `pruneExpandedRowKeys\\s*\\(\\s*['"]${pageKey}['"]\\s*,\\s*queueData\\.value\\.map\\s*\\(\\s*\\(\\s*row\\s*\\)\\s*=>\\s*String\\s*\\(\\s*row\\.id\\s*\\)\\s*\\)\\s*,?\\s*\\)`,
      ),
      `${messagePrefix} should prune expanded keys on activation only when queue data is non-empty`,
    )

    const layoutIndex = body.search(
      /nextTick\s*\(\s*\(\s*\)\s*=>\s*{[\s\S]*?const\s+table\s*=\s*isMobileView\.value\s*\?\s*mobileTableRef\.value\s*:\s*desktopTableRef\.value[\s\S]*?table\?\.doLayout\s*\(\s*\)/,
    )
    assert.ok(
      layoutIndex > guardEnd,
      `${messagePrefix} should recalculate its active table after activation outside the non-empty queue data guard`,
    )
    assert.match(
      source,
      /nextTick\s*\(\s*\(\s*\)\s*=>\s*{[\s\S]*?const\s+table\s*=\s*isMobileView\.value\s*\?\s*mobileTableRef\.value\s*:\s*desktopTableRef\.value[\s\S]*?table\?\.doLayout\s*\(\s*\)/,
      `${messagePrefix} should recalculate its active table after activation`,
    )
  }

  const assertQueueMutationsUseContext = (source, functionNames, messagePrefix) => {
    assert.match(
      source,
      /const\s+queueMutationContextVersion\s*=\s*ref\s*\(\s*0\s*\)/,
      `${messagePrefix} should version queue mutation operations`,
    )
    assert.match(
      source,
      /const\s+activeQueueMutationContext\s*=\s*ref\s*<\s*QueueMutationContextSnapshot\s*\|\s*null\s*>\s*\(\s*null\s*\)/,
      `${messagePrefix} should track the active queue mutation context`,
    )
    assert.match(
      getLocalFunctionBody(source, 'isQueueMutationContextCurrent'),
      /isPageActive[\s\S]*?activeQueueMutationContext\.value[\s\S]*?snapshot\.contextVersion\s*===\s*queueMutationContextVersion\.value/,
      `${messagePrefix} queue mutations should require active page and current mutation version`,
    )
    assert.match(
      getLocalFunctionBody(source, 'invalidateQueueMutationContext'),
      /queueMutationContextVersion\.value\s*\+=\s*1[\s\S]*?activeQueueMutationContext\.value\s*=\s*null/,
      `${messagePrefix} should invalidate queue mutations by version`,
    )
    assert.match(
      getLocalFunctionBody(source, 'deactivateQueuePage'),
      /invalidateQueueMutationContext\s*\(\s*\)/,
      `${messagePrefix} deactivation should invalidate pending queue mutations`,
    )
    assert.match(
      source,
      /onUnmounted\s*\(\s*\(\s*\)\s*=>\s*{[\s\S]*?invalidateQueueMutationContext\s*\(\s*\)/,
      `${messagePrefix} unmount should invalidate pending queue mutations`,
    )

    for (const functionName of functionNames) {
      const body = getLocalFunctionBody(source, functionName)
      assert.match(
        body,
        /const\s+operationContext\s*=\s*startQueueMutationContext\s*\(\s*\)/,
        `${messagePrefix} ${functionName} should start a mutation context`,
      )
      assert.match(
        body,
        /await[\s\S]*?if\s*\(\s*!isQueueMutationContextCurrent\s*\(\s*operationContext\s*\)\s*\)\s*{[\s\S]*?return[\s\S]*?}/,
        `${messagePrefix} ${functionName} should re-check context after awaited work`,
      )
      assert.match(
        body,
        /catch\s*(?:\([^)]*\))?\s*{[\s\S]*?if\s*\(\s*!isQueueMutationContextCurrent\s*\(\s*operationContext\s*\)\s*\)\s*{[\s\S]*?return[\s\S]*?}/,
        `${messagePrefix} ${functionName} catch should ignore stale mutation responses`,
      )
      assert.match(
        body,
        /finally\s*{[\s\S]*?if\s*\(\s*isQueueMutationContextCurrent\s*\(\s*operationContext\s*\)\s*\)/,
        `${messagePrefix} ${functionName} finally should only finish the current mutation context`,
      )
    }
  }

  for (const queuePage of [
    {
      path: 'src/components/AppUploadQueue.vue',
      key: 'upload-queue',
      rowType: 'UploadTask',
      counterField: 'uploading',
      failureMessage: '获取上传队列数据失败',
      legacyFailureMessage: '加载上传队列数据失败',
      mutationFunctions: [
        'clearQueue',
        'clearSuccessAndFailedTasks',
        'retryAllFailedTasks',
        'pauseAllTasks',
        'resumeAllTasks',
      ],
    },
    {
      path: 'src/components/AppDownloadQueue.vue',
      key: 'download-queue',
      rowType: 'DownloadTask',
      counterField: 'downloading',
      failureMessage: '获取下载队列数据失败',
      legacyFailureMessage: '加载下载队列数据失败',
      mutationFunctions: [
        'clearQueue',
        'clearSuccessAndFailedTasks',
        'pauseAllTasks',
        'resumeAllTasks',
      ],
    },
  ]) {
    const source = readSource(queuePage.path)
    const messagePrefix = queuePage.path

    assertImportsNamed(source, ['computed', 'nextTick', 'onActivated'], 'vue', messagePrefix)
    assertImportsNamed(source, ['usePageStateStore'], '@/stores/pageState', messagePrefix)
    assertImportsNamed(
      source,
      ['mergeStableList', 'retainExistingKeys'],
      '@/composables/useStableList',
      messagePrefix,
    )
    assertImportsNamed(
      source,
      ['useBackgroundRefresh'],
      '@/composables/useBackgroundRefresh',
      messagePrefix,
    )

    assertComputedPageState(source, queuePage.key, messagePrefix)
    assertLoadQueueData(
      source,
      queuePage.key,
      queuePage.counterField,
      queuePage.failureMessage,
      queuePage.legacyFailureMessage,
      messagePrefix,
    )
    assertLoadQueueDataCoalescesInFlightChanges(source, messagePrefix)
    assertTablesUseControlledExpansion(source, queuePage.rowType, messagePrefix)
    assertQueueDetailPresentation(source, messagePrefix)
    assert.doesNotMatch(
      source,
      /popper-class=/,
      `${messagePrefix} should use Element Plus default Tooltip styles`,
    )
    assert.doesNotMatch(
      source,
      /detailExpansionInitialized/,
      `${messagePrefix} should not automatically initialize expanded rows`,
    )
    assert.doesNotMatch(
      source,
      /watch\s*\(\s*isMobileView/,
      `${messagePrefix} should not add a breakpoint-specific expansion default`,
    )
    assert.match(
      source,
      new RegExp(
        `const\\s+pruneExpandedRowsAfterLoad\\s*=\\s*\\(\\)\\s*=>\\s*{[\\s\\S]*?retainExistingKeys\\s*\\(\\s*pageState\\.expandedRowKeys\\s*,\\s*queueData\\.value\\s*,\\s*\\(\\s*row\\s*\\)\\s*=>\\s*row\\.id\\s*\\)`,
      ),
      `${messagePrefix} should retain only expanded rows that remain in the refreshed list`,
    )
    assert.match(
      source,
      /<el-button[\s\S]*?type="info"[\s\S]*?@click="refreshQueue"[\s\S]*?:loading="backgroundRefreshing"/,
      `${messagePrefix} refresh button should show background refresh state`,
    )
    assert.match(
      source,
      new RegExp(
        `const\\s+handleExpandChange\\s*=\\s*\\(\\s*row:\\s*${queuePage.rowType}\\s*,\\s*expandedRows:\\s*${queuePage.rowType}\\[\\]\\s*\\)\\s*=>\\s*{[\\s\\S]*?pageStateStore\\.setExpandedRowKeys\\s*\\(\\s*['"]${queuePage.key}['"][\\s\\S]*?expandedRows\\.map\\s*\\(\\s*\\(\\s*item\\s*\\)\\s*=>\\s*String\\s*\\(\\s*item\\.id\\s*\\)\\s*\\)`,
      ),
      `${messagePrefix} should persist controlled expanded rows`,
    )
    assertActivationRepair(source, queuePage.key, messagePrefix)
    assertQueueMutationsUseContext(source, queuePage.mutationFunctions, messagePrefix)
  }

  const uploadSource = readSource('src/components/AppUploadQueue.vue')
  const downloadSource = readSource('src/components/AppDownloadQueue.vue')
  const queueTaskDetailUtilsSource = readSource('src/utils/queueTaskDetailUtils.ts')
  const expandButtonSource = readSource('src/components/queue/QueueTaskExpandButton.vue')
  const mainStyles = readSource('src/assets/main.css')

  assert.match(
    expandButtonSource,
    /:icon="CaretRight"[\s\S]*?:aria-expanded="expanded"[\s\S]*?@click\.stop="emit\('toggle'\)"/,
    '共享展开按钮应使用可访问的图标控件并向父组件发出切换事件',
  )

  assert.doesNotMatch(
    mainStyles,
    /\.queue-(?:upload-detail-tooltip|summary-tooltip|status-error-tooltip)/,
    '队列 Tooltip 不应保留覆盖 Element Plus 默认样式的全局选择器',
  )

  assert.match(
    uploadSource,
    /<div\s+class="mobile-task-metrics">[\s\S]*?getUploadedSizeLabel\(scope\.row\)[\s\S]*?getUploadProgressPercent\(scope\.row\)[\s\S]*?formatByteRate\(scope\.row\.upload_speed_bytes\)/,
    '上传移动摘要应始终显示已上传大小、百分比和速度',
  )
  assert.doesNotMatch(
    uploadSource,
    /<el-progress\s+v-if="scope\.row\.status === 1"/,
    '上传进度条应始终显示',
  )
  assert.match(
    uploadSource,
    /getUploadQueueLocationSummary\(scope\.row\)/,
    '上传位置应使用共享的位置摘要工具',
  )
  assert.match(
    queueTaskDetailUtilsSource,
    /`\$\{sourcePath\}\\n上传至 \$\{targetPath\}`/,
    '共享上传位置摘要应将“上传至”和目标路径放在单独一行',
  )
  assert.match(
    downloadSource,
    /getDownloadQueueLocationSummary\(scope\.row\)/,
    '下载位置应使用共享的位置摘要工具',
  )
  assert.match(
    queueTaskDetailUtilsSource,
    /`\$\{sourcePath\}\\n下载至 \$\{targetPath\}`/,
    '共享下载位置摘要应将“下载至”和目标路径放在单独一行',
  )
  for (const [source, direction] of [
    [uploadSource, '上传至'],
    [downloadSource, '下载至'],
  ]) {
    assert.match(
      source,
      new RegExp(`<span class="desktop-location-direction">${direction}</span>`),
      `${direction} 应作为独立元素展示`,
    )
    assert.match(
      source,
      /\.desktop-location-direction\s*{[\s\S]*?color:\s*var\(--el-text-color-secondary\)/,
      `${direction} 应使用次级文本色弱化展示`,
    )
  }
  assert.match(
    uploadSource,
    /<div\s+class="mobile-task-meta">[\s\S]*?# \{\{ scope\.row\.id \}\}[\s\S]*?<div\s+class="mobile-task-title-row">/,
    '上传移动摘要应将任务 ID、来源和状态置于文件名上方',
  )
  assert.match(
    downloadSource,
    /<div\s+class="mobile-task-meta">[\s\S]*?# \{\{ scope\.row\.id \}\}[\s\S]*?<div\s+class="mobile-task-title-row">/,
    '下载移动摘要应将任务 ID、来源和状态置于文件名上方',
  )
  assert.doesNotMatch(
    uploadSource,
    /isRapidUploadSuccess|mobile-task-stage-result/,
    '上传移动摘要不应单独展示秒传或阶段结果',
  )
  assert.match(
    uploadSource,
    /const shouldShowUploadStageSummary\s*=\s*\(task: UploadTask\): boolean\s*=>\s*Boolean\(getUploadStageSummaryLabel\(task\)\)/,
    '上传桌面结果列应展示包括上传完成在内的所有可用结果',
  )
  assert.match(
    uploadSource,
    /<el-tag\s+v-if="shouldShowUploadStageSummary\(scope\.row\)"\s+:type="getUploadStageResultTagType\(scope\.row\)"\s+effect="light"/,
    '上传桌面结果列应展示可用结果',
  )
  assert.match(
    uploadSource,
    /<span\s+class="desktop-task-id"># \{\{ scope\.row\.id \}\}<\/span>/,
    '上传桌面任务摘要应显示任务 ID',
  )
  assert.match(
    downloadSource,
    /<span\s+class="desktop-task-id"># \{\{ scope\.row\.id \}\}<\/span>/,
    '下载桌面任务摘要应显示任务 ID',
  )
  assert.match(
    uploadSource,
    /<el-tag\s+class="desktop-task-source"\s+size="small"\s+effect="plain">[\s\S]*?getUploadSourceName\(scope\.row\.source\)/,
    '上传桌面任务摘要应以标签展示任务来源',
  )
  assert.match(
    downloadSource,
    /<el-tag\s+class="desktop-task-source"\s+size="small"\s+effect="plain">[\s\S]*?getDownloadSourceName\(scope\.row\.source\)/,
    '下载桌面任务摘要应以标签展示任务来源',
  )
  assert.match(
    uploadSource,
    /<el-tooltip\s+v-if="getUploadTransportDetailSummary\(scope\.row\)"\s+:content="getUploadTransportDetailSummary\(scope\.row\)"\s+placement="top"/,
    '上传阶段详情 Tooltip should show the shared full transport summary with Element Plus defaults',
  )
  assert.doesNotMatch(
    uploadSource,
    /getUploadDetailSummary\s*=\s*\(/,
    '上传页面不应保留只含局部字段的旧详情摘要',
  )
})
