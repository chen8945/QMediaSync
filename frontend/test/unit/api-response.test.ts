import { AxiosHeaders } from 'axios'
import { describe, expect, it } from 'vitest'
import { unwrapResponse } from '@/api/response'
import { HttpResponseError, parseHttpError } from '@/http/errors'

const response = <T>(data: T, code = 200, status = 200) => ({
  data: { code, message: '', data },
  status,
  statusText: 'OK',
  headers: {},
  config: { headers: new AxiosHeaders(), method: 'post', url: '/api/example' },
})

describe('业务响应校验', () => {
  it.each([null, false, 0, '', [], {}])('成功响应保留合法 data：%j', (data) => {
    expect(unwrapResponse(response(data))).toBe(data)
  })

  it('成功数据中的警告原样交给调用方', () => {
    const data = { id: 1, warnings: [{ code: 'QUEUE_NOT_EMPTY', message: '仍有排队任务' }] }
    expect(unwrapResponse(response(data))).toBe(data)
  })

  it('HTTP 200 的业务失败抛出保留完整响应的错误', () => {
    const original = response({ field_errors: [{ field: 'name' }], warnings: ['warning'] }, 500)
    try {
      unwrapResponse(original)
      expect.unreachable('业务失败不能返回成功数据')
    } catch (error) {
      expect(error).toBeInstanceOf(HttpResponseError)
      expect(parseHttpError(error)).toMatchObject({ kind: 'application', response: original })
      expect(parseHttpError(error).details).toBe(original.data.data)
      expect((error as HttpResponseError).config).toBe(original.config)
    }
  })

  it('非成功 HTTP 和缺失业务码均不能被当成成功', () => {
    expect(() => unwrapResponse(response(null, 200, 503))).toThrow(HttpResponseError)
    const malformed = { ...response(null), data: { data: null } }
    expect(() => unwrapResponse(malformed as never)).toThrow(HttpResponseError)
  })
})
