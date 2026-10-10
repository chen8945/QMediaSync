import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { setImmediate } from 'node:timers/promises'
import { Script } from 'node:vm'
import {
  BrowserErrorCaptureEnum,
  Window,
  type HTMLElement,
  type HTMLButtonElement,
  type HTMLInputElement,
} from 'happy-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const htmlPath = resolve(__dirname, '../../../backend/assets/db_config.html')
const html = readFileSync(htmlPath, 'utf8')
  .replace(/\{\{ if \.isRestricted \}\}[\s\S]*?\{\{ end \}\}/, '')
  .replace('{{ if .isWindows }}localhost{{ else }}postgres{{ end }}', 'postgres')

interface PendingRequest {
  url: string
  body: Record<string, unknown>
  resolve: (result: Record<string, unknown>) => void
  reject: (error: Error) => void
}

const existingDatabase = { success: true, dbExists: true, hasOtherTables: true }
const changedFields = [
  { field: 'host', value: 'postgres-b' },
  { field: 'port', value: '5433' },
  { field: 'user', value: 'another-user' },
  { field: 'password', value: 'another-password' },
  { field: 'database', value: 'another-database' },
  { field: 'ssl', value: true },
]

let page: Window
let requests: PendingRequest[]
const confirm = vi.fn<() => boolean>()

function element<T extends HTMLElement>(selector: string): T {
  const result = page.document.querySelector<T>(selector)
  if (!result) throw new Error(`找不到页面元素：${selector}`)
  return result
}

function button(text: string): HTMLButtonElement {
  const result = Array.from(page.document.querySelectorAll('button')).find(
    (candidate) => candidate.textContent.trim() === text,
  )
  if (!result) throw new Error(`找不到按钮：${text}`)
  return result
}

function changeField(field: string, value: string | boolean, dispatchEvent = true) {
  const input = element<HTMLInputElement>(`#${field}`)
  if (typeof value === 'boolean') input.checked = value
  else input.value = value
  if (dispatchEvent) {
    input.dispatchEvent(
      new page.Event(typeof value === 'boolean' ? 'change' : 'input', { bubbles: true }),
    )
  }
}

function testConnection(): PendingRequest {
  const count = requests.length
  element<HTMLButtonElement>('#btn-test').click()
  expect(requests).toHaveLength(count + 1)
  const request = requests[count]!
  expect(request.url).toBe('/api/config/test-db')
  return request
}

async function reply(request: PendingRequest, result: Record<string, unknown>) {
  request.resolve(result)
  await setImmediate()
}

function saveRequests() {
  return requests.filter((request) => request.url === '/api/config/save')
}

beforeEach(() => {
  page = new Window({
    url: 'http://localhost:12333/db-config.html',
    settings: {
      enableJavaScriptEvaluation: false,
      suppressInsecureJavaScriptEnvironmentWarning: true,
      errorCapture: BrowserErrorCaptureEnum.disabled,
    },
  })
  requests = []
  confirm.mockReset().mockReturnValue(false)
  Object.defineProperty(page, 'confirm', { value: confirm })
  vi.spyOn(page, 'fetch').mockImplementation(
    (url, options) =>
      new Promise((resolve, reject) => {
        requests.push({
          url: String(url),
          body: JSON.parse(String(options?.body)),
          resolve: (result) => resolve(new page.Response(JSON.stringify(result))),
          reject,
        })
      }),
  )
  // 渲染非 Windows、非受限目录分支，保留真实页面、内联脚本及事件处理器。
  expect(html).not.toContain('{{')
  page.document.write(html)
  // happy-dom 自动执行会包裹顶层函数；按全局脚本执行才能保留真实 onclick 绑定。
  page.happyDOM.settings.enableJavaScriptEvaluation = true
  for (const script of page.document.scripts) {
    new Script(script.textContent, { filename: htmlPath }).runInContext(page)
  }
  button('选择并配置 PostgreSQL').click()
})

afterEach(async () => {
  await page.happyDOM.close()
  vi.restoreAllMocks()
})

