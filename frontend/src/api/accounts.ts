import type { AxiosInstance, AxiosResponse } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import type { V115AuthProvider, V115AuthSourceType } from '@/components/cloud-auth/v115AuthSources'
import type { V115QrCodePayload, V115QrCodeStatusPayload } from '@/types/v115Auth'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface CloudAccount {
  id: number
  source_type: '115' | '123' | 'openlist' | 'baidupan'
  name: string
  user_id: string
  username: string
  base_url: string
  created_at: number
  authorized: boolean
  password?: string
  auth_type?: string
  app_id_name?: string
  app_name?: string
  display_name?: string
  app_id?: string
  auth_source_type?: V115AuthSourceType
  auth_provider?: V115AuthProvider
  requires_encryption_key?: boolean
  deprecated?: boolean
  token_failed_reason?: string
}

export interface CloudDiskStatus {
  user_id: string
  username: string
  used_space: number
  total_space: number
  member_level: string
  expire_time: string
}

export interface CreateAccountPayload {
  source_type: string
  name: string
  app_id?: string
  app_id_name?: string
  custom_app_name?: string
  auth_source_type?: V115AuthSourceType
  auth_provider?: V115AuthProvider
}

export interface OpenListAccountPayload {
  id?: number
  source_type?: string
  name?: string
  base_url: string
  auth_type: string
  username?: string
  password?: string
  token?: string
}

export interface PrepareAccountAuthorizationPayload {
  account_id: number
  source_type: string
  confirmed: boolean
  auth_source_type: V115AuthSourceType
  auth_provider: V115AuthProvider
  app_id?: string
  app_id_name?: string
  custom_app_name?: string
}

export interface AccountAuthorizationSession {
  authorization_id: string
  expires_in: number
}

export interface OAuthURLParams {
  account_id: number
  redirect_url: string
  authorization_id?: string
}

export interface OAuthConfirmPayload {
  account_id: number
  data?: string
  payload?: Record<string, string>
  authorization_id?: string
}

export interface V115OAuthURLData {
  auth_url?: string
  state?: string
  polling?: boolean
}

export interface V115AppIDOption {
  app_id: string
  app_name: string
  display_name: string
  deprecated?: boolean
}

export interface V115AppIDSearchResult {
  items: V115AppIDOption[] | null
  total: number
  offset?: number
  limit?: number
}

export const V115_QR_STATUS_TIMEOUT_MS = 70_000

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const accountPublicMessages: Readonly<Record<string, string>> = {
  查询开放平台账号失败: '查询账号失败',
  '账号 ID 不存在': '账号不存在，请重新加载后重试',
  '创建开放平台账号失败：账号备注已存在，请换一个': '账号备注已存在，请换一个',
  '创建开放平台账号失败：当前账号已存在，不允许添加重复账号': '当前账号已存在，不允许添加重复账号',
  '更新开放平台账号资料失败：账号备注已存在，请换一个': '账号备注已存在，请换一个',
  '更新开放平台账号资料失败：当前账号已存在，不允许添加重复账号':
    '当前账号已存在，不允许添加重复账号',
  '创建 OpenList 账号失败：当前账号已存在，不允许添加重复账号':
    '当前账号已存在，不允许添加重复账号',
  '更新 OpenList 账号失败：当前账号已存在，不允许添加重复账号':
    '当前账号已存在，不允许添加重复账号',
  '更新 OpenList 账号失败：切换为 Token 认证需要提供新的 Token': '切换为令牌认证时请填写新的令牌',
  '更新 OpenList 账号失败：切换为用户名密码认证需要提供用户名':
    '切换为用户名密码认证时请填写用户名',
  '更新 OpenList 账号失败：切换为用户名密码认证需要提供密码': '切换为用户名密码认证时请填写密码',
  '更新 OpenList 账号失败：OpenList Token 不能为空': '请填写 OpenList 令牌',
  '更新 OpenList 账号失败：OpenList 用户名和密码不能为空': '请填写 OpenList 用户名和密码',
  '更新 OpenList 账号失败：OpenList 账号配置已更新或账号已删除，请重新加载后重试':
    'OpenList 账号配置已更新或账号已删除，请重新加载后重试',
}

export const authorizationPublicMessages: Readonly<Record<string, string>> = {
  ...accountPublicMessages,
  参数错误: '授权请求参数错误',
  '115 授权来源无效': '115 授权来源无效，请重新选择授权来源',
  'OAuth 中转未配置 OAUTH_RELAY_ENCRYPTION_KEY': 'OAuth 中转未配置共享密钥，请检查服务配置',
  '保存 115 授权失败：当前账号已存在，不允许添加重复账号': '当前账号已存在，不允许添加重复账号',
  '保存 115 授权失败：授权会话不存在、已过期或已取消': '授权会话不存在、已过期或已取消',
  '保存 115 授权失败：该账号已有授权会话进行中': '该账号已有授权流程进行中，请先取消后再试',
  '更新用户信息失败：当前账号已存在，不允许添加重复账号': '当前账号已存在，不允许添加重复账号',
}

