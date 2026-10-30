import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const source = readFileSync(
  resolve(__dirname, '../../src/views/sync/AppSyncDirectoryForm.vue'),
  'utf-8',
)

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

describe('AppSyncDirectoryForm 目录监控保存防呆', () => {
  it('提交保存前先触发 Element Plus 表单校验，不能只依赖 blur 规则', () => {
    const handleSubmitBody = getFunctionBody('handleSubmit')
    const validateIndex = handleSubmitBody.indexOf('await formRef.value.validate()')
    const saveIndex = handleSubmitBody.indexOf('syncDirectorySave.saveAndRun')

    expect(validateIndex).toBeGreaterThanOrEqual(0)
    expect(saveIndex).toBeGreaterThanOrEqual(0)
    expect(validateIndex).toBeLessThan(saveIndex)
  })

  it('目录监控规则加载失败时阻止保存，避免把空数组当最终集合提交', () => {
    expect(source).toContain('directoryUploadRulesLoadFailed')
    expect(source).toContain('目录监控上传规则加载失败，请刷新或重试后再保存')
    expect(source).toContain('if (directoryUploadRulesLoadFailed.value)')
  })

  it('关闭总开关时只清理自动创建且未填写的草稿规则', () => {
    expect(source).toContain('autoCreated')
    expect(source).toContain('removeCanceledDirectoryUploadDraftRules')
    expect(source).toContain('if (!nextEnabled)')
  })

  it('关闭总开关时存在手动新增未完成规则，应阻止关闭并提示补完整或删除', () => {
    expect(source).toContain('hasIncompleteManualDirectoryUploadDraftRules')
    expect(source).toContain('请补完整目录监控上传规则，或删除未完成的规则后再关闭')
    expect(source).toContain('form.directory_upload_enabled = true')
  })

  it('未完成规则会在对应目录输入项上展示字段级错误', () => {
    expect(source).toContain('directoryUploadRuleFieldErrors')
    expect(source).toContain('getDirectoryUploadRuleFieldError')
    expect(source).toMatch(/:error="getDirectoryUploadRuleFieldError\(rule,\s*'monitor_path'\)"/)
    expect(source).toMatch(
      /:error="getDirectoryUploadRuleFieldError\(rule,\s*'remote_root_path'\)"/,
    )
  })

  it('后端返回的目录监控规则字段错误都有可见展示点', () => {
    expect(source).toContain('DirectoryUploadRuleField')
    expect(source).toMatch(/:error="getDirectoryUploadRuleFieldError\(rule,\s*'watch_mode'\)"/)
    expect(source).toMatch(/:error="getDirectoryUploadRuleFieldError\(rule,\s*'overwrite_mode'\)"/)
    expect(source).toContain("getDirectoryUploadRuleFieldError(rule, 'remote_root_id')")
    expect(source).toContain('getDirectoryUploadRuleGeneralError(rule)')
  })

  it('保存接口返回基础字段错误时，将错误定位到 Element Plus 表单字段', () => {
    expect(source).toContain('syncPathFieldErrors')
    expect(source).toContain('getSyncPathFieldError')
    expect(source).toContain('fieldError.client_id')
    expect(source).toContain(':error="getSyncPathFieldError(\'local_path\')"')
  })
})
