import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(__dirname, '../../src/views/sync/AppSyncDirectoryForm.vue'),
  'utf-8',
)

describe('AppSyncDirectoryForm 目录监控上传文案', () => {
  it('使用用户可理解的目录监控上传名称和示例', () => {
    expect(source).toContain('目标目录')
    expect(source).toContain('监控模式')
    expect(source).toContain('自动（推荐）')
    expect(source).toContain('value="fsnotify"')
    expect(source).toContain('性能模式')
    expect(source).toContain('兼容模式')
    expect(source).toContain('遇到同名文件时')
    expect(source).toContain('value="skip_same">跳过')
    expect(source).toContain('value="fail_conflict">停止')
    expect(source).toContain('value="replace_conflict">覆盖')
    expect(source).toContain('例如当前同步目录的远端路径是')
  })

  it('不再展示原来的专业文案', () => {
    expect(source).not.toContain('网盘保存位置')
    expect(source).not.toContain('115 上传根目录')
    expect(source).not.toContain('上传根目录必须位于当前同步目录的远端路径之下')
    expect(source).not.toContain('文件系统监控')
    expect(source).not.toContain('定时补偿扫描')
    expect(source).not.toContain('文件发现方式')
    expect(source).not.toContain('仅实时发现')
    expect(source).not.toContain('仅定期查漏')
    expect(source).not.toContain('远端冲突策略')
    expect(source).not.toContain('value="watcher"')
    expect(source).not.toContain('同 SHA1 / 大小跳过')
    expect(source).not.toContain('始终上传')
  })

  it('不展示和提交内置计时参数', () => {
    expect(source).not.toContain('稳定窗口')
    expect(source).not.toContain('查漏间隔')
    expect(source).not.toContain('stability_seconds')
    expect(source).not.toContain('stability_check_interval_seconds')
    expect(source).not.toContain('stability_required_count')
    expect(source).not.toContain('rescan_interval_seconds')
  })

  it('自定义设置展示在目录监控上传前面', () => {
    const customSettingIndex = source.indexOf('label="自定义设置"')
    const directoryUploadIndex = source.indexOf('>目录监控上传<')

    expect(customSettingIndex).toBeGreaterThanOrEqual(0)
    expect(directoryUploadIndex).toBeGreaterThanOrEqual(0)
    expect(customSettingIndex).toBeLessThan(directoryUploadIndex)
  })
})
