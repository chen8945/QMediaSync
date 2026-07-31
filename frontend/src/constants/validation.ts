export const THREAD_LIMITS = {
  downloadThreads: { min: 1, max: 10 },
  fileDetailThreads: { min: 2, max: 10 },
  openlistQPS: { min: 2, max: 10 },
  openlistRetry: { min: 1, max: 10 },
  openlistRetryDelay: { min: 30, max: 3600 },
  fileListPageSize: { min: 100, max: 1150 },
  urlValidityCheckTimeout: { min: 1, max: 9 },
} as const

export const SCRAPE_THREAD_LIMITS = {
  localMax: 20,
  remoteMax: 5,
  min: 1,
} as const

export const STRM_GLOBAL_OPTIONS = {
  localProxy: [0, 1],
  uploadMeta: [0, 1, 2],
  downloadMeta: [0, 1],
  deleteDir: [0, 1],
  addPath: [1, 2, 3],
  checkMetaMtime: [0, 1],
} as const

export const STRM_CUSTOM_OPTIONS = {
  localProxy: [-1, 0, 1],
  uploadMeta: [-1, 0, 1, 2],
  downloadMeta: [-1, 0, 1],
  deleteDir: [-1, 0, 1],
  addPath: [-1, 1, 2, 3],
  checkMetaMtime: [-1, 0, 1],
} as const

export const HTTP_URL_PATTERN = /^(http|https):\/\/[^\s/$.?#].[^\s]*$/

export const CRON_DEFAULTS = {
  embySync: '0 * * * *',
} as const

// PROXY_SCHEMES 出站代理协议白名单，与后端 validation 包的 proxySchemes 保持一致。
// 这里是前端唯一来源：协议提示、表单帮助和校验都从它派生，后端新增协议时只改这一处。
export const PROXY_SCHEMES = ['http', 'https', 'socks5', 'socks5h'] as const

// PROXY_PORT_RANGE 代理端口范围，与后端 validation.PortInRange 一致。
export const PROXY_PORT_RANGE = { min: 1, max: 65535 } as const

// PROXY_SCHEME_HINT 协议不受支持时的提示，与后端 ProxySchemeHint 同构。
export const PROXY_SCHEME_HINT = `只支持 ${PROXY_SCHEMES.join('、')}`

// PROXY_URL_PLACEHOLDER 代理地址输入框占位符。示例端口是各协议的社区惯例，不随白名单变化。
export const PROXY_URL_PLACEHOLDER = '例如：http://127.0.0.1:7890 或 socks5://127.0.0.1:1080'

// PROXY_URL_HELP 代理地址表单帮助文案。
export const PROXY_URL_HELP =
  `格式：协议://[用户名:密码@]主机:端口，可用协议为 ${PROXY_SCHEMES.join('、')}；` +
  '省略主机名表示本机代理，例如 http://:7890；留空表示不使用代理'

// PROXY_CREDENTIALS_MASKED_HINT 已保存凭据被脱敏时的提示。
// 接口不回传明文凭据，输入框里显示的是占位串；不改动直接保存会沿用已存的凭据。
export const PROXY_CREDENTIALS_MASKED_HINT =
  '已保存的用户名和密码不会回显，显示为 xxxxx；不修改直接保存或测试将沿用原有凭据，需更换请重新输入完整地址'

// PROXY_URL_MESSAGES 代理地址校验提示，与后端 validation.ProxyURL 的错误原因一一对应。
export const PROXY_URL_MESSAGES = {
  whitespace: '代理地址不能包含空格或控制字符',
  format: '代理地址格式无效，请填写“协议://主机:端口”',
  scheme: PROXY_SCHEME_HINT,
  port: `端口必须在 ${PROXY_PORT_RANGE.min}-${PROXY_PORT_RANGE.max} 之间`,
} as const
