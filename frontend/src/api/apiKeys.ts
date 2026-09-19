import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface ApiKey {
  id: number
  name: string
  key_prefix: string
  last_used_at?: number | null
  created_at: number
  is_active: boolean
}

export interface CreatedApiKey extends ApiKey {
  key: string
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const apiKeyPublicMessages: Readonly<Record<string, string>> = {
  'name：长度超出允许范围': 'API Key 名称长度必须在 1 到 64 个字符之间',
  'name：不能包含控制字符': 'API Key 名称不能包含控制字符',
  'is_active：不能为空': '请选择 API Key 状态',
}

export async function fetchApiKeys(http: AxiosInstance): Promise<ApiKey[]> {
  return (
    unwrapResponse(await http.get<APIResponse<ApiKey[] | null>>(`${SERVER_URL}/api-keys`)) ?? []
  )
}

export async function createApiKey(http: AxiosInstance, name: string): Promise<CreatedApiKey> {
  return unwrapResponse(
    await http.post<APIResponse<CreatedApiKey>>(`${SERVER_URL}/api-keys`, { name }),
  )
}

export async function updateApiKeyStatus(
  http: AxiosInstance,
  id: number,
  isActive: boolean,
): Promise<void> {
  unwrapResponse(
    await http.put<APIResponse<null>>(`${SERVER_URL}/api-keys/${id}/status`, {
      is_active: isActive,
    }),
  )
}

export async function deleteApiKey(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/api-keys/${id}`))
}
