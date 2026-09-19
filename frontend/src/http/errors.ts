import { isAxiosError, isCancel } from 'axios'

export type HttpErrorKind =
  | 'cancelled'
  | 'timeout'
  | 'network'
  | 'unauthorized'
  | 'origin'
  | 'csrf'
  | 'forbidden'
  | 'not-found'
  | 'method-not-allowed'
  | 'rate-limit'
  | 'server'
  | 'application'
  | 'unknown'

export interface HttpRequestContext {
  method?: string
  url?: string
}

export interface HttpErrorResponse {
  status: number
  data: unknown
  config?: HttpRequestContext
  headers?: unknown
}

export interface HttpErrorDiagnostics {
  method?: string
  path?: string
  status?: number
  errorCode?: string
}

export interface ParsedHttpError {
  kind: HttpErrorKind
  message: string
  handled: boolean
  shouldNotify: boolean
  diagnostics: HttpErrorDiagnostics
  response?: HttpErrorResponse
  details: unknown
  retryAfterSeconds?: number
}

export interface ParseHttpErrorOptions {
  fallbackMessage?: string
  // 领域 API 可改写字段名等文案；其他业务原因直接使用服务端消息。
  publicMessages?: Readonly<Record<string, string>>
}

const handledAuthErrors = new WeakSet<object>()

export function markAuthInvalidationHandled(error: object): void {
  handledAuthErrors.add(error)
}

const asRecord = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' ? (value as Record<string, unknown>) : undefined

// 保留原始响应供领域层读取字段错误；日志只能使用 parseHttpError 的 diagnostics。
export class HttpResponseError extends Error {
  readonly config?: HttpRequestContext

  constructor(readonly response: HttpErrorResponse) {
    super(parseHttpError({ response }).message)
    this.name = 'HttpResponseError'
    this.config = response.config
  }
}

// 只在 fetch 调用本身的 catch 中包装，避免把后续解析/程序异常当成网络故障。
export class FetchRequestError extends Error {
  constructor(
    readonly cause: unknown,
    readonly config: HttpRequestContext,
  ) {
    super('请求失败')
    this.name = 'FetchRequestError'
  }
}

const errorKinds: Record<string, HttpErrorKind> = {
  REQUEST_ORIGIN_INVALID: 'origin',
  CSRF_TOKEN_INVALID: 'csrf',
  AUTHENTICATION_REQUIRED: 'unauthorized',
  AUTHENTICATION_INVALID: 'unauthorized',
  SESSION_INVALID: 'unauthorized',
  FORBIDDEN: 'forbidden',
}

const messages: Record<HttpErrorKind, string> = {
  cancelled: '',
  timeout: '请求超时，请稍后重试',
  network: '无法获取服务器响应，请检查网络连接、服务状态或访问配置',
  unauthorized: '登录已失效，请重新登录',
  origin: '访问地址校验失败。使用反向代理时，请检查域名、协议和端口的转发配置',
  csrf: '请求安全校验失败，请刷新页面后重试；若问题持续，请重新登录',
  forbidden: '请求被服务器拒绝',
  'not-found': '请求的资源或接口不存在',
  'method-not-allowed': '请求方式不受支持',
  'rate-limit': '请求过于频繁，请稍后重试',
  server: '服务器处理请求失败。如问题持续，请查看服务日志',
  application: '操作失败，请稍后重试',
  unknown: '请求失败，请稍后重试',
}

const safeMethod = (method: unknown) =>
  typeof method === 'string' ? method.toUpperCase() : undefined

