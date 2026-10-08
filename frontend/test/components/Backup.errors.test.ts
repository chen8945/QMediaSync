import axios, { AxiosError, CanceledError } from 'axios'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppBackupSettings from '@/components/AppBackupSettings.vue'
import AppBackupRestore from '@/components/AppBackupRestore.vue'
import AppBackupRecords from '@/components/AppBackupRecords.vue'
import { httpKey } from '@/http/client'
import { useBackupStore } from '@/stores/backup'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import type { APIResponse } from '@/api/types'

const configData = {
  backup_enabled: 1,
  backup_cron: '0 3 * * *',
  backup_retention: 7,
  backup_max_count: 10,
  backup_compress: 1,
}
const record = {
  id: 7,
  created_at: 1,
  status: 'completed',
  file_path: '/backups/kept-name.zip',
  file_size: 100,
  file_status: 'available',
  backup_type: 'manual',
  backup_duration: 1,
  created_reason: '手动备份',
}
const setup = async (
  component: typeof AppBackupSettings | typeof AppBackupRestore | typeof AppBackupRecords,
  records = [record],
) => {
  const reply = vi
    .fn<() => Promise<APIResponse<unknown>>>()
    .mockResolvedValue({ code: 200, message: '', data: null })
  const read = vi.fn(async (url: string): Promise<unknown> => {
    if (url.endsWith('/config')) return { code: 200, data: configData }
    if (url.endsWith('/list')) return { code: 200, data: { list: records, total: records.length } }
    if (url.endsWith('/files'))
      return {
        code: 200,
        data: {
          directory: '/config/backups',
          files: [{ file_name: 'stored-upload.zip', file_size: 512, modified_at: 1 }],
        },
      }
    if (url.includes('/download/')) return new Blob(['PK archive'])
    return { code: 200, data: [] }
  })
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await read(config.url) : await reply(),
  }))
  const pinia = createPinia()
  const store = useBackupStore(pinia)
  const poll = vi.spyOn(store, 'startProgressPolling').mockImplementation(() => {})
  const wrapper = mount(component, {
    global: {
      plugins: [pinia],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: { PageHeader: { template: '<div><slot name="actions" /></div>' } },
    },
  })
  await flushPromises()
  return { wrapper, reply, read, adapter, poll }
}
const click = async (wrapper: VueWrapper, label: string) => {
  const button = wrapper.findAll('button').find((button) => button.text() === label)
  expect(button, label).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}