describe('数据库配置页面', () => {
  it('忽略参数修改后才返回的旧成功，不弹出清库确认或放行保存', async () => {
    confirm.mockReturnValue(true)
    const oldRequest = testConnection()

    changeField('database', 'another-database')
    await reply(oldRequest, existingDatabase)

    expect(confirm).not.toHaveBeenCalled()
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
    expect(element<HTMLButtonElement>('#btn-test').textContent).not.toContain('连接成功')
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()).toHaveLength(0)
  })

  it('旧成功不会覆盖重新测试的失败，也不会弹出清库确认', async () => {
    const oldRequest = testConnection()
    changeField('host', 'postgres-b')
    const newRequest = testConnection()
    await reply(newRequest, { success: false, error: '新连接失败' })

    await reply(oldRequest, existingDatabase)

    expect(confirm).not.toHaveBeenCalled()
    expect(element('#message').textContent).toBe('新连接失败')
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
  })

  it('参数改走又改回后重新测试，参数相同的旧成功仍不能覆盖新失败', async () => {
    const originalHost = element<HTMLInputElement>('#host').value
    const oldRequest = testConnection()
    changeField('host', 'postgres-b')
    changeField('host', originalHost)
    const newRequest = testConnection()
    expect(newRequest.body).toEqual(oldRequest.body)
    await reply(newRequest, { success: false, error: '本次连接失败' })

    await reply(oldRequest, existingDatabase)

    expect(confirm).not.toHaveBeenCalled()
    expect(element('#message').textContent).toBe('本次连接失败')
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()).toHaveLength(0)
  })

  it('空端口的当前测试失败仍显示错误，不被当成旧响应忽略', async () => {
    changeField('port', '')
    await reply(testConnection(), { success: false, error: '端口无效' })

    expect(element('#message').textContent).toBe('端口无效')
    expect(element<HTMLButtonElement>('#btn-test').disabled).toBe(false)
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
  })

  it.each(['业务失败', '请求异常'])('旧%s不会覆盖重新测试的成功', async (failure) => {
    const oldRequest = testConnection()
    changeField('host', 'postgres-b')
    const newRequest = testConnection()
    await reply(newRequest, { success: true, message: '新连接成功' })

    if (failure === '业务失败') {
      await reply(oldRequest, { success: false, error: '旧连接失败' })
    } else {
      oldRequest.reject(new Error('旧请求异常'))
      await setImmediate()
    }

    expect(element('#message').textContent).toBe('新连接成功')
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(false)
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()[0]?.body).toEqual({
      ...newRequest.body,
      engine: 'postgres',
      dropDatabase: false,
    })
  })

  it('重新测试时立即停止放行保存，且新结果不会沿用上次清库选择', async () => {
    confirm.mockReturnValue(true)
    await reply(testConnection(), existingDatabase)
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(false)

    const newRequest = testConnection()
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()).toHaveLength(0)

    await reply(newRequest, { success: true, dbExists: false })
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()[0]?.body.dropDatabase).toBe(false)
    expect(confirm).toHaveBeenCalledTimes(1)
  })

  it.each(changedFields)(
    '确认清库后修改 $field，必须重新测试并重新选择',
    async ({ field, value }) => {
      confirm.mockReturnValue(true)
      await reply(testConnection(), existingDatabase)

      changeField(field, value)
      expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
      element<HTMLButtonElement>('#btn-save').click()
      expect(saveRequests()).toHaveLength(0)

      confirm.mockReturnValue(false)
      const newRequest = testConnection()
      await reply(newRequest, existingDatabase)
      element<HTMLButtonElement>('#btn-save').click()
      expect(confirm).toHaveBeenCalledTimes(2)
      expect(saveRequests()[0]?.body).toEqual({
        ...newRequest.body,
        engine: 'postgres',
        dropDatabase: false,
      })
    },
  )

  it.each(changedFields)(
    '未触发输入事件而修改 $field 时，保存仍核对实际参数',
    async ({ field, value }) => {
      confirm.mockReturnValue(true)
      await reply(testConnection(), existingDatabase)

      changeField(field, value, false)
      element<HTMLButtonElement>('#btn-save').click()

      expect(saveRequests()).toHaveLength(0)
      expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
    },
  )

  it('返回引擎选择后重新选择 PostgreSQL，必须重新测试', async () => {
    confirm.mockReturnValue(true)
    await reply(testConnection(), existingDatabase)

    button('返回上一步').click()
    button('选择并配置 PostgreSQL').click()

    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(true)
    await reply(testConnection(), { success: true, dbExists: false })
    element<HTMLButtonElement>('#btn-save').click()
    expect(saveRequests()[0]?.body.dropDatabase).toBe(false)
  })

  it('切换 SQLite 后保存时不会携带 PostgreSQL 清库许可', async () => {
    confirm.mockReturnValue(true)
    await reply(testConnection(), existingDatabase)

    button('返回上一步').click()
    button('使用 SQLite 初始化').click()
    button('继续使用 SQLite 进行初始化').click()

    expect(saveRequests()).toHaveLength(1)
    expect(saveRequests()[0]?.body).toEqual({ engine: 'sqlite', dropDatabase: false })
  })

  it.each([
    { choice: '新数据库', existing: false, drop: false },
    { choice: '保留现有数据', existing: true, drop: false },
    { choice: '确认删除现有数据', existing: true, drop: true },
  ])('当前参数测试成功后可以保存：$choice', async ({ existing, drop }) => {
    confirm.mockReturnValue(drop)
    const request = testConnection()
    await reply(request, { success: true, dbExists: existing, hasOtherTables: existing })

    expect(confirm).toHaveBeenCalledTimes(existing ? 1 : 0)
    expect(element<HTMLButtonElement>('#btn-save').disabled).toBe(false)
    element<HTMLButtonElement>('#btn-save').click()

    expect(saveRequests()).toHaveLength(1)
    expect(saveRequests()[0]?.body).toEqual({
      ...request.body,
      engine: 'postgres',
      dropDatabase: drop,
    })
  })
})
