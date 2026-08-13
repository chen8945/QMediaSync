export interface CloudAccountAppInfo {
  source_type: string
  app_id?: string
  app_id_name?: string
  app_name?: string
  display_name?: string
  auth_source_type?: string
  auth_provider?: string
}

export const isCustomV115App = (account: CloudAccountAppInfo): boolean =>
  account.source_type === '115' &&
  (account.auth_source_type ? account.auth_source_type === 'custom_appid' : Boolean(account.app_id))

export const getV115AppInfoRows = (
  account: CloudAccountAppInfo,
): Array<{ label: string; value: string }> => {
  if (!isCustomV115App(account)) {
    return [
      {
        label: '开放平台应用',
        value: account.display_name || account.app_name || account.app_id_name || '-',
      },
    ]
  }
  return [
    { label: '应用名', value: account.app_id_name || '自定义' },
    { label: 'APP ID', value: account.app_id || '-' },
  ]
}
