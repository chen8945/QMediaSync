import type { SaveSyncPathPayload } from '@/api/syncPaths'
import { useSyncDirectorySave } from '@/composables/useSyncDirectorySave'
import type { AxiosStatic } from 'axios'
import { describe, expect, it, vi } from 'vitest'

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

describe('useSyncDirectorySave', () => {
  it('保存失败时保留字段错误且不执行成功回调', async () => {
    const http = mockHTTP({
      data: {
        code: 500,
        message: '目录监控上传规则校验失败',
        data: {
          error_code: 'DIRECTORY_UPLOAD_RULE_CONFLICT',
          field_errors: [{ client_id: 'rule-2', field: 'monitor_path', message: '监控目录重叠' }],
        },
      },
    })
    const onSuccess = vi.fn()
    const state = useSyncDirectorySave(http)

    const result = await state.saveAndRun(0, payload, 'key-1', onSuccess)

    expect(result).toBeNull()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(state.errorMessage.value).toBe('目录监控上传规则校验失败')
    expect(state.errorCode.value).toBe('DIRECTORY_UPLOAD_RULE_CONFLICT')
    expect(state.fieldErrors.value).toEqual([
      { client_id: 'rule-2', field: 'monitor_path', message: '监控目录重叠' },
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
    expect(state.warnings.value).toEqual(responseData.warnings)
  })
})
