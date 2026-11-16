import { AxiosHeaders } from 'axios'
import { describe, expect, expectTypeOf, it } from 'vitest'
import { getSettingsSaveResult, unwrapResponse } from '@/api/response'
import type { APIResponse } from '@/api/types'
import { HttpResponseError, parseHttpError, type HttpErrorResponse } from '@/http/errors'

const response = <T>(data: T, status = 200) => ({
  data: { status, data },
  status,
  statusText: 'OK',
  headers: {},
  config: { headers: new AxiosHeaders(), method: 'post', url: '/api/example' },
})

describe('公共响应包络', () => {
  it.each([null, false, 0, '', [], {}])('成功响应保留合法 data：%j', (data) => {
    expect(unwrapResponse(response(data)).data).toBe(data)
  })

  it('一次解包保留成功消息、顶层警告和领域数据', () => {
    const data = { id: 1, warnings: [{ code: 'QUEUE_NOT_EMPTY', message: '领域任务状态' }] }
    const original = response(data)
    const body: APIResponse<typeof data> = {
      ...original.data,
      message: '测试保存反馈',
      warnings: ['测试应用警告'],
    }
    const result = unwrapResponse({ ...original, data: body })
    expectTypeOf(result.data).toEqualTypeOf<typeof data>()
    expect(result).toEqual(body)
    expect(result.data).toBe(data)
    expect(getSettingsSaveResult({ ...original, data: body })).toEqual({
      message: '测试保存反馈',
      warnings: ['测试应用警告'],
    })
  })

  it('可选成功元信息缺省时归一为空警告，消息留给操作级回退', () => {
    expect(unwrapResponse(response(null))).toEqual({ status: 200, data: null, warnings: [] })
    expect(getSettingsSaveResult(response(null))).toEqual({ warnings: [] })
  })

  it.each([201, 202])('保留 HTTP %s 的创建或受理结果', (status) => {
    expect(unwrapResponse(response({ id: 12 }, status)).status).toBe(status)
  })

  it('原生 fetch 的现有响应结构复用同一解包，并保留领域类型', () => {
    const original: HttpErrorResponse = {
      status: 200,
      data: { status: 200, data: { entries: [], pos: 12 } },
      config: { method: 'get', url: '/api/logs' },
    }
    expect(unwrapResponse<{ entries: unknown[]; pos: number }>(original).data.pos).toBe(12)
  })

  it('请求失败保留部分执行 data、顶层字段错误和原始响应', () => {
    const original = {
      ...response({ accepted: 2, items: [{ rule_id: 1, accepted: 2 }] }, 409),
      data: {
        status: 409,
        data: { accepted: 2, items: [{ rule_id: 1, accepted: 2 }] },
        error_code: 'DIRECTORY_UPLOAD_RULE_CONFLICT',
        message: '测试扫描冲突',
        instance: '/request-errors/abcdefghijklmnopqrstuvwx',
        field_errors: [{ field: 'monitor_path', message: '测试字段反馈', client_id: 'rule-1' }],
      },
    }
    try {
      unwrapResponse(original)
      expect.unreachable('失败响应不能返回成功数据')
    } catch (error) {
      expect(error).toBeInstanceOf(HttpResponseError)
      expect(parseHttpError(error)).toMatchObject({ kind: 'application', response: original })
      expect(parseHttpError(error).details).toBe(original.data.data)
      expect((error as HttpResponseError).config).toBe(original.config)
    }
  })

  it.each([
    { data: null },
    { status: 200 },
    { status: 202, data: null },
    { status: 200, data: null, message: 12 },
    { status: 200, data: null, warnings: ['测试警告', 12] },
    { status: 200, data: null, error_code: 'INTERNAL_ERROR' },
  ])('拒绝缺失或不一致的传输字段：%j', (data) => {
    expect(() => unwrapResponse({ ...response(null), data })).toThrow(HttpResponseError)
  })
})
