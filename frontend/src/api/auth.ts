import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParseHttpErrorOptions } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface User {
  id: string
  username: string
  email?: string
  role?: string
}

export interface UserSession {
  session_id: string
  current?: boolean
  ip_address?: string
  user_agent?: string
  created_at?: number
  last_seen_at?: number
  expires_at: number
}

export interface SessionResponseData {
  authenticated: boolean
  user?: User
  csrf_token?: string
  session?: UserSession
}

export interface SetupStatus {
  required: boolean
}

export interface CreateInitialAdminPayload {
  setup_token: string
  username: string
  password: string
}

export interface LoginRequest {
  username: string
  password: string
  totp_code: string
  rememberMe: boolean
}

export const initialAdminErrorOptions: ParseHttpErrorOptions = {
  fallbackMessage: '创建管理员失败',
  publicMessages: {
    '创建管理员失败：username：不能为空': '创建管理员失败：用户名：不能为空',
    '创建管理员失败：username：长度必须在 3 到 20 个字符之间':
      '创建管理员失败：用户名：长度必须在 3 到 20 个字符之间',
    '创建管理员失败：username：只能包含英文和数字': '创建管理员失败：用户名：只能包含英文和数字',
    '创建管理员失败：password：长度不能小于 6': '创建管理员失败：密码：长度不能小于 6',
    '创建管理员失败：password：不能是纯数字或纯字母': '创建管理员失败：密码：不能是纯数字或纯字母',
  },
}

export async function fetchSetupStatus(http: AxiosInstance): Promise<SetupStatus> {
  return unwrapResponse(await http.get<APIResponse<SetupStatus>>(`${SERVER_URL}/setup/status`))
}

export async function createInitialAdmin(
  http: AxiosInstance,
  payload: CreateInitialAdminPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<unknown>>(`${SERVER_URL}/setup/admin`, payload))
}

export async function login(http: AxiosInstance, payload: LoginRequest): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<unknown>>(`${SERVER_URL}/login`, payload, {
      headers: { 'Content-Type': 'application/json' },
      skipAuthInvalidation: true,
    }),
  )
}

export async function fetchSession(http: AxiosInstance): Promise<SessionResponseData> {
  return unwrapResponse(
    await http.get<APIResponse<SessionResponseData>>(`${SERVER_URL}/session`, {
      withCredentials: true,
      skipAuthInvalidation: true,
    }),
  )
}

export async function logout(http: AxiosInstance): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<unknown>>(`${SERVER_URL}/logout`, undefined, {
      withCredentials: true,
      skipAuthInvalidation: true,
    }),
  )
}
