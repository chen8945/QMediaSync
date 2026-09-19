import { closeAllRealtimeSources } from '@/composables/realtimeSources'
import type { AxiosInstance } from 'axios'
import {
  fetchSession,
  logout as requestLogout,
  type SessionResponseData,
  type User,
  type UserSession,
} from '@/api/auth'
import { parseHttpError, type ParsedHttpError } from '@/http/errors'
import { defineStore } from 'pinia'
import { computed, shallowRef } from 'vue'

export type { User, UserSession } from '@/api/auth'

export interface LoginPayload {
  user: User
  csrfToken: string
  session?: UserSession
}

type AuthStatus = 'checking' | 'authenticated' | 'anonymous'

export type SessionRefreshState = 'authenticated' | 'anonymous' | 'unavailable'

export type SessionRefreshResult = {
  state: SessionRefreshState
  error?: ParsedHttpError
}

const clearLegacyWebStorage = () => {
  localStorage.removeItem('auth_token')
  localStorage.removeItem('auth_user')
  sessionStorage.removeItem('auth_token')
  sessionStorage.removeItem('auth_user')
}

export const useAuthStore = defineStore('auth', () => {
  const user = shallowRef<User | null>(null)
  const session = shallowRef<UserSession | null>(null)
  const csrfToken = shallowRef<string | null>(null)
  const authStatus = shallowRef<AuthStatus>('checking')
  const isLoggingOut = shallowRef(false)
  const hasInitialized = shallowRef(false)
  const sessionVersion = shallowRef(0)
  let bootstrapPromise: Promise<boolean> | null = null

  const isAuthenticated = computed(() => authStatus.value === 'authenticated' && !!user.value)

  const applySession = (payload: LoginPayload) => {
    sessionVersion.value += 1
    user.value = payload.user
    session.value = payload.session || null
    csrfToken.value = payload.csrfToken
    authStatus.value = 'authenticated'
    hasInitialized.value = true
    clearLegacyWebStorage()
  }

  const clearAuth = () => {
    sessionVersion.value += 1
    closeAllRealtimeSources()
    user.value = null
    session.value = null
    csrfToken.value = null
    authStatus.value = 'anonymous'
    hasInitialized.value = true
    bootstrapPromise = null
    clearLegacyWebStorage()
  }

  const applySessionResponse = (data?: SessionResponseData) => {
    if (!data?.authenticated || !data.user || !data.csrf_token) return false
    applySession({
      user: data.user,
      csrfToken: data.csrf_token,
      session: data.session,
    })
    return true
  }

  const refreshSession = async (http: AxiosInstance): Promise<SessionRefreshResult> => {
    authStatus.value = 'checking'
    try {
      const data = await fetchSession(http)
      if (applySessionResponse(data)) {
        return { state: 'authenticated' }
      }
      if (data?.authenticated === false) {
        clearAuth()
        return { state: 'anonymous' }
      }
      throw new Error('会话响应不完整')
    } catch (error) {
      const failure = parseHttpError(error, { fallbackMessage: '登录会话验证失败，请稍后重试' })
      if (failure.shouldNotify) console.error('恢复登录会话失败：', failure.diagnostics)
      clearAuth()
      return { state: 'unavailable', error: failure }
    }
  }

  const bootstrapAuth = async (http: AxiosInstance) => {
    if (bootstrapPromise) return bootstrapPromise

    bootstrapPromise = (async () => {
      const result = await refreshSession(http)
      bootstrapPromise = null
      return result.state === 'authenticated'
    })()

    return bootstrapPromise
  }

  const login = (payload: LoginPayload) => {
    applySession(payload)
  }

  const logout = () => {
    if (isLoggingOut.value) return
    isLoggingOut.value = true
    clearAuth()
    setTimeout(() => {
      isLoggingOut.value = false
    }, 1000)
  }

  const logoutWithServer = async (http: AxiosInstance) => {
    if (isLoggingOut.value) return
    isLoggingOut.value = true
    try {
      await requestLogout(http)
    } catch (error) {
      const failure = parseHttpError(error)
      if (failure.shouldNotify && failure.kind !== 'unauthorized') {
        console.error('服务端退出登录失败：', failure.diagnostics)
      }
    } finally {
      clearAuth()
      setTimeout(() => {
        isLoggingOut.value = false
      }, 1000)
    }
  }

  const updateUser = (userData: Partial<User>) => {
    if (user.value) {
      user.value = { ...user.value, ...userData }
    }
  }

  return {
    user,
    session,
    csrfToken,
    authStatus,
    isLoggingOut,
    hasInitialized,
    sessionVersion,
    isAuthenticated,
    bootstrapAuth,
    refreshSession,
    login,
    logout,
    logoutWithServer,
    clearAuth,
    updateUser,
  }
})
