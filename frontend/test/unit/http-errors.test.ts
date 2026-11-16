import { AxiosError, AxiosHeaders, CanceledError } from 'axios'
import { describe, expect, it } from 'vitest'
import {
  FetchRequestError,
  HttpResponseError,
  markAuthInvalidationHandled,
  parseHttpError,
} from '@/http/errors'

const instance = '/request-errors/abcdefghijklmnopqrstuvwx'
const failureBody = (status: number, error_code = 'INVALID_REQUEST', message = '测试失败') => ({
  status,
  data: null as unknown,
  error_code,
  message,
  instance,
})
const responseError = (status: number, data: unknown, method = 'get') =>
  new HttpResponseError({ status, data, config: { method, url: '/api/example' } })

describe('公共 HTTP 错误解析', () => {
  it('恢复维护保留明确的重启操作提示', () => {
    const parsed = parseHttpError(
      responseError(503, failureBody(503, 'DATABASE_MAINTENANCE', 'internal secret')),
    )
    expect(parsed.kind).toBe('maintenance')
    expect(parsed.message).toContain('重启')
    expect(parsed.shouldNotify).toBe(true)
    expect(parsed.message).not.toContain('secret')
  })

  it.each([
    [403, 'REQUEST_ORIGIN_INVALID', 'origin'],
    [403, 'CSRF_TOKEN_INVALID', 'csrf'],
    [401, 'AUTHENTICATION_REQUIRED', 'unauthorized'],
    [401, 'AUTHENTICATION_INVALID', 'unauthorized'],
    [401, 'SESSION_INVALID', 'unauthorized'],
    [401, 'RESTORE_RECEIPT_INVALID', 'application'],
    [403, 'FORBIDDEN', 'forbidden'],
  ])('依据 HTTP %s 与顶层机器码 %s 分类', (status, error_code, kind) => {
    const failure = parseHttpError(
      responseError(Number(status), failureBody(Number(status), String(error_code))),
    )
    expect(failure.kind).toBe(kind)
    expect(failure.diagnostics.errorCode).toBe(error_code)
  })

  it('请求字段与部分执行结果原样保留，领域内部错误码不参与请求分类', () => {
    const data = {
      accepted: 2,
      error_code: 'CSRF_TOKEN_INVALID',
      items: [{ rule_id: 1, accepted: 2 }],
    }
    const field_errors = [{ field: 'monitor_path', message: '测试字段原因', client_id: 'rule-1' }]
    const error = responseError(409, {
      ...failureBody(409, 'DIRECTORY_UPLOAD_RULE_CONFLICT'),
      data,
      field_errors,
    })
    const failure = parseHttpError(error)
    expect(failure.kind).toBe('application')
    expect(failure.diagnostics.errorCode).toBe('DIRECTORY_UPLOAD_RULE_CONFLICT')
    expect(failure.details).toBe(data)
    expect(failure.response).toBe(error.response)
    expect((failure.response?.data as { field_errors: unknown }).field_errors).toBe(field_errors)
  })

  it('未知码按 HTTP 回退，不根据消息猜测来源或 CSRF 原因', () => {
    const failure = parseHttpError(
      responseError(403, failureBody(403, 'NEW_ERROR', 'CSRF 校验失败')),
    )
    expect(failure.kind).toBe('forbidden')
    expect(failure.diagnostics.errorCode).toBe('NEW_ERROR')
  })

  it('登录凭据失败保持统一说明，不透传具体匹配原因', () => {
    const messages = ['用户不存在', '密码错误', '验证码错误']
    for (const message of messages) {
      const failure = parseHttpError(
        responseError(401, failureBody(401, 'AUTHENTICATION_INVALID', message)),
      )
      expect(failure.kind).toBe('unauthorized')
      expect(failure.message).toBe('登录失败')
    }
  })

  it.each([
    [401, 'unauthorized'],
    [403, 'forbidden'],
    [404, 'not-found'],
    [405, 'method-not-allowed'],
    [429, 'rate-limit'],
    [500, 'server'],
    [502, 'server'],
  ])('非包络 HTTP %s 只使用安全状态回退', (status, kind) => {
    const failure = parseHttpError(responseError(Number(status), '<html>upstream secret</html>'))
    expect(failure.kind).toBe(kind)
    expect(failure.message).not.toContain('secret')
  })

  it('认证标记不能覆盖真实内部故障，也不读取成功任务内的认证错误码', () => {
    expect(
      parseHttpError(responseError(500, failureBody(500, 'AUTHENTICATION_REQUIRED'))).kind,
    ).toBe('server')
    const failure = parseHttpError(
      responseError(200, {
        status: 200,
        data: { error_code: 'SESSION_INVALID', status: 'failed' },
      }),
    )
    expect(failure.kind).toBe('unknown')
    expect(failure.diagnostics.errorCode).toBeUndefined()
  })

  it('只有真正传输失败归类无响应，程序异常和 Axios 参数错误保持未知', () => {
    expect(parseHttpError(new AxiosError('secret', 'ERR_NETWORK')).kind).toBe('network')
    expect(parseHttpError(new AxiosError('secret', 'ECONNRESET', undefined, {})).kind).toBe(
      'network',
    )
    expect(parseHttpError(new FetchRequestError(new TypeError('Failed to fetch'), {})).kind).toBe(
      'network',
    )
    for (const error of [
      new Error('network unavailable'),
      new TypeError('Failed to fetch'),
      new AxiosError('invalid option secret', 'ERR_BAD_OPTION_VALUE'),
      new FetchRequestError(new SyntaxError('invalid JSON secret'), {}),
      undefined,
      null,
      'arbitrary secret',
    ]) {
      const failure = parseHttpError(error, { fallbackMessage: '测试操作回退' })
      expect(failure.kind).toBe('unknown')
      expect(failure.message).toBe('测试操作回退')
    }
  })

  it('取消请求不提示，超时写操作提示结果未确认', () => {
    for (const error of [
      new CanceledError('secret'),
      new FetchRequestError(new DOMException('secret', 'AbortError'), {}),
    ]) {
      expect(parseHttpError(error)).toMatchObject({ kind: 'cancelled', shouldNotify: false })
    }
    expect(parseHttpError(new AxiosError('secret', 'ETIMEDOUT')).kind).toBe('timeout')
    for (const method of ['post', 'put', 'patch', 'delete']) {
      const failure = parseHttpError(
        new AxiosError('timeout secret', 'ETIMEDOUT', { method, headers: new AxiosHeaders() }),
      )
      expect(failure.kind).toBe('timeout')
      expect(failure.message).toContain('操作结果尚未确认')
      expect(failure.message).toContain('避免重复提交')
    }
    expect(
      parseHttpError(new AxiosError('Request aborted', 'ECONNABORTED', undefined, {})).kind,
    ).toBe('network')
    expect(
      parseHttpError(new FetchRequestError(new DOMException('timeout', 'TimeoutError'), {})).kind,
    ).toBe('timeout')
  })

  it.each([400, 403, 404, 409, 500, 502])('合法 HTTP %s 失败保留安全服务端消息', (status) => {
    expect(
      parseHttpError(
        responseError(status, failureBody(status, 'REQUEST_FAILED', '  测试安全失败信息  ')),
        { fallbackMessage: '测试操作回退' },
      ).message,
    ).toBe('测试安全失败信息')
  })

  it.each([
    { message: 'secret' },
    { ...failureBody(500), status: 401 },
    { ...failureBody(500), message: '' },
    { ...failureBody(500), instance: 'https://user:secret@example.test/?token=secret' },
    { status: 500, error_code: 'INTERNAL_ERROR', message: 'secret', instance },
  ])('不完整或不安全的包络不回显原文及实例：%j', (data) => {
    const failure = parseHttpError(responseError(500, data))
    expect(failure.kind).toBe('server')
    expect(failure.message).not.toContain('secret')
    expect(failure.diagnostics.errorCode).toBeUndefined()
    expect(failure.diagnostics.instance).toBeUndefined()
  })

  it('保留必要字段文案改写，普通业务原因不依赖映射', () => {
    const options = { publicMessages: { 测试字段原因: '测试本地字段反馈' } }
    expect(
      parseHttpError(
        responseError(400, failureBody(400, 'INVALID_REQUEST', '测试字段原因')),
        options,
      ).message,
    ).toBe('测试本地字段反馈')
    expect(
      parseHttpError(
        responseError(400, failureBody(400, 'INVALID_REQUEST', 'constructor')),
        options,
      ).message,
    ).toBe('constructor')
  })

  it.each([
    'https://user:password@example.test/api/login?token=secret#secret',
    '//user:password@example.test/api/login?token=secret#secret',
  ])('诊断只包含筛选后的方法、路径、状态、机器码与安全实例：%s', (url) => {
    const config = {
      headers: new AxiosHeaders({ Authorization: 'Bearer secret', Cookie: 'secret' }),
      method: 'post',
      url,
      data: { password: 'secret' },
    }
    const response = {
      config,
      status: 403,
      statusText: 'secret',
      headers: {},
      data: failureBody(403, 'REQUEST_ORIGIN_INVALID', 'secret'),
    }
    const failure = parseHttpError(
      new AxiosError('secret', 'ERR_BAD_REQUEST', config, { secret: true }, response),
    )
    expect(failure.diagnostics).toEqual({
      method: 'POST',
      path: '/api/login',
      status: 403,
      errorCode: 'REQUEST_ORIGIN_INVALID',
      instance,
    })
    expect(failure.message).not.toContain('secret')
  })

  it.each([{ 'Retry-After': '83' }, new AxiosHeaders({ 'Retry-After': '83' })])(
    '从 Retry-After 读取等待秒数，不解析产品文案',
    (headers) => {
      const error = new HttpResponseError({ status: 429, data: '<html>secret</html>', headers })
      expect(parseHttpError(error).retryAfterSeconds).toBe(83)
    },
  )

  it('无有效 Retry-After 时不猜测等待秒数', () => {
    for (const value of [undefined, 'soon', '-1', '1.5']) {
      const error = new HttpResponseError({
        status: 429,
        data: failureBody(429, 'RATE_LIMITED', '测试限流反馈'),
        headers: { 'Retry-After': value },
      })
      expect(parseHttpError(error).retryAfterSeconds).toBeUndefined()
    }
  })

  it('已处理认证错误保持分类和原响应但抑制提示', () => {
    const error = responseError(401, failureBody(401, 'SESSION_INVALID'))
    markAuthInvalidationHandled(error)
    expect(parseHttpError(error)).toMatchObject({
      kind: 'unauthorized',
      handled: true,
      shouldNotify: false,
      response: error.response,
    })
  })
})
