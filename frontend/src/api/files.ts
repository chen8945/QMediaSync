import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import type { DirInfo, FileSystemItem } from '@/typing'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export type NetFileSortBy = 'default' | 'name' | 'time' | 'size' | 'type'
export type NetFileSortOrder = 'asc' | 'desc'
export type BrowseScope = 'files' | 'directories'

export interface BrowseSortValue {
  sort_by: NetFileSortBy
  sort_order: NetFileSortOrder
  folders_first?: boolean
}

export interface BrowseSortOptions {
  fields: NetFileSortBy[]
  folders_first: boolean
  default: BrowseSortValue
}

export async function fetchBrowseSortOptions(
  http: AxiosInstance,
  source_type: string,
  scope: BrowseScope,
) {
  const options = unwrapResponse(
    await http.get<APIResponse<BrowseSortOptions>>(`${SERVER_URL}/path/sort-options`, {
      params: { source_type, scope },
    }),
  )
  if (
    !options ||
    !Array.isArray(options.fields) ||
    !options.fields.length ||
    !options.default ||
    !options.fields.includes(options.default.sort_by) ||
    !['asc', 'desc'].includes(options.default.sort_order) ||
    typeof options.folders_first !== 'boolean'
  ) {
    throw new Error('排序能力响应不完整')
  }
  return options
}

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
  folders_first?: boolean
}

export interface DirectoryQuery {
  parent_id: string
  parent_path: string
  source_type: string
  account_id: number
}

export interface DirectoryListQuery extends DirectoryQuery {
  sort_by?: NetFileSortBy
  sort_order?: NetFileSortOrder
  refresh?: 0 | 1
}

export interface CreateDirectoryPayload extends DirectoryQuery {
  name: string
}

export interface DeleteFileQuery {
  parent_id: string
  file_id: string
  account_id: number
}

export interface BatchFilePayload {
  parent_id: string
  file_ids: string[]
  account_id: number
}

export interface MoveFilesPayload extends BatchFilePayload {
  target_parent_id: string
}

export interface FileTransferResult {
  status: 'completed' | 'submitted'
  task_ids: string[]
}

export interface RenameFilePayload {
  parent_id: string
  file_id: string
  new_name: string
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
  'file_ids：不能为空': '请选择要操作的文件或目录',
  'file_ids：包含无效的文件 ID': '选中项包含无效的文件或目录',
  'target_parent_id：不能为空': '请选择目标目录',
  'new_name：不能为空': '请输入新名称',
  'new_name：文件名不合法': '新名称不合法',
  'new_name：不能包含路径分隔符': '新名称不能包含路径分隔符',
  'new_name：不能包含控制字符': '新名称不能包含控制字符',
  'path_id：不能为空': '请选择源文件或目录',
  'target_path：不能为空': '请选择目标目录',
  'name：不能为空': '请输入文件夹名称',
  'name：文件夹名不合法': '文件夹名不合法',
  'name：不能包含路径分隔符': '文件夹名不能包含路径分隔符',
  'name：不能包含控制字符': '文件夹名不能包含控制字符',
  'sort_by：不支持的排序字段': '不支持的排序字段',
  'sort_order：不支持的排序方向': '不支持的排序方向',
}

export async function fetchFiles(
  http: AxiosInstance,
  params: NetFileListQuery,
  signal?: AbortSignal,
) {
  return unwrapResponse(
    await http.get<APIResponse<NetFileListPayload | FileSystemItem[] | null>>(
      `${SERVER_URL}/path/files`,
      { params, timeout: 60000, ...(signal ? { signal } : {}) },
    ),
  )
}

export async function fetchDirectories(
  http: AxiosInstance,
  params: DirectoryListQuery,
  signal?: AbortSignal,
) {
  return unwrapResponse(
    await http.get<APIResponse<DirInfo[] | null>>(`${SERVER_URL}/path/list`, {
      timeout: 60000,
      params,
      ...(signal ? { signal } : {}),
    }),
  )
}

export async function createDirectory(http: AxiosInstance, payload: CreateDirectoryPayload) {
  return unwrapResponse(await http.post<APIResponse<DirInfo>>(`${SERVER_URL}/path/create`, payload))
}

export async function deleteFile(http: AxiosInstance, params: DeleteFileQuery): Promise<void> {
  unwrapResponse(await http.delete<APIResponse<null>>(`${SERVER_URL}/path`, { params }))
}

export async function deleteFiles(http: AxiosInstance, payload: BatchFilePayload): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/path/delete-batch`, payload))
}

export async function moveFiles(http: AxiosInstance, payload: MoveFilesPayload) {
  return unwrapResponse(
    await http.post<APIResponse<FileTransferResult | null>>(`${SERVER_URL}/path/move`, payload),
  )
}

export async function copyFiles(http: AxiosInstance, payload: MoveFilesPayload) {
  return unwrapResponse(
    await http.post<APIResponse<FileTransferResult | null>>(`${SERVER_URL}/path/copy`, payload),
  )
}

export async function renameFile(http: AxiosInstance, payload: RenameFilePayload): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/path/rename`, payload))
}

export async function generateManualStrm(
  http: AxiosInstance,
  payload: ManualStrmPayload,
): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/sync/manual`, payload))
}
