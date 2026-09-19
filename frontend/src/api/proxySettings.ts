import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface ProxySettings {
  http_proxy: string
  credentials_masked?: string
}

export interface ProxySettingsPayload {
  http_proxy: string
  preserve_proxy_credentials: boolean
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const proxySettingsPublicMessages: Readonly<Record<string, string>> = {
  出站代理连接测试失败: '出站代理连接测试失败，请检查代理配置和服务状态',
  'http_proxy：不能为空': '请输入代理服务器地址',
  'http_proxy：必须是有效的代理 URL': '代理地址格式不正确，请检查协议、主机和端口',
  'http_proxy：只支持 http、https、socks5、socks5h':
    '代理地址只支持 http、https、socks5 和 socks5h',
  'http_proxy：端口必须在 1-65535 之间': '代理端口必须在 1-65535 之间',
}

export async function fetchProxySettings(http: AxiosInstance): Promise<ProxySettings> {
  const response = await http.get<APIResponse<ProxySettings>>(`${SERVER_URL}/setting/http-proxy`)
  const data = unwrapResponse(response)
  // 保存后的回读必须拿到实际脱敏地址，空响应不能被当成清空代理成功。
  if (!data || typeof data.http_proxy !== 'string') throw new HttpResponseError(response)
  return data
}

export async function saveProxySettings(
  http: AxiosInstance,
  payload: ProxySettingsPayload,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/setting/http-proxy`, payload, {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

export async function testProxyConnection(
  http: AxiosInstance,
  payload: ProxySettingsPayload,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/setting/test-http-proxy`, payload, {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}
