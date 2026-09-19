import { SERVER_URL } from '@/const'
import type { AxiosInstance } from 'axios'
import { HttpResponseError, type ParsedHttpError } from '@/http/errors'
import type { DirectoryUploadRule } from '@/typing'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface SyncPathSettingPayload {
  local_proxy: number
  strm_base_url: string
  cron: string
  min_video_size: number
  video_ext_arr: string[]
  meta_ext_arr: string[]
  exclude_name_arr: string[]
  exclude_name_regex_arr: string[]
  upload_meta: number
  download_meta: number
  delete_dir: number
  add_path: number
  check_meta_mtime: number
}

export interface SyncPathPayload {
  source_type: string
  account_id: number
  base_cid: string
  local_path: string
  remote_path: string
  enable_cron: boolean
  custom_config: boolean
  setting: SyncPathSettingPayload
}

export interface DirectoryUploadRulePayload {
  client_id: string
  id: number
  enabled: boolean
  monitor_path: string
  remote_root_path: string
  remote_root_id: string
  recursive: boolean
  watch_mode: string
  upload_metadata: boolean
  startup_scan_enabled: boolean
  processed_cache_ttl_seconds: number
  delete_source_after_success: boolean
  ignore_patterns: string[]
  overwrite_mode: string
}

export interface SaveSyncPathPayload {
  sync_path: SyncPathPayload
  directory_upload: null | {
    enabled: boolean
    rules: DirectoryUploadRulePayload[]
  }
}

export interface SyncPathFieldError {
  client_id?: string
  field: string
  message: string
}

export interface SaveSyncPathResponseData {
  warnings: string[]
}

export interface SyncPath extends Omit<SyncPathPayload, 'setting'>, SyncPathSettingPayload {
  id: number
  created_at: number
  updated_at: number
  last_sync_at: number
  account_name: string
  directory_upload_enabled: boolean
  is_running: number
  baidu_sync_method: 1 | 2
  upload_meta: -1 | 0 | 1 | 2
  download_meta: -1 | 0 | 1
  delete_dir: -1 | 0 | 1
  add_path: -1 | 1 | 2 | 3
  check_meta_mtime: -1 | 0 | 1
}

export interface SyncPathListParams {
  page?: number
  page_size?: number
  source_type?: string
}

