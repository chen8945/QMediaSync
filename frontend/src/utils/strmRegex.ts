export interface StrmRegexPrecheck {
  error?: string
  notice?: string
}

const backendNotice = '此表达式包含浏览器无法完整预检的语法，保存时由服务器校验。'

// ponytail: 仅适配语法预检副本，不模拟 Go 匹配；其余语法差异交由现有保存接口校验。
export function precheckStrmRegex(pattern: string): StrmRegexPrecheck {
  if (pattern === '') return { error: '正则表达式不能为空' }

  let source = ''
  let inClass = false
  let classStart = 0
  let needsBackend = /[\uD800-\uDFFF]/.test(pattern)

  for (let i = 0; i < pattern.length; i++) {
    const char = pattern[i]
    const rest = pattern.slice(i)

    if (char === '\\') {
      const next = pattern[i + 1]
      if (!inClass && next === 'Q') {
        const end = pattern.indexOf('\\E', i + 2)
        const literal = pattern.slice(i + 2, end < 0 ? undefined : end)
        source += literal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
        i = end < 0 ? pattern.length : end + 1
        continue
      }
      if ((next === 'x' || next === 'p' || next === 'P') && pattern[i + 2] === '{') {
        const end = pattern.indexOf('}', i + 3)
        needsBackend = true
        source += pattern.slice(i, end < 0 ? undefined : end + 1)
        i = end < 0 ? pattern.length : end
        continue
      }
      if (!inClass && (next === 'k' || next === 'g') && /[<'{]/.test(pattern[i + 2] || '')) {
        return { error: 'Go/RE2 不支持反向引用（如 \\k<name>）' }
      }
      if (next && /[0-7]/.test(next)) {
        const octal = pattern.slice(i + 1).match(/^[0-7]{1,3}/)![0]
        if (next !== '0' && octal.length === 1) {
          return { error: 'Go/RE2 不支持反向引用（如 \\1）；八进制转义请使用两到三位数字' }
        }
        source += `\\u${Number.parseInt(octal, 8).toString(16).padStart(4, '0')}`
        i += octal.length
        continue
      }
      if (next === '8' || next === '9') {
        return { error: 'Go/RE2 不支持反向引用（如 \\1）' }
      }
      if (!inClass && (next === 'A' || next === 'z' || next === 'b' || next === 'B')) {
        const anchor = next === 'A' ? '^' : next === 'z' ? '$' : `\\${next}`
        source += `(?:${anchor})`
      } else if (next === 'a') {
        source += '\\x07'
      } else {
        if (next === 'p' || next === 'P') {
          needsBackend = true
        }
        source += pattern.slice(i, i + 2)
      }
      i++
      continue
    }

    if (inClass) {
      if (rest.startsWith('[:')) {
        const end = pattern.indexOf(':]', i + 2)
        if (end >= 0) {
          source += 'a'
          needsBackend = true
          i = end + 1
          continue
        }
      }
      if (char === ']') {
        if (i === classStart || (i === classStart + 1 && pattern[classStart] === '^')) {
          source += '\\]'
          continue
        }
        inClass = false
      }
      source += char
      continue
    }

    if (char === '[') {
      inClass = true
      classStart = i + 1
    } else if (char === '(') {
      if (/^\(\?(?:[=!]|<[=!])/.test(rest)) {
        return { error: 'Go/RE2 不支持前后向断言（如 (?=...)、(?<=...)）' }
      }
      if (rest.startsWith('(?P=')) {
        return { error: 'Go/RE2 不支持反向引用（如 (?P=name)）' }
      }
      if (rest.startsWith('(?>')) {
        return { error: 'Go/RE2 不支持原子组（如 (?>...)）' }
      }
      const flags = rest.match(/^\(\?([imsU]*)(?:-([imsU]+))?([:)])/)
      if (flags) {
        if (flags[3] === ')' && /[*+?}]$/.test(source)) needsBackend = true
        source += flags[3] === ':' ? '(?:' : ''
        i += flags[0].length - 1
        continue
      }
      const namedGroup = rest.match(/^\(\?P?<[^>]+>/)
      if (namedGroup) {
        source += '(?:'
        i += namedGroup[0].length - 1
        continue
      }
    } else if (char === '{') {
      const repeat = rest.match(/^\{(0|[1-9]\d*)(?:,(0|[1-9]\d*)?)?\}/)
      if (repeat) {
        if (Number(repeat[1]) > 1000 || Number(repeat[2]) > 1000) {
          return { error: 'Go/RE2 的单个重复次数不能超过 1000' }
        }
        if (rest[repeat[0].length] === '+') {
          return { error: 'Go/RE2 不支持占有量词（如 a++、a{1,3}+）' }
        }
        source += repeat[0]
        i += repeat[0].length - 1
        continue
      }
      // Go 将含前导零的重复次数等写法视为字面量，浏览器的解析可能不同。
      if (/^\{\d/.test(rest)) needsBackend = true
    } else if ('*+?'.includes(char) && pattern[i + 1] === '+') {
      return { error: 'Go/RE2 不支持占有量词（如 a++、a{1,3}+）' }
    } else if (char === '^' || char === '$') {
      // Go 允许重复零宽断言，临时加组避免浏览器误报“无可重复内容”。
      source += `(?:${char})`
      continue
    }
    source += char
  }

  try {
    new RegExp(source)
  } catch {
    if (!needsBackend) return { error: '正则格式错误，请检查括号、字符组和量词' }
  }
  return needsBackend ? { notice: backendNotice } : {}
}

export function strmRegexListError(patterns: string[]): string | undefined {
  for (const [index, pattern] of patterns.entries()) {
    const { error } = precheckStrmRegex(pattern)
    if (error) return `第 ${index + 1} 条：${error}`
  }
}
