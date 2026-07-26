import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(__dirname, '../../src/components/AppUploadQueue.vue'), 'utf-8')
const queueTaskDetailUtilsSource = readFileSync(
  resolve(__dirname, '../../src/utils/queueTaskDetailUtils.ts'),
  'utf-8',
)

const getLocalFunctionBody = (functionName: string) => {
  const match = new RegExp(
    `const\\s+${functionName}\\s*=\\s*\\([^)]*\\)\\s*(?::\\s*[^=]+)?=>\\s*{`,
  ).exec(source)
  expect(match).not.toBeNull()

  const bodyStart = source.indexOf('{', match?.index)
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

describe('AppUploadQueue 上传队列事件', () => {
  it('展示面向用户的上传状态文案', () => {
    const statusLabels = [
      ['0', '等待上传'],
      ['1', '正在上传'],
      ['2', '上传完成'],
      ['3', '上传失败'],
      ['4', '已取消'],
      ['5', '等待完成处理'],
      ['6', '正在完成处理'],
    ]
    for (const [value, label] of statusLabels) {
      expect(source).toContain(`<el-option label="${label}" :value="${value}"></el-option>`)
      expect(queueTaskDetailUtilsSource).toContain(`return '${label}'`)
    }

    expect(source).toContain('getUploadStatusText,')
    expect(source).toContain("from '@/utils/queueTaskDetailUtils'")
  })

  it('只有明确的上传队列 patch 事件才走局部合并', () => {
    const body = getLocalFunctionBody('isUploadQueuePatch')

    expect(source).toContain(
      "const uploadQueuePatchReasons = ['progress', 'source_cleanup_changed'] as const",
    )
    expect(source).toContain("'source_deleted_at',")
    expect(body).toContain('uploadQueuePatchReasons.includes')
    expect(body.indexOf('data.reason')).toBeLessThan(body.indexOf('progressPatchFields.some'))
  })
})
