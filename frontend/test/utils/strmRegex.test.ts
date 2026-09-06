import { describe, expect, it } from 'vitest'
import { precheckStrmRegex, strmRegexListError } from '@/utils/strmRegex'

describe('STRM 正则预检', () => {
  it.each([
    'sample',
    '(?i)(sample|trailer)',
    '(?im-s)^sample$',
    '(?U)sample.*',
    '(?i:abc)(?-i:XYZ)',
    'a*(?i)*',
    String.raw`\A(?P<name>sample)\z`,
    String.raw`\Q(?=sample),a++;\E`,
    '[()+?=]+',
    String.raw`\\1`,
    String.raw`\123`,
    String.raw`\12`,
    'a{1,3}',
    'a{1000}',
    '  Sample,Trailer;  ',
  ])('允许常用表达式 %s', (pattern) => {
    expect(precheckStrmRegex(pattern).error).toBeUndefined()
  })

  it.each([
    ['(?=sample)', '前后向断言'],
    ['(?<!sample)', '前后向断言'],
    [String.raw`(sample)\1`, '反向引用'],
    [String.raw`(?P<name>sample)\k<name>`, '反向引用'],
    ['(?P=name)', '反向引用'],
    ['(?>sample)', '原子组'],
    ['a++', '占有量词'],
    ['a{1,3}+', '占有量词'],
    ['a{1001}', '1000'],
    ['[sample', '格式错误'],
    ['', '不能为空'],
  ])('为明确不兼容表达式 %s 提供提示', (pattern, message) => {
    expect(precheckStrmRegex(pattern).error).toContain(message)
  })

  it.each([String.raw`\p{Han}+`, String.raw`[\x{1001}-\x{1003}]`, 'a{02}+'])(
    '无法可靠预检的 Go 语法 %s 交后端校验',
    (pattern) => {
      const result = precheckStrmRegex(pattern)
      expect(result.error).toBeUndefined()
      expect(result.notice).toContain('保存时由服务器校验')
    },
  )

  it('列表允许留空并定位错误条目', () => {
    expect(strmRegexListError([])).toBeUndefined()
    expect(strmRegexListError(['sample', '(?=trailer)'])).toContain('第 2 条')
  })
})
