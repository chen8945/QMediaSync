import type { AxiosResponse } from 'axios'
import { HttpResponseError } from '@/http/errors'
import type { APIResponse } from './types'

export function unwrapResponse<T>(response: AxiosResponse<APIResponse<T>>): T {
  if (response.status < 200 || response.status >= 300 || response.data?.code !== 200) {
    throw new HttpResponseError(response)
  }
  return response.data.data
}
