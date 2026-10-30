import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

describe('AppFileManager 移动端分页', () => {
  it('使用公共响应式分页并避免 header 额外页大小控件挤占空间', () => {
    const source = readFileSync(resolve('src/views/files/AppFileManager.vue'), 'utf8')

    expect(source).toContain(
      "import ResponsivePagination from '@/components/common/ResponsivePagination.vue'",
    )
    expect(source).toContain('<ResponsivePagination')
    expect(source).not.toContain('style="width: 100px; margin-right: 10px"')
    expect(source).toContain('<el-table\n              v-if="!isMobile"')
    expect(source).toContain('<el-table\n              v-else')
    expect(source).not.toMatch(/<el-table\s+v-if="!isMobile"\s+class="hidden-md-and-down"/)
    expect(source).not.toMatch(/<el-table\s+v-else\s+class="hidden-md-and-up"/)
  })
})
