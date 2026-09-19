import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'

export interface SystemVersion {
  version: string
  build_time?: number
  date: string
  isWindows: boolean
  isRelease: boolean
}

// 版本接口直接返回对象，历史契约不包含业务 code 包络。
export async function fetchSystemVersion(http: AxiosInstance): Promise<SystemVersion> {
  const response = await http.get<SystemVersion>(`${SERVER_URL}/version`)
  if (
    response.status < 200 ||
    response.status >= 300 ||
    !response.data ||
    typeof response.data.version !== 'string'
  ) {
    throw new HttpResponseError(response)
  }
  return response.data
}
