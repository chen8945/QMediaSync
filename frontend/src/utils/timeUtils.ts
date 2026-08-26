// 时间格式化工具函数

export type MaybeUnixDateTime = number | string | null | undefined
export type MaybeTimeValue = MaybeUnixDateTime

const DATE_TIME_PLACEHOLDER = '-'

const dateTimeFormatterOptions: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
}

const normalizeDateTimeParts = (value: string): string =>
  value.trim().replace(/\//g, '-').replace('T', ' ').replace(/Z$/, '')

const formatDateObject = (date: Date): string => {
  if (Number.isNaN(date.getTime())) {
    return DATE_TIME_PLACEHOLDER
  }

  return date.toLocaleString('zh-CN', dateTimeFormatterOptions).replace(/\//g, '-')
}

const parseMaybeTimeValue = (value: MaybeTimeValue): Date | null => {
  if (value === null || value === undefined || value === '') {
    return null
  }

  if (typeof value === 'number') {
    if (!value) {
      return null
    }
    return new Date(value * 1000)
  }

  const trimmed = value.trim()
  if (!trimmed) {
    return null
  }

  if (/^\d+$/.test(trimmed)) {
    const timestamp = Number(trimmed)
    return timestamp ? new Date(timestamp * 1000) : null
  }

  if (trimmed.includes('T') || /(?:Z|[+-]\d{2}:\d{2})$/.test(trimmed)) {
    return new Date(trimmed)
  }

  const normalized = normalizeLegacyDateTime(trimmed)
  if (normalized === DATE_TIME_PLACEHOLDER) {
    return null
  }

  return new Date(normalized.replace(' ', 'T'))
}

export const normalizeLegacyDateTime = (value: string): string => {
  const normalized = normalizeDateTimeParts(value)
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(normalized)) {
    return normalized
  }

  return DATE_TIME_PLACEHOLDER
}

export const formatUnixDateTime = (timestamp: number | null | undefined): string => {
  if (!timestamp) {
    return DATE_TIME_PLACEHOLDER
  }

  return formatDateObject(new Date(timestamp * 1000))
}

export const formatMaybeUnixDateTime = (value: MaybeUnixDateTime): string => {
  if (value === null || value === undefined || value === '') {
    return DATE_TIME_PLACEHOLDER
  }

  if (typeof value === 'number') {
    return formatUnixDateTime(value)
  }

  const trimmed = value.trim()
  if (!trimmed) {
    return DATE_TIME_PLACEHOLDER
  }

  if (/^\d+$/.test(trimmed)) {
    return formatUnixDateTime(Number(trimmed))
  }

  if (trimmed.includes('T') || /(?:Z|[+-]\d{2}:\d{2})$/.test(trimmed)) {
    return formatDateObject(new Date(trimmed))
  }

  return normalizeLegacyDateTime(trimmed)
}

export const formatUnixDate = (timestamp: number | null | undefined): string => {
  const date = parseMaybeTimeValue(timestamp)
  if (!date || Number.isNaN(date.getTime())) {
    return DATE_TIME_PLACEHOLDER
  }

  return date
    .toLocaleDateString('zh-CN', {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    })
    .replace(/\//g, '-')
}

export const formatRelativeTime = (
  value: MaybeTimeValue,
  nowSeconds = Math.floor(Date.now() / 1000),
): string => {
  const date = parseMaybeTimeValue(value)
  if (!date || Number.isNaN(date.getTime())) {
    return DATE_TIME_PLACEHOLDER
  }

  const diffSeconds = nowSeconds - Math.floor(date.getTime() / 1000)
  if (diffSeconds < 0) {
    return formatDateObject(date)
  }
  if (diffSeconds < 60) {
    return '刚刚'
  }

  const diffMinutes = Math.floor(diffSeconds / 60)
  if (diffMinutes < 60) {
    return `${diffMinutes} 分钟前`
  }

  const diffHours = Math.floor(diffSeconds / 3600)
  if (diffHours < 24) {
    return `${diffHours} 小时前`
  }

  const diffDays = Math.floor(diffSeconds / 86400)
  if (diffDays < 30) {
    return `${diffDays} 天前`
  }

  return formatDateObject(date)
}

/**
 * 格式化时间戳为日期时间字符串 (YYYY-MM-DD HH:MM:SS)
 * @param timestamp 时间戳 (秒)
 * @returns 格式化后的日期时间字符串
 */
export const formatTimestamp = (timestamp: number): string => {
  return formatUnixDateTime(timestamp)
}

/**
 * 格式化日期时间戳为可读字符串
 * @param timestamp 时间戳 (秒)
 * @returns 格式化后的日期时间字符串
 */
export const formatDateTime = (timestamp: number): string => {
  return formatUnixDateTime(timestamp)
}

/**
 * 格式化时间戳为时间字符串
 * @param timestamp 时间戳 (秒)
 * @returns 格式化后的时间字符串
 */
export const formatTime = (timestamp: number): string => {
  return formatUnixDateTime(timestamp)
}

/**
 * 格式化秒数为可读的时间段
 * @param seconds 秒数
 * @returns 格式化后的时间字符串
 */
export const formatDuration = (seconds: number): string => {
  if (!seconds || seconds <= 0) return '0 秒'

  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const secs = Math.floor(seconds % 60)

  const parts: string[] = []
  if (hours > 0) parts.push(`${hours} 小时`)
  if (minutes > 0) parts.push(`${minutes} 分`)
  if (secs > 0 || parts.length === 0) parts.push(`${secs} 秒`)

  return parts.join(' ')
}