export interface SyncPathList {
  list: SyncPath[]
  total: number
  page?: number
  page_size?: number
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。

export const syncPathPublicMessages: Readonly<Record<string, string>> = {
  'ID 参数格式错误': '同步目录 ID 格式错误',
  'ID 参数不能为空': '请选择同步目录',
  'id：必须大于 0': '请选择有效的同步目录',
  删除同步路径失败: '删除同步目录失败',
  获取目录监控上传规则失败: '加载目录监控上传规则失败',
  相同幂等键的创建请求正在处理: '创建请求仍在处理，请稍后重试',
  幂等键冲突: '创建请求冲突，请稍后重试',
  'account_id：非本地来源必须选择账号': '非本地来源必须选择账号',
  'base_cid：不能为空': '请选择来源目录',
  'local_path：不能为空': '请选择目标目录',
  'remote_path：不能为空': '请选择来源目录',
}

const fieldPublicMessages: Readonly<Record<string, string>> = {
  同步来源不能修改: '同步来源不能修改',
  同步账号不能修改: '同步账号不能修改',
  账号不存在: '账号不存在',
  账号类型与同步源类型不一致: '账号类型与同步源类型不一致',
  规则不属于当前同步目录: '规则不属于当前同步目录',
  不能为空: '不能为空',
  格式错误: '格式错误',
  非本地来源必须选择账号: '非本地来源必须选择账号',
  '不能小于 -1': '不能小于 -1',
  不是允许的取值: '不是允许的取值',
  '必须是有效的 HTTP URL': '必须是有效的 HTTP URL',
  '只支持 http 或 https': '只支持 HTTP 或 HTTPS',
  '端口必须在 1-65535 之间': '端口必须在 1-65535 之间',
  '仅支持 5 位 cron 表达式或 robfig 描述符': '仅支持 5 位 Cron 表达式或 robfig 描述符',
  不能包含空值: '不能包含空值',
  不能包含空白字符: '不能包含空白字符',
  '扩展名必须以 . 开头': '扩展名必须以 . 开头',
  正则表达式不能为空: '正则表达式不能为空',
  监控目录不能为空: '监控目录不能为空',
  '监控目录不能等于 STRM 本地目录': '监控目录不能等于 STRM 本地目录',
  远端上传根目录不能为空: '远端上传根目录不能为空',
  '远端上传根目录 ID 不能为空': '远端上传根目录 ID 不能为空',
  同步远端目录不能为空: '同步远端目录不能为空',
  '目录监控上传已启用，请至少启用一条规则': '请至少启用一条目录监控上传规则',
}

const asRecord = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' ? (value as Record<string, unknown>) : undefined

export function syncPathFieldErrors(error: ParsedHttpError): SyncPathFieldError[] {
  if (error.kind !== 'application') return []
  const body = asRecord(error.response?.data)
  const fields = body?.field_errors ?? asRecord(error.details)?.field_errors
  if (!Array.isArray(fields)) return []
  return fields.flatMap((value) => {
    const item = asRecord(value)
    if (!item || typeof item.field !== 'string' || typeof item.message !== 'string') return []
    let message = Object.hasOwn(fieldPublicMessages, item.message)
      ? fieldPublicMessages[item.message]!
      : '该字段无效，请检查后重试'
    if (/^exclude_name_regex_arr(?:\[\d+\])?$/.test(item.field)) {
      message = item.message === '正则表达式不能为空' ? '正则表达式不能为空' : '正则表达式无效'
    }
    return [
      {
        field: item.field,
        message,
        ...(typeof item.client_id === 'string' ? { client_id: item.client_id } : {}),
      },
    ]
  })
}

const saveWarnings = new Set([
  '同步目录已保存，但创建本地目录失败',
  '同步目录已保存，但重载定时同步任务失败',
  '同步目录已保存，但重载目录监控上传服务失败',
])

export function syncPathSaveWarning(warning: string): string {
  return saveWarnings.has(warning) ? warning : '同步目录已保存，但后续处理失败，请查看服务日志'
}

export async function saveSyncPathAggregate(
  http: AxiosInstance,
  id: number,
  payload: SaveSyncPathPayload,
  idempotencyKey: string,
): Promise<SaveSyncPathResponseData> {
  const response =
    id > 0
      ? await http.put<APIResponse<SaveSyncPathResponseData>>(
          `${SERVER_URL}/sync/paths/${id}`,
          payload,
        )
      : await http.post<APIResponse<SaveSyncPathResponseData>>(
          `${SERVER_URL}/sync/paths`,
          payload,
          {
            headers: { 'Idempotency-Key': idempotencyKey },
          },
        )
  const data = unwrapResponse(response)
  if (
    !asRecord(data) ||
    (data.warnings != null &&
      (!Array.isArray(data.warnings) || !data.warnings.every((item) => typeof item === 'string')))
  ) {
    throw new HttpResponseError(response)
  }
  return { ...data, warnings: data.warnings ?? [] }
}

export async function fetchSyncPaths(
  http: AxiosInstance,
  params?: SyncPathListParams,
  options?: { timeout?: number },
): Promise<SyncPathList> {
  const response = await http.get<APIResponse<SyncPathList>>(`${SERVER_URL}/sync/path-list`, {
    ...options,
    params,
  })
  const data = unwrapResponse(response)
  if (!asRecord(data) || (data.list !== null && !Array.isArray(data.list)))
    throw new HttpResponseError(response)
  return { ...data, list: data.list ?? [], total: data.total ?? 0 }
}

export async function fetchSyncPath(http: AxiosInstance, id: number): Promise<SyncPath> {
  const response = await http.get<APIResponse<SyncPath>>(`${SERVER_URL}/sync/path/${id}`)
  const data = unwrapResponse(response)
  if (!data?.id) throw new HttpResponseError(response)
  return data
}

export async function fetchDirectoryUploadRules(
  http: AxiosInstance,
  syncPathId?: number,
): Promise<DirectoryUploadRule[]> {
  const response = await http.get<APIResponse<{ list: DirectoryUploadRule[] | null }>>(
    `${SERVER_URL}/directory-upload/rules`,
    syncPathId === undefined ? undefined : { params: { sync_path_id: syncPathId } },
  )
  const data = unwrapResponse(response)
  if (!asRecord(data) || (data.list !== null && !Array.isArray(data.list)))
    throw new HttpResponseError(response)
  return data.list ?? []
}

const jsonHeaders = { 'Content-Type': 'application/json' }

export async function deleteSyncPath(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(
    await http.post(`${SERVER_URL}/sync/path-delete`, { id }, { headers: jsonHeaders }),
  )
}

export async function startSyncPath(
  http: AxiosInstance,
  id: number,
  full = false,
): Promise<{ is_running?: number } | null> {
  return unwrapResponse(
    await http.post(
      `${SERVER_URL}/sync/path/${full ? 'full-start' : 'start'}`,
      { id },
      { headers: jsonHeaders },
    ),
  )
}

export async function stopSyncPath(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(await http.post(`${SERVER_URL}/sync/path/stop`, { id }, { headers: jsonHeaders }))
}

export async function toggleSyncPathCron(http: AxiosInstance, id: number): Promise<void> {
  unwrapResponse(
    await http.post(`${SERVER_URL}/sync/path/toggle-cron`, { id }, { headers: jsonHeaders }),
  )
}

export async function scanSyncPathDirectoryUpload(
  http: AxiosInstance,
  id: number,
): Promise<{ accepted?: number } | null> {
  return unwrapResponse(await http.post(`${SERVER_URL}/directory-upload/sync-paths/${id}/scan`))
}

export async function fetchSyncPathScrapeRelations(
  http: AxiosInstance,
  id: number,
): Promise<number[]> {
  const response = await http.get<APIResponse<number[] | null>>(
    `${SERVER_URL}/sync/path/${id}/scrape-paths`,
  )
  const data = unwrapResponse(response)
  if (data !== null && (!Array.isArray(data) || !data.every((item) => typeof item === 'number')))
    throw new HttpResponseError(response)
  return data ?? []
}

export async function saveSyncPathScrapeRelations(
  http: AxiosInstance,
  id: number,
  scrapePathIds: number[],
): Promise<void> {
  unwrapResponse(
    await http.post(`${SERVER_URL}/sync/path/scrape-paths`, { id, scrape_path_id: scrapePathIds }),
  )
}
