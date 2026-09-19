import { readFileSync } from 'node:fs'
import { strict as assert } from 'node:assert'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'vitest'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const readSource = (path: string) => readFileSync(resolve(frontendRoot, path), 'utf8')

test('AppHome lazily imports heavy chart and log components', () => {
  const source = readSource('src/components/AppHome.vue')

  assert.match(
    source,
    /import\s*\{\s*defineAsyncComponent\s*,\s*ref\s*,\s*useTemplateRef\s*\}\s*from ['"]vue['"]/,
  )
  assert.doesNotMatch(source, /import\s+HourlyStatsChart\s+from ['"]\.\/HourlyStatsChart\.vue['"]/)
  assert.doesNotMatch(source, /import\s+AppLogViewer\s+from ['"]\.\/AppLogViewer\.vue['"]/)
  assert.match(
    source,
    /const\s+HourlyStatsChart\s*=\s*defineAsyncComponent\(\(\)\s*=>\s*import\(['"]\.\/HourlyStatsChart\.vue['"]\)\)/,
  )
  assert.match(
    source,
    /const\s+AppLogViewer\s*=\s*defineAsyncComponent\(\(\)\s*=>\s*import\(['"]\.\/AppLogViewer\.vue['"]\)\)/,
  )
})

test('AppHome only mounts log viewer while dialog is open and disconnects safely', () => {
  const source = readSource('src/components/AppHome.vue')

  assert.match(
    source,
    /const\s+logViewerRef\s*=\s*useTemplateRef<\{\s*disconnect\?:\s*\(\)\s*=>\s*void\s*\}>\(['"]logViewerRef['"]\)/,
  )
  assert.match(source, /logViewerRef\.value\?\.disconnect\?\.\(\)/)
  assert.match(source, /<AppLogViewer\b[^>]*\bv-if=["']showLogDialog["']/s)
})

test('queue stats polling keeps existing data after first successful load', () => {
  const source = readSource('src/composables/useQueueStats.ts')

  assert.match(source, /const\s+hasLoaded\s*=\s*ref\(false\)/)
  assert.match(source, /queueStatsLoading\.value\s*=\s*!hasLoaded\.value/)
  assert.match(
    source,
    /queueStats\.value\s*=\s*await fetchQueueStats\(http\)\s+hasLoaded\.value\s*=\s*true/s,
  )
  assert.match(
    source,
    /catch\s*\([^)]*\)\s*\{[\s\S]*?queueStats\.value\s*=\s*null[\s\S]*?currentPollingInterval\s*=\s*Math\.min/,
  )
  assert.match(
    source,
    /catch\s*\([^)]*\)\s*\{[\s\S]*?if\s*\(\s*!hasLoaded\.value\s*\)\s*\{\s*queueStats\.value\s*=\s*null\s*\}/,
  )
  assert.doesNotMatch(source, /queueStatsLoading\.value\s*=\s*true/)
  assert.match(source, /if\s*\(inFlight\)\s*\{\s*return\s*\}\s*inFlight\s*=\s*true/)
  assert.match(source, /finally\s*\{\s*inFlight\s*=\s*false/)
  assert.match(
    source,
    /if\s*\(!isPollingActive\s*\|\|\s*queueStatsTimer\s*\|\|\s*document\.hidden\)/,
  )
  assert.match(source, /import.*fetchQueueStats.*from ['"]@\/api\/dashboard['"]/)
})

test('hourly stats polling keeps existing chart data after first successful load', () => {
  const source = readSource('src/composables/useHourlyStats.ts')

  assert.match(source, /const\s+hasLoaded\s*=\s*ref\(false\)/)
  assert.match(source, /hourlyStatsLoading\.value\s*=\s*!hasLoaded\.value/)
  assert.match(
    source,
    /hourlyStats\.value\s*=\s*await fetchHourlyStats\(http\)\s+hasLoaded\.value\s*=\s*true/s,
  )
  assert.match(source, /import.*fetchHourlyStats.*from ['"]@\/api\/dashboard['"]/)
  assert.match(
    source,
    /catch\s*\([^)]*\)\s*\{[\s\S]*?if\s*\(\s*!hasLoaded\.value\s*\)\s*\{\s*hourlyStats\.value\s*=\s*null\s*\}/,
  )
  assert.doesNotMatch(source, /hourlyStatsLoading\.value\s*=\s*true/)
})

test('update collapse items use version as stable identity', () => {
  const source = readSource('src/components/AppUpdate.vue')

  assert.match(
    source,
    /<el-collapse-item\b[^>]*v-for=["']update in updateList["'][^>]*:key=["']update\.version["'][^>]*:name=["']`update-\$\{update\.version\}`["']/s,
  )
  assert.doesNotMatch(source, /v-for=["']\(\s*update\s*,\s*index\s*\)\s+in\s+updateList["']/)
  assert.doesNotMatch(source, /:key=["']index["']/)
  assert.doesNotMatch(source, /:name=["']`update-\$\{index\}`["']/)
})

test('update collapse starts empty instead of using stale index identity', () => {
  const source = readSource('src/components/AppUpdate.vue')

  assert.doesNotMatch(
    source,
    /const\s+activeNames\s*=\s*ref<string\[\]>\(\s*\[\s*['"]update-0['"]\s*\]\s*\)/,
  )
  assert.match(source, /const\s+activeNames\s*=\s*ref<string\[\]>\(\s*\[\s*\]\s*\)/)
})

test('update collapse initializes first version once without overriding user state', () => {
  const source = readSource('src/components/AppUpdate.vue')

  assert.match(source, /import\s*\{\s*[^}]*\bwatch\b[^}]*\}\s*from ['"]vue['"]/)
  assert.match(source, /const\s+hasInitializedActiveNames\s*=\s*ref\(false\)/)
  assert.match(
    source,
    /watch\(\s*updateList\s*,\s*\(\s*updates\s*\)\s*=>\s*\{[\s\S]*?if\s*\(\s*hasInitializedActiveNames\.value\s*\|\|\s*activeNames\.value\.length\s*>\s*0\s*\|\|\s*updates\.length\s*===\s*0\s*\)\s*\{[\s\S]*?return[\s\S]*?\}[\s\S]*?activeNames\.value\s*=\s*\[\s*`update-\$\{updates\[0\]\.version\}`\s*\][\s\S]*?hasInitializedActiveNames\.value\s*=\s*true[\s\S]*?\}\s*\)/,
  )
})
