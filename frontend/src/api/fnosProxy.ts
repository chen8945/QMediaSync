import type { AxiosInstance, AxiosResponse } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface FnosPathMapping {
  fnos_root: string
  local_root: string
}

export interface FnosProxyConfig {
  enabled: boolean
  fnos_url: string
  path_mappings: FnosPathMapping[]
}

export interface FnosProxyRuntime {
  running: boolean
  listen_port: number
  https_enabled: boolean
  https_port: number
  restart_required: boolean
  last_error: string
}

export interface FnosProxySettings {
  config: FnosProxyConfig
  runtime: FnosProxyRuntime
}

const fieldLabels: Readonly<Record<string, string>> = {
  fnos_url: '飞牛影视地址',
  path_mappings: 'STRM 路径映射',
}

export function fnosProxyPublicMessage(message: string): string {
  return message
    .replace(/^(fnos_url|path_mappings)：/, (_match, field: string) => `${fieldLabels[field]}：`)
    .replace('QMS 可访问根目录', 'QMediaSync 中的 STRM 文件夹')
    .replace('飞牛影视 根目录', '飞牛影视中的 STRM 文件夹')
}

const isPort = (value: unknown) =>
  typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= 65535

const settingsResponse = (response: AxiosResponse<APIResponse<FnosProxySettings>>) => {
  const data = unwrapResponse(response)
  const config = data?.config
  const runtime = data?.runtime
  if (
    !config ||
    typeof config.enabled !== 'boolean' ||
    typeof config.fnos_url !== 'string' ||
    !Array.isArray(config.path_mappings) ||
    !config.path_mappings.every(
      (row) =>
        row !== null &&
        typeof row === 'object' &&
        typeof row.fnos_root === 'string' &&
        typeof row.local_root === 'string',
    ) ||
    !runtime ||
    typeof runtime.running !== 'boolean' ||
    !isPort(runtime.listen_port) ||
    typeof runtime.https_enabled !== 'boolean' ||
    !isPort(runtime.https_port) ||
    typeof runtime.restart_required !== 'boolean' ||
    typeof runtime.last_error !== 'string'
  ) {
    throw new HttpResponseError(response)
  }
  return data
}

export async function fetchFnosProxySettings(http: AxiosInstance): Promise<FnosProxySettings> {
  return settingsResponse(
    await http.get<APIResponse<FnosProxySettings>>(`${SERVER_URL}/setting/fnos-proxy`),
  )
}

export async function saveFnosProxySettings(
  http: AxiosInstance,
  config: FnosProxyConfig,
): Promise<FnosProxySettings> {
  return settingsResponse(
    await http.post<APIResponse<FnosProxySettings>>(`${SERVER_URL}/setting/fnos-proxy`, config),
  )
}
