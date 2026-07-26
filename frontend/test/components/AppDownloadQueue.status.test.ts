import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

import { getDownloadStatusTagType, getDownloadStatusText } from '@/utils/queueTaskDetailUtils'

const source = readFileSync(
  resolve(__dirname, '../../src/components/AppDownloadQueue.vue'),
  'utf-8',
)
const threadSettingsSource = readFileSync(
  resolve(__dirname, '../../src/components/AppThreadSettings.vue'),
  'utf-8',
)

describe('AppDownloadQueue 下载状态文案', () => {
  it('使用与上传队列一致的下载状态命名', () => {
    const statusLabels: Array<[number, string]> = [
      [0, '等待下载'],
      [1, '正在下载'],
      [2, '下载完成'],
      [3, '下载失败'],
      [4, '已取消'],
    ]

    for (const [value, label] of statusLabels) {
      expect(source).toContain(`<el-option label="${label}" :value="${value}"></el-option>`)
      expect(getDownloadStatusText(value)).toBe(label)
    }

    expect(getDownloadStatusTagType(4)).toBe('warning')
    expect(source).toContain('getDownloadStatusText,')
    expect(source).toContain("from '@/utils/queueTaskDetailUtils'")
    expect(threadSettingsSource).toContain('“正在下载”的任务数')
    expect(threadSettingsSource).not.toContain('“下载中”')
  })
})
