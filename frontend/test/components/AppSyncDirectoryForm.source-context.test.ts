import axios from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage, ElSelect, ElSwitch } from 'element-plus'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppSyncDirectoryForm from '@/views/sync/AppSyncDirectoryForm.vue'
import { httpKey } from '@/http/client'

const directory = {
  id: 12,
  account_id: 1,
  source_type: '115',
  local_path: '/strm',
  base_cid: 'old-root',
  remote_path: '/old-media',
  custom_config: false,
  video_ext_arr: [],
  meta_ext_arr: [],
  exclude_name_arr: [],
  exclude_name_regex_arr: [],
  min_video_size: -1,
  upload_meta: -1,
  download_meta: -1,
  delete_dir: -1,
  add_path: -1,
  check_meta_mtime: -1,
  baidu_sync_method: 1,
  cron: '',
  enable_cron: false,
  directory_upload_enabled: true,
  strm_base_url: '',
}
const uploadRule = {
  id: 5,
  enabled: true,
  monitor_path: '/incoming',
  remote_root_path: '/old-media/uploads',
  remote_root_id: 'old-upload-root',
  recursive: true,
  watch_mode: 'auto',
  overwrite_mode: 'skip_same',
}
const DialogStub = defineComponent({
  props: { modelValue: Boolean },
  // 关闭动画期间选择器仍可能发出事件。
  template: '<section v-show="modelValue" role="dialog"><slot /></section>',
})
const SelectorStub = defineComponent({
  name: 'DirectorySelector',
  props: ['rootId', 'rootPath', 'sourceType', 'accountId'],
  emits: ['update:modelValue', 'select', 'cancel'],
  template: '<div />',
})
const field = (wrapper: VueWrapper, label: string) =>
  wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === label)!
const inputValue = (wrapper: VueWrapper, label: string) =>
  (field(wrapper, label).get('input').element as HTMLInputElement).value
const changeSelect = async (wrapper: VueWrapper, label: string, value: string | number) => {
  const select = field(wrapper, label).findComponent(ElSelect)
  select.vm.$emit('update:modelValue', value)
  select.vm.$emit('change', value)
  await flushPromises()
}
const chooseDirectory = async (wrapper: VueWrapper, label: string, id: string, path: string) => {
  await field(wrapper, label).get('button').trigger('click')
  const selector = wrapper.findComponent(SelectorStub)
  selector.vm.$emit('update:modelValue', { id, path, name: path })
  await nextTick()
  selector.vm.$emit('select')
  await flushPromises()
}
const mountForm = async (edit = false) => {
  const adapter = vi.fn(async (config) => {
    let data: unknown = { code: 200, data: [] }
    if (config.method !== 'get') {
      data = {
        code: 200,
        data: {
          sync_path: { id: 12 },
          directory_upload: { enabled: false, rules: [] },
          warnings: [],
        },
      }
    } else if (config.url.endsWith('/version')) {
      data = { version: 'v1', date: '', isWindows: false, isRelease: false }
    } else if (config.url.endsWith('/sync/path/12')) {
      data = { code: 200, data: directory }
    } else if (config.url.endsWith('/directory-upload/rules')) {
      data = { code: 200, data: { list: [uploadRule] } }
    } else if (config.url.endsWith('/account/list')) {
      data = {
        code: 200,
        data: [
          { id: 1, name: '115 A', source_type: '115' },
          { id: 2, name: '115 B', source_type: '115' },
          { id: 3, name: 'OpenList', source_type: 'openlist' },
        ],
      }
    }
    return { config, status: 200, statusText: 'OK', headers: {}, data }
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/sync/add', component: { template: '<div />' } },
      { path: '/sync/:id', component: { template: '<div />' } },
      { path: '/sync-directories', name: 'sync-directories', component: { template: '<div />' } },
    ],
  })
  await router.push(edit ? '/sync/12' : '/sync/add')
  const wrapper = mount(AppSyncDirectoryForm, {
    global: {
      plugins: [router],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, DirectorySelector: SelectorStub, ElDialog: DialogStub },
    },
  })
  await flushPromises()
  const writes = () => adapter.mock.calls.filter(([config]) => config.method !== 'get')
  const payload = () => JSON.parse(writes().at(-1)![0].data)
  const submit = async () => {
    await wrapper
      .findAll('button')
      .find((button) => button.text() === (edit ? '保存修改' : '确定添加'))!
      .trigger('click')
    await flushPromises()
  }
  return { wrapper, writes, payload, submit }
}
const fillNewForm = async (wrapper: VueWrapper) => {
  await changeSelect(wrapper, '同步源类型', '115')
  await changeSelect(wrapper, '网盘账号', 1)
  await chooseDirectory(wrapper, '来源路径', 'old-root', '/old-media')
  await chooseDirectory(wrapper, '目标路径', '/strm', '/strm')
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
})
afterEach(() => vi.restoreAllMocks())

