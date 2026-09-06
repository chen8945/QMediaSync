import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { precheckStrmRegex, strmRegexListError } from '@/utils/strmRegex'

const cases = JSON.parse(
  readFileSync(
    resolve(__dirname, '../../../backend/internal/validation/testdata/strm_regex_cases.json'),
    'utf8',
  ),
) as { name: string; pattern: string; valid: boolean; browser_error?: string }[]

describe('STRM 正则前后端共享回归', () => {
  it.each(cases.filter((item) => item.valid))('合法表达式不误拦截：$name', ({ pattern }) => {
    expect(precheckStrmRegex(pattern).error).toBeUndefined()
  })

  it.each(cases.filter((item) => item.browser_error))(
    '明确不兼容项能提示：$name',
    ({ pattern, browser_error }) => {
      expect(precheckStrmRegex(pattern).error).toContain(browser_error)
    },
  )

  it('无法预检的语法交后端判断', () => {
    const result = precheckStrmRegex(String.raw`\p{UnknownClass}`)
    expect(result.error).toBeUndefined()
    expect(result.notice).toContain('保存时由服务器校验')
  })

  it('列表允许留空并定位错误条目', () => {
    expect(strmRegexListError([])).toBeUndefined()
    expect(strmRegexListError(['sample', '(?=trailer)'])).toContain('第 2 条')
  })
})
