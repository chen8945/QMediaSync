import { describe, expect, it } from 'vitest'

import {
  buildV115CreatePayload,
  getDefaultV115ChangeSelection,
  getV115AuthAction,
  webAuthProviders,
  type V115AccountAuthInfo,
} from '@/components/cloud-auth/v115AuthSources'

describe('v115AuthSources', () => {
  it('扫码授权内置 APP ID 创建 payload 使用数字 APP ID', () => {
    expect(
      buildV115CreatePayload({
        authMode: 'qr',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'qmediasync',
        customAppId: '',
        customAppName: '',
      }),
    ).toEqual({
      auth_source_type: 'built_in_appid',
      auth_provider: 'official_pkce',
      app_id: '100197849',
      app_id_name: 'QMediaSync',
    })
  })

  it('网页授权创建 payload 使用 provider 来源', () => {
    expect(
      buildV115CreatePayload({
        authMode: 'oauth',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'clouddrive',
        customAppId: '',
        customAppName: '',
      }),
    ).toEqual({
      auth_source_type: 'third_party_service',
      auth_provider: 'clouddrive',
      app_id: '100195313',
      app_id_name: 'CloudDrive',
    })
  })

  it('网页授权选项按 provider 选择，并移除旧的 MQ 授权入口', () => {
    expect(webAuthProviders.map((provider) => provider.provider)).toEqual([
      'qmediasync',
      'moviepilot',
      'clouddrive',
    ])
    expect(webAuthProviders.map((provider) => provider.label)).not.toEqual(
      expect.arrayContaining(['Q115-STRM', 'MQ的媒体库']),
    )

    expect(
      buildV115CreatePayload({
        authMode: 'oauth',
        selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
        selectedWebProvider: 'clouddrive',
        customAppId: '',
        customAppName: '',
      }),
    ).toEqual({
      auth_source_type: 'third_party_service',
      auth_provider: 'clouddrive',
      app_id: '100195313',
      app_id_name: 'CloudDrive',
    })
  })

  it('网页授权不再展示 OpenList', () => {
    expect(webAuthProviders.some((provider) => String(provider.provider) === 'openlist')).toBe(
      false,
    )
    expect(
      webAuthProviders.find((provider) => provider.provider === 'clouddrive')?.disabled,
    ).not.toBe(true)
  })

  it('授权动作按 auth_provider 分发', () => {
    const qrAccount: V115AccountAuthInfo = {
      source_type: '115',
      auth_source_type: 'built_in_appid',
      auth_provider: 'official_pkce',
      app_id: '100197849',
    }
    const oauthAccount: V115AccountAuthInfo = {
      source_type: '115',
      auth_source_type: 'built_in_relay',
      auth_provider: 'qmediasync',
      app_id_name: 'QMediaSync',
    }
    const thirdPartyAccount: V115AccountAuthInfo = {
      source_type: '115',
      auth_source_type: 'third_party_service',
      auth_provider: 'moviepilot',
      app_id: '100197847',
    }
    const legacyOAuthAccount: V115AccountAuthInfo = {
      source_type: '115',
      app_id_name: 'MQ的媒体库',
    }
    const legacyQrAccount: V115AccountAuthInfo = {
      source_type: '115',
      app_id: 'legacy-app-id',
      app_id_name: 'QMediaSync',
    }

    expect(getV115AuthAction(qrAccount)).toBe('pkce')
    expect(getV115AuthAction(oauthAccount)).toBe('oauth')
    expect(getV115AuthAction(thirdPartyAccount)).toBe('oauth')
    expect(getV115AuthAction(legacyOAuthAccount)).toBe('oauth')
    expect(getV115AuthAction(legacyQrAccount)).toBe('pkce')
  })

  it('更换授权对已废弃 QR 来源默认选择有效应用', () => {
    expect(
      getDefaultV115ChangeSelection({
        source_type: '115',
        auth_source_type: 'built_in_appid',
        auth_provider: 'official_pkce',
        app_id: '100197665',
        app_id_name: 'Q115-STRM',
      }),
    ).toMatchObject({
      authMode: 'qr',
      selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
    })
  })

  it('更换网页授权时复用仍有效的 provider', () => {
    expect(
      getDefaultV115ChangeSelection({
        source_type: '115',
        auth_source_type: 'third_party_service',
        auth_provider: 'moviepilot',
      }),
    ).toMatchObject({
      authMode: 'oauth',
      selectedWebProvider: 'moviepilot',
    })
  })
})
