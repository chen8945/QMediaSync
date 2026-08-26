import { SERVER_URL } from '@/const'
import { closeAllRealtimeSources } from '@/composables/realtimeSources'
import { isAxiosError, type AxiosInstance } from 'axios'
import { defineStore } from 'pinia'
import { computed, shallowRef } from 'vue'

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

export interface LoginPayload {
  user: User
  csrfToken: string
  session?: UserSession
}

type AuthStatus = 'checking' | 'authenticated' | 'anonymous'

export type SessionRefreshState = 'authenticated' | 'anonymous' | 'unavailable'

export type SessionRefreshResult = {
  state: SessionRefreshState
}

type SessionResponseData = {
  authenticated: boolean
  user?: User
  csrf_token?: string
  session?: UserSession
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
  let bootstrapPromise: Promise<boolean> | null = null

  const isAuthenticated = computed(() => authStatus.value === 'authenticated' && !!user.value)

  const applySession = (payload: LoginPayload) => {
    user.value = payload.user
    session.value = payload.session || null
    csrfToken.value = payload.csrfToken
    authStatus.value = 'authenticated'
    hasInitialized.value = true
    clearLegacyWebStorage()
  }

  const clearAuth = () => {
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
      const response = await http.get(`${SERVER_URL}/session`, {
        withCredentials: true,
        skipAuthInvalidation: true,
      })
      if (response.data?.code === 200 && applySessionResponse(response.data.data)) {
        return { state: 'authenticated' }
      }
      if (response.data?.code === 200 && response.data?.data?.authenticated === false) {
        clearAuth()
        return { state: 'anonymous' }
      }
      console.error('恢复登录会话失败：', response.data)
    } catch (error) {
      console.error('恢复登录会话失败：', error)
    }
    clearAuth()
    return { state: 'unavailable' }
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
      await http.post(`${SERVER_URL}/logout`, undefined, {
        withCredentials: true,
        skipAuthInvalidation: true,
      })
    } catch (error) {
      if (!isAxiosError(error) || error.response?.status !== 401) {
        console.error('服务端退出登录失败：', error)
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
