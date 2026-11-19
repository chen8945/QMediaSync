import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import type { LogEntry } from '@/types/log'
import { fetchJSONResponse } from './fetchResponse'
import { unwrapResponse } from './response'

export interface LogSnapshot {
  entries: LogEntry[]
  file_id: string
  pos: number
  start_pos?: number
  cursor?: number
}

export async function fetchLogCatchUp(
  path: string,
  cursor: number,
  signal?: AbortSignal,
  fileID?: string,
) {
  const response = await fetchJSONResponse(
    `/api/logs/catch-up?path=${encodeURIComponent(path)}&cursor=${cursor}&limit=500${fileID ? `&file_id=${encodeURIComponent(fileID)}` : ''}`,
    { credentials: 'include', signal },
  )
  const data = unwrapResponse<{
    entries: LogEntry[]
    cursor: number
    has_more: boolean
    file_id: string
  } | null>(response).data
  if (
    !data ||
    !Array.isArray(data.entries) ||
    typeof data.cursor !== 'number' ||
    typeof data.file_id !== 'string' ||
    !data.file_id ||
    (fileID !== undefined && data.file_id !== fileID)
  )
    throw new HttpResponseError(response)
  return data
}

export async function fetchLogSnapshot(
  path: string,
  pos: number,
  limit: number,
  signal?: AbortSignal,
  fileID?: string,
): Promise<LogSnapshot> {
  const url = `/api/logs/old?path=${encodeURIComponent(path)}&pos=${pos}&direction=forward&limit=${limit}${fileID ? `&file_id=${encodeURIComponent(fileID)}` : ''}`
  const response = await fetchJSONResponse(url, {
    credentials: 'include',
    ...(signal ? { signal } : {}),
  })
  const data = unwrapResponse<LogSnapshot | null>(response).data
  if (
    !data ||
    !Array.isArray(data.entries) ||
    typeof data.pos !== 'number' ||
    typeof data.file_id !== 'string' ||
    !data.file_id ||
    (fileID !== undefined && data.file_id !== fileID)
  ) {
    throw new HttpResponseError(response)
  }
  return data as LogSnapshot
}

export function logDownloadURL(path: string): string {
  return `${SERVER_URL}/logs/download?path=${encodeURIComponent(path)}`
}

export function logViewURL(path: string): string {
  return `${SERVER_URL}/logs/view?path=${encodeURIComponent(path)}`
}
