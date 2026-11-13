import type { AxiosResponse } from 'axios'
import { HttpResponseError } from '@/http/errors'
import type { APIResponse, SettingsSaveResult } from './types'

export function unwrapResponse<T>(response: AxiosResponse<APIResponse<T>>): T {
  if (response.status < 200 || response.status >= 300 || response.data?.code !== 200) {
    throw new HttpResponseError(response)
  }
  return response.data.data
}

export function getSettingsSaveResult(
  response: AxiosResponse<APIResponse<unknown> & { warnings?: unknown }>,
): SettingsSaveResult {
  const warnings = response.data.warnings === undefined ? [] : response.data.warnings
  if (!Array.isArray(warnings) || !warnings.every((warning) => typeof warning === 'string'))
    throw new HttpResponseError(response)
  return { warnings }
}
