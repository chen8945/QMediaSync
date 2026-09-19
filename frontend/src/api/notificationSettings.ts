import type { AxiosInstance, AxiosResponse } from 'axios'
import { SERVER_URL } from '@/const'
import { HttpResponseError } from '@/http/errors'
import type {
  ChannelType,
  NotificationChannel,
  NotificationConfig,
  NotificationRule,
} from '@/utils/notificationUtils'
import type { APIResponse } from './types'

export interface NotificationChannelPayload extends NotificationConfig {
  channel_name: string
  channel_id?: number
}

export interface NotificationChannelDetails {
  channel: NotificationChannel & { description?: string }
  config?: NotificationConfig | null
}

// 仅改写需要本地化的业务字段和文案，其他原因使用服务端消息。

export const notificationPublicMessages: Readonly<Record<string, string>> = {
  参数错误: '请求参数错误',
  渠道不存在: '通知渠道不存在，请刷新列表后重试',
  配置不存在: '通知渠道配置不存在，请刷新列表后重试',
  渠道类型不匹配: '通知渠道类型不匹配，请刷新列表后重试',
  未知的渠道类型: '不支持的通知渠道类型',
  'channel_id：不能为空': '请选择通知渠道',
  'channel_name：不能为空': '请填写渠道名称',
  'bot_token：不能为空': '请填写 Bot Token',
  'chat_id：不能为空': '请填写 Chat ID',
  'nickname：不能为空': '请填写昵称',
  'device_key：不能为空': '请填写设备密钥',
  'sc_key：不能为空': '请填写 SCKEY',
  'endpoint：不能为空': '请填写请求地址',
  'endpoint：必须是有效的 HTTP URL': '请填写有效的 HTTP 请求地址',
  'endpoint：只支持 http 或 https': '请求地址只支持 http 或 https',
  'endpoint：端口必须在 1-65535 之间': '请求地址端口必须在 1-65535 之间',
  'server_url：必须是有效的 HTTP URL': '请填写有效的 HTTP 服务器地址',
  'server_url：只支持 http 或 https': '服务器地址只支持 http 或 https',
  'server_url：端口必须在 1-65535 之间': '服务器地址端口必须在 1-65535 之间',
  'icon：必须是有效的 HTTP URL': '请填写有效的 HTTP 图标地址',
  'icon：只支持 http 或 https': '图标地址只支持 http 或 https',
  'icon：端口必须在 1-65535 之间': '图标地址端口必须在 1-65535 之间',
  'method：不能为空': '请选择请求方法',
  'method：必须是 GET 或 POST': '请求方法必须是 GET 或 POST',
  'format：必须是 JSON、form 或 text': '数据格式必须是 JSON、form 或 text',
  'template：不能为空': '请填写消息模板',
  'template：Form 模板无效：必须是 key=value&key2=value2 格式':
    'Form 模板必须是 key=value&key2=value2 格式',
  'auth_token：Bearer 鉴权需要提供 auth_token': 'Bearer 鉴权需要填写 Token',
  'auth_user：Basic 鉴权需要提供 auth_user 或 auth_pass': 'Basic 鉴权需要填写用户名或密码',
  'auth_header_key：Header 鉴权需要提供 auth_header_key 与 auth_token':
    'Header 鉴权需要填写 Header 名称和值',
  'auth_query_key：Query 鉴权需要提供 auth_query_key 与 auth_token': 'Query 鉴权需要填写参数名和值',
  'auth_type：必须是 none、bearer、basic、header 或 query': '请选择支持的鉴权类型',
  'headers 必须是对象': '自定义请求头格式错误',
  'headers：Header 名称不能为空': 'Header 名称不能为空',
  'headers：Header 名称包含非法字符': 'Header 名称包含非法字符',
}

function unwrapNotificationResponse<T>(response: AxiosResponse<APIResponse<T>>): T {
  // 通知管理沿用 code=0；不得把该约定扩展到其他领域接口。
  if (response.status < 200 || response.status >= 300 || response.data?.code !== 0) {
    throw new HttpResponseError(response)
  }
  return response.data.data
}

const channelsURL = `${SERVER_URL}/setting/notification/channels`
const rulesURL = `${SERVER_URL}/setting/notification/rules`

export async function fetchNotificationChannels(
  http: AxiosInstance,
): Promise<NotificationChannel[]> {
  return unwrapNotificationResponse(await http.get<APIResponse<NotificationChannel[]>>(channelsURL))
}

export async function fetchNotificationChannel(
  http: AxiosInstance,
  type: ChannelType,
  id: number,
): Promise<NotificationChannelDetails> {
  return unwrapNotificationResponse(
    await http.get<APIResponse<NotificationChannelDetails>>(`${channelsURL}/${type}/${id}`),
  )
}

export async function createNotificationChannel(
  http: AxiosInstance,
  type: ChannelType,
  payload: NotificationChannelPayload,
): Promise<void> {
  unwrapNotificationResponse(
    await http.post<APIResponse<unknown>>(`${channelsURL}/${type}`, payload),
  )
}

export async function updateNotificationChannel(
  http: AxiosInstance,
  type: ChannelType,
  payload: NotificationChannelPayload & { channel_id: number },
): Promise<void> {
  unwrapNotificationResponse(
    await http.put<APIResponse<unknown>>(`${channelsURL}/${type}`, payload),
  )
}

export async function setNotificationChannelEnabled(
  http: AxiosInstance,
  id: number,
  enabled: boolean,
): Promise<void> {
  unwrapNotificationResponse(
    await http.post<APIResponse<null>>(`${channelsURL}/status`, {
      channel_id: id,
      is_enabled: enabled,
    }),
  )
}

export async function testNotificationChannel(http: AxiosInstance, id: number): Promise<void> {
  unwrapNotificationResponse(
    await http.post<APIResponse<null>>(`${channelsURL}/test`, { channel_id: id }),
  )
}

export async function deleteNotificationChannel(http: AxiosInstance, id: number): Promise<void> {
  unwrapNotificationResponse(await http.delete<APIResponse<null>>(`${channelsURL}/${id}`))
}

export async function fetchNotificationRules(
  http: AxiosInstance,
  channelId: number,
): Promise<NotificationRule[]> {
  return unwrapNotificationResponse(
    await http.get<APIResponse<NotificationRule[]>>(rulesURL, {
      params: { channel_id: channelId },
    }),
  )
}

export async function saveNotificationRule(
  http: AxiosInstance,
  payload: Pick<NotificationRule, 'channel_id' | 'event_type' | 'is_enabled'>,
): Promise<void> {
  unwrapNotificationResponse(await http.put<APIResponse<null>>(rulesURL, payload))
}
