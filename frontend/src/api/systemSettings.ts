import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { ParsedHttpError } from '@/http/errors'
import type { LogLevel } from '@/types/log'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface StrmSettings {
  video_ext_arr: string[]
  min_video_size: number
  meta_ext_arr: string[]
  cron: string
  strm_base_url: string
  upload_meta: 0 | 1 | 2
  download_meta: 0 | 1
  delete_dir: 0 | 1
  multi_playback_enabled: 0 | 1
  local_proxy: 0 | 1
  exclude_name_arr: string[]
  exclude_name_regex_arr: string[]
  add_path: 1 | 2 | 3
  check_meta_mtime: 0 | 1
}

export interface ThreadSettings {
  download_threads: number
  upload_threads?: number
  file_detail_threads: number
  openlist_qps: number
  openlist_retry: number
  openlist_retry_delay: number
  file_list_page_size: number
  url_validity_check_enabled?: 0 | 1
  url_validity_check_timeout_seconds?: number
  upload_rapid_wait_enabled?: 0 | 1
  upload_rapid_wait_timeout_seconds?: number
  upload_rapid_wait_interval_seconds?: number
  upload_rapid_wait_min_size?: number
  upload_rapid_wait_force_size?: number
  upload_rapid_wait_skip_upload?: 0 | 1
}

export interface LogSettings {
  level: LogLevel
  maxSizeMB: number
  maxBackups: number
  maxAgeDays: number
}

