import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export type ScrapeCategoryType = 'movie' | 'tvshow'

export interface ScrapeCategory {
  id: number
  name: string
  language_array?: string[]
  country_array?: string[]
  genre_id_array: number[]
  created_at?: number
}

export interface ScrapeRegion {
  code: string
  name: string
}

export interface ScrapeGenre {
  id: number
  name: string
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const scrapeCategoryPublicMessages: Readonly<Record<string, string>> = {
  'ID 参数格式错误': '分类 ID 格式错误',
  'name：长度超出允许范围': '分类名称长度必须在 1 到 64 个字符之间',
  'name：不能包含控制字符': '分类名称不能包含控制字符',
  'name：不能包含路径分隔符': '分类名称不能包含路径分隔符',
  'name：不能为空白字符': '分类名称不能为空白字符',
  'name：不能是 . 或 ..': '分类名称不能是 . 或 ..',
  'language_array：不能为空': '语言代码不能为空',
  'language_array：语言代码格式不正确': '语言代码格式不正确',
  'country_array：国家代码格式不正确': '国家代码格式不正确',
  'genre_id_array：必须大于 0': '类别 ID 必须大于 0',
}

export async function fetchScrapeLanguages(http: AxiosInstance): Promise<ScrapeRegion[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<ScrapeRegion[] | null>>(`${SERVER_URL}/scrape/language`),
    ) ?? []
  )
}

export async function fetchScrapeCountries(http: AxiosInstance): Promise<ScrapeRegion[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<ScrapeRegion[] | null>>(`${SERVER_URL}/scrape/countries`),
    ) ?? []
  )
}

export async function fetchScrapeGenres(
  http: AxiosInstance,
  type: ScrapeCategoryType,
): Promise<ScrapeGenre[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<ScrapeGenre[] | null>>(`${SERVER_URL}/scrape/${type}-genre`),
    ) ?? []
  )
}

export async function fetchScrapeCategories(
  http: AxiosInstance,
  type: ScrapeCategoryType,
): Promise<ScrapeCategory[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<ScrapeCategory[] | null>>(
        `${SERVER_URL}/scrape/${type}-categories`,
      ),
    ) ?? []
  )
}

export async function saveScrapeCategory(
  http: AxiosInstance,
  type: ScrapeCategoryType,
  payload: ScrapeCategory,
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/${type}-categories`, payload),
  )
}

export async function deleteScrapeCategory(
  http: AxiosInstance,
  type: ScrapeCategoryType,
  id: number,
): Promise<void> {
  unwrapResponse(
    await http.delete<APIResponse<null>>(`${SERVER_URL}/scrape/${type}-categories/${id}`),
  )
}
