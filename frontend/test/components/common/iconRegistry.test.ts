import { Cloudy, Setting } from '@element-plus/icons-vue'
import { describe, expect, it } from 'vitest'

import { getIconComponent } from '@/components/common/iconRegistry'

describe('共享图标注册表', () => {
  it('提供云服务图标并保留未知图标的 Setting 回退', () => {
    expect(getIconComponent('Cloudy')).toBe(Cloudy)
    expect(getIconComponent('missing-icon')).toBe(Setting)
    expect(getIconComponent()).toBe(Setting)
  })
})
