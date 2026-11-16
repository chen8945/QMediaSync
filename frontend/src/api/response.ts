import { HttpResponseError, type HttpErrorResponse } from '@/http/errors'
import { isAPIResponse } from '@/http/envelope'
import type { APIResponse, SettingsSaveResult } from './types'

export function unwrapResponse<T>(
  response: HttpErrorResponse & { data: APIResponse<T> },
): APIResponse<T> & { warnings: string[] }
export function unwrapResponse<T = unknown>(
  response: HttpErrorResponse,
): APIResponse<T> & { warnings: string[] }
export function unwrapResponse<T>(
  response: HttpErrorResponse,
): APIResponse<T> & { warnings: string[] } {
  if (response.status < 200 || response.status >= 300 || !isAPIResponse(response)) {
    throw new HttpResponseError(response)
  }
  const body = response.data as APIResponse<T>
  return { ...body, warnings: body.warnings ?? [] }
}

export function getSettingsSaveResult(response: HttpErrorResponse): SettingsSaveResult {
  const { warnings, message } = unwrapResponse(response)
  return message?.trim() ? { warnings, message } : { warnings }
}
