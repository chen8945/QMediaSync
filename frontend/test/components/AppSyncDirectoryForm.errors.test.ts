import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ElMessage, ElSelect } from 'element-plus'
import { createMemoryHistory, createRouter } from 'vue-router'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppSyncDirectoryForm from '@/components/AppSyncDirectoryForm.vue'
import { httpKey } from '@/http/client'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'

const directory = {
  id: 12,
  account_id: 1,
  local_path: '/strm',
  base_cid: 'root',
  source_type: '115',
  custom_config: true,
  video_ext_arr: ['.mkv'],
  meta_ext_arr: ['.nfo'],
  exclude_name_arr: [],
  exclude_name_regex_arr: ['^keep$'],
  remote_path: '/remote',
  min_video_size: -1,
  upload_meta: -1,
  download_meta: -1,
  delete_dir: -1,
  add_path: -1,
  check_meta_mtime: -1,
  baidu_sync_method: 1,
  cron: '',
  enable_cron: false,
  directory_upload_enabled: false,
  strm_base_url: '',
}
const DialogStub = defineComponent({
  props: { modelValue: Boolean },
  template: '<section v-if="modelValue" role="dialog"><slot /><slot name="footer" /></section>',
})
const SelectorStub = defineComponent({
  name: 'DirectorySelector',
  emits: ['update:modelValue', 'select', 'cancel'],
  template: '<div />',
})
const failure = (code: string, status = 403) =>
  new HttpResponseError({
    status,
    data: { code: 500, error_code: code, message: 'private SQL token' },
    config: { method: 'put', url: '/api/sync/paths/12?token=private' },
  })
const mountForm = async (edit = true, readError?: unknown, ruleError?: unknown) => {
  const getReply = vi.fn(async (url: string): Promise<unknown> => {
    if (url.endsWith('/version'))
      return { version: 'v1', date: '', isWindows: false, isRelease: false }
    if (url.endsWith('/sync/path/12')) {
      if (readError) throw readError
      return { code: 200, data: directory }
    }
    if (url.endsWith('/directory-upload/rules')) {
      if (ruleError) throw ruleError
      return { code: 200, data: { list: [] } }
    }
    return { code: 200, data: [] }
  })
  const saveReply = vi.fn<() => Promise<APIResponse<unknown>>>().mockResolvedValue({
    code: 200,
    message: '',
    data: {
      sync_path: { id: 12 },
      directory_upload: { enabled: false, rules: [] },
      warnings: [],
    },
  })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await getReply(config.url) : await saveReply(),
  }))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/sync-directory/add',
        name: 'sync-directory-add',
        component: { template: '<div />' },
      },
      {
        path: '/sync-directory/:id',
        name: 'sync-directory-edit',
        component: { template: '<div />' },
      },
      { path: '/sync-directories', name: 'sync-directories', component: { template: '<div />' } },
    ],
  })
  await router.push(edit ? '/sync-directory/12' : '/sync-directory/add')
  await router.isReady()
  const replace = vi.spyOn(router, 'replace')
  const wrapper = mount(AppSyncDirectoryForm, {
    global: {
      plugins: [router],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: true, DirectorySelector: SelectorStub, ElDialog: DialogStub },
    },
  })
  await flushPromises()
  const submit = async () => {
    await wrapper
      .findAll('button')
      .find((item) => item.text() === (edit ? '保存修改' : '确定添加'))!
      .trigger('click')
    await flushPromises()
  }
  return { wrapper, adapter, saveReply, getReply, replace, submit }
}
const field = (wrapper: VueWrapper, label: string) =>
  wrapper
    .findAll('.el-form-item')
    .find((item) => item.find('.el-form-item__label').text() === label)!
