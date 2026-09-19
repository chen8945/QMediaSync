import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { User, UserSession } from './auth'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export type LoginSession = Required<UserSession>

export interface TwoFactorSetup {
  secret: string
  otpauth_url: string
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const userSettingsPublicMessages: Readonly<Record<string, string>> = {
  'username：不能为空': '用户名不能为空',
  'username：长度必须在 3 到 20 个字符之间': '用户名长度必须在 3 到 20 个字符之间',
  'username：只能包含英文和数字': '用户名只能包含英文和数字',
  'new_password：长度不能小于 6': '密码长度不能小于 6',
  'new_password：不能是纯数字或纯字母': '密码不能是纯数字或纯字母',
}

export async function fetchCurrentUser(http: AxiosInstance): Promise<User> {
  return unwrapResponse(await http.get<APIResponse<User>>(`${SERVER_URL}/user/info`))
}

export async function changeUserCredentials(
  http: AxiosInstance,
  payload: { username: string; new_password: string },
): Promise<boolean> {
  return unwrapResponse(
    await http.post<APIResponse<boolean>>(`${SERVER_URL}/user/change`, payload, {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

export async function fetchTwoFactorStatus(http: AxiosInstance): Promise<{ enabled: boolean }> {
  return unwrapResponse(
    await http.get<APIResponse<{ enabled: boolean }>>(`${SERVER_URL}/user/two-factor/status`),
  )
}

export async function setupTwoFactor(http: AxiosInstance): Promise<TwoFactorSetup> {
  return unwrapResponse(
    await http.post<APIResponse<TwoFactorSetup>>(`${SERVER_URL}/user/two-factor/setup`),
  )
}

export async function enableTwoFactor(http: AxiosInstance, code: string): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/user/two-factor/enable`, { totp_code: code }),
  )
}

export async function disableTwoFactor(
  http: AxiosInstance,
  payload: { password: string; totp_code: string },
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/user/two-factor/disable`, payload),
  )
}

export async function fetchLoginSessions(http: AxiosInstance): Promise<LoginSession[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<LoginSession[] | null>>(`${SERVER_URL}/user/sessions`),
    ) ?? []
  )
}

export async function revokeLoginSession(http: AxiosInstance, sessionId: string): Promise<void> {
  unwrapResponse(
    await http.delete<APIResponse<null>>(
      `${SERVER_URL}/user/sessions/${encodeURIComponent(sessionId)}`,
    ),
  )
}

export async function revokeOtherLoginSessions(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/user/sessions/revoke-others`))
}
