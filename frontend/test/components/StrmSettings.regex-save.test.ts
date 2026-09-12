// @vitest-environment happy-dom
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElFormItem, ElMessage } from 'element-plus'
import { afterEach, describe, expect, it, vi } from 'vitest'
import AppStrmSettings from '@/components/AppStrmSettings.vue'
import AppSyncDirectoryForm from '@/components/AppSyncDirectoryForm.vue'
import { httpKey } from '@/http/client'

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '12' } }),
  useRouter: () => ({ replace: vi.fn(), back: vi.fn() }),
}))

type Page = 'global' | 'directory'
const listFields = ['video_ext', 'meta_ext', 'exclude_name', 'exclude_name_regex'] as const
type StrmLists = Record<`${(typeof listFields)[number]}_arr`, string[]>
const wrappers: VueWrapper[] = []
const originalWidth = window.innerWidth

afterEach(() => {
  wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
  window.innerWidth = originalWidth
  vi.restoreAllMocks()
})

function createHTTP(
  patterns: string[] | undefined = ['^Before$'],
  globalLists: Partial<StrmLists> = {},
) {
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
      return { data: { code: 200, data: { ...settings, ...globalLists } } }
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
    attachTo: document.body,
    global: {
      provide: { [httpKey]: http },
      stubs: { PageHeader: true, DirectorySelector: true },
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

function listItem(wrapper: VueWrapper, field: string) {
  const item = wrapper.findAllComponents(ElFormItem).find((item) => item.props('prop') === field)
  expect(item, field + ' 控件应存在').toBeDefined()
  return item!
}

function listValues(item: VueWrapper) {
  return item.findAll('.el-tag').map((tag) => {
    const code = tag.find('code')
    return code.exists() ? code.element.textContent : tag.get('.el-tag__content').text()
  })
}

function listButton(item: VueWrapper, label: string) {
  const button = item.findAll('button').find((button) => button.text() === label)
  expect(button, label + ' 按钮应存在').toBeDefined()
  return button!
}

describe('STRM 列表导入与清空', () => {
  it.each([1280, 375])(
    '目录表单在 %i px 下合并四类全局列表，重复导入不增加重复项',
    async (width) => {
      vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
      const pattern = '  (?i)Sample{1,3},Trailer;\\D+  '
      const globalLists: StrmLists = {
        video_ext_arr: ['.MKV', '.mp4', '.avi', '.AVI'],
        meta_ext_arr: ['.NFO', '.srt', '.ass', '.ASS'],
        exclude_name_arr: ['SAMPLE', 'trailer', 'Extras', 'extras'],
        exclude_name_regex_arr: ['^Before$', '^before$', '\\D+', '\\d+', pattern, pattern, ' '],
      }
      const http = createHTTP(undefined, globalLists)
      const wrapper = await mountPage('directory', http, width)
      const cases = [
        { field: 'video_ext', draft: 'MP4', expected: ['.mkv', '.MP4', '.avi'] },
        { field: 'meta_ext', draft: 'SRT', expected: ['.nfo', '.SRT', '.ass'] },
        { field: 'exclude_name', draft: 'Trailer', expected: ['sample', 'Trailer', 'Extras'] },
        {
          field: 'exclude_name_regex',
          draft: '\\D+',
          expected: ['^Before$', '\\D+', '^before$', '\\d+', pattern, ' '],
        },
      ]
      expect(wrapper.text()).toContain('保存后生效')
      expect(wrapper.text()).toContain('填写后覆盖全局列表')
      for (const { field, draft, expected } of cases) {
        const item = listItem(wrapper, field)
        await listButton(item, '+ 添加').trigger('click')
        await item.get('input').setValue(draft)
        await item.get('input').trigger('keydown', { key: 'Enter' })
        for (let attempt = 0; attempt < 2; attempt++) {
          await listButton(item, '导入全局设置').trigger('click')
          await flushPromises()
          expect(listValues(item)).toEqual(expected)
        }
      }
      expect(http.post).not.toHaveBeenCalled()
      expect(http.put).not.toHaveBeenCalled()
      await save(wrapper, 'directory')
      expect(http.put).toHaveBeenCalledOnce()
      expect(http.put.mock.calls[0]![1].sync_path.setting).toMatchObject(
        Object.fromEntries(cases.map(({ field, expected }) => [field + '_arr', expected])),
      )
      wrappers.pop()!.unmount()
      const reloaded = await mountPage('directory', http, width)
      for (const { field, expected } of cases) {
        expect(listValues(listItem(reloaded, field))).toEqual(expected)
      }
    },
  )

  it.each([
    { page: 'global' as const, width: 1280 },
    { page: 'global' as const, width: 375 },
    { page: 'directory' as const, width: 1280 },
    { page: 'directory' as const, width: 375 },
  ])('$page 在 $width px 下逐项清空，保存前不写入服务器', async ({ page, width }) => {
    vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
    const http = createHTTP()
    const wrapper = await mountPage(page, http, width)
    const items = listFields.map((field) =>
      listItem(wrapper, page === 'global' ? field + '_arr' : field),
    )
    const expected = items.map(listValues)
    for (let index = 0; index < items.length; index++) {
      const item = items[index]!
      await listButton(item, '清空').trigger('click')
      const popups = () =>
        new DOMWrapper(document.body).findAll('.el-popconfirm').filter((popup) => popup.isVisible())
      await vi.waitFor(() => expect(popups()).toHaveLength(1))
      const popup = popups()[0]!
      await popup
        .findAll('button')
        .find((button) => button.text() === '清空')!
        .trigger('click')
      await flushPromises()
      await vi.waitFor(() => expect(popup.isVisible()).toBe(false))
      expected[index] = []
      expect(items.map(listValues)).toEqual(expected)
      expect(listButton(item, '清空').element.disabled).toBe(true)
    }
    expect(http.post).not.toHaveBeenCalled()
    expect(http.put).not.toHaveBeenCalled()
    await save(wrapper, page)
    const request = page === 'global' ? http.post : http.put
    expect(request).toHaveBeenCalledOnce()
    const payload =
      page === 'global' ? http.post.mock.calls[0]![1] : http.put.mock.calls[0]![1].sync_path.setting
    expect(payload).toMatchObject(
      Object.fromEntries(listFields.map((field) => [field + '_arr', []])),
    )
    if (page === 'directory') {
      wrappers.pop()!.unmount()
      const reloaded = await mountPage(page, http, width)
      for (const field of listFields) {
        expect(listValues(listItem(reloaded, field))).toEqual([])
      }
      expect(reloaded.text()).toContain('列表为空时继承全局设置')
    } else {
      expect(wrapper.text()).toContain('列表为空并保存后使用配置默认扩展名')
    }
  })

  it('导入期间禁用编辑和保存，请求失败或全局列表为空时保留当前内容', async () => {
    vi.spyOn(ElMessage, 'success').mockImplementation(() => undefined as never)
    const error = vi.spyOn(ElMessage, 'error').mockImplementation(() => undefined as never)
    const http = createHTTP(undefined, { video_ext_arr: [] })
    const wrapper = await mountPage('directory', http)
    const item = listItem(wrapper, 'video_ext')
    let rejectImport!: (reason: Error) => void
    http.get.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectImport = reject
        }),
    )
    await listButton(item, '导入全局设置').trigger('click')
    expect(listButton(item, '+ 添加').element.disabled).toBe(true)
    expect(listButton(item, '清空').element.disabled).toBe(true)
    expect(listButton(wrapper, '保存修改').element.disabled).toBe(true)
    await listButton(wrapper, '保存修改').trigger('click')
    expect(http.put).not.toHaveBeenCalled()
    rejectImport(new Error('network unavailable'))
    await flushPromises()
    expect(error).toHaveBeenCalledWith('获取全局 STRM 设置失败')
    expect(listValues(item)).toEqual(['.mkv'])
    expect(listButton(item, '清空').element.disabled).toBe(false)
    expect(listButton(wrapper, '保存修改').element.disabled).toBe(false)
    await listButton(item, '导入全局设置').trigger('click')
    await flushPromises()
    expect(listValues(item)).toEqual(['.mkv'])
    expect(http.put).not.toHaveBeenCalled()
  })
})

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
    expect(wrapper.find('input[aria-label="正则排除名称"]').exists()).toBe(false)
    await wrapper
      .get('.strm-regex-input')
      .findAll('button')
      .find((button) => button.text() === '+ 添加')!
      .trigger('click')
    const input = wrapper.get('input[aria-label="正则排除名称"]')
    await input.setValue(pattern)
    await input.trigger('keydown', { key: 'Enter' })
    expect(wrapper.find('input[aria-label="正则排除名称"]').exists()).toBe(false)
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
      await wrapper.get('.strm-regex-input .el-tag__close').trigger('click')
      await save(wrapper, page)
      expect(page === 'global' ? http.post : http.put).toHaveBeenCalledTimes(2)
      wrappers.pop()!.unmount()
      const reloaded = await mountPage(page, http)
      expect(reloaded.findAll('.el-tag code')).toHaveLength(0)
    },
  )
})
