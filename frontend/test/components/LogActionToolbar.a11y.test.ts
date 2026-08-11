import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('日志操作工具栏无障碍名称', () => {
  it('紧凑模式隐藏文字时仍为每个图标按钮提供 accessible name', () => {
    const source = readFileSync(resolve('src/components/log/LogActionToolbar.vue'), 'utf8')

    for (const label of ['连接实时日志', '断开实时日志', '清空当前显示日志', '下载日志文件']) {
      expect(source).toContain(`aria-label="${label}"`)
    }
  })
})
