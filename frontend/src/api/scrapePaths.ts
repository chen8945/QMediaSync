import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParseHttpErrorOptions } from '@/http/errors'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface ScrapePath {
  id: number
  source_type: string
  account_id: number
  media_type: string
  source_path: string
  source_path_id: string
  dest_path: string
  dest_path_id: string
  scrape_type: string
  rename_type: string
  enable_category: boolean
  folder_name_template: string
  file_name_template: string
  delete_keyword: string[]
  min_video_file_size: number
  video_ext_list: string[]
  enable_ai: string
  ai_prompt: string
  exclude_no_image_actor: boolean
  force_delete_source_path: boolean
  enable_cron?: boolean
  cron_expression?: string
  cron_description?: string
  enable_fanart_tv: boolean
  max_threads: number
  is_running: number
  created_at?: number
  updated_at?: number
}

export type SaveScrapePathPayload = Omit<
  ScrapePath,
  'source_type' | 'account_id' | 'media_type' | 'is_running' | 'created_at' | 'updated_at'
> &
  Partial<Pick<ScrapePath, 'source_type' | 'account_id' | 'media_type'>>

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const scrapePathErrorOptions: ParseHttpErrorOptions = {
  publicMessages: {
    'ID 参数格式错误': '目录 ID 格式错误',
    'id：刮削路径不存在': '刮削目录不存在',
    'id：必须大于 0': '请选择有效的刮削目录',
    'scrape_path_id：必须大于 0': '请选择有效的刮削目录',
    'source_type：不能修改来源类型': '不能修改来源类型',
    'source_type：不是允许的取值': '请选择有效的来源类型',
    'account_id：不能修改账号': '不能修改关联账号',
    'account_id：非本地来源必须选择账号': '非本地来源必须选择账号',
    'media_type：不能修改媒体类型': '不能修改媒体类型',
    'media_type：不是允许的取值': '请选择有效的媒体类型',
    'scrape_type：不是允许的取值': '请选择有效的操作方式',
    'rename_type：不是允许的取值': '当前来源不支持所选整理方式',
    'rename_type：仅刮削不需要整理方式': '仅刮削不需要整理方式',
    'source_path：不能为空': '来源目录不能为空',
    'dest_path：不能为空': '目标目录不能为空',
    'folder_name_template：不能包含反斜杠，多级目录请使用 /':
      '文件夹重命名模板不能包含反斜杠，多级目录请使用 /',
    'folder_name_template：不能以 / 开头': '文件夹重命名模板不能以 / 开头',
    'folder_name_template：不能包含 .. 路径片段': '文件夹重命名模板不能包含 .. 路径片段',
    'file_name_template：不能包含反斜杠，多级目录请使用 /':
      '文件重命名模板不能包含反斜杠，多级目录请使用 /',
    'file_name_template：不能以 / 开头': '文件重命名模板不能以 / 开头',
    'file_name_template：不能包含 .. 路径片段': '文件重命名模板不能包含 .. 路径片段',
    'video_ext_list：不能包含空值': '视频文件扩展名不能包含空值',
    'video_ext_list：不能包含空白字符': '视频文件扩展名不能包含空白字符',
    'video_ext_list：扩展名必须以 . 开头': '视频文件扩展名必须以 . 开头',
    'min_video_file_size：不能小于 0': '最小视频文件大小不能小于 0',
    'max_threads：取值超出允许范围': '刮削线程数超出允许范围',
    'cron_expression：不能为空': 'Cron 表达式不能为空',
    'cron_expression：仅支持 5 位 cron 表达式或 robfig 描述符':
      '仅支持 5 位 Cron 表达式或 robfig 描述符',
  },
}

export async function fetchScrapePaths(
  http: AxiosInstance,
  params?: { source_type?: string },
): Promise<ScrapePath[] | null> {
  return unwrapResponse(
    await http.get<APIResponse<ScrapePath[] | null>>(
      `${SERVER_URL}/scrape/pathes`,
      params ? { params } : undefined,
    ),
  )
}

export async function fetchScrapePath(http: AxiosInstance, id: number): Promise<ScrapePath> {
  return unwrapResponse(
    await http.get<APIResponse<ScrapePath>>(`${SERVER_URL}/scrape/pathes/${id}`),
  )
}

export async function saveScrapePath(
  http: AxiosInstance,
  payload: SaveScrapePathPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/pathes`, payload))
}

export async function deleteScrapePath(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/scrape/pathes/${id}`))
}

export async function startScrapePath(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/pathes/start`, { id }))
}

export async function stopScrapePath(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/pathes/stop`, { id }))
}

export async function toggleScrapePathCron(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/pathes/toggle-cron`, { id }),
  )
}

export async function fetchScrapeSyncPathIDs(
  http: AxiosInstance,
  id: number,
): Promise<number[] | null> {
  return unwrapResponse(
    await http.get<APIResponse<number[] | null>>(`${SERVER_URL}/scrape/sync-pathes`, {
      params: { scrape_path_id: id },
    }),
  )
}

export async function saveScrapeSyncPathIDs(
  http: AxiosInstance,
  id: number,
  syncPathIDs: number[],
): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/scrape/sync-pathes`, {
      scrape_path_id: id,
      sync_path_ids: syncPathIDs,
    }),
  )
}

export async function validateScrapePathCron(
  http: AxiosInstance,
  expression: string,
): Promise<{ description: string }> {
  return unwrapResponse(
    await http.post<APIResponse<{ description: string }>>(`${SERVER_URL}/cron/validate`, {
      cron_expression: expression,
    }),
  )
}
