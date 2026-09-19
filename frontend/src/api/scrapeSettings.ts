import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface AiSettings {
  ai_base_url: string
  ai_api_key: string
  ai_model_name: string
  ai_timeout: number
}

export type AiConnectionSettings = Omit<AiSettings, 'ai_timeout'>

export interface TmdbSettings {
  tmdb_url: string
  tmdb_image_url: string
  tmdb_enable_proxy: boolean
  tmdb_api_key: string
  tmdb_access_token: string
  fanart_api_key: string
  tmdb_language: string
  tmdb_image_language: string
  local_max_threads?: number
}

export type TmdbConnectionSettings = Omit<TmdbSettings, 'fanart_api_key' | 'local_max_threads'>

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const scrapeSettingsPublicMessages: Readonly<Record<string, string>> = {
  'ai_base_url：必须是有效的 HTTP URL': 'AI 接口地址必须是有效的 HTTP URL',
  'ai_base_url：只支持 http 或 https': 'AI 接口地址只支持 HTTP 或 HTTPS',
  'ai_base_url：端口必须在 1-65535 之间': 'AI 接口地址的端口必须在 1-65535 之间',
  'ai_model_name：长度超出允许范围': 'AI 模型名称长度必须在 1 到 128 个字符之间',
  'ai_model_name：不能包含控制字符': 'AI 模型名称不能包含控制字符',
  'ai_timeout：取值超出允许范围': 'AI 请求超时时间必须在 5 到 600 秒之间',
  '测试 AI 识别失败，识别出的电影名称不是名侦探柯南': 'AI 识别结果不符合预期，请检查模型设置',
  'tmdb_url：必须是有效的 HTTP URL': 'TMDB 接口地址必须是有效的 HTTP URL',
  'tmdb_url：只支持 http 或 https': 'TMDB 接口地址只支持 HTTP 或 HTTPS',
  'tmdb_url：端口必须在 1-65535 之间': 'TMDB 接口地址的端口必须在 1-65535 之间',
  'tmdb_image_url：必须是有效的 HTTP URL': 'TMDB 图片地址必须是有效的 HTTP URL',
  'tmdb_image_url：只支持 http 或 https': 'TMDB 图片地址只支持 HTTP 或 HTTPS',
  'tmdb_image_url：端口必须在 1-65535 之间': 'TMDB 图片地址的端口必须在 1-65535 之间',
  'tmdb_language：语言代码格式不正确': 'TMDB 首选元数据语言的语言代码格式不正确',
  'tmdb_image_language：语言代码格式不正确': 'TMDB 首选图片语言的语言代码格式不正确',
}

export async function fetchAiSettings(http: AxiosInstance): Promise<AiSettings> {
  return unwrapResponse(await http.get<APIResponse<AiSettings>>(`${SERVER_URL}/scrape/ai-settings`))
}

export async function saveAiSettings(http: AxiosInstance, payload: AiSettings): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/ai-settings`, payload))
}

export async function testAiConnection(
  http: AxiosInstance,
  payload: AiConnectionSettings,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/ai-test`, payload, {
      timeout: 120000,
    }),
  )
}

export async function fetchTmdbSettings(http: AxiosInstance): Promise<TmdbSettings> {
  return unwrapResponse(await http.get<APIResponse<TmdbSettings>>(`${SERVER_URL}/scrape/tmdb`))
}

export async function saveTmdbSettings(http: AxiosInstance, payload: TmdbSettings): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/tmdb`, payload))
}

// false 表示 TMDB 连接测试未通过；来源、安全校验等请求失败仍由 unwrapResponse 抛出。
export async function testTmdbConnection(
  http: AxiosInstance,
  payload: TmdbConnectionSettings,
): Promise<boolean> {
  return unwrapResponse(
    await http.post<APIResponse<boolean>>(`${SERVER_URL}/scrape/tmdb-test`, payload, {
      timeout: 20000,
    }),
  )
}
