import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { DirInfo, FileSystemItem } from '@/typing'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export type NetFileSortBy = 'default' | 'name' | 'time' | 'size' | 'type'
export type NetFileSortOrder = 'asc' | 'desc'

export interface NetFileListCacheMeta {
  status: 'hit' | 'miss' | 'partial_hit' | 'refresh'
  batch_start: number
  batch_size: number
  cached_at: number
  expires_at: number
}

export interface NetFileListPayload {
  list: FileSystemItem[]
  total: number
  total_exact: boolean
  has_more: boolean
  page: number
  page_size: number
  sort_by: NetFileSortBy
  sort_order: NetFileSortOrder
  cache?: NetFileListCacheMeta
}

export interface NetFileListQuery {
  account_id: number
  path: string
  page: number
  page_size: number
  refresh: number
  sort_by?: NetFileSortBy
  sort_order?: NetFileSortOrder
}

export interface DirectoryQuery {
  parent_id: string
  parent_path: string
  source_type: string
  account_id: number
}

export interface CreateDirectoryPayload extends DirectoryQuery {
  name: string
}

export interface DeleteFileQuery {
  parent_id: string
  file_id: string
  account_id: number
}

export interface ManualStrmPayload {
  path_id: string
  target_path: string
  account_id: number
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。
export const filePublicMessages: Readonly<Record<string, string>> = {
  'account_id：必须大于 0': '请先选择网盘账号',
  'source_type：不是允许的取值': '未知的同步源类型',
  'file_id：不能为空': '请选择要删除的文件或目录',
  'path_id：不能为空': '请选择源文件或目录',
  'target_path：不能为空': '请选择目标目录',
  'name：不能为空': '请输入文件夹名称',
  'name：文件夹名不合法': '文件夹名不合法',
  'name：不能包含路径分隔符': '文件夹名不能包含路径分隔符',
  'name：不能包含控制字符': '文件夹名不能包含控制字符',
  'sort_by：不支持的排序字段': '不支持的排序字段',
  'sort_order：不支持的排序方向': '不支持的排序方向',
}

export async function fetchFiles(http: AxiosInstance, params: NetFileListQuery) {
  return unwrapResponse(
    await http.get<APIResponse<NetFileListPayload | FileSystemItem[] | null>>(
      `${SERVER_URL}/path/files`,
      { params, timeout: 60000 },
    ),
  )
}

export async function fetchDirectories(http: AxiosInstance, params: DirectoryQuery) {
  return unwrapResponse(
    await http.get<APIResponse<DirInfo[] | null>>(`${SERVER_URL}/path/list`, {
      timeout: 60000,
      params,
    }),
  )
}

export async function createDirectory(http: AxiosInstance, payload: CreateDirectoryPayload) {
  return unwrapResponse(await http.post<APIResponse<DirInfo>>(`${SERVER_URL}/path/create`, payload))
}

export async function deleteFile(http: AxiosInstance, params: DeleteFileQuery): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/path`, { params }))
}

export async function generateManualStrm(
  http: AxiosInstance,
  payload: ManualStrmPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/sync/manual`, payload))
}
