export const sourceTypeOptions: Array<Record<string, string>> = [
  {
    label: '115 网盘',
    value: '115',
  },
  {
    label: '百度网盘',
    value: 'baidupan',
  },
  {
    label: 'OpenList',
    value: 'openlist',
  },
  {
    label: '本地目录',
    value: 'local',
  },
]

export const sourceTypeTagMap: Record<string, string> = {
  '115': 'success',
  baidupan: 'danger',
  openlist: 'warning',
  local: 'info',
}

export const sourceTypeMap: Record<string, string> = {
  '115': '115 网盘',
  baidupan: '百度网盘',
  openlist: 'OpenList',
  local: '本地目录',
}
