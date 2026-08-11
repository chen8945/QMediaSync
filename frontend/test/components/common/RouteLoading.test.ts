import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const readSource = (path: string) => readFileSync(resolve(path), 'utf8')

describe('路由加载态分层契约', () => {
  it('只表达页面模块加载并在移动端隐藏重复页面标题', () => {
    const source = readSource('src/components/common/RouteLoading.vue')

    expect(source).toContain('role="status"')
    expect(source).toContain('aria-live="polite"')
    expect(source).toContain('aria-busy="true"')
    expect(source).toContain('加载中…')
    expect(source).toContain('.route-loading-title {')
    expect(source).toContain('display: none;')
    expect(source).toContain('prefers-reduced-motion: reduce')
  })

  it('错误态保留可键盘操作的重试按钮和移动端标题策略', () => {
    const source = readSource('src/components/common/RouteLoadError.vue')

    expect(source).toContain('role="alert"')
    expect(source).toContain('type="button"')
    expect(source).toContain('aria-label="重新加载页面"')
    expect(source).toContain('@media (max-width: 768px)')
    expect(source).toContain('.route-load-error-title {')
  })
})
