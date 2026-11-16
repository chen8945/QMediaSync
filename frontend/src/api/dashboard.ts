import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export interface Announcement {
  id?: number | string
  time: string
  title: string
  content: string
}

export interface HourlyStat {
  hour_ts: number
  total_requests: number
  throttled_requests: number
  avg_duration: string
}

export interface HourlyStatsData {
  start_date: string
  end_date: string
  total_requests: number
  total_throttled: number
  hourly_stats: HourlyStat[]
  query_time_range_days: number
}

export interface QueueStats {
  avg_response_time_ms: number
  is_throttled: boolean
  last_throttle_time: string | null
  qph_count: number
  qpm_count: number
  qps_count: number
  throttle_recover_time: string | null
  throttle_wait_time: string
  throttled_count: number
  throttled_elapsed_time: string
  throttled_remaining_time: string
  time_window_seconds: number
  total_requests: number
}

export async function fetchAnnouncements(http: AxiosInstance): Promise<Announcement[]> {
  return (
    unwrapResponse(await http.get<APIResponse<Announcement[] | null>>(`${SERVER_URL}/announce`))
      .data ?? []
  )
}

export async function fetchHourlyStats(http: AxiosInstance): Promise<HourlyStatsData | null> {
  return unwrapResponse(
    await http.get<APIResponse<HourlyStatsData | null>>(`${SERVER_URL}/115/stats/hourly`),
  ).data
}

export async function fetchQueueStats(http: AxiosInstance): Promise<QueueStats | null> {
  return unwrapResponse(
    await http.get<APIResponse<QueueStats | null>>(`${SERVER_URL}/115/queue/stats`),
  ).data
}
