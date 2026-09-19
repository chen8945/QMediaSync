import axios, { type AxiosInstance } from 'axios'
import { describe, expect, it, vi } from 'vitest'
import {
  createNotificationChannel,
  deleteNotificationChannel,
  fetchNotificationChannel,
  fetchNotificationChannels,
  fetchNotificationRules,
  saveNotificationRule,
  setNotificationChannelEnabled,
  testNotificationChannel,
  updateNotificationChannel,
} from '@/api/notificationSettings'
import { HttpResponseError } from '@/http/errors'
import type { ChannelType } from '@/utils/notificationUtils'

const createHTTP = (code = 0, data: unknown = null, status = 200) => {
  const adapter = vi.fn(async (config) => ({
    config,
    status,
    statusText: '',
    headers: {},
    data: { code, message: 'internal token=private', data },
  }))
  return { http: axios.create({ adapter }), adapter }
}

describe('通知渠道 API', () => {
  it.each<ChannelType>(['telegram', 'meow', 'bark', 'serverchan', 'webhook'])(
    '保留 %s 类型对应的查询、创建和更新接口',
    async (type) => {
      const detail = {
        channel: { id: 7, channel_type: type },
        config: { endpoint: 'https://notify.example', headers: { Authorization: 'private' } },
      }
      const { http, adapter } = createHTTP(0, detail)
      expect(await fetchNotificationChannel(http, type, 7)).toEqual(detail)
      const payload = { channel_name: '通知', ...detail.config }
      await createNotificationChannel(http, type, payload)
      await updateNotificationChannel(http, type, { ...payload, channel_id: 7 })
      expect(adapter.mock.calls.map(([config]) => [config.method, config.url])).toEqual([
        ['get', `/api/setting/notification/channels/${type}/7`],
        ['post', `/api/setting/notification/channels/${type}`],
        ['put', `/api/setting/notification/channels/${type}`],
      ])
      expect(JSON.parse(adapter.mock.calls[1]![0].data)).toEqual(payload)
      expect(JSON.parse(adapter.mock.calls[2]![0].data)).toEqual({ ...payload, channel_id: 7 })
    },
  )

  it('查询账号渠道和规则保留服务端数据与渠道筛选', async () => {
    const rows = [{ id: 7, channel_id: 5, is_enabled: false }]
    const { http, adapter } = createHTTP(0, rows)
    expect(await fetchNotificationChannels(http)).toEqual(rows)
    expect(await fetchNotificationRules(http, 5)).toEqual(rows)
    expect(adapter.mock.calls[0]![0].url).toBe('/api/setting/notification/channels')
    expect(adapter.mock.calls[1]![0]).toMatchObject({
      url: '/api/setting/notification/rules',
      params: { channel_id: 5 },
    })
  })

  it('状态、测试、删除和规则保存接受 code=0 的空结果，保留 false', async () => {
    const { http, adapter } = createHTTP()
    await setNotificationChannelEnabled(http, 7, false)
    await testNotificationChannel(http, 7)
    await deleteNotificationChannel(http, 7)
    await saveNotificationRule(http, {
      channel_id: 7,
      event_type: 'sync_finish',
      is_enabled: false,
    })
    expect(
      adapter.mock.calls.map(([config]) => [
        config.method,
        config.url,
        config.data && JSON.parse(config.data),
      ]),
    ).toEqual([
      ['post', '/api/setting/notification/channels/status', { channel_id: 7, is_enabled: false }],
      ['post', '/api/setting/notification/channels/test', { channel_id: 7 }],
      ['delete', '/api/setting/notification/channels/7', undefined],
      [
        'put',
        '/api/setting/notification/rules',
        { channel_id: 7, event_type: 'sync_finish', is_enabled: false },
      ],
    ])
  })

  const operations: Array<[string, (http: AxiosInstance) => Promise<unknown>]> = [
    ['list', fetchNotificationChannels],
    ['detail', (http) => fetchNotificationChannel(http, 'telegram', 7)],
    ['create', (http) => createNotificationChannel(http, 'telegram', { channel_name: '通知' })],
    [
      'update',
      (http) =>
        updateNotificationChannel(http, 'telegram', { channel_name: '通知', channel_id: 7 }),
    ],
    ['enable', (http) => setNotificationChannelEnabled(http, 7, true)],
    ['test', (http) => testNotificationChannel(http, 7)],
    ['delete', (http) => deleteNotificationChannel(http, 7)],
    ['rules', (http) => fetchNotificationRules(http, 7)],
    [
      'saveRule',
      (http) =>
        saveNotificationRule(http, { channel_id: 7, event_type: 'sync_finish', is_enabled: true }),
    ],
  ]
  it.each(operations)('%s 拒绝 HTTP 200/code=1，不因 data 为真误判成功', async (_, operation) => {
    const { http } = createHTTP(1, true)
    await expect(operation(http)).rejects.toMatchObject({
      response: { status: 200, data: { code: 1, data: true } },
    })
  })

  it('通知专用 code=0 判定不接受 code=200，且业务成功不能覆盖 HTTP 失败', async () => {
    await expect(testNotificationChannel(createHTTP(200).http, 7)).rejects.toBeInstanceOf(
      HttpResponseError,
    )
    await expect(testNotificationChannel(createHTTP(0, null, 403).http, 7)).rejects.toMatchObject({
      response: { status: 403 },
    })
  })
})
