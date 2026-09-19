import { FetchRequestError, HttpResponseError, type HttpErrorResponse } from '@/http/errors'

// 现有原生 fetch 读取仍使用 Cookie；仅传输调用本身的异常属于传输错误。
export async function fetchJSONResponse(
  url: string,
  options: RequestInit = {},
): Promise<HttpErrorResponse> {
  const config = { method: options.method ?? 'GET', url }
  let response: Response
  try {
    response = await fetch(url, options)
  } catch (error) {
    throw new FetchRequestError(error, config)
  }

  let data: unknown
  try {
    data = await response.json()
  } catch (error) {
    // 读取正文时的超时/中止仍属传输错误；其余解析异常不能归为无响应。
    const name = (error as { name?: unknown } | null)?.name
    if (name === 'TimeoutError' || name === 'AbortError') throw new FetchRequestError(error, config)
    // 非 JSON 错误页仍保留 HTTP 状态。
    if (response.ok) throw new HttpResponseError({ status: response.status, data, config })
  }
  const result = { status: response.status, data, headers: response.headers, config }
  if (!response.ok) throw new HttpResponseError(result)
  return result
}