const selectFile = async (wrapper: VueWrapper) => {
  await wrapper.get('#tab-upload').trigger('click')
  await flushPromises()
  const input = wrapper.get('input[type="file"]')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: [new File(['zip'], 'keep.zip', { type: 'application/zip' })],
  })
  await input.trigger('change')
  await flushPromises()
  vi.mocked(ElMessage.success).mockClear()
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
  vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
  vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('备份页面失败反馈', () => {
  it('从服务器目录恢复文件只发送文件名，保留原有回执与进度流程', async () => {
    const { wrapper, adapter, reply, poll, read } = await setup(AppBackupRestore)
    expect(wrapper.text()).toContain('/config/backups')
    expect(wrapper.text()).toContain('stored-upload.zip')
    expect(wrapper.text()).toContain('每次恢复时会校验所选备份')
    expect(read).toHaveBeenCalledExactlyOnceWith('/api/backup/files')
    reply.mockResolvedValueOnce({
      code: 200,
      message: '',
      data: { restore_receipt: 'file-receipt' },
    })
    await click(wrapper, '恢复')
    const request = adapter.mock.calls.find(([config]) => config.method === 'post')?.[0]
    expect(request.url).toBe('/api/backup/restore')
    expect(JSON.parse(request.data)).toEqual({ file_name: 'stored-upload.zip' })
    expect(poll).toHaveBeenCalledWith('restore', undefined, expect.anything(), 'file-receipt')
  })

  it('服务器文件列表读取失败不伪装成空目录，允许刷新重试', async () => {
    const { wrapper, read, reply, poll } = await setup(AppBackupRestore)
    read.mockResolvedValueOnce({ code: 500, message: '备份目录不可访问', data: null })
    await click(wrapper, '刷新文件列表')
    expect(wrapper.text()).toContain('备份目录不可访问')
    expect(wrapper.text()).toContain('stored-upload.zip')
    expect(reply).not.toHaveBeenCalled()
    expect(poll).not.toHaveBeenCalled()
    await click(wrapper, '刷新文件列表')
    expect(wrapper.text()).not.toContain('备份目录不可访问')
  })

  it('服务器文件恢复失败保留文件入口，确认取消不发起恢复', async () => {
    const { wrapper, reply, poll } = await setup(AppBackupRestore)
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce('cancel')
    await click(wrapper, '恢复')
    expect(reply).not.toHaveBeenCalled()
    reply.mockResolvedValueOnce({ code: 500, message: '备份文件已不存在', data: null })
    await click(wrapper, '恢复')
    expect(wrapper.text()).toContain('stored-upload.zip')
    expect(ElMessage.error).toHaveBeenCalledWith('备份文件已不存在')
    expect(poll).not.toHaveBeenCalled()
  })

  it.each(['unconfirmed', 'failed'])('任务 %s 但文件存在时仍可选择恢复', async (status) => {
    const { wrapper, reply, poll } = await setup(AppBackupRecords, [{ ...record, status }])
    expect(wrapper.text()).toContain(status === 'unconfirmed' ? '结果未记录' : '失败')
    expect(wrapper.text()).toContain('文件存在')
    await click(wrapper, '恢复')
    expect(reply).toHaveBeenCalledOnce()
    expect(poll).toHaveBeenCalledOnce()
  })

  it.each([
    { file_status: 'missing', label: '文件已缺失' },
    { file_status: 'unavailable', label: '文件不可用' },
  ])('历史成功而文件 $file_status 时保留状态但不提供恢复和下载', async ({ file_status, label }) => {
    const { wrapper } = await setup(AppBackupRecords, [{ ...record, file_status }])
    expect(wrapper.text()).toContain('成功')
    expect(wrapper.text()).toContain(label)
    expect(
      wrapper.findAll('button').some((button) => ['恢复', '下载'].includes(button.text())),
    ).toBe(false)
  })

  it.each([
    { component: AppBackupRestore, upload: true, label: '开始恢复' },
    { component: AppBackupRecords, upload: false, label: '恢复' },
  ])(
    '$label 成功后携带恢复凭证查询，并说明回滚与重启要求',
    async ({ component, upload, label }) => {
      const { wrapper, reply, poll } = await setup(component)
      if (upload) await selectFile(wrapper)
      reply.mockResolvedValueOnce({
        code: 200,
        message: '',
        data: { restore_receipt: 'test-restore-receipt' },
      })
      await click(wrapper, label)
      expect(poll).toHaveBeenCalledWith(
        'restore',
        undefined,
        expect.anything(),
        'test-restore-receipt',
      )
      expect(ElMessageBox.confirm).toHaveBeenCalledWith(
        expect.stringContaining('导入失败会回滚'),
        expect.any(String),
        expect.any(Object),
      )
      const notice = vi.mocked(ElMessageBox.confirm).mock.lastCall?.[0]
      expect(notice).toContain('config/')
      expect(notice).toContain('encryption.key')
      expect(notice).toContain('重启应用服务')
      expect(notice).toContain('重新登录')
      expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain(
        'test-restore-receipt',
      )
    },
  )

  it('保存业务失败保留设置输入，不显示成功或查询新的 Cron', async () => {
    const { wrapper, reply, read } = await setup(AppBackupSettings)
    const retention = wrapper
      .findAll('input')
      .find((input) => input.attributes('role') === 'spinbutton')!
    await retention.setValue('12')
    await retention.trigger('change')
    reply.mockResolvedValueOnce({ code: 500, message: '备份目录不可写', data: null })
    read.mockClear()
    await click(wrapper, '保存配置')
    expect((retention.element as HTMLInputElement).value).toBe('12')
    expect(ElMessage.error).toHaveBeenCalledWith('备份目录不可写')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(read).not.toHaveBeenCalled()
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('secret')
  })

  it('保存成功后的 Cron 刷新失败只报告刷新失败', async () => {
    const { wrapper, read } = await setup(AppBackupSettings)
    read.mockResolvedValueOnce({ code: 500, message: 'Cron 表达式无效', data: null })
    await click(wrapper, '保存配置')
    expect(ElMessage.success).toHaveBeenCalledWith('备份配置保存成功')
    expect(ElMessage.error).toHaveBeenCalledExactlyOnceWith('Cron 表达式无效')
  })

  it.each(['business', 'limit', 'timeout', 'csrf', 'cancel', 'handled'])(
    '上传恢复 %s 保留文件且不启动轮询',
    async (kind) => {
      const { wrapper, reply, poll, adapter } = await setup(AppBackupRestore)
      await selectFile(wrapper)
      if (kind === 'business')
        reply.mockResolvedValueOnce({ code: 500, message: '恢复包格式不正确', data: null })
      else if (kind === 'limit')
        reply.mockRejectedValueOnce(
          new HttpResponseError({
            status: 413,
            data: {
              code: 413,
              error_code: 'BACKUP_ARCHIVE_LIMIT',
              message: '备份文件大小不能超过 1 GiB',
            },
          }),
        )
      else if (kind === 'timeout')
        reply.mockRejectedValueOnce(
          new AxiosError('secret', 'ETIMEDOUT', {
            method: 'post',
            url: '/x',
            headers: {},
          } as never),
        )
      else if (kind === 'cancel') reply.mockRejectedValueOnce(new CanceledError())
      else {
        const error = new HttpResponseError({
          status: kind === 'handled' ? 401 : 403,
          data: {
            code: 500,
            error_code: kind === 'handled' ? 'SESSION_INVALID' : 'CSRF_TOKEN_INVALID',
            message: 'internal secret',
          },
        })
        if (kind === 'handled') markAuthInvalidationHandled(error)
        reply.mockRejectedValueOnce(error)
      }
      await click(wrapper, '开始恢复')
      expect(wrapper.text()).toContain('keep.zip')
      expect(poll).not.toHaveBeenCalled()
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(adapter.mock.calls.filter(([config]) => config.method === 'post')).toHaveLength(1)
      if (kind === 'cancel' || kind === 'handled') expect(ElMessage.error).not.toHaveBeenCalled()
      else {
        expect(ElMessage.error).toHaveBeenCalledTimes(1)
        expect(JSON.stringify(vi.mocked(ElMessage.error).mock.calls)).not.toContain('secret')
        if (kind === 'timeout')
          expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('操作结果尚未确认'))
        if (kind === 'csrf')
          expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('请求安全校验失败'))
        if (kind === 'limit')
          expect(ElMessage.error).toHaveBeenCalledWith('备份文件大小不能超过 1 GiB')
      }
    },
  )

  it.each(['手动备份', '恢复', '删除'])('%s 业务失败没有成功后续动作', async (label) => {
    const { wrapper, reply, read, poll } = await setup(AppBackupRecords)
    reply.mockResolvedValueOnce({ code: 500, message: '恢复包格式不正确', data: null })
    read.mockClear()
    await click(wrapper, label)
    await vi.advanceTimersByTimeAsync(2000)
    expect(poll).not.toHaveBeenCalled()
    expect(read).not.toHaveBeenCalled()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledTimes(1)
  })

  it('下载错误 JSON 不生成文件，正常 ZIP 沿用记录文件名', async () => {
    const { wrapper, read } = await setup(AppBackupRecords)
    const createURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:backup')
    const revokeURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const clickLink = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    read.mockResolvedValueOnce(
      new Blob(['{"code":500,"message":"备份记录不存在","data":null}'], {
        type: 'application/json',
      }),
    )
    await click(wrapper, '下载')
    expect(createURL).not.toHaveBeenCalled()
    expect(ElMessage.error).toHaveBeenCalledWith('备份记录不存在')
    await click(wrapper, '下载')
    expect(createURL).toHaveBeenCalledTimes(1)
    expect((clickLink.mock.instances[0] as HTMLAnchorElement).download).toBe('kept-name.zip')
    expect(revokeURL).toHaveBeenCalledWith('blob:backup')
  })
})
