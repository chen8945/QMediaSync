import { mount } from '@vue/test-utils'
import { Document, Folder, Picture, VideoPlay } from '@element-plus/icons-vue'
import { describe, expect, it } from 'vitest'
import { getFileIconByName } from '@/utils/fileIconUtils'

describe('文件图标动态组件', () => {
  it.each([
    { name: '目录.mkv', directory: true, icon: Folder },
    { name: 'movie.MKV', directory: false, icon: VideoPlay },
    { name: 'poster.jpg', directory: false, icon: Picture },
    { name: 'movie.nfo', directory: false, icon: Document },
    { name: 'unknown', directory: false, icon: Document },
  ])('$name 无全局注册也能渲染正确的 SVG', ({ name, directory, icon }) => {
    const wrapper = mount({
      setup: () => ({ icon: getFileIconByName(name, directory) }),
      template: '<component :is="icon" />',
    })
    expect(wrapper.findComponent(icon).exists()).toBe(true)
    expect(wrapper.find('svg path').exists()).toBe(true)
    wrapper.unmount()
  })
})
