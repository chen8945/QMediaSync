import axios, { AxiosError, type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import * as backup from '@/api/backup'
import * as update from '@/api/update'
import { HttpResponseError, markAuthInvalidationHandled, parseHttpError } from '@/http/errors'
import { installAuthResponseInterceptor } from '@/http/authInterceptor'

const settings: backup.BackupConfigInput = {
  backup_enabled: 1,
  backup_cron: '0 3 * * *',
  backup_retention: 7,
  backup_max_count: 10,
  backup_compress: 1,
}
const file = new File(['archive'], 'backup.zip', { type: 'application/zip' })
const operations = (http: AxiosInstance) => [
  () => backup.fetchBackupConfig(http),
  () => backup.saveBackupConfig(http, settings),
  () => backup.fetchBackupRecords(http, { page: 2, page_size: 50, type: 'all' }),
  () => backup.createBackup(http, '手动备份'),
  () => backup.deleteBackup(http, 7),
  () => backup.restoreBackup(http, 7),
  () => backup.uploadAndRestoreBackup(http, file),
  () => backup.fetchBackupStatus(http),
  () => update.fetchUpdateList(http, 'github', true),
  () => update.fetchUpdateProgress(http),
  () => update.startUpdate(http, 'v1.2.3', 'github'),
  () => update.cancelUpdate(http),
  () => update.fetchIsFnOS(http),
  () => backup.restartAfterRestore(http, 'test-restore-receipt'),
]

describe('备份和更新 API', () => {
  it('上传超限保留安全原因，不清理当前登录状态', async () => {
    const http = axios.create({
      adapter: async (config) => ({
        config,
        status: 413,
        statusText: '',
        headers: {},
        data: {
          code: 413,
          error_code: 'BACKUP_ARCHIVE_LIMIT',
          message: '备份文件大小不能超过 1 GiB',
        },
      }),
    })
    const clearAuth = vi.fn()
    const onAuthenticationInvalidated = vi.fn()
    installAuthResponseInterceptor(http, {
      getAuthStore: () => ({
        isAuthenticated: true,
        isLoggingOut: false,
        sessionVersion: 1,
        clearAuth,
      }),
      onAuthenticationInvalidated,
    })
    const error = await backup.uploadAndRestoreBackup(http, file).catch((error: unknown) => error)
    expect(parseHttpError(error)).toMatchObject({
      kind: 'application',
      message: '备份文件大小不能超过 1 GiB',
      diagnostics: { status: 413, errorCode: 'BACKUP_ARCHIVE_LIMIT' },
    })
    expect(clearAuth).not.toHaveBeenCalled()
    expect(onAuthenticationInvalidated).not.toHaveBeenCalled()
  })

  it('恢复入口返回查询凭证，凭证查询不因旧登录会话失效而跳转', async () => {
    const receipt = 'test-restore-receipt'
    let status = 200
    const adapter = vi.fn(async (config) => ({
      config,
      status,
      statusText: '',
      headers: {},
      data: { code: status, data: { restore_receipt: receipt } },
    }))
    const http = axios.create({ adapter })
    const clearAuth = vi.fn()
    const onAuthenticationInvalidated = vi.fn()
    installAuthResponseInterceptor(http, {
      getAuthStore: () => ({
        isAuthenticated: true,
        isLoggingOut: false,
        sessionVersion: 1,
        clearAuth,
      }),
      onAuthenticationInvalidated,
    })

    await expect(backup.restoreBackup(http, 7)).resolves.toEqual({ restore_receipt: receipt })
    await expect(backup.uploadAndRestoreBackup(http, file)).resolves.toEqual({
      restore_receipt: receipt,
    })
    await backup.fetchBackupStatus(http, receipt)
    expect(adapter.mock.lastCall?.[0]).toMatchObject({
      url: '/api/backup/status',
      skipAuthInvalidation: true,
    })
    expect(adapter.mock.lastCall?.[0].headers.get('X-Restore-Receipt')).toBe(receipt)
    expect(adapter.mock.lastCall?.[0].params).toBeUndefined()
    expect(http.getUri(adapter.mock.lastCall?.[0])).toBe('/api/backup/status')
    await backup.restartAfterRestore(http, receipt)
    expect(adapter.mock.lastCall?.[0]).toMatchObject({
      url: '/api/backup/restart',
      method: 'post',
      skipAuthInvalidation: true,
    })
    expect(adapter.mock.lastCall?.[0].headers.get('X-Restore-Receipt')).toBe(receipt)
    expect(adapter.mock.lastCall?.[0].params).toBeUndefined()
    expect(adapter.mock.lastCall?.[0].data).toBeNull()

    status = 401
    const error = await backup.fetchBackupStatus(http, receipt).catch((error: unknown) => error)
    expect(error).toBeInstanceOf(HttpResponseError)
    expect(clearAuth).not.toHaveBeenCalled()
    expect(onAuthenticationInvalidated).not.toHaveBeenCalled()
    expect(JSON.stringify(parseHttpError(error).diagnostics)).not.toContain(receipt)
    await expect(backup.restartAfterRestore(http, receipt)).rejects.toBeInstanceOf(
      HttpResponseError,
    )
    expect(clearAuth).not.toHaveBeenCalled()
    expect(onAuthenticationInvalidated).not.toHaveBeenCalled()
    await expect(backup.fetchBackupStatus(http)).rejects.toBeInstanceOf(HttpResponseError)
    expect(onAuthenticationInvalidated).toHaveBeenCalledOnce()
  })

  it('保留接口、方法、参数、上传体与专用超时，成功 null / false 不被误判', async () => {
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: { code: 200, message: '', data: config.url.endsWith('is-fn-os') ? false : null },
    }))
    const http = axios.create({ adapter, timeout: 8765 })
    for (const request of operations(http)) await request()
    const calls = adapter.mock.calls.map(([config]) => config)
    expect(calls.map(({ method, url }) => [method, url])).toEqual([
      ['get', '/api/backup/config'],
      ['put', '/api/backup/config'],
      ['get', '/api/backup/list'],
      ['post', '/api/backup/create'],
      ['delete', '/api/backup/records/7'],
      ['post', '/api/backup/restore'],
      ['post', '/api/backup/upload-restore'],
      ['get', '/api/backup/status'],
      ['get', '/api/update/last?channel=github&force=1'],
      ['get', '/api/update/progress'],
      ['post', '/api/update/to-version'],
      ['post', '/api/update/cancel'],
      ['get', '/api/path/is-fn-os'],
      ['post', '/api/backup/restart'],
    ])
    expect(JSON.parse(calls[1].data)).toEqual(settings)
    expect(calls[2].params).toEqual({ page: 2, page_size: 50, type: 'all' })
    expect(JSON.parse(calls[3].data)).toEqual({ reason: '手动备份' })
    expect(JSON.parse(calls[5].data)).toEqual({ record_id: 7 })
    expect(calls[6].data.get('file')).toBe(file)
    expect(calls[6].headers.get('Content-Type')).toBe('multipart/form-data')
    expect(calls[6].timeout).toBe(600000)
    expect(JSON.parse(calls[10].data)).toEqual({ version: 'v1.2.3', channel: 'github' })
    expect(calls.filter((_, i) => i !== 6).every((config) => config.timeout === 8765)).toBe(true)
    await expect(update.fetchIsFnOS(http)).resolves.toBe(false)
  })

  it.each([200, 403, 503])('HTTP %i 的失败响应不会完成操作', async (status) => {
    const body = { code: status === 200 ? 500 : 200, message: 'internal secret', data: null }
    const http = axios.create({
      adapter: async (config) => ({ config, status, statusText: '', headers: {}, data: body }),
    })
    for (const request of operations(http)) {
      await expect(request()).rejects.toMatchObject({
        name: 'HttpResponseError',
        response: { status, data: body },
      })
    }
  })

  it('备份 ZIP 保持 blob 请求，JSON 业务错误与 HTML 不作为文件返回', async () => {
    const zip = new Blob(['PK\u0003\u0004 archive'], { type: 'application/octet-stream' })
    const adapter = vi.fn(async (config) => ({
      config,
      status: 200,
      statusText: 'OK',
      headers: {},
      data: zip,
    }))
    await expect(backup.downloadBackup(axios.create({ adapter }), 7)).resolves.toBe(zip)
    expect(adapter.mock.calls[0][0].responseType).toBe('blob')
    for (const body of [
      new Blob([JSON.stringify({ code: 500, message: '备份记录不存在', data: null })], {
        type: 'application/json',
      }),
      new Blob([JSON.stringify({ code: 500, message: 'internal secret', data: null })]),
      new Blob(['<html>secret</html>'], { type: 'text/html' }),
    ]) {
      const http = axios.create({
        adapter: async (config) => ({
          config,
          status: 200,
          statusText: 'OK',
          headers: {},
          data: body,
        }),
      })
      await expect(backup.downloadBackup(http, 7)).rejects.toBeInstanceOf(HttpResponseError)
    }
  })

  it('拒绝的 blob 响应可识别 CSRF，已处理 401 保留同一异常与静默标记', async () => {
    for (const status of [403, 401]) {
      let thrown: AxiosError | undefined
      const http = axios.create({
        adapter: async (config) => {
          const response = {
            config,
            status,
            statusText: '',
            headers: {},
            data: new Blob(
              [
                JSON.stringify({
                  code: 500,
                  message: 'internal secret',
                  error_code: status === 403 ? 'CSRF_TOKEN_INVALID' : 'SESSION_INVALID',
                }),
              ],
              { type: 'application/json' },
            ),
          }
          thrown = new AxiosError('request secret', 'ERR_BAD_REQUEST', config, undefined, response)
          if (status === 401) markAuthInvalidationHandled(thrown)
          throw thrown
        },
      })
      const error = await backup.downloadBackup(http, 7).catch((error: unknown) => error)
      expect(error).toBe(thrown)
      expect(parseHttpError(error)).toMatchObject({
        kind: status === 403 ? 'csrf' : 'unauthorized',
        shouldNotify: status !== 401,
      })
    }
  })

  it('写入超时只提交一次并保留结果未确认提示', async () => {
    const adapter = vi.fn(async (config) => {
      throw new AxiosError('private secret', 'ETIMEDOUT', config)
    })
    const error = await update
      .cancelUpdate(axios.create({ adapter }))
      .catch((error: unknown) => error)
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(parseHttpError(error).message).toContain('操作结果尚未确认')
  })
})
