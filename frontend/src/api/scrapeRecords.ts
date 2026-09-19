import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParseHttpErrorOptions } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface ScrapeRecord {
  id: number
  type: 'movie' | 'tvshow' | 'other'
  path: string
  file_name: string
  media_name: string
  original_name: string
  year: number
  tmdb_id: number
  season_number: number
  episode_number: number
  episode_name?: string
  status:
    'scanned' | 'scraping' | 'scraped' | 'scrape_failed' | 'renaming' | 'renamed' | 'rename_failed'
  failed_reason: string
  created_at: number
  updated_at: number
  scanned_at: number
  scraped_at: number
  renamed_at: number
  audio_count: number
  subtitle_count: number
  resolution: string
  resolution_level: string
  is_hdr: boolean
  category_name: string
  new_dest_path: string
  new_dest_name: string
  path_is_scraping: boolean
  source_full_path: string
  dest_full_path: string
  source_type: string
  rename_type: string
  scrape_type: string
}

export interface ScrapeRecordsQuery {
  page: number
  pageSize: number
  status?: string
  type?: string
  name?: string
}

export interface ScrapeRecordsPage {
  list: ScrapeRecord[] | null
  total: number
}

export interface TmdbSearchQuery {
  type: ScrapeRecord['type'] | ''
  name?: string
  year?: string | number
  tmdb_id?: string | number
}

export interface TmdbSearchResult {
  tmdb_id: number
  title: string
  original_title: string
  year: number
  poster_url: string
  overview: string
}

export interface ReidentifyScrapeRecordPayload {
  id: number
  tmdb_id: number
  season: number
  episode: number
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const scrapeRecordErrorOptions: ParseHttpErrorOptions = {
  publicMessages: {
    'name：请输入名称或 TMDB ID': '请输入名称或 TMDB ID',
    'tmdb_id：必须大于 0': 'TMDB ID 必须大于 0',
    'type：不是允许的取值': '请选择电影或电视剧',
    'year：取值超出允许范围': '年份必须在 1900 到 2100 之间',
    'id：必须大于 0': '记录 ID 必须大于 0',
    'ids：不能为空': '请选择记录',
    'ids：格式不正确': '记录 ID 格式不正确',
    'ids：必须大于 0': '记录 ID 必须大于 0',
  },
}

export async function fetchScrapeRecords(
  http: AxiosInstance,
  params: ScrapeRecordsQuery,
): Promise<ScrapeRecordsPage> {
  return unwrapResponse(
    await http.get<APIResponse<ScrapeRecordsPage>>(`${SERVER_URL}/scrape/records`, { params }),
  )
}

export async function searchScrapeTmdb(
  http: AxiosInstance,
  params: TmdbSearchQuery,
): Promise<TmdbSearchResult[] | null> {
  return unwrapResponse(
    await http.get<APIResponse<TmdbSearchResult[] | null>>(`${SERVER_URL}/scrape/tmdb-search`, {
      params,
      timeout: 30000,
    }),
  )
}

export async function reidentifyScrapeRecord(
  http: AxiosInstance,
  payload: ReidentifyScrapeRecordPayload,
): Promise<string> {
  const response = await http.post<APIResponse<unknown>>(
    `${SERVER_URL}/scrape/re-scrape`,
    payload,
    { timeout: 60000 },
  )
  unwrapResponse(response)
  return response.data.message
}

export async function deleteScrapeRecords(http: AxiosInstance, ids: number[]): Promise<void> {
  unwrapResponse(
    await http.delete<APIResponse<null>>(`${SERVER_URL}/scrape/records?ids=${ids.join(',')}`),
  )
}

export async function renameFailedScrapeRecords(http: AxiosInstance, ids: number[]): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/rename-failed?ids=${ids.join(',')}`),
  )
}

export async function clearFailedScrapeRecords(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/clear-failed`))
}

export async function truncateScrapeRecords(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/truncate-all`))
}

export async function finishScrapeRecord(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/finish`, { id }))
}

export function getScrapeRecordsExportUrl(ids: number[]): string {
  return `${SERVER_URL}/scrape/records/export?ids=${ids.join(',')}`
}
