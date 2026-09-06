// @vitest-environment happy-dom
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AppStrmSettings from '@/components/AppStrmSettings.vue'
import AppSyncDirectoryForm from '@/components/AppSyncDirectoryForm.vue'
import { httpKey } from '@/http/client'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '12' } }),
  useRouter: () => ({ replace: vi.fn(), back: vi.fn() }),
}))

type Page = 'global' | 'directory'
const wrappers: VueWrapper[] = []
const originalWidth = window.innerWidth

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  window.innerWidth = originalWidth
  vi.restoreAllMocks()
})

function createHTTP(patterns: string[] | undefined = ['^Before$']) {
  let settings = {
    video_ext_arr: ['.mkv'],
    meta_ext_arr: ['.nfo'],
    exclude_name_arr: ['sample'],
    exclude_name_regex_arr: patterns,
    min_video_size: 0,
    cron: '0 * * * *',
    strm_base_url: 'http://qms.local',
    upload_meta: 0,
    download_meta: 0,
    delete_dir: 0,
    local_proxy: 0,
    add_path: 3,
    check_meta_mtime: 0,
  }
  const success = {
    data: {
      code: 200,
      data: {
        sync_path: { id: 12 },
        directory_upload: { enabled: false, rules: [] },
        warnings: [],
      },
    },
  }
  const http = {
    get: vi.fn(async (url: string) => {
      if (url.endsWith('/version')) return { data: { isWindows: false } }
      if (url.endsWith('/setting/cron')) return { data: { code: 200, data: [] } }
      if (url.includes('/sync/path/')) {
        return {
          data: {
            code: 200,
            data: {
              ...settings,
              id: 12,
              source_type: 'local',
              account_id: 0,
              base_cid: '/media',
              local_path: '/strm',
              remote_path: '/media',
              custom_config: true,
              enable_cron: false,
            },
          },
        }
      }
      return { data: { code: 200, data: { ...settings } } }
    }),
    post: vi.fn(async (_url: string, body: typeof settings) => {
      settings = JSON.parse(JSON.stringify(body))
      return success
    }),
    put: vi.fn(async (_url: string, body: { sync_path: { setting: typeof settings } }) => {
      settings = JSON.parse(JSON.stringify(body.sync_path.setting))
      return success
    }),
  }
  return http
}

async function mountPage(page: Page, http: ReturnType<typeof createHTTP>, width = 1280) {
  window.innerWidth = width
  const wrapper = mount(page === 'global' ? AppStrmSettings : AppSyncDirectoryForm, {
    global: {
      provide: { [httpKey]: http },
      stubs: { PageHeader: true, DirectorySelector: true, MetadataExtInput: true },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

async function save(wrapper: VueWrapper, page: Page) {
  const label = page === 'global' ? '保存 STRM 配置' : '保存修改'
  const button = wrapper.findAll('button').find((item) => item.text() === label)
  expect(button, `${label} 按钮应存在`).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

describe('STRM 排除规则保存与读回', () => {
  it.each([
    { page: 'global' as const, width: 1280 },
    { page: 'global' as const, width: 375 },
    { page: 'directory' as const, width: 1280 },
    { page: 'directory' as const, width: 375 },
  ])('$page 在 $width px 下按原文保存并读回', async ({ page, width }) => {
    vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
    const http = createHTTP()
    const wrapper = await mountPage(page, http, width)
    const pattern = String.raw`  (?i)Sample{1,3},Trailer;\D+  `
    const input = wrapper.get('input[aria-label="正则排除名称"]')
    await input.setValue(pattern)
    await input.trigger('keydown', { key: 'Enter' })
    await save(wrapper, page)

    const request = page === 'global' ? http.post : http.put
    expect(request).toHaveBeenCalledOnce()
    expect(http.get.mock.calls.every(([url]) => !url.includes('regex'))).toBe(true)
    wrappers.pop()!.unmount()
    const reloaded = await mountPage(page, http, width)
    expect(reloaded.findAll('.el-tag code').map((tag) => tag.element.textContent)).toEqual([
      '^Before$',
      pattern,
    ])
    expect(reloaded.text()).toContain('排除名称')
    expect(reloaded.text()).toContain('不区分大小写')
  })

  it.each(['global', 'directory'] as const)('%s 保存前预检已加载的规则', async (page) => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
    const http = createHTTP(['sample', '(?=trailer)'])
    const wrapper = await mountPage(page, http)
    await save(wrapper, page)

    expect(http.post).not.toHaveBeenCalled()
    expect(http.put).not.toHaveBeenCalled()
    await vi.waitFor(() => expect(wrapper.text()).toContain('第 2 条'))
    expect(wrapper.text()).toContain('前后向断言')
  })

  it.each(['global', 'directory'] as const)(
    '%s 展示后端最终校验失败原因并保留规则',
    async (page) => {
      vi.spyOn(console, 'error').mockImplementation(() => {})
      vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
      const pattern = String.raw`\p{UnknownClass}`
      const http = createHTTP([pattern])
      const error = {
        response: {
          data: {
            message: 'exclude_name_regex_arr[0]：正则表达式无效',
            data: {
              error_code: 'INVALID_REQUEST',
              field_errors: [{ field: 'exclude_name_regex_arr[0]', message: '未知字符类' }],
            },
          },
        },
      }
      http.post.mockRejectedValueOnce(error)
      http.put.mockRejectedValueOnce(error)
      const wrapper = await mountPage(page, http)
      await save(wrapper, page)

      expect(page === 'global' ? http.post : http.put).toHaveBeenCalledOnce()
      expect(wrapper.get('.el-tag code').element.textContent).toBe(pattern)
      await vi.waitFor(() =>
        expect(wrapper.text()).toContain(
          page === 'global' ? '正则表达式无效' : '第 1 条：未知字符类',
        ),
      )
      expect(wrapper.text()).not.toContain('检查网络连接')
      await wrapper.get('.el-tag__close').trigger('click')
      await save(wrapper, page)
      expect(page === 'global' ? http.post : http.put).toHaveBeenCalledTimes(2)
      wrappers.pop()!.unmount()
      const reloaded = await mountPage(page, http)
      expect(reloaded.findAll('.el-tag code')).toHaveLength(0)
    },
  )
})
