export type V115AuthStatus = 'idle' | 'waiting' | 'scanned' | 'confirmed' | 'expired' | 'failed'

export type V115AuthSourceType =
  'built_in_appid' | 'built_in_relay' | 'third_party_service' | 'custom_appid'

export type V115AuthProvider =
  'official_pkce' | 'mqfamily' | 'qmediasync' | 'moviepilot' | 'clouddrive'

export interface V115QrCodePayload {
  uid: string
  time: number
  sign: string
  qrcode: string
  expires: number
}

export interface V115QrCodeStatusPayload {
  status: V115AuthStatus
  tip: string
}
