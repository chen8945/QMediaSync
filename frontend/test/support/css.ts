// 源码 CSS 契约断言的公共解析工具。
// happy-dom 不计算动画、媒体查询和单文件组件的 <style>，这类样式只能对源码文本做结构断言，
// 因此把解析逻辑集中在这里，避免每个测试各抄一份扫描器。

/** 取出 @media 等 at-rule 的完整块内容，支持块内嵌套花括号。 */
export function extractMediaBlock(css: string, query: string): string {
  const start = css.indexOf(query)
  if (start < 0) {
    throw new Error(`未找到 ${query}`)
  }

  const blockStart = css.indexOf('{', start)
  if (blockStart < 0) {
    throw new Error(`未找到 ${query} 的样式块起始花括号`)
  }

  let depth = 0
  for (let index = blockStart; index < css.length; index += 1) {
    if (css[index] === '{') {
      depth += 1
    }
    if (css[index] === '}') {
      depth -= 1
      if (depth === 0) {
        return css.slice(blockStart + 1, index)
      }
    }
  }

  throw new Error(`未找到 ${query} 的完整样式块`)
}

/** 取出单条选择器规则的声明内容，选择器按字面量匹配。 */
export function extractRule(css: string, selector: string): string {
  const pattern = new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\s*\\{([^}]+)\\}`)
  const match = css.match(pattern)
  if (!match) {
    throw new Error(`未找到 ${selector} 规则`)
  }
  return match[1]
}
