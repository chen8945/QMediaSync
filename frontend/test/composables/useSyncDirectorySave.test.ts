import type { SaveSyncPathPayload } from '@/api/syncPaths'
import { useSyncDirectorySave } from '@/composables/useSyncDirectorySave'
import { AxiosError, CanceledError, type AxiosStatic } from 'axios'
import { HttpResponseError, markAuthInvalidationHandled } from '@/http/errors'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const payload: SaveSyncPathPayload = {
  sync_path: {
    source_type: '115',
    account_id: 1,
    base_cid: 'root',
    local_path: '/strm',
    remote_path: '/remote',
    enable_cron: false,
    custom_config: false,
    setting: {
      local_proxy: 0,
      strm_base_url: '',
      cron: '',
      min_video_size: -1,
      video_ext_arr: [],
      meta_ext_arr: [],
      exclude_name_arr: [],
      exclude_name_regex_arr: [],
      upload_meta: -1,
      download_meta: -1,
      delete_dir: -1,
      add_path: -1,
      check_meta_mtime: -1,
    },
  },
  directory_upload: null,
}

function mockHTTP(response: unknown): AxiosStatic {
  return {
    post: vi.fn().mockResolvedValue(response),
    put: vi.fn().mockResolvedValue(response),
  } as unknown as AxiosStatic
}

beforeEach(() => vi.spyOn(console, 'error').mockImplementation(() => {}))
afterEach(() => vi.restoreAllMocks())

describe('useSyncDirectorySave', () => {
  it('保存失败时保留字段错误且不执行成功回调', async () => {
    const http = mockHTTP({
      data: {
        code: 500,
        message: '目录监控上传规则校验失败',
        data: {
          error_code: 'DIRECTORY_UPLOAD_RULE_CONFLICT',
          field_errors: [
            { client_id: 'rule-2', field: 'monitor_path', message: '监控目录不能为空' },
          ],
        },
      },
    })
    const onSuccess = vi.fn()
    const state = useSyncDirectorySave(http)

    const result = await state.saveAndRun(0, payload, 'key-1', onSuccess)

    expect(result).toBeNull()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(state.errorMessage.value).toBe('目录监控上传规则校验失败')
    expect(state.fieldErrors.value).toEqual([
      { client_id: 'rule-2', field: 'monitor_path', message: '监控目录不能为空' },
    ])
  })

  it('保存成功后返回 warnings 并执行一次成功回调', async () => {
    const responseData = {
      sync_path: { id: 12 },
      directory_upload: { enabled: false, rules: [] },
      warnings: ['同步目录已保存，但重载定时同步任务失败'],
    }
    const http = mockHTTP({ data: { code: 200, message: '保存成功', data: responseData } })
    const onSuccess = vi.fn()
    const state = useSyncDirectorySave(http)

    const result = await state.saveAndRun(12, payload, 'unused', onSuccess)

    expect(result).toEqual(responseData)
    expect(onSuccess).toHaveBeenCalledOnce()
    expect(onSuccess).toHaveBeenCalledWith(responseData)
  })
  it('顶层错误码和字段优先，动态正则与路径消息不能透传', async () => {
    const http = mockHTTP({
      status: 400,
      data: {
        code: 500,
        message: '参数校验失败',
        error_code: 'INVALID_REQUEST',
        field_errors: [
          { field: 'exclude_name_regex_arr[2]', message: '正则表达式无效：private secret' },
        ],
        data: {
          error_code: 'DIRECTORY_UPLOAD_RULE_CONFLICT',
          field_errors: [
            { client_id: '7', field: 'monitor_path', message: '/private/path overlaps' },
          ],
        },
      },
    })
    const state = useSyncDirectorySave(http)
    await state.saveAndRun(0, payload, 'key', vi.fn())
    expect(state.errorMessage.value).toBe('参数校验失败')
    expect(state.fieldErrors.value).toEqual([
      { field: 'exclude_name_regex_arr[2]', message: '正则表达式无效' },
    ])
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toMatch(/private|secret|SQL/)
  })

  it.each([
    ['REQUEST_ORIGIN_INVALID', '访问地址校验失败'],
    ['CSRF_TOKEN_INVALID', '请求安全校验失败'],
  ])('%s 优先于嵌套字段错误，阻止回调且不误报网络', async (code, message) => {
    const http = mockHTTP({
      status: 403,
      data: {
        code: 500,
        error_code: code,
        message: 'private',
        data: {
          error_code: 'INVALID_REQUEST',
          field_errors: [{ field: 'local_path', message: '不能为空' }],
        },
      },
    })
    const state = useSyncDirectorySave(http)
    const onSuccess = vi.fn()
    expect(await state.saveAndRun(0, payload, 'key', onSuccess)).toBeNull()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(state.errorMessage.value).toContain(message)
    expect(state.errorMessage.value).not.toContain('网络')
    expect(state.fieldErrors.value).toEqual([])
  })

  it('取消和已处理 401 静默清空上次错误，不执行成功回调', async () => {
    const http = mockHTTP({ data: { code: 500, message: 'private', data: null } })
    const state = useSyncDirectorySave(http)
    const onSuccess = vi.fn()
    await state.saveAndRun(0, payload, 'key', onSuccess)
    vi.mocked(console.error).mockClear()
    const handled = new HttpResponseError({ status: 401, data: { code: 401 } })
    markAuthInvalidationHandled(handled)
    for (const error of [new CanceledError(), handled]) {
      vi.mocked(http.post).mockRejectedValueOnce(error)
      expect(await state.saveAndRun(0, payload, 'key', onSuccess)).toBeNull()
      expect(state.errorMessage.value).toBe('')
      expect(state.fieldErrors.value).toEqual([])
    }
    expect(onSuccess).not.toHaveBeenCalled()
    expect(console.error).not.toHaveBeenCalled()
  })

  it('超时仅提交一次且不执行下游动作；未知提交后警告仍有安全提示', async () => {
    const http = mockHTTP({
      data: {
        code: 200,
        data: {
          sync_path: { id: 12 },
          directory_upload: { enabled: false, rules: [] },
          warnings: ['private postcommit exception'],
        },
      },
    })
    vi.mocked(http.post).mockRejectedValueOnce(new AxiosError('private', 'ETIMEDOUT'))
    const state = useSyncDirectorySave(http)
    const onSuccess = vi.fn()
    await state.saveAndRun(0, payload, 'same-key', onSuccess)
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(state.errorMessage.value).toContain('请求超时')
    expect(onSuccess).not.toHaveBeenCalled()
    await state.saveAndRun(0, payload, 'same-key', onSuccess)
    expect(onSuccess).toHaveBeenCalledOnce()
    expect(onSuccess.mock.calls[0]![0].warnings).toEqual([
      '同步目录已保存，但后续处理失败，请查看服务日志',
    ])
  })
})
