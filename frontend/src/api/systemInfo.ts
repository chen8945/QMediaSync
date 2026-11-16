import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface SystemVersion {
  version: string
  build_time?: number
  date: string
  isWindows: boolean
  isRelease: boolean
}

export async function fetchSystemVersion(http: AxiosInstance): Promise<SystemVersion> {
  const response = await http.get<APIResponse<SystemVersion>>(`${SERVER_URL}/version`)
  const data = unwrapResponse(response).data
  if (!data || typeof data.version !== 'string') {
    throw new HttpResponseError(response)
  }
  return data
}