const fillNewForm = async (wrapper: VueWrapper) => {
  const source = wrapper.findComponent(ElSelect)
  source.vm.$emit('update:modelValue', 'local')
  source.vm.$emit('change', 'local')
  await nextTick()
  for (const [label, path] of [
    ['来源路径', '/remote'],
    ['目标路径', '/strm'],
  ]) {
    await field(wrapper, label!).get('button').trigger('click')
    const selector = wrapper.findComponent(SelectorStub)
    selector.vm.$emit('update:modelValue', { id: path, path, name: path })
    await nextTick()
    selector.vm.$emit('select')
    await nextTick()
  }
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

describe('同步目录表单错误行为', () => {
  it('业务失败保留输入、显示安全字段错误且不导航', async () => {
    const { wrapper, saveReply, replace, submit } = await mountForm()
    saveReply.mockResolvedValueOnce({
      code: 500,
      message: '参数校验失败',
      data: {
        error_code: 'INVALID_REQUEST',
        field_errors: [
          { field: 'exclude_name_regex_arr[0]', message: '正则表达式无效：private token' },
        ],
      },
    })
    await submit()
    expect(saveReply).toHaveBeenCalledOnce()
    await vi.waitFor(() => expect(wrapper.text()).toContain('第 1 条：正则表达式无效'))
    expect(wrapper.text()).not.toContain('private')
    expect((field(wrapper, '目标路径').get('input').element as HTMLInputElement).value).toBe(
      '/strm',
    )
    expect(ElMessage.error).toHaveBeenCalledWith('参数校验失败')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private')
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('%s 正确显示且不跳转', async (code, text) => {
    const { saveReply, submit, replace } = await mountForm()
    saveReply.mockRejectedValueOnce(failure(code))
    await submit()
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining(text))
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('取消、已处理 401 不提示、不导航', async () => {
    const { saveReply, submit, replace } = await mountForm()
    const handled = failure('SESSION_INVALID', 401)
    markAuthInvalidationHandled(handled)
    for (const error of [new CanceledError(), handled]) {
      saveReply.mockRejectedValueOnce(error)
      await submit()
    }
    expect(saveReply).toHaveBeenCalledTimes(2)
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('新增超时及幂等冲突不自动重试或换键；成功保留警告并导航', async () => {
    const { wrapper, saveReply, adapter, submit, replace } = await mountForm(false)
    await fillNewForm(wrapper)
    saveReply.mockRejectedValueOnce(new AxiosError('private', 'ETIMEDOUT'))
    await submit()
    expect(saveReply).toHaveBeenCalledTimes(1)
    expect(replace).not.toHaveBeenCalled()
    saveReply.mockResolvedValueOnce({
      code: 409,
      message: '相同幂等键的创建请求正在处理',
      error_code: 'IDEMPOTENCY_CONFLICT',
      data: null,
    })
    await submit()
    expect(saveReply).toHaveBeenCalledTimes(2)
    expect(replace).not.toHaveBeenCalled()
    saveReply.mockResolvedValueOnce({
      code: 200,
      message: '',
      data: {
        sync_path: { id: 12 },
        directory_upload: { enabled: false, rules: [] },
        warnings: ['同步目录已保存，但重载定时同步任务失败'],
      },
    })
    await submit()
    const writes = adapter.mock.calls.filter(([config]) => config.method === 'post')
    expect(writes).toHaveLength(3)
    expect(new Set(writes.map(([config]) => config.headers.get('Idempotency-Key'))).size).toBe(1)
    expect(writes[0]![0].headers.get('Idempotency-Key')).toBeTruthy()
    expect(ElMessage.warning).toHaveBeenCalledWith('同步目录已保存，但重载定时同步任务失败')
    expect(ElMessage.success).toHaveBeenCalledOnce()
    expect(replace).toHaveBeenCalledWith({ name: 'sync-directories' })
  })

  it('规则读取失败不能把空规则集合保存回去', async () => {
    const { submit, saveReply, replace } = await mountForm(
      true,
      undefined,
      failure('REQUEST_ORIGIN_INVALID'),
    )
    await submit()
    expect(saveReply).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledWith('目录监控上传规则加载失败，请刷新或重试后再保存')
    expect(replace).not.toHaveBeenCalled()
  })

  it('详情读取取消保留页面且不提示、不导航', async () => {
    const { replace } = await mountForm(true, new CanceledError())
    expect(replace).not.toHaveBeenCalled()
    expect(ElMessage.error).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('Cron 预览保留参数，安全校验失败不误报为表达式错误', async () => {
    const { wrapper, getReply, adapter } = await mountForm()
    const cron = field(wrapper, '定时同步表达式').get('input')
    getReply.mockRejectedValueOnce(failure('CSRF_TOKEN_INVALID'))
    await cron!.setValue('  @daily  ')
    await cron!.trigger('blur')
    await flushPromises()
    expect(adapter.mock.calls.at(-1)![0].params).toEqual({ cron: '  @daily  ' })
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('请求安全校验失败'))
  })
  it('导入全局设置失败保留已有扩展名，不显示导入成功', async () => {
    const { wrapper, getReply } = await mountForm()
    getReply.mockRejectedValueOnce(failure('REQUEST_ORIGIN_INVALID'))
    await wrapper
      .findAll('button')
      .find((item) => item.text() === '导入全局设置')!
      .trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('.mkv')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('访问地址校验失败'))
    expect(ElMessage.success).not.toHaveBeenCalled()
  })
})
