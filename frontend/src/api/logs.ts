import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import type { LogEntry } from '@/types/log'
import { fetchJSONResponse } from './fetchResponse'
import { unwrapResponse } from './response'

export interface LogSnapshot {
  entries: LogEntry[]
  pos: number
  start_pos?: number
}

export async function fetchLogSnapshot(
  path: string,
  pos: number,
  limit: number,
  signal?: AbortSignal,
): Promise<LogSnapshot> {
  const url = `/api/logs/old?path=${encodeURIComponent(path)}&pos=${pos}&direction=forward&limit=${limit}`
  const response = await fetchJSONResponse(url, {
    credentials: 'include',
    ...(signal ? { signal } : {}),
  })
  const data = unwrapResponse<LogSnapshot | null>(response).data
  if (!data || !Array.isArray(data.entries) || typeof data.pos !== 'number') {
    throw new HttpResponseError(response)
  }
  return data as LogSnapshot
}

export function logDownloadURL(path: string): string {
  return `${SERVER_URL}/logs/download?path=${encodeURIComponent(path)}`
}
