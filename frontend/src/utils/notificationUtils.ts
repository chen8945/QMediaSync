// 渠道类型
export type ChannelType = 'telegram' | 'meow' | 'bark' | 'serverchan' | 'webhook'

// 事件类型
export type EventType =
  | 'sync_finish'
  | 'sync_error'
  | 'scrape_finish'
  | 'scrape_error'
  | 'system_alert'
  | 'media_added'
  | 'media_removed'
  | 'playback_start'
  | 'playback_pause'
  | 'playback_stop'

// 通知渠道接口
export interface NotificationChannel {
  id: number
  channel_type: ChannelType
  channel_name: string
  is_enabled: boolean
  created_at: number
  updated_at: number
  config?: NotificationConfig
  rules?: NotificationRule[]
}

// 通知配置接口（不同类型的配置）
export interface NotificationConfig {
  // Telegram
  bot_token?: string
  chat_id?: string
  proxy_url?: string

  // MeoW
  nickname?: string
  endpoint?: string

  // Bark
  device_key?: string
  server_url?: string
  sound?: string
  icon?: string

  // Server酱
  sc_key?: string

  // Webhook
  method?: string
  format?: string
  template?: string
  query_param?: string
  auth_type?: string
  auth_token?: string
  auth_user?: string
  auth_pass?: string
  auth_header_key?: string
  auth_query_key?: string
  headers?: Record<string, string>
  description?: string
}

export interface WebhookHeaderRow {
  id: number
  key: string
  value: string
}

export function webhookHeaderRecordToRows(
  headers?: Record<string, string> | null,
): WebhookHeaderRow[] {
  return Object.entries(headers ?? {}).map(([key, value], index) => ({
    id: index + 1,
    key,
    value,
  }))
}

export function webhookHeaderRowsToRecord(rows: WebhookHeaderRow[]): Record<string, string> {
  return rows.reduce<Record<string, string>>((headers, row) => {
    const key = row.key.trim()
    if (key) {
      headers[key] = row.value
    }
    return headers
  }, {})
}

// 通知规则接口
export interface NotificationRule {
  id: number
  channel_id: number
  event_type: EventType
  is_enabled: boolean
  created_at: string
  updated_at: string
}

// 获取渠道类型名称
export function getChannelTypeName(type: ChannelType): string {
  const nameMap: Record<ChannelType, string> = {
    telegram: 'Telegram',
    meow: 'MeoW',
    bark: 'Bark',
    serverchan: 'Server酱',
    webhook: 'Webhook',
  }
  return nameMap[type] || type
}

// 获取事件类型名称
export function getEventTypeName(type: EventType): string {
  const nameMap: Record<EventType, string> = {
    sync_finish: 'STRM 同步完成',
    sync_error: 'STRM 同步错误',
    scrape_finish: '刮削完成',
    scrape_error: '刮削错误',
    system_alert: '系统警告',
    media_added: 'Emby 媒体添加',
    media_removed: 'Emby 媒体移除',
    playback_start: 'Emby 播放开始',
    playback_pause: 'Emby 播放暂停',
    playback_stop: 'Emby 播放停止',
  }
  return nameMap[type] || type
}
