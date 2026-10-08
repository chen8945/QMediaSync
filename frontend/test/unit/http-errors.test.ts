import { AxiosError, AxiosHeaders, CanceledError } from 'axios'
import { describe, expect, it } from 'vitest'
import {
  FetchRequestError,
  HttpResponseError,
  markAuthInvalidationHandled,
  parseHttpError,
} from '@/http/errors'

const responseError = (status: number, data: unknown, method = 'get') =>
  new HttpResponseError({ status, data, config: { method, url: '/api/example' } })

describe('公共 HTTP 错误解析', () => {
  it('恢复维护错误在登录和普通请求中明确提示重启服务，不泄露服务端原文', () => {
    const parsed = parseHttpError(
      responseError(503, {
        code: 503,
        error_code: 'DATABASE_MAINTENANCE',
        message: 'internal secret',
      }),
    )
    expect(parsed.kind).toBe('maintenance')
    expect(parsed.message).toBe(
      '数据库恢复维护中，服务暂不可用。请等待恢复结束，重启 QMS 服务后重新登录',
    )
    expect(parsed.shouldNotify).toBe(true)
    expect(parsed.message).not.toContain('secret')
  })
  it.each([
    ['REQUEST_ORIGIN_INVALID', 'origin'],
    ['CSRF_TOKEN_INVALID', 'csrf'],
    ['AUTHENTICATION_REQUIRED', 'unauthorized'],
    ['AUTHENTICATION_INVALID', 'unauthorized'],
    ['SESSION_INVALID', 'unauthorized'],
    ['FORBIDDEN', 'forbidden'],
  ])('优先使用顶层机器码 %s', (error_code, kind) => {
    const failure = parseHttpError(
      responseError(403, {
        code: 500,
        message: 'CSRF 校验失败',
        error_code,
        data: { error_code: 'CSRF_TOKEN_INVALID' },
      }),
    )
    expect(failure.kind).toBe(kind)
    expect(failure.diagnostics.errorCode).toBe(error_code)
  })

  it('兼容嵌套机器码并原样保留字段错误、警告与响应', () => {
    const data = {
      error_code: 'DIRECTORY_UPLOAD_RULE_CONFLICT',
      field_errors: [{ field: 'monitor_path', message: '目录冲突', client_id: 'rule-1' }],
      warnings: ['目录仍有排队任务'],
    }
    const error = responseError(409, { code: 500, data })
    const failure = parseHttpError(error)
    expect(failure.kind).toBe('application')
    expect(failure.diagnostics.errorCode).toBe('DIRECTORY_UPLOAD_RULE_CONFLICT')
    expect(failure.details).toBe(data)
    expect(failure.response).toBe(error.response)
    expect(
      parseHttpError(responseError(403, { code: 500, data: { error_code: 'CSRF_TOKEN_INVALID' } }))
        .kind,
    ).toBe('csrf')
  })

  it.each([
    ['请求来源无效', 'origin'],
    ['请求来源校验失败', 'forbidden'],
    ['CSRF 校验失败', 'csrf'],
    ['来源不正确', 'forbidden'],
    ['CSRF 校验失败：内部错误', 'forbidden'],
  ])('仅兼容已知旧版完整消息 %s', (message, kind) => {
    expect(parseHttpError(responseError(403, { code: 500, message })).kind).toBe(kind)
  })

  it('未知顶层码按 HTTP 状态回退，不使用嵌套码或旧文案覆盖', () => {
    const failure = parseHttpError(
      responseError(403, {
        code: 500,
        error_code: 'NEW_ERROR',
        message: '请求来源无效',
        data: { error_code: 'CSRF_TOKEN_INVALID' },
      }),
    )
    expect(failure.kind).toBe('forbidden')
    expect(failure.diagnostics.errorCode).toBe('NEW_ERROR')
  })

  it.each([
    [401, 'unauthorized'],
    [403, 'forbidden'],
    [404, 'not-found'],
    [405, 'method-not-allowed'],
    [429, 'rate-limit'],
    [500, 'server'],
    [502, 'server'],
  ])('分类 HTTP %s', (status, kind) => {
    expect(parseHttpError(responseError(status, '<html>upstream secret</html>')).kind).toBe(kind)
  })

  it('业务 code 不冒充 HTTP 状态', () => {
    expect(parseHttpError(responseError(200, { code: 500 })).kind).toBe('application')
    expect(parseHttpError(responseError(200, { code: 401 })).kind).toBe('application')
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
      const failure = parseHttpError(error)
      expect(failure.kind).toBe('unknown')
      expect(failure.message).toBe('请求失败，请稍后重试')
    }
  })

  it('取消请求不提示，超时写操作提示结果未确认', () => {
    for (const error of [
      new CanceledError('secret'),
      new FetchRequestError(new DOMException('secret', 'AbortError'), {}),
    ]) {
      expect(parseHttpError(error)).toMatchObject({
        kind: 'cancelled',
        shouldNotify: false,
        message: '',
      })
    }
    for (const method of ['post', 'put', 'patch', 'delete']) {
      const failure = parseHttpError(
        new AxiosError('timeout secret', 'ETIMEDOUT', { method, headers: new AxiosHeaders() }),
      )
      expect(failure.kind).toBe('timeout')
      expect(failure.message).toContain('操作结果尚未确认')
      expect(failure.message).toContain('避免重复提交')
    }
    // 浏览器中止（非主动取消）不冒充超时。
    expect(
      parseHttpError(new AxiosError('Request aborted', 'ECONNABORTED', undefined, {})).kind,
    ).toBe('network')
    expect(
      parseHttpError(
        new FetchRequestError(new DOMException('timeout', 'TimeoutError'), { method: 'get' }),
      ).message,
    ).toBe('请求超时，请稍后重试')
  })

  it('显示新的业务原因，空消息和程序异常使用回退文案', () => {
    const options = { fallbackMessage: '保存失败' }
    expect(
      parseHttpError(
        responseError(200, { code: 500, message: '  目录已被任务 12 占用  ' }),
        options,
      ).message,
    ).toBe('目录已被任务 12 占用')
    expect(
      parseHttpError(responseError(200, { code: 1, message: '渠道暂不可用' }), options).message,
    ).toBe('渠道暂不可用')
    for (const message of ['', '  ', undefined]) {
      expect(parseHttpError(responseError(200, { code: 500, message }), options).message).toBe(
        '保存失败',
      )
    }
    expect(parseHttpError(new Error('internal error'), options).message).toBe('保存失败')
  })

  it('保留字段文案改写，普通业务原因不依赖映射', () => {
    const options = { publicMessages: { 'name：不能为空': '名称不能为空' } }
    expect(
      parseHttpError(responseError(200, { code: 500, message: 'name：不能为空' }), options).message,
    ).toBe('名称不能为空')
    expect(
      parseHttpError(responseError(200, { code: 500, message: 'constructor' }), options).message,
    ).toBe('constructor')
  })

  it('诊断只包含方法、无凭据与查询参数的路径、状态、机器码', () => {
    const config = {
      headers: new AxiosHeaders({ Authorization: 'Bearer secret', Cookie: 'secret' }),
      method: 'post',
      url: 'https://user:password@example.test/api/login?token=secret#secret',
      data: { password: 'secret' },
    }
    const response = {
      config,
      status: 403,
      statusText: 'secret',
      headers: {},
      data: { code: 500, message: 'secret', error_code: 'REQUEST_ORIGIN_INVALID' },
    }
    const failure = parseHttpError(
      new AxiosError('secret', 'ERR_BAD_REQUEST', config, { secret: true }, response),
    )
    expect(failure.diagnostics).toEqual({
      method: 'POST',
      path: '/api/login',
      status: 403,
      errorCode: 'REQUEST_ORIGIN_INVALID',
    })
    expect(failure.message).not.toContain('secret')
    expect(parseHttpError(responseError(200, {}, 'get')).diagnostics.path).toBe('/api/example')
  })

  it('保留服务端的限流等待提示，不回显任意限流错误文本', () => {
    expect(
      parseHttpError(responseError(429, { code: 500, message: '请求过于频繁，请 83 秒后再试' })),
    ).toMatchObject({ retryAfterSeconds: 83, message: '请求过于频繁，请 83 秒后再试' })
    expect(parseHttpError(responseError(429, { code: 500, message: 'secret 123' })).message).toBe(
      '请求过于频繁，请稍后重试',
    )
  })

  it('已处理的认证错误保持分类与详情但抑制提示', () => {
    const error = responseError(401, { code: 401 })
    markAuthInvalidationHandled(error)
    expect(parseHttpError(error)).toMatchObject({
      kind: 'unauthorized',
      handled: true,
      shouldNotify: false,
    })
  })
})