function unwrapRequiredData<T>(response: AxiosResponse<APIResponse<T | null>>): T {
  const data = unwrapResponse(response)
  if (data == null) throw new HttpResponseError(response)
  return data
}

export async function listAccounts(
  http: AxiosInstance,
  sourceType?: string,
): Promise<CloudAccount[]> {
  const response = await http.get<APIResponse<CloudAccount[] | null>>(
    `${SERVER_URL}/account/list`,
    sourceType === undefined ? undefined : { params: { source_type: sourceType } },
  )
  return unwrapResponse(response) ?? []
}

export async function createAccount(
  http: AxiosInstance,
  payload: CreateAccountPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<unknown>>(`${SERVER_URL}/account/add`, payload))
}

export async function saveOpenListAccount(
  http: AxiosInstance,
  payload: OpenListAccountPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/account/openlist`, payload))
}

export async function updateAccount(
  http: AxiosInstance,
  payload: { id: number; name: string; app_id_name?: string },
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/account/update`, payload))
}

export async function deleteAccount(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/account/delete`, { id }))
}

export async function fetchAccountStatus(
  http: AxiosInstance,
  source: '115' | 'baidupan',
  accountId: number,
): Promise<CloudDiskStatus> {
  return unwrapRequiredData(
    await http.get<APIResponse<CloudDiskStatus | null>>(`${SERVER_URL}/${source}/status`, {
      params: { account_id: accountId },
    }),
  )
}

export async function prepareAccountAuthorization(
  http: AxiosInstance,
  payload: PrepareAccountAuthorizationPayload,
): Promise<AccountAuthorizationSession> {
  const response = await http.post<APIResponse<AccountAuthorizationSession | null>>(
    `${SERVER_URL}/account/authorization/prepare`,
    payload,
  )
  const data = unwrapRequiredData(response)
  if (typeof data.authorization_id !== 'string' || !data.authorization_id.trim())
    throw new HttpResponseError(response)
  return data
}

export async function cancelAccountAuthorization(
  http: AxiosInstance,
  accountId: number,
  authorizationId: string,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/account/authorization/cancel`, {
      account_id: accountId,
      authorization_id: authorizationId,
    }),
  )
}

export async function fetchV115OAuthURL(
  http: AxiosInstance,
  params: OAuthURLParams,
): Promise<V115OAuthURLData | string> {
  return unwrapRequiredData(
    await http.get<APIResponse<V115OAuthURLData | string | null>>(`${SERVER_URL}/115/oauth-url`, {
      params,
    }),
  )
}

export async function fetchV115OAuthStatus(
  http: AxiosInstance,
  params: { account_id: number; state: string; authorization_id?: string },
): Promise<{ done: boolean }> {
  return unwrapRequiredData(
    await http.get<APIResponse<{ done: boolean } | null>>(`${SERVER_URL}/115/oauth-status`, {
      params,
    }),
  )
}

export async function confirmV115OAuth(
  http: AxiosInstance,
  payload: OAuthConfirmPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/115/oauth-confirm`, payload))
}

export async function fetchBaiduOAuthURL(
  http: AxiosInstance,
  params: OAuthURLParams,
): Promise<string> {
  return unwrapRequiredData(
    await http.get<APIResponse<string | null>>(`${SERVER_URL}/baidupan/oauth-url`, { params }),
  )
}

export async function confirmBaiduOAuth(
  http: AxiosInstance,
  payload: OAuthConfirmPayload,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/baidupan/oauth-confirm`, payload),
  )
}

export async function openV115QRCode(
  http: AxiosInstance,
  accountId: number,
  authorizationId?: string,
): Promise<V115QrCodePayload> {
  return unwrapRequiredData(
    await http.post<APIResponse<V115QrCodePayload | null>>(`${SERVER_URL}/auth/115-qrcode-open`, {
      account_id: accountId,
      ...(authorizationId ? { authorization_id: authorizationId } : {}),
    }),
  )
}

export async function fetchV115QRCodeStatus(
  http: AxiosInstance,
  payload: { account_id: number; uid: string; authorization_id?: string },
): Promise<V115QrCodeStatusPayload> {
  return unwrapRequiredData(
    await http.post<APIResponse<V115QrCodeStatusPayload | null>>(
      `${SERVER_URL}/auth/115-qrcode-status`,
      payload,
      { timeout: V115_QR_STATUS_TIMEOUT_MS },
    ),
  )
}

export async function fetchV115AppIds(
  http: AxiosInstance,
  params: { keyword?: string; offset?: number; limit?: number } = {},
): Promise<V115AppIDSearchResult> {
  return unwrapRequiredData(
    await http.get<APIResponse<V115AppIDSearchResult | null>>(`${SERVER_URL}/115/appids`, {
      params,
    }),
  )
}