export interface LogSettingsResponse extends Omit<LogSettings, 'level'> {
  level: string
  levels: string[]
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const systemSettingsPublicMessages: Readonly<Record<string, string>> = {
  'strm_base_url：不能为空': '请输入 STRM 直连地址',
  'strm_base_url：必须是有效的 HTTP URL': 'STRM 直连地址必须是有效的 HTTP URL',
  'strm_base_url：只支持 http 或 https': 'STRM 直连地址只支持 HTTP 或 HTTPS',
  'strm_base_url：端口必须在 1-65535 之间': 'STRM 直连地址的端口必须在 1-65535 之间',
  'cron：不能为空': '请输入 Cron 表达式',
  'cron：仅支持 5 位 cron 表达式或 robfig 描述符': '仅支持 5 位 Cron 表达式或 robfig 描述符',
  '仅支持 5 位 cron 表达式或 robfig 描述符': '仅支持 5 位 Cron 表达式或 robfig 描述符',
  'min_video_size：取值超出允许范围': '最小文件大小不能小于 0',
  'video_ext_arr：不能包含空值': '视频文件扩展名不能包含空值',
  'video_ext_arr：不能包含空白字符': '视频文件扩展名不能包含空白字符',
  'video_ext_arr：扩展名必须以 . 开头': '视频文件扩展名必须以 . 开头',
  'meta_ext_arr：不能包含空值': '元数据扩展名不能包含空值',
  'meta_ext_arr：不能包含空白字符': '元数据扩展名不能包含空白字符',
  'meta_ext_arr：扩展名必须以 . 开头': '元数据扩展名必须以 . 开头',
  'local_proxy：不是允许的取值': '本地代理播放设置无效',
  'multi_playback_enabled：不是允许的取值': '115 多端播放设置无效',
  'upload_meta：不是允许的取值': '网盘不存在的元数据处理设置无效',
  'download_meta：不是允许的取值': '下载元数据设置无效',
  'delete_dir：不是允许的取值': '空目录处理设置无效',
  'add_path：不是允许的取值': 'STRM 链接路径设置无效',
  'check_meta_mtime：不是允许的取值': '元数据修改时间检查设置无效',
  'download_threads：取值超出允许范围': '下载 QPS 必须在 1 到 10 之间',
  'upload_threads：取值超出允许范围': '同时上传任务数必须在 1 到 10 之间',
  'file_detail_threads：取值超出允许范围': '115 接口 QPS 必须在 2 到 10 之间',
  'openlist_qps：取值超出允许范围': 'OpenList QPS 必须在 2 到 10 之间',
  'openlist_retry：取值超出允许范围': 'OpenList 重试次数必须在 1 到 10 之间',
  'openlist_retry_delay：取值超出允许范围': 'OpenList 重试延迟必须在 30 到 3600 秒之间',
  'file_list_page_size：取值超出允许范围': '文件列表分页大小必须在 100 到 1150 之间',
  'url_validity_check_enabled：不是允许的取值': 'URL 有效性检查设置无效',
  'url_validity_check_timeout_seconds：取值超出允许范围': 'URL 有效性检查超时必须在 1 到 9 秒之间',
  'upload_rapid_wait_enabled：不是允许的取值': '秒传等待设置无效',
  'upload_rapid_wait_timeout_seconds：取值超出允许范围': '秒传等待超时必须在 0 到 86400 秒之间',
  'upload_rapid_wait_interval_seconds：取值超出允许范围': '秒传等待间隔必须在 1 到 3600 秒之间',
  'upload_rapid_wait_min_size：取值超出允许范围': '秒传等待最小文件大小不能小于 0',
  'upload_rapid_wait_force_size：取值超出允许范围': '强制等待文件大小不能小于 0',
  'upload_rapid_wait_skip_upload：不是允许的取值': '秒传等待跳过上传设置无效',
  'level：必须是 debug、info、warn 或 error': '日志等级必须是 debug、info、warn 或 error',
  'maxSizeMB：取值超出允许范围': '单文件最大大小必须在 1 到 1024 MB 之间',
  'maxBackups：取值超出允许范围': '保留备份数必须在 1 到 100 之间',
  'maxAgeDays：取值超出允许范围': '保留天数必须在 1 到 365 之间',
}

// 全局 STRM 的旧校验响应仅有 message；只提取确定的字段位置与固定原因。
// Go regexp 的动态错误尾部可能包含输入原文，不能作为公开消息或诊断输出。
export function strmRegexValidationIssue(
  error: ParsedHttpError,
): { position: number; reason: 'empty' | 'invalid' } | undefined {
  if (error.kind !== 'application' || error.response?.status !== 400) return undefined
  const body = error.response.data
  if (body === null || typeof body !== 'object' || !('message' in body)) return undefined
  if (typeof body.message !== 'string') return undefined
  const match =
    /^exclude_name_regex_arr\[(0|[1-9]\d{0,8})\]：(正则表达式不能为空|正则表达式无效(?:：[\s\S]+)?)$/.exec(
      body.message,
    )
  if (!match) return undefined
  return {
    position: Number(match[1]) + 1,
    reason: match[2] === '正则表达式不能为空' ? 'empty' : 'invalid',
  }
}

export async function fetchStrmSettings(http: AxiosInstance): Promise<StrmSettings> {
  return unwrapResponse(
    await http.get<APIResponse<StrmSettings>>(`${SERVER_URL}/setting/strm-config`),
  )
}

export async function saveStrmSettings(http: AxiosInstance, payload: StrmSettings): Promise<void> {
  unwrapResponse(
    await http.post<APIResponse<null>>(`${SERVER_URL}/setting/strm-config`, payload, {
      headers: { 'Content-Type': 'application/json' },
    }),
  )
}

export async function fetchCronTimes(
  http: AxiosInstance,
  cron: string,
): Promise<(string | number)[]> {
  return (
    unwrapResponse(
      await http.get<APIResponse<(string | number)[] | null>>(`${SERVER_URL}/setting/cron`, {
        params: { cron },
      }),
    ) ?? []
  )
}

export async function fetchThreadSettings(http: AxiosInstance): Promise<ThreadSettings> {
  return unwrapResponse(
    await http.get<APIResponse<ThreadSettings>>(`${SERVER_URL}/setting/threads`),
  )
}

export async function saveThreadSettings(
  http: AxiosInstance,
  payload: ThreadSettings,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/setting/threads`, payload))
}

export async function fetchLogSettings(http: AxiosInstance): Promise<LogSettingsResponse> {
  return unwrapResponse(
    await http.get<APIResponse<LogSettingsResponse>>(`${SERVER_URL}/setting/log`),
  )
}

export async function saveLogSettings(
  http: AxiosInstance,
  payload: LogSettings,
): Promise<LogSettingsResponse> {
  return unwrapResponse(
    await http.post<APIResponse<LogSettingsResponse>>(`${SERVER_URL}/setting/log`, payload),
  )
}
