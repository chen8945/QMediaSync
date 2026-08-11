import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const __dirname = dirname(fileURLToPath(import.meta.url))

describe('AppSyncTaskDetail stream mode', () => {
  it('uses the dedicated stream source and task log panel', () => {
    const source = readFileSync(
      resolve(__dirname, '../../src/components/AppSyncTaskDetail.vue'),
      'utf-8',
    )

    expect(source).toContain('useSyncTaskStream')
    expect(source).toContain('SyncTaskLogPanel')
    expect(source).not.toContain('AppLogViewer')
    expect(source).not.toContain('loadTaskInfo')
  })

  it('keeps the shared header inside the detail card and visible on mobile', () => {
    const source = readFileSync(
      resolve(__dirname, '../../src/components/AppSyncTaskDetail.vue'),
      'utf-8',
    )

    expect(source).toMatch(
      /<el-card class="task-detail-card"[\s\S]*<template #header>[\s\S]*<PageHeader/,
    )
    expect(source).toContain('actions-layout="stacked"')
    expect(source).toContain('icon=""')
    expect(source).toContain('show-identity-on-mobile')
    expect(source).toMatch(/\.sync-task-detail-container\s*\{[\s\S]*?gap:\s*0;/)
    expect(source).toMatch(
      /\.task-detail-card\s*:deep\(\.qms-page-header\)\s*\{[\s\S]*?margin-bottom:\s*0;/,
    )
  })
})
