import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppScrapePathForm from '@/components/AppScrapePathForm.vue'
import { httpKey } from '@/http/client'

const wrappers: VueWrapper[] = []
const createHTTP = () => ({
  get: vi.fn().mockResolvedValue({ status: 200, data: { code: 200, data: [] } }),
  post: vi.fn(),
})
const mountForm = async (http = createHTTP()) => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/scrape/add', component: { template: '<div />' } }],
  })
  await router.push('/scrape/add')
  const wrapper = mount(AppScrapePathForm, {
    attachTo: document.body,
    global: {
      plugins: [router],
      provide: { [httpKey]: http },
      stubs: { PageHeader: true, DirectorySelector: true },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})
afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  vi.restoreAllMocks()
})

describe('刮削目录表单请求失败行为', () => {
  it('表单校验失败只展示字段错误，不提示保存失败', async () => {
    const http = createHTTP()
    const wrapper = await mountForm(http)
    await wrapper.get('input[value="local"]').setValue()
    await wrapper.get('input[value="only_scrape"]').setValue()
    const submit = wrapper.findAll('button').find((button) => button.text() === '确定添加')
    await submit!.trigger('click')
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.text()).toContain('请选择来源目录'))
    expect(http.post).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })
})