// 丢弃协议、凭据、主机与查询参数，避免诊断日志泄露令牌。
const safePath = (url: unknown) =>
  typeof url === 'string'
    ? url.split(/[?#]/)[0]!.replace(/^[a-z][a-z\d+.-]*:\/\/[^/]*/i, '') || '/'
    : undefined

// 兼容当前登录限流接口的明确等待文案，不从其他错误文本猜测原因。
const getRetryAfterSeconds = (body: Record<string, unknown> | undefined) => {
  const value =
    typeof body?.message === 'string'
      ? /^请求过于频繁，请 (\d+) 秒后再试$/.exec(body.message)?.[1]
      : undefined
  return value === undefined ? undefined : Number(value)
}

export function parseHttpError(
  error: unknown,
  options: ParseHttpErrorOptions = {},
): ParsedHttpError {
  const record = asRecord(error)
  const response = asRecord(record?.response) as HttpErrorResponse | undefined
  const body = asRecord(response?.data)
  const details = body?.data
  const nested = asRecord(details)
  const code = body?.error_code ?? nested?.error_code
  const errorCode = typeof code === 'string' ? code : undefined
  const config = asRecord(record?.config) ?? asRecord(response?.config)
  const method = safeMethod(config?.method)
  const path = safePath(config?.url)
  const status = typeof response?.status === 'number' ? response.status : undefined
  const transportError = error instanceof FetchRequestError ? error.cause : error
  const transport = asRecord(transportError)
  let kind: HttpErrorKind = 'unknown'

  if (isCancel(transportError) || transport?.name === 'AbortError') {
    kind = 'cancelled'
  } else if (
    // 客户端开启 clarifyTimeoutError，ECONNABORTED 仅表示浏览器中止，不归为超时。
    (isAxiosError(error) && error.code === 'ETIMEDOUT') ||
    (error instanceof FetchRequestError && transport?.name === 'TimeoutError')
  ) {
    kind = 'timeout'
  } else if (errorCode && Object.hasOwn(errorKinds, errorCode)) {
    kind = errorKinds[errorCode]!
  } else if (!errorCode && status === 403 && body?.message === '请求来源无效') {
    kind = 'origin'
  } else if (!errorCode && status === 403 && body?.message === 'CSRF 校验失败') {
    kind = 'csrf'
  } else if (status === 401) {
    kind = 'unauthorized'
  } else if (status === 403) {
    kind = 'forbidden'
  } else if (status === 404) {
    kind = 'not-found'
  } else if (status === 405) {
    kind = 'method-not-allowed'
  } else if (status === 429) {
    kind = 'rate-limit'
  } else if (status !== undefined && status >= 500) {
    kind = 'server'
  } else if (
    !response &&
    ((isAxiosError(error) && (error.code === 'ERR_NETWORK' || error.request != null)) ||
      (error instanceof FetchRequestError && transportError instanceof TypeError))
  ) {
    kind = 'network'
  } else if (typeof body?.code === 'number' && body.code !== 200) {
    kind = 'application'
  }

  let message = messages[kind]
  if (kind === 'application' || kind === 'unknown') {
    message = options.fallbackMessage ?? message
    const reason = typeof body?.message === 'string' ? body.message.trim() : ''
    if (kind === 'application' && reason) {
      message =
        options.publicMessages && Object.hasOwn(options.publicMessages, reason)
          ? options.publicMessages[reason]!
          : reason
    }
  }
  if (kind === 'timeout' && method && !['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    message = '请求超时，操作结果尚未确认。请先检查操作是否已生效，避免重复提交'
  }
  const retryAfterSeconds = kind === 'rate-limit' ? getRetryAfterSeconds(body) : undefined
  if (retryAfterSeconds !== undefined) {
    message = `请求过于频繁，请 ${retryAfterSeconds} 秒后再试`
  }
  const handled = record !== undefined && handledAuthErrors.has(record)
  const diagnostics: HttpErrorDiagnostics = {}
  if (method) diagnostics.method = method
  if (path) diagnostics.path = path
  if (status !== undefined) diagnostics.status = status
  if (errorCode) diagnostics.errorCode = errorCode

  return {
    kind,
    message,
    handled,
    shouldNotify: kind !== 'cancelled' && !handled,
    diagnostics,
    response,
    details,
    retryAfterSeconds,
  }
}
