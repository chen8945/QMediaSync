import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const sourcePath = resolve(process.cwd(), 'src/views/sync/AppSyncDirectoryForm.vue')
const source = readFileSync(sourcePath, 'utf8')

const getFunctionBody = (functionName: string) => {
  const pattern = new RegExp(
    `const\\s+${functionName}\\s*=\\s*(?:async\\s*)?\\([^)]*\\)\\s*(?::\\s*[^=]+)?=>\\s*{`,
  )
  const match = pattern.exec(source)
  expect(match, `${functionName} should exist`).not.toBeNull()

  const bodyStart = source.indexOf('{', match!.index)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1
    if (source[index] === '}') depth -= 1
    if (depth === 0) return source.slice(bodyStart, index + 1)
  }

  throw new Error(`${functionName} body should close`)
}

describe('AppSyncDirectoryForm directory_upload_enabled source of truth', () => {
  it('通过同步目录聚合接口保存总开关和规则', () => {
    const buildPayloadBody = getFunctionBody('buildSaveSyncPathPayload')
    expect(buildPayloadBody).toContain('directory_upload:')
    expect(buildPayloadBody).toContain('enabled: form.directory_upload_enabled')
    expect(buildPayloadBody).toContain('rules: rulesToSave.map(buildDirectoryUploadPayload)')

    const handleSubmitBody = getFunctionBody('handleSubmit')
    expect(handleSubmitBody).toContain('syncDirectorySave.saveAndRun')
    expect(handleSubmitBody).toContain('buildSaveSyncPathPayload()')
  })
})
