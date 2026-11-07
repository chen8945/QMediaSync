import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage, ElSelect } from 'element-plus'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppScrapePathForm from '@/views/scrape/AppScrapePathForm.vue'
import { httpKey } from '@/http/client'
import type { ScrapePath } from '@/api/scrapePaths'

const DialogStub = defineComponent({
  props: { modelValue: Boolean },
  template: '<section v-if="modelValue" role="dialog"><slot /></section>',
})
const SelectorStub = defineComponent({
  name: 'DirectorySelector',
  emits: ['update:modelValue', 'select', 'cancel'],
  template: '<div />',
})
const directory: ScrapePath = {
  id: 12,
  source_type: 'local',
  account_id: 0,
  media_type: 'tvshow',
  source_path: '/source',
  source_path_id: '/source',
  dest_path: '/dest',
  dest_path_id: '/dest',
  scrape_type: 'scrape_and_rename',
  rename_type: 'copy',
  enable_category: false,
  folder_name_template: 'custom/{title}',
  file_name_template: '',
  delete_keyword: [],
  min_video_file_size: 0,
  video_ext_list: ['.mkv'],
  exclude_no_image_actor: false,
  enable_ai: 'off',
  ai_prompt: '',
  force_delete_source_path: false,
  enable_cron: false,
  cron_expression: '',
  enable_fanart_tv: false,
  max_threads: 5,
  is_running: 0,
}
const accounts = [
  { id: 1, source_type: '115', name: '账号 A', authorized: true },
  { id: 2, source_type: '115', name: '账号 B', authorized: true },
  { id: 3, source_type: 'openlist', name: 'OpenList', authorized: true },
]

const mountForm = async (existing?: Partial<ScrapePath>) => {
  const http = {
    get: vi.fn(async (url: string) => ({
      status: 200,
      data: {
        code: 200,
        data: url.endsWith('/scrape/pathes/12') ? { ...directory, ...existing } : accounts,
      },
    })),
    post: vi.fn().mockResolvedValue({ status: 200, data: { code: 200, data: null } }),
  }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/scrape/add', component: { template: '<div />' } },
      { path: '/scrape/edit/:id', component: { template: '<div />' } },
      { path: '/scrape/pathes', name: 'scrape-pathes', component: { template: '<div />' } },
    ],
  })
  await router.push(existing ? '/scrape/edit/12' : '/scrape/add')
  const wrapper = mount(AppScrapePathForm, {
    attachTo: document.body,
    global: {
      plugins: [router],
      provide: { [httpKey]: http },
      stubs: { PageHeader: true, ElDialog: DialogStub, DirectorySelector: SelectorStub },
    },
  })
  await flushPromises()
  const submit = async () => {
    await wrapper
      .findAll('button')
      .find((button) => button.text() === (existing ? '保存修改' : '确定添加'))!
      .trigger('click')
    await flushPromises()
  }
  return { wrapper, http, submit }
}
const field = (wrapper: VueWrapper, label: string) =>
  wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === label)!
