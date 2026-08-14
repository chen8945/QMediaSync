export type V115AuthMode = 'qr' | 'oauth'

export type V115AuthSourceType =
  'built_in_appid' | 'built_in_relay' | 'third_party_service' | 'custom_appid'

export type V115AuthProvider =
  'official_pkce' | 'mqfamily' | 'qmediasync' | 'moviepilot' | 'clouddrive'

export interface V115SelectedQrApp {
  appId: string
  appName: string
}

export interface V115CreateSelection {
  authMode: V115AuthMode
  selectedQrApp: V115SelectedQrApp
  selectedWebProvider: V115AuthProvider
  customAppId: string
  customAppName: string
}

export interface V115CreatePayload {
  auth_source_type: V115AuthSourceType
  auth_provider: V115AuthProvider
  app_id: string
  app_id_name: string
  custom_app_name?: string
}

export interface V115AccountAuthInfo {
  source_type: string
  app_id?: string
  app_id_name?: string
  auth_source_type?: V115AuthSourceType
  auth_provider?: V115AuthProvider
}

const webAuthorizationSourceTypes: V115AuthSourceType[] = ['built_in_relay', 'third_party_service']

const legacyWebAuthorizationAppNames = ['QMediaSync', 'Q115-STRM', 'MQ的媒体库']

const isV115WebAuthorizationSource = (sourceType?: V115AuthSourceType): boolean =>
  sourceType !== undefined && webAuthorizationSourceTypes.includes(sourceType)

const isLegacyV115WebAuthorizationAppName = (appName?: string, fallback = ''): boolean =>
  legacyWebAuthorizationAppNames.includes(appName || fallback)

export const getDefaultV115ChangeSelection = (
  account: V115AccountAuthInfo,
): V115CreateSelection => {
  const currentProvider = account.auth_provider
  const currentWebProvider = webAuthProviders.find(
    (provider) => provider.provider === currentProvider,
  )
  const isWebAuthorization =
    isV115WebAuthorizationSource(account.auth_source_type) ||
    (!account.auth_source_type && isLegacyV115WebAuthorizationAppName(account.app_id_name))

  return {
    authMode: isWebAuthorization ? 'oauth' : 'qr',
    selectedQrApp: { appId: '100197849', appName: 'QMediaSync' },
    selectedWebProvider: currentWebProvider?.provider ?? defaultWebAuthProviderValue,
    customAppId: '',
    customAppName: '',
  }
}

export type V115AuthAction = 'pkce' | 'oauth' | 'unsupported'

export interface V115WebAuthProviderOption {
  label: string
  sourceType: V115AuthSourceType
  provider: V115AuthProvider
  appId?: string
  appName: string
  disabled?: boolean
}

export const pinnedBuiltInAppIDs = [
  { label: 'QMediaSync', value: '100197849', appName: 'QMediaSync' },
] as const

export const featuredBuiltInAppIDs = [
  { label: 'MoviePilot-115', value: '100197847', appName: 'MoviePilot-115' },
  { label: 'OpenList', value: '100197303', appName: 'OpenList' },
  { label: 'CloudDrive', value: '100195313', appName: 'CloudDrive' },
  { label: '媒体播放器', value: '100195125', appName: '媒体播放器' },
] as const

export const webAuthProviders: V115WebAuthProviderOption[] = [
  {
    label: 'QMediaSync',
    sourceType: 'built_in_relay',
    provider: 'qmediasync',
    appName: 'QMediaSync',
  },
  {
    label: 'MoviePilot',
    sourceType: 'third_party_service',
    provider: 'moviepilot',
    appId: '100197847',
    appName: 'MoviePilot-115',
  },
  {
    label: 'CloudDrive',
    sourceType: 'third_party_service',
    provider: 'clouddrive',
    appId: '100195313',
    appName: 'CloudDrive',
  },
]

export const defaultWebAuthProviderValue: V115AuthProvider = webAuthProviders[0].provider

export const buildV115CreatePayload = (selection: V115CreateSelection): V115CreatePayload => {
  if (selection.authMode === 'oauth') {
    const provider = webAuthProviders.find(
      (item) => item.provider === selection.selectedWebProvider,
    )
    return {
      auth_source_type: provider?.sourceType ?? 'built_in_relay',
      auth_provider: provider?.provider ?? 'qmediasync',
      app_id: provider?.appId || '',
      app_id_name: provider?.appName || provider?.label || '',
    }
  }

  if (selection.selectedQrApp.appId === 'custom') {
    return {
      auth_source_type: 'custom_appid',
      auth_provider: 'official_pkce',
      app_id: selection.customAppId,
      app_id_name: selection.customAppName,
      custom_app_name: selection.customAppName,
    }
  }

  return {
    auth_source_type: 'built_in_appid',
    auth_provider: 'official_pkce',
    app_id: selection.selectedQrApp.appId,
    app_id_name: selection.selectedQrApp.appName,
  }
}

export const getV115AuthAction = (account: V115AccountAuthInfo): V115AuthAction => {
  if (account.source_type !== '115') {
    return 'unsupported'
  }
  if (account.auth_provider === 'official_pkce') {
    return 'pkce'
  }
  if (isV115WebAuthorizationSource(account.auth_source_type)) {
    return 'oauth'
  }
  if (!account.auth_source_type) {
    if (account.app_id) {
      return 'pkce'
    }
    if (isLegacyV115WebAuthorizationAppName(account.app_id_name, 'QMediaSync')) {
      return 'oauth'
    }
  }
  return 'unsupported'
}