describe.each([390, 1280])('同步目录来源联动（%d px）', (width) => {
  beforeEach(() => vi.stubGlobal('innerWidth', width))
  afterEach(() => vi.unstubAllGlobals())

  it('换账号后清空来源并阻止提交，重新选择后只提交新账号的目录', async () => {
    const { wrapper, writes, payload, submit } = await mountForm()
    await fillNewForm(wrapper)
    await changeSelect(wrapper, '网盘账号', 2)
    expect(inputValue(wrapper, '来源路径')).toBe('')
    expect(inputValue(wrapper, '目标路径')).toBe('/strm')
    expect(inputValue(wrapper, 'STRM 存放目录')).toBe('')
    expect(wrapper.text()).not.toContain('/old-media')
    await submit()
    expect(writes()).toHaveLength(0)
    await vi.waitFor(() => expect(wrapper.text()).toContain('请选择来源目录'))
    await chooseDirectory(wrapper, '来源路径', 'new-root', '/new-media')
    await submit()
    expect(payload().sync_path).toMatchObject({
      source_type: '115',
      account_id: 2,
      base_cid: 'new-root',
      remote_path: '/new-media',
      local_path: '/strm',
    })
  })

  it.each(['openlist', 'local'])('切换到 %s 清空账号及旧目录，保留本地输出', async (source) => {
    const { wrapper, writes, payload, submit } = await mountForm()
    await fillNewForm(wrapper)
    await changeSelect(wrapper, '同步源类型', source)
    expect(wrapper.text()).not.toContain('/old-media')
    if (source === 'openlist') {
      expect(field(wrapper, '网盘账号').findComponent(ElSelect).props('modelValue')).toBe('')
      await changeSelect(wrapper, '网盘账号', 3)
    }
    expect(inputValue(wrapper, '来源路径')).toBe('')
    expect(inputValue(wrapper, '目标路径')).toBe('/strm')
    expect(inputValue(wrapper, 'STRM 存放目录')).toBe(source === 'local' ? '/strm' : '')
    await submit()
    expect(writes()).toHaveLength(0)
    await chooseDirectory(wrapper, '来源路径', '/new-source', '/new-source')
    await submit()
    expect(payload().sync_path).toMatchObject({
      source_type: source,
      account_id: source === 'local' ? 0 : 3,
      base_cid: '/new-source',
      remote_path: '/new-source',
      local_path: '/strm',
    })
  })

  it.each(['账号', '来源'])('切换%s关闭选择器后，忽略旧草稿和迟到的确认事件', async (target) => {
    const { wrapper, writes, submit } = await mountForm()
    await fillNewForm(wrapper)
    await field(wrapper, '来源路径').get('button').trigger('click')
    const selector = wrapper.findComponent(SelectorStub)
    const staleDirectory = { id: 'stale-root', path: '/stale-media', name: 'stale' }
    selector.vm.$emit('update:modelValue', staleDirectory)
    await nextTick()
    if (target === '账号') {
      await changeSelect(wrapper, '网盘账号', 2)
    } else {
      await changeSelect(wrapper, '同步源类型', 'openlist')
      await changeSelect(wrapper, '网盘账号', 3)
    }
    expect(wrapper.findComponent(DialogStub).props('modelValue')).toBe(false)
    selector.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, '来源路径')).toBe('')
    selector.vm.$emit('update:modelValue', staleDirectory)
    await nextTick()
    selector.vm.$emit('select')
    await flushPromises()
    expect(inputValue(wrapper, '来源路径')).toBe('')
    expect(inputValue(wrapper, '目标路径')).toBe('/strm')
    expect(wrapper.text()).not.toContain('/stale-media')
    await submit()
    expect(writes()).toHaveLength(0)
  })

  it('换账号清理上传远程目标，保留监控路径和选项并使用新目录上下文', async () => {
    const { wrapper, payload, submit } = await mountForm()
    await fillNewForm(wrapper)
    const uploadSwitch = field(wrapper, '目录监控上传').findComponent(ElSwitch)
    uploadSwitch.vm.$emit('update:modelValue', true)
    uploadSwitch.vm.$emit('change', true)
    await nextTick()
    await chooseDirectory(wrapper, '监控目录 1', '/incoming', '/incoming')
    await chooseDirectory(wrapper, '目标目录 1', 'old-upload-root', '/old-media/uploads')
    await field(wrapper, '上传元数据').get('input').setValue()
    await changeSelect(wrapper, '网盘账号', 2)
    expect(inputValue(wrapper, '监控目录 1')).toBe('/incoming')
    expect(inputValue(wrapper, '目标目录 1')).toBe('')
    expect(field(wrapper, '上传元数据').get('input').element).toHaveProperty('checked', true)
    await chooseDirectory(wrapper, '来源路径', 'new-root', '/new-media')
    await field(wrapper, '目标目录 1').get('button').trigger('click')
    const selector = wrapper.findComponent(SelectorStub)
    expect(selector.props()).toMatchObject({
      accountId: 2,
      rootId: 'new-root',
      rootPath: '/new-media',
    })
    selector.vm.$emit('update:modelValue', {
      id: 'new-upload-root',
      path: '/new-media/uploads',
      name: 'uploads',
    })
    await nextTick()
    selector.vm.$emit('select')
    await flushPromises()
    await submit()
    expect(payload().directory_upload).toMatchObject({
      enabled: true,
      rules: [
        {
          monitor_path: '/incoming',
          remote_root_id: 'new-upload-root',
          remote_root_path: '/new-media/uploads',
          upload_metadata: true,
        },
      ],
    })
  })

  it('编辑回填后直接保存，保留原目录及已有监控配置', async () => {
    const { wrapper, payload, submit } = await mountForm(true)
    expect(inputValue(wrapper, '来源路径')).toBe('old-root')
    expect(inputValue(wrapper, '目标目录 1')).toBe('/old-media/uploads')
    await submit()
    expect(payload().sync_path).toMatchObject({
      account_id: 1,
      base_cid: 'old-root',
      remote_path: '/old-media',
      local_path: '/strm',
    })
    expect(payload().directory_upload.rules[0]).toMatchObject(uploadRule)
  })
})