const choose = async (wrapper: VueWrapper, value: string) => {
  await wrapper.get(`input[type="radio"][value="${value}"]`).setValue()
  await flushPromises()
}
const chooseAccount = async (wrapper: VueWrapper, id: number) => {
  const select = field(wrapper, '网盘账号').findComponent(ElSelect)
  select.vm.$emit('update:modelValue', id)
  select.vm.$emit('change', id)
  await flushPromises()
}
const chooseDirectory = async (wrapper: VueWrapper, label: string, id: string, path = id) => {
  await field(wrapper, label).get('button').trigger('click')
  const selector = wrapper.findComponent(SelectorStub)
  selector.vm.$emit('update:modelValue', { id, path, name: path })
  await nextTick()
  selector.vm.$emit('select')
  await nextTick()
}
const fillDirectories = async (wrapper: VueWrapper, prefix = '') => {
  await chooseDirectory(wrapper, '来源路径', `${prefix}/source`)
  await chooseDirectory(wrapper, '目标路径', `${prefix}/dest`)
}
const selectedRenameType = (wrapper: VueWrapper) =>
  field(wrapper, '整理方式').find('input:checked').attributes('value')

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe.each([390, 1280])('刮削目录选项联动（宽度 %i）', (width) => {
  beforeEach(() => vi.stubGlobal('innerWidth', width))

  it('新增不默认选择整理方式，未选择时阻止保存并提示', async () => {
    const { wrapper, http, submit } = await mountForm()
    await choose(wrapper, 'local')
    await fillDirectories(wrapper)
    expect(field(wrapper, '整理方式').find('input:checked').exists()).toBe(false)
    await submit()
    await vi.waitFor(() => expect(wrapper.text()).toContain('请选择整理方式'))
    expect(http.post).not.toHaveBeenCalled()
  })

  it('仅刮削不要求整理方式，提交 same，切回整理仍未选中', async () => {
    const { wrapper, http, submit } = await mountForm()
    await choose(wrapper, 'local')
    await choose(wrapper, 'only_scrape')
    await chooseDirectory(wrapper, '来源路径', '/source')
    await submit()
    expect(http.post).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ rename_type: 'same' }),
    )
    await choose(wrapper, 'only_rename')
    expect(field(wrapper, '整理方式').find('input:checked').exists()).toBe(false)
  })

  it.each(['move', 'copy'])('保留 %s 选择，经过仅刮削后切回仍按原选择提交', async (renameType) => {
    const { wrapper, http, submit } = await mountForm()
    await choose(wrapper, 'local')
    await choose(wrapper, renameType)
    await fillDirectories(wrapper)
    await choose(wrapper, 'only_scrape')
    await submit()
    expect(http.post).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.objectContaining({ rename_type: 'same' }),
    )
    await choose(wrapper, 'only_rename')
    expect(selectedRenameType(wrapper)).toBe(renameType)
    await submit()
    expect(http.post).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.objectContaining({ rename_type: renameType }),
    )
  })

  it.each(['soft_symlink', 'hard_symlink'])(
    '切换远程来源后清空 %s，仍禁用链接且不自动选择移动',
    async (renameType) => {
      const { wrapper } = await mountForm()
      await choose(wrapper, 'local')
      await choose(wrapper, renameType)
      await choose(wrapper, '115')
      expect(field(wrapper, '整理方式').find('input:checked').exists()).toBe(false)
      expect(wrapper.get(`input[value="${renameType}"]`).attributes('disabled')).toBeDefined()
    },
  )

  it('仅刮削隐藏的链接选择也在切换远程来源后失效', async () => {
    const { wrapper, http, submit } = await mountForm()
    await choose(wrapper, 'local')
    await choose(wrapper, 'soft_symlink')
    await choose(wrapper, 'only_scrape')
    await choose(wrapper, '115')
    await chooseAccount(wrapper, 1)
    await choose(wrapper, 'scrape_and_rename')
    await fillDirectories(wrapper)
    await submit()
    await vi.waitFor(() => expect(wrapper.text()).toContain('请选择整理方式'))
    expect(http.post).not.toHaveBeenCalled()
  })

  it.each(['move', 'copy'])(
    '切换来源保留合法的 %s，清理账号与旧目录后提交新目录',
    async (renameType) => {
      const { wrapper, http, submit } = await mountForm()
      await chooseAccount(wrapper, 1)
      await choose(wrapper, renameType)
      await fillDirectories(wrapper, '/old')
      await choose(wrapper, 'openlist')
      expect(field(wrapper, '网盘账号').findComponent(ElSelect).props('modelValue')).toBe(0)
      expect(wrapper.text()).not.toContain('/old/source')
      await chooseAccount(wrapper, 3)
      expect(field(wrapper, '来源路径').get('input').element.value).toBe('')
      expect(field(wrapper, '目标路径').get('input').element.value).toBe('')
      expect(selectedRenameType(wrapper)).toBe(renameType)
      await fillDirectories(wrapper, '/new')
      await submit()
      expect(http.post).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          account_id: 3,
          source_type: 'openlist',
          source_path: '/new/source',
          source_path_id: '/new/source',
          dest_path: '/new/dest',
          dest_path_id: '/new/dest',
          rename_type: renameType,
        }),
      )
    },
  )

  it('切换同来源账号清理目录和选择器草稿，保留合法整理方式', async () => {
    const { wrapper, http, submit } = await mountForm()
    await chooseAccount(wrapper, 1)
    await choose(wrapper, 'copy')
    await fillDirectories(wrapper, '/old')
    await field(wrapper, '来源路径').get('button').trigger('click')
    const staleSelector = wrapper.findComponent(SelectorStub)
    staleSelector.vm.$emit('update:modelValue', {
      id: 'old-draft',
      path: '/old/draft',
      name: 'old',
    })
    await nextTick()
    await chooseAccount(wrapper, 2)
    staleSelector.vm.$emit('update:modelValue', {
      id: 'old-draft',
      path: '/old/draft',
      name: 'old',
    })
    staleSelector.vm.$emit('select')
    await nextTick()
    expect(field(wrapper, '来源路径').get('input').element.value).toBe('')
    expect(field(wrapper, '目标路径').get('input').element.value).toBe('')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(selectedRenameType(wrapper)).toBe('copy')
    await fillDirectories(wrapper, '/new')
    await submit()
    expect(http.post).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        account_id: 2,
        source_path: '/new/source',
        source_path_id: '/new/source',
        dest_path: '/new/dest',
        dest_path_id: '/new/dest',
      }),
    )
  })

  it.each([
    { media_type: 'other', file_name_template: '{num} - {title}' },
    { media_type: 'tvshow', file_name_template: '{{ title }} - {{ season_episode }}' },
    { media_type: 'tvshow', file_name_template: '' },
  ])('编辑 $media_type 时禁用媒体类型，回填及保存保留模板 $file_name_template', async (saved) => {
    const { wrapper, http, submit } = await mountForm({
      ...saved,
      scrape_type: saved.media_type === 'other' ? 'only_rename' : 'scrape_and_rename',
    })
    for (const input of field(wrapper, '媒体类型').findAll('input')) {
      expect(input.attributes('disabled')).toBeDefined()
    }
    expect(field(wrapper, '文件夹重命名模板').get('input').element.value).toBe(
      directory.folder_name_template,
    )
    expect(field(wrapper, '文件重命名模板').get('input').element.value).toBe(
      saved.file_name_template,
    )
    expect(selectedRenameType(wrapper)).toBe('copy')
    await submit()
    expect(http.post).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({
        source_path: directory.source_path,
        dest_path: directory.dest_path,
        folder_name_template: directory.folder_name_template,
        file_name_template: saved.file_name_template,
      }),
    )
  })

  it('编辑仅刮削目录切回整理时要求手动选择', async () => {
    const { wrapper, http, submit } = await mountForm({
      scrape_type: 'only_scrape',
      rename_type: 'same',
    })
    await choose(wrapper, 'only_rename')
    expect(field(wrapper, '整理方式').find('input:checked').exists()).toBe(false)
    await submit()
    await vi.waitFor(() => expect(wrapper.text()).toContain('请选择整理方式'))
    expect(http.post).not.toHaveBeenCalled()
  })

  it('新增时主动切换媒体类型仍应用默认模板及其他类型的仅整理限制', async () => {
    const { wrapper } = await mountForm()
    await choose(wrapper, 'tvshow')
    expect(field(wrapper, '文件重命名模板').get('input').element.value).toContain(
      '{season_episode}',
    )
    await choose(wrapper, 'other')
    expect(wrapper.get<HTMLInputElement>('input[value="only_rename"]').element.checked).toBe(true)
    expect(wrapper.get('input[value="only_scrape"]').attributes('disabled')).toBeDefined()
    expect(field(wrapper, '文件重命名模板').get('input').element.value).toBe('{num}')
  })
})
